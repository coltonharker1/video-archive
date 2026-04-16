package pipeline

import (
	"context"
	"fmt"
	"log/slog"
	"path/filepath"

	"github.com/colton/video-archive/internal/config"
	"github.com/colton/video-archive/internal/store"
)

// BackfillResult summarises a backfill-frame-dims run.
type BackfillResult struct {
	Scanned int
	Updated int
	Failed  int
}

// BackfillFrameDims populates width/height on frame rows that are missing them
// by reading the stored JPEG header. Idempotent — safe to re-run.
func BackfillFrameDims(ctx context.Context, cfg config.Config, db *store.DB) (*BackfillResult, error) {
	frames, err := db.ListFramesMissingDims()
	if err != nil {
		return nil, fmt.Errorf("listing frames missing dims: %w", err)
	}

	res := &BackfillResult{Scanned: len(frames)}
	for _, f := range frames {
		if ctx.Err() != nil {
			return res, ctx.Err()
		}
		abs := filepath.Join(cfg.DataDir, f.FramePath)
		w, h, err := readImageDims(abs)
		if err != nil {
			slog.Warn("could not read frame dims", "frame_id", f.ID, "path", abs, "error", err)
			res.Failed++
			continue
		}
		if err := db.UpdateFrameDims(f.ID, w, h); err != nil {
			slog.Warn("could not update frame dims", "frame_id", f.ID, "error", err)
			res.Failed++
			continue
		}
		res.Updated++
	}
	return res, nil
}
