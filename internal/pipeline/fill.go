package pipeline

import (
	"context"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"strings"

	"github.com/colton/video-archive/internal/config"
	"github.com/colton/video-archive/internal/model"
	"github.com/colton/video-archive/internal/store"
	"github.com/colton/video-archive/internal/video"
)

// FillOptions controls the fill-sampling pass.
type FillOptions struct {
	PaddingMs int64   // how far before/after each detection to densify (default 2000)
	FPS       float64 // frames per second within each window (default 2.0)
}

// DefaultFillOptions returns sensible defaults.
func DefaultFillOptions() FillOptions {
	return FillOptions{PaddingMs: 2000, FPS: 2.0}
}

// FillResult describes the outcome of a fill call.
type FillResult struct {
	WindowCount int
	FrameCount  int
	Skipped     bool
	SkipReason  string // populated when Skipped=true
}

// FillSampleAroundDetections extracts additional frames at opts.FPS inside
// windows of [t - PaddingMs, t + PaddingMs] for every scout-pass detection.
// Windows that overlap are merged. Extracted frames are inserted with
// pass="fill"; the unique(recording_id, timestamp_ms) constraint dedups any
// timestamps that coincide with existing scout frames.
//
// Idempotent. Skips (returns Skipped=true) when any of these is true:
//   - fill frames already exist for this recording
//   - tracks already exist for this recording — fill is a pre-track stage;
//     retroactive fill after tracking would orphan its detections, since
//     TrackFaces is itself idempotent ("tracks exist → skip"). Clear tracks
//     (and downstream clusters) first if you really want to re-run fill.
func FillSampleAroundDetections(ctx context.Context, cfg config.Config, db *store.DB, recordingID int64, opts FillOptions) (*FillResult, error) {
	if existing, err := db.CountFrames(recordingID, "fill"); err != nil {
		return nil, fmt.Errorf("counting fill frames: %w", err)
	} else if existing > 0 {
		slog.Info("fill frames already exist, skipping", "recording_id", recordingID, "count", existing)
		return &FillResult{FrameCount: existing, Skipped: true, SkipReason: "fill_frames_exist"}, nil
	}
	if existing, err := db.CountTracks(recordingID); err != nil {
		return nil, fmt.Errorf("counting tracks: %w", err)
	} else if existing > 0 {
		slog.Info("tracks already exist, skipping fill to avoid orphan detections",
			"recording_id", recordingID, "tracks", existing)
		return &FillResult{Skipped: true, SkipReason: "tracks_exist"}, nil
	}

	rec, err := db.GetRecording(recordingID)
	if err != nil {
		return nil, fmt.Errorf("getting recording: %w", err)
	}

	dets, err := db.ListDetections(recordingID)
	if err != nil {
		return nil, fmt.Errorf("listing detections: %w", err)
	}
	if len(dets) == 0 {
		slog.Info("no detections to fill around", "recording_id", recordingID)
		return &FillResult{}, nil
	}

	windows := planFillWindows(dets, opts.PaddingMs, rec.DurationMs)
	if len(windows) == 0 {
		return &FillResult{}, nil
	}

	masterPath := filepath.Join(cfg.DataDir, rec.MasterPath)
	outputDir := filepath.Join(cfg.FramesDir(), fmt.Sprintf("%d", recordingID))

	inserted := 0
	for i, w := range windows {
		result, err := video.ExtractFrames(ctx, cfg.Ffmpeg, video.ExtractFramesOptions{
			InputPath:   masterPath,
			OutputDir:   outputDir,
			FPS:         opts.FPS,
			Deinterlace: rec.Interlaced,
			MaxWidth:    640,
			Pass:        "fill",
			StartMs:     w[0],
			DurationMs:  w[1] - w[0],
		})
		if err != nil {
			slog.Warn("fill window extraction failed", "recording_id", recordingID,
				"window_start_ms", w[0], "window_end_ms", w[1], "error", err)
			continue
		}
		slog.Info("fill window extracted",
			"recording_id", recordingID,
			"window", fmt.Sprintf("[%d/%d] %.1fs-%.1fs", i+1, len(windows),
				float64(w[0])/1000, float64(w[1])/1000),
			"frames", result.FrameCount)
	}

	// Register every fill_*.jpg in the output dir that isn't already in the DB.
	entries, err := os.ReadDir(outputDir)
	if err != nil {
		return nil, fmt.Errorf("reading frames dir: %w", err)
	}
	for _, e := range entries {
		if e.IsDir() || !strings.HasPrefix(e.Name(), "fill_") || !strings.HasSuffix(e.Name(), ".jpg") {
			continue
		}
		timestampMs, err := video.ParseTimestampFromFilename(e.Name())
		if err != nil {
			slog.Warn("skipping unparseable fill frame", "file", e.Name(), "error", err)
			continue
		}
		absPath := filepath.Join(outputDir, e.Name())
		relPath, _ := filepath.Rel(cfg.DataDir, absPath)
		w, h, _ := readImageDims(absPath)
		_, newRow, err := db.InsertFrame(&model.FrameSample{
			RecordingID: recordingID,
			TimestampMs: timestampMs,
			Pass:        "fill",
			FramePath:   relPath,
			Width:       w,
			Height:      h,
		})
		if err != nil {
			return nil, fmt.Errorf("inserting fill frame: %w", err)
		}
		// An existing scout row at this exact timestamp beat us to the
		// (recording_id, timestamp_ms) unique slot. Drop the redundant JPEG
		// so it doesn't linger on disk without a DB row.
		if !newRow {
			os.Remove(absPath)
			continue
		}
		inserted++
	}

	slog.Info("fill sampling complete",
		"recording_id", recordingID,
		"windows", len(windows),
		"new_frames", inserted)
	return &FillResult{WindowCount: len(windows), FrameCount: inserted}, nil
}

// planFillWindows expands each detection into [t-padding, t+padding] and
// merges overlapping intervals. Windows are clamped to [0, totalMs].
// Output is sorted ascending and guaranteed non-overlapping.
func planFillWindows(dets []model.Detection, paddingMs, totalMs int64) [][2]int64 {
	if len(dets) == 0 {
		return nil
	}
	intervals := make([][2]int64, 0, len(dets))
	for _, d := range dets {
		start := d.TimestampMs - paddingMs
		if start < 0 {
			start = 0
		}
		end := d.TimestampMs + paddingMs
		if totalMs > 0 && end > totalMs {
			end = totalMs
		}
		if end > start {
			intervals = append(intervals, [2]int64{start, end})
		}
	}
	// Already sorted by timestamp (ListDetections orders by timestamp_ms),
	// but sort defensively in case a caller passes something else.
	sortIntervals(intervals)

	merged := intervals[:0]
	cur := intervals[0]
	for _, iv := range intervals[1:] {
		if iv[0] <= cur[1] {
			if iv[1] > cur[1] {
				cur[1] = iv[1]
			}
			continue
		}
		merged = append(merged, cur)
		cur = iv
	}
	merged = append(merged, cur)
	return merged
}

func sortIntervals(ivs [][2]int64) {
	// small N (typically <500), insertion sort avoids importing sort for one call
	for i := 1; i < len(ivs); i++ {
		for j := i; j > 0 && ivs[j-1][0] > ivs[j][0]; j-- {
			ivs[j-1], ivs[j] = ivs[j], ivs[j-1]
		}
	}
}
