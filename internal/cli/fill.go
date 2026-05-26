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

var (
	fillPadding int64
	fillFPS     float64
	fillNoRedetect bool
)

var fillCmd = &cobra.Command{
	Use:   "fill [recording_id]",
	Short: "Extract denser frames around existing detections, then re-run detect",
	Long: `Reads the scout-pass detections for a recording, expands each into a
window of ±padding seconds, merges overlapping windows, and extracts additional
frames at --fps within those windows. Newly extracted frames are inserted with
pass="fill" and fed into a second detect pass.

Idempotent — if any fill-pass frames already exist, this is a no-op.`,
	Args: cobra.ExactArgs(1),
	RunE: runFill,
}

func init() {
	fillCmd.Flags().Int64Var(&fillPadding, "padding", 2000, "pad each detection by this many ms on either side")
	fillCmd.Flags().Float64Var(&fillFPS, "fps", 2.0, "frames per second within each window")
	fillCmd.Flags().BoolVar(&fillNoRedetect, "no-redetect", false, "extract fill frames but don't run detect on them")
	RootCmd.AddCommand(fillCmd)
}

func runFill(cmd *cobra.Command, args []string) error {
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

	opts := pipeline.FillOptions{PaddingMs: fillPadding, FPS: fillFPS}
	result, err := pipeline.FillSampleAroundDetections(context.Background(), cfg, db, id, opts)
	if err != nil {
		return err
	}

	if result.Skipped {
		switch result.SkipReason {
		case "tracks_exist":
			fmt.Printf("Recording %d already has tracks; fill is a pre-track stage. "+
				"Clear tracks+clusters first if you really want to re-run fill.\n", id)
		default:
			fmt.Printf("Fill frames already exist for recording %d (%d frames)\n", id, result.FrameCount)
		}
		return nil
	}
	fmt.Printf("Extracted %d fill frames across %d windows for recording %d\n",
		result.FrameCount, result.WindowCount, id)

	if fillNoRedetect || result.FrameCount == 0 {
		return nil
	}

	ml := mlclient.New(cfg.MLWorkerURL)
	health, err := ml.Health(context.Background())
	if err != nil || !health.ModelsLoaded {
		fmt.Printf("ML worker unavailable at %s — run 'va detect %d' yourself to process fill frames.\n",
			cfg.MLWorkerURL, id)
		return nil
	}

	dr, err := pipeline.DetectFaces(context.Background(), cfg, db, ml, id)
	if err != nil {
		return fmt.Errorf("detect: %w", err)
	}
	if dr.Skipped {
		fmt.Println("No new frames to detect on.")
	} else {
		fmt.Printf("Detected %d additional faces across %d fill frames\n", dr.FacesFound, dr.FramesDone)
	}
	return nil
}
