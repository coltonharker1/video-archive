package pipeline

import (
	"context"
	"fmt"
	"image"
	_ "image/jpeg" // register JPEG decoder for image.DecodeConfig
	"log/slog"
	"os"
	"path/filepath"
	"strings"

	"github.com/colton/video-archive/internal/config"
	"github.com/colton/video-archive/internal/model"
	"github.com/colton/video-archive/internal/store"
	"github.com/colton/video-archive/internal/video"
)

// readImageDims reads a JPEG file's dimensions without decoding pixels.
func readImageDims(path string) (int, int, error) {
	f, err := os.Open(path)
	if err != nil {
		return 0, 0, err
	}
	defer f.Close()
	cfg, _, err := image.DecodeConfig(f)
	if err != nil {
		return 0, 0, err
	}
	return cfg.Width, cfg.Height, nil
}

// SampleResult describes the outcome of a sample call.
type SampleResult struct {
	FrameCount int
	Skipped    bool
}

// SampleRecording extracts frames from a recording at the configured FPS.
// Idempotent — if frames already exist for this recording and pass, skips.
func SampleRecording(ctx context.Context, cfg config.Config, db *store.DB, recordingID int64) (*SampleResult, error) {
	rec, err := db.GetRecording(recordingID)
	if err != nil {
		return nil, fmt.Errorf("getting recording: %w", err)
	}

	pass := "scout"

	// Idempotency check
	existing, err := db.CountFrames(recordingID, pass)
	if err != nil {
		return nil, fmt.Errorf("counting existing frames: %w", err)
	}
	if existing > 0 {
		slog.Info("frames already exist, skipping", "recording_id", recordingID, "count", existing)
		return &SampleResult{FrameCount: existing, Skipped: true}, nil
	}

	masterPath := filepath.Join(cfg.DataDir, rec.MasterPath)
	outputDir := filepath.Join(cfg.FramesDir(), fmt.Sprintf("%d", recordingID))

	result, err := video.ExtractFrames(ctx, cfg.Ffmpeg, video.ExtractFramesOptions{
		InputPath:   masterPath,
		OutputDir:   outputDir,
		FPS:         cfg.SampleFPS,
		Deinterlace: rec.Interlaced,
		MaxWidth:    640,
		Pass:        pass,
	})
	if err != nil {
		return nil, fmt.Errorf("extracting frames: %w", err)
	}

	// Register frames in DB
	entries, err := os.ReadDir(outputDir)
	if err != nil {
		return nil, fmt.Errorf("reading frames dir: %w", err)
	}

	registered := 0
	for _, e := range entries {
		if e.IsDir() || !strings.HasPrefix(e.Name(), pass+"_") || !strings.HasSuffix(e.Name(), ".jpg") {
			continue
		}

		timestampMs, err := video.ParseTimestampFromFilename(e.Name())
		if err != nil {
			slog.Warn("skipping unparseable frame", "file", e.Name(), "error", err)
			continue
		}

		absPath := filepath.Join(outputDir, e.Name())
		relPath, _ := filepath.Rel(cfg.DataDir, absPath)
		w, h, dimErr := readImageDims(absPath)
		if dimErr != nil {
			slog.Warn("could not read frame dims", "file", e.Name(), "error", dimErr)
		}
		frame := &model.FrameSample{
			RecordingID: recordingID,
			TimestampMs: timestampMs,
			Pass:        pass,
			FramePath:   relPath,
			Width:       w,
			Height:      h,
		}

		if _, _, err := db.InsertFrame(frame); err != nil {
			return nil, fmt.Errorf("inserting frame: %w", err)
		}
		registered++
	}

	slog.Info("sampling complete", "recording_id", recordingID, "frames", registered)
	return &SampleResult{FrameCount: result.FrameCount, Skipped: false}, nil
}
