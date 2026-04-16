package cli

import (
	"context"
	"fmt"
	"strconv"

	"github.com/colton/video-archive/internal/config"
	"github.com/colton/video-archive/internal/pipeline"
	"github.com/spf13/cobra"
)

var sampleCmd = &cobra.Command{
	Use:   "sample [recording_id]",
	Short: "Extract frames from a recording at the configured sample rate",
	Args:  cobra.ExactArgs(1),
	RunE:  runSample,
}

func init() {
	RootCmd.AddCommand(sampleCmd)
}

func runSample(cmd *cobra.Command, args []string) error {
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

	result, err := pipeline.SampleRecording(context.Background(), cfg, db, id)
	if err != nil {
		return err
	}

	if result.Skipped {
		fmt.Printf("Frames already exist for recording %d (%d frames)\n", id, result.FrameCount)
		return nil
	}

	fmt.Printf("Extracted %d frames from recording %d\n", result.FrameCount, id)
	return nil
}
