package cli

import (
	"fmt"
	"strconv"

	"github.com/spf13/cobra"
)

var showCmd = &cobra.Command{
	Use:   "show [recording_id]",
	Short: "Show details for a recording",
	Args:  cobra.ExactArgs(1),
	RunE:  runShow,
}

func init() {
	RootCmd.AddCommand(showCmd)
}

func runShow(cmd *cobra.Command, args []string) error {
	id, err := strconv.ParseInt(args[0], 10, 64)
	if err != nil {
		return fmt.Errorf("invalid recording ID: %w", err)
	}

	cfg := getConfig()
	db, err := openDB(cfg)
	if err != nil {
		return err
	}
	defer db.Close()

	rec, err := db.GetRecording(id)
	if err != nil {
		return fmt.Errorf("recording not found: %w", err)
	}

	fmt.Printf("ID:          %d\n", rec.ID)
	fmt.Printf("Slug:        %s\n", rec.Slug)
	if rec.Label != "" {
		fmt.Printf("Label:       %s\n", rec.Label)
	}
	fmt.Printf("Date:        %s\n", rec.Date)
	fmt.Printf("Master:      %s\n", rec.MasterPath)

	if rec.DurationMs > 0 {
		sec := rec.DurationMs / 1000
		fmt.Printf("Duration:    %d:%02d\n", sec/60, sec%60)
	}
	if rec.Width > 0 {
		fmt.Printf("Resolution:  %dx%d\n", rec.Width, rec.Height)
	}
	if rec.FPS > 0 {
		fmt.Printf("FPS:         %.2f\n", rec.FPS)
	}
	if rec.Codec != "" {
		fmt.Printf("Codec:       %s\n", rec.Codec)
	}
	if rec.Interlaced {
		fmt.Println("Interlaced:  yes")
	}

	// Show pipeline stats
	frameCount, _ := db.CountFrames(id, "")
	detCount, _ := db.CountDetections(id)
	embCount, _ := db.CountEmbeddings(id)
	trackCount, _ := db.CountTracks(id)
	clusterCount, _ := db.CountClusters(id)

	if frameCount > 0 {
		fmt.Printf("\nPipeline:\n")
		fmt.Printf("  Frames:      %d\n", frameCount)
	}
	if detCount > 0 {
		fmt.Printf("  Detections:  %d\n", detCount)
	}
	if embCount > 0 {
		fmt.Printf("  Embeddings:  %d\n", embCount)
	}
	if trackCount > 0 {
		fmt.Printf("  Tracks:      %d\n", trackCount)
	}
	if clusterCount > 0 {
		fmt.Printf("  Clusters:    %d\n", clusterCount)
	}

	return nil
}
