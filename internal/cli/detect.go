package cli

import (
	"context"
	"fmt"
	"strconv"

	"github.com/colton/video-archive/internal/config"
	"github.com/colton/video-archive/internal/mlclient"
	"github.com/colton/video-archive/internal/pipeline"
	"github.com/spf13/cobra"
)

var detectCmd = &cobra.Command{
	Use:   "detect [recording_id]",
	Short: "Detect faces in sampled frames (requires ML worker)",
	Long:  "Sends frames to the Python ML worker for face detection and embedding. Run 'python worker/worker.py' first.",
	Args:  cobra.ExactArgs(1),
	RunE:  runDetect,
}

func init() {
	RootCmd.AddCommand(detectCmd)
}

func runDetect(cmd *cobra.Command, args []string) error {
	id, err := strconv.ParseInt(args[0], 10, 64)
	if err != nil {
		return fmt.Errorf("invalid recording ID: %w", err)
	}

	cfg := config.Default()
	db, err := openDB(cfg)
	if err != nil {
		return err
	}
	defer db.Close()

	ml := mlclient.New(cfg.MLWorkerURL)

	// Check worker health
	health, err := ml.Health(context.Background())
	if err != nil {
		return fmt.Errorf("ML worker not reachable at %s: %w\nStart it with: cd worker && python worker.py", cfg.MLWorkerURL, err)
	}
	if !health.ModelsLoaded {
		return fmt.Errorf("ML worker models not loaded yet, try again shortly")
	}

	result, err := pipeline.DetectFaces(context.Background(), cfg, db, ml, id)
	if err != nil {
		return err
	}

	if result.Skipped {
		fmt.Printf("All frames already processed for recording %d\n", id)
		return nil
	}

	fmt.Printf("Detected %d faces across %d frames in recording %d\n",
		result.FacesFound, result.FramesDone, id)
	return nil
}
