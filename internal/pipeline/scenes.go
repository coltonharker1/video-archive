package pipeline

import (
	"context"
	"fmt"
	"log/slog"
	"path/filepath"

	"github.com/colton/video-archive/internal/config"
	"github.com/colton/video-archive/internal/model"
	"github.com/colton/video-archive/internal/store"
	"github.com/colton/video-archive/internal/video"
)

// SceneOptions controls scene detection behaviour.
type SceneOptions struct {
	Threshold      float64 // scdet sensitivity (default 5.0)
	MergeGapMs     int64   // collapse boundaries within this gap (default 1000)
	MinDurationMs  int64   // absorb scenes shorter than this into the previous (default 3000)
}

// DefaultSceneOptions returns sensible defaults for VHS source material.
// With the yadif+hqdn3d preprocessing in video.DetectScenes, threshold=10
// (ffmpeg's documented default) is the correct choice. Earlier versions
// shipped threshold=5 because preprocessing was absent and the noise floor
// was higher — don't lower this without also removing the denoise prefix.
func DefaultSceneOptions() SceneOptions {
	return SceneOptions{
		Threshold:     10.0,
		MergeGapMs:    1000,
		MinDurationMs: 3000,
	}
}

// SceneResult describes the outcome of scene detection.
type SceneResult struct {
	SceneCount     int
	BoundaryCount  int // raw boundaries before post-processing
}

// DetectScenes runs shot-boundary detection on a recording and stores the
// resulting scenes. Overwrites any existing scenes for this recording.
func DetectScenes(ctx context.Context, cfg config.Config, db *store.DB, recordingID int64, opts SceneOptions) (*SceneResult, error) {
	rec, err := db.GetRecording(recordingID)
	if err != nil {
		return nil, fmt.Errorf("get recording: %w", err)
	}

	masterPath := filepath.Join(cfg.DataDir, rec.MasterPath)

	raw, err := video.DetectScenes(ctx, cfg.Ffmpeg, masterPath, opts.Threshold)
	if err != nil {
		return nil, fmt.Errorf("ffmpeg scene detection: %w", err)
	}

	slog.Info("raw scene boundaries", "recording_id", recordingID, "count", len(raw))

	merged := video.MergeBoundaries(raw, opts.MergeGapMs)

	// Build scenes from boundaries. Each boundary starts a new scene; the
	// previous scene ends at this boundary.
	durationMs := rec.DurationMs
	if durationMs <= 0 {
		return nil, fmt.Errorf("recording %d has no duration", recordingID)
	}

	type pendingScene struct {
		startMs int64
		score   float64
	}
	var pending []pendingScene

	// First scene always starts at 0.
	pending = append(pending, pendingScene{startMs: 0, score: 0})
	for _, b := range merged {
		if b.TimeMs > 0 && b.TimeMs < durationMs {
			pending = append(pending, pendingScene{startMs: b.TimeMs, score: b.Score})
		}
	}

	// Convert to scenes with end times.
	type sceneEntry struct {
		startMs int64
		endMs   int64
		score   float64
	}
	var entries []sceneEntry
	for i, p := range pending {
		endMs := durationMs
		if i+1 < len(pending) {
			endMs = pending[i+1].startMs
		}
		entries = append(entries, sceneEntry{startMs: p.startMs, endMs: endMs, score: p.score})
	}

	// Post-process: absorb short scenes into the previous one.
	if opts.MinDurationMs > 0 {
		var filtered []sceneEntry
		for _, e := range entries {
			dur := e.endMs - e.startMs
			if dur < opts.MinDurationMs && len(filtered) > 0 {
				filtered[len(filtered)-1].endMs = e.endMs
				continue
			}
			filtered = append(filtered, e)
		}
		entries = filtered
	}

	// Persist: clear old scenes and insert new ones.
	if err := db.DeleteScenes(recordingID); err != nil {
		return nil, fmt.Errorf("clearing old scenes: %w", err)
	}

	for _, e := range entries {
		_, err := db.InsertScene(&model.Scene{
			RecordingID: recordingID,
			StartMs:     e.startMs,
			EndMs:       e.endMs,
			Score:       e.score,
		})
		if err != nil {
			return nil, fmt.Errorf("inserting scene: %w", err)
		}
	}

	slog.Info("scenes stored",
		"recording_id", recordingID,
		"raw_boundaries", len(raw),
		"scenes", len(entries),
	)

	return &SceneResult{
		SceneCount:    len(entries),
		BoundaryCount: len(raw),
	}, nil
}
