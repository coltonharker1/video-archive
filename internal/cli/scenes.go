package cli

import (
	"context"
	"fmt"
	"strconv"

	"github.com/colton/video-archive/internal/config"
	"github.com/colton/video-archive/internal/pipeline"
	"github.com/spf13/cobra"
)

var (
	sceneThreshold   float64
	sceneMergeGap    int64
	sceneMinDuration int64
)

var scenesCmd = &cobra.Command{
	Use:   "scenes [recording_id]",
	Short: "Detect scene boundaries via shot-change detection",
	Long:  "Runs FFmpeg's scdet filter to find shot boundaries, then stores scenes with post-processing (merge nearby, minimum duration).",
	Args:  cobra.ExactArgs(1),
	RunE:  runScenes,
}

func init() {
	scenesCmd.Flags().Float64Var(&sceneThreshold, "threshold", 10.0, "scdet sensitivity (ffmpeg range 8-14; 10 is default; preprocessing applied: yadif+hqdn3d)")
	scenesCmd.Flags().Int64Var(&sceneMergeGap, "merge-gap", 1000, "collapse boundaries within this many ms")
	scenesCmd.Flags().Int64Var(&sceneMinDuration, "min-duration", 3000, "absorb scenes shorter than this (ms) into the previous")
	RootCmd.AddCommand(scenesCmd)
}

func runScenes(cmd *cobra.Command, args []string) error {
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

	opts := pipeline.SceneOptions{
		Threshold:     sceneThreshold,
		MergeGapMs:    sceneMergeGap,
		MinDurationMs: sceneMinDuration,
	}

	result, err := pipeline.DetectScenes(context.Background(), cfg, db, id, opts)
	if err != nil {
		return err
	}

	fmt.Printf("Detected %d scenes from %d raw boundaries in recording %d\n",
		result.SceneCount, result.BoundaryCount, id)
	return nil
}
