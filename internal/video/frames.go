package video

import (
	"bytes"
	"context"
	"fmt"
	"log/slog"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
)

// ExtractFramesOptions controls frame extraction.
type ExtractFramesOptions struct {
	InputPath   string  // absolute path to video file
	OutputDir   string  // absolute path to output directory
	FPS         float64 // frames per second to extract
	Deinterlace bool    // apply yadif deinterlace filter
	MaxWidth    int     // downscale to this width (0 = no scaling)
	Pass        string  // label for filenames: "scout", "fill", "track"
}

// ExtractFramesResult describes the output of frame extraction.
type ExtractFramesResult struct {
	FrameCount int
	OutputDir  string
}

// ExtractFrames uses FFmpeg to extract frames from a video at a fixed rate.
// Frames are saved as JPEG files named {pass}_{timestamp_ms}.jpg where
// timestamp_ms is zero-padded to 12 digits.
func ExtractFrames(ctx context.Context, ffmpegPath string, opts ExtractFramesOptions) (*ExtractFramesResult, error) {
	if err := os.MkdirAll(opts.OutputDir, 0755); err != nil {
		return nil, fmt.Errorf("creating output dir: %w", err)
	}

	// Build video filter chain
	var filters []string
	if opts.Deinterlace {
		filters = append(filters, "yadif=1")
	}
	if opts.MaxWidth > 0 {
		filters = append(filters, fmt.Sprintf("scale=%d:-1", opts.MaxWidth))
	}
	filters = append(filters, fmt.Sprintf("fps=%.4f", opts.FPS))

	// FFmpeg outputs sequential frame numbers. We use a tmp_ prefix and
	// rename to timestamp-based names after extraction.
	tmpPattern := filepath.Join(opts.OutputDir, "tmp_%06d.jpg")

	args := []string{
		"-i", opts.InputPath,
		"-vf", strings.Join(filters, ","),
		"-q:v", "2",
		"-start_number", "0",
		tmpPattern,
	}

	cmd := exec.CommandContext(ctx, ffmpegPath, args...)
	applySubprocessSafety(cmd)
	var stderr bytes.Buffer
	cmd.Stderr = &stderr

	slog.Info("extracting frames", "input", opts.InputPath, "fps", opts.FPS, "deinterlace", opts.Deinterlace)

	if err := cmd.Run(); err != nil {
		return nil, fmt.Errorf("ffmpeg frame extraction failed: %w\nstderr: %s", err, stderr.String())
	}

	// Collect tmp_ files and rename to timestamp-based names
	entries, err := os.ReadDir(opts.OutputDir)
	if err != nil {
		return nil, fmt.Errorf("reading output dir: %w", err)
	}

	var tmpFiles []string
	for _, e := range entries {
		if !e.IsDir() && strings.HasPrefix(e.Name(), "tmp_") && strings.HasSuffix(e.Name(), ".jpg") {
			tmpFiles = append(tmpFiles, e.Name())
		}
	}
	sort.Strings(tmpFiles)

	for i, name := range tmpFiles {
		timestampMs := int64(float64(i) / opts.FPS * 1000)
		newName := fmt.Sprintf("%s_%012d.jpg", opts.Pass, timestampMs)

		oldPath := filepath.Join(opts.OutputDir, name)
		newPath := filepath.Join(opts.OutputDir, newName)
		if err := os.Rename(oldPath, newPath); err != nil {
			slog.Warn("failed to rename frame", "from", name, "to", newName, "error", err)
		}
	}

	count := len(tmpFiles)
	slog.Info("frames extracted", "count", count, "output_dir", opts.OutputDir)
	return &ExtractFramesResult{FrameCount: count, OutputDir: opts.OutputDir}, nil
}

// ParseTimestampFromFilename extracts the timestamp in ms from a frame filename
// like "scout_000000012500.jpg" -> 12500.
func ParseTimestampFromFilename(filename string) (int64, error) {
	name := strings.TrimSuffix(filename, filepath.Ext(filename))
	parts := strings.SplitN(name, "_", 2)
	if len(parts) != 2 {
		return 0, fmt.Errorf("unexpected filename format: %s", filename)
	}
	ms, err := strconv.ParseInt(parts[1], 10, 64)
	if err != nil {
		return 0, fmt.Errorf("parsing timestamp from %s: %w", filename, err)
	}
	return ms, nil
}
