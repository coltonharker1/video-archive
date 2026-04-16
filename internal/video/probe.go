package video

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"math"
	"os/exec"
	"strconv"
	"strings"

	"github.com/colton/video-archive/internal/model"
)

// ffprobeOutput maps the JSON structure returned by ffprobe.
type ffprobeOutput struct {
	Format struct {
		Duration string `json:"duration"`
	} `json:"format"`
	Streams []struct {
		CodecType    string `json:"codec_type"`
		CodecName    string `json:"codec_name"`
		Width        int    `json:"width"`
		Height       int    `json:"height"`
		RFrameRate   string `json:"r_frame_rate"`
		FieldOrder   string `json:"field_order"`
	} `json:"streams"`
}

// Probe extracts metadata from a video file using ffprobe.
func Probe(ctx context.Context, ffprobePath, filePath string) (*model.VideoMeta, error) {
	args := []string{
		"-v", "quiet",
		"-print_format", "json",
		"-show_format",
		"-show_streams",
		filePath,
	}

	cmd := exec.CommandContext(ctx, ffprobePath, args...)
	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr

	if err := cmd.Run(); err != nil {
		return nil, fmt.Errorf("ffprobe failed: %w\nstderr: %s", err, stderr.String())
	}

	var out ffprobeOutput
	if err := json.Unmarshal(stdout.Bytes(), &out); err != nil {
		return nil, fmt.Errorf("parsing ffprobe output: %w", err)
	}

	meta := &model.VideoMeta{}

	// Parse duration
	if dur, err := strconv.ParseFloat(out.Format.Duration, 64); err == nil {
		meta.DurationMs = int64(math.Round(dur * 1000))
	}

	// Find the video stream
	for _, s := range out.Streams {
		if s.CodecType != "video" {
			continue
		}
		meta.Width = s.Width
		meta.Height = s.Height
		meta.Codec = s.CodecName
		meta.FPS = parseFPS(s.RFrameRate)
		meta.Interlaced = isInterlaced(s.FieldOrder)
		break
	}

	return meta, nil
}

// parseFPS converts a fractional frame rate string like "30000/1001" to float64.
func parseFPS(rate string) float64 {
	parts := strings.SplitN(rate, "/", 2)
	if len(parts) != 2 {
		f, _ := strconv.ParseFloat(rate, 64)
		return f
	}
	num, _ := strconv.ParseFloat(parts[0], 64)
	den, _ := strconv.ParseFloat(parts[1], 64)
	if den == 0 {
		return 0
	}
	return num / den
}

// isInterlaced checks the field_order to determine if the video is interlaced.
func isInterlaced(fieldOrder string) bool {
	switch fieldOrder {
	case "tt", "bb", "tb", "bt":
		return true
	default:
		return false
	}
}
