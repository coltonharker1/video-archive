package cli

import (
	"fmt"
	"os/exec"
	"runtime"
	"strconv"

	"github.com/colton/video-archive/internal/config"
	"github.com/colton/video-archive/internal/review"
	"github.com/spf13/cobra"
)

var reviewPort int

var reviewCmd = &cobra.Command{
	Use:   "review [recording_id]",
	Short: "Open the review UI to name and manage face clusters",
	Long:  "Starts a local web server with the review interface. With a recording_id, opens directly to that recording's cluster review page; without one, opens the home page.",
	Args:  cobra.MaximumNArgs(1),
	RunE:  runReview,
}

func init() {
	reviewCmd.Flags().IntVar(&reviewPort, "port", 8090, "port for the review web server")
	RootCmd.AddCommand(reviewCmd)
}

func runReview(cmd *cobra.Command, args []string) error {
	cfg := config.Default()
	db, err := openDB(cfg)
	if err != nil {
		return err
	}
	// Note: don't defer db.Close() — server runs until killed

	srv := review.New(cfg, db, reviewPort)

	var url string
	if len(args) == 1 {
		id, err := strconv.ParseInt(args[0], 10, 64)
		if err != nil {
			return fmt.Errorf("invalid recording ID: %w", err)
		}
		if _, err := db.GetRecording(id); err != nil {
			return fmt.Errorf("recording %d not found: %w", id, err)
		}
		url = srv.URL(id)
	} else {
		url = fmt.Sprintf("http://127.0.0.1:%d/", reviewPort)
	}

	fmt.Printf("Review server starting at %s\n", url)
	fmt.Println("Press Ctrl+C to stop")

	openBrowser(url)

	return srv.ListenAndServe()
}

func openBrowser(url string) {
	var cmd *exec.Cmd
	switch runtime.GOOS {
	case "darwin":
		cmd = exec.Command("open", url)
	case "linux":
		cmd = exec.Command("xdg-open", url)
	default:
		return
	}
	cmd.Start()
}
