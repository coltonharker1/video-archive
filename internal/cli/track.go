package cli

import (
	"context"
	"fmt"
	"strconv"

	"github.com/colton/video-archive/internal/config"
	"github.com/colton/video-archive/internal/pipeline"
	"github.com/spf13/cobra"
)

var trackCmd = &cobra.Command{
	Use:   "track [recording_id]",
	Short: "Build face tracks from detections",
	Long:  "Groups detections across frames into continuous appearance tracks using embedding similarity.",
	Args:  cobra.ExactArgs(1),
	RunE:  runTrack,
}

func init() {
	RootCmd.AddCommand(trackCmd)
}

func runTrack(cmd *cobra.Command, args []string) error {
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

	result, err := pipeline.TrackFaces(context.Background(), cfg, db, id)
	if err != nil {
		return err
	}

	if result.Skipped {
		fmt.Printf("Tracks already exist for recording %d (%d tracks)\n", id, result.TrackCount)
		return nil
	}

	fmt.Printf("Built %d tracks for recording %d\n", result.TrackCount, id)
	return nil
}
