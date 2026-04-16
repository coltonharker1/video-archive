package cli

import (
	"context"
	"fmt"

	"github.com/colton/video-archive/internal/config"
	"github.com/colton/video-archive/internal/pipeline"
	"github.com/spf13/cobra"
)

var backfillFramesCmd = &cobra.Command{
	Use:   "backfill-frames",
	Short: "Populate width/height on frames that were ingested before those fields were recorded",
	RunE:  runBackfillFrames,
}

func init() {
	RootCmd.AddCommand(backfillFramesCmd)
}

func runBackfillFrames(cmd *cobra.Command, args []string) error {
	cfg := config.Default()
	db, err := openDB(cfg)
	if err != nil {
		return err
	}
	defer db.Close()

	res, err := pipeline.BackfillFrameDims(context.Background(), cfg, db)
	if err != nil {
		return err
	}
	fmt.Printf("Scanned %d frames — updated %d, failed %d\n", res.Scanned, res.Updated, res.Failed)
	return nil
}
