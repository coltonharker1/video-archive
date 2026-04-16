package video

import (
	"bufio"
	"bytes"
	"context"
	"fmt"
	"math"
	"os/exec"
	"regexp"
	"sort"
	"strconv"
)

// SceneBoundary is a single shot-change detection from ffmpeg's scdet filter.
type SceneBoundary struct {
	TimeMs int64
	Score  float64
}

var scdetRe = regexp.MustCompile(`lavfi\.scd\.score:\s*([\d.]+),\s*lavfi\.scd\.time:\s*([\d.]+)`)

// DetectScenes runs ffmpeg's scdet filter on a video file and returns detected
// scene boundaries sorted by time.
//
// The filter chain preprocesses the signal before detection:
//   - yadif=mode=1 deinterlaces (critical for VHS — interlaced fields wreck
//     consecutive-frame SAD comparisons scdet relies on)
//   - hqdn3d applies spatial+temporal denoising so the noise floor drops below
//     the scene-change signal; without this, VHS noise swamps real cuts
//   - scdet then compares the cleaned consecutive frames
//
// Threshold controls sensitivity of the final scdet pass. FFmpeg's documented
// usable range is 8–14 (default 10). Lower values detect noise; higher values
// miss soft transitions. With the denoise prefix, 10 is the right default for
// VHS source. Clean digital source can use 12–14.
func DetectScenes(ctx context.Context, ffmpegPath, filePath string, threshold float64) ([]SceneBoundary, error) {
	filter := fmt.Sprintf("yadif=mode=1,hqdn3d=4:3:6:4.5,scdet=threshold=%g", threshold)
	cmd := exec.CommandContext(ctx, ffmpegPath, "-i", filePath, "-vf", filter, "-f", "null", "-")
	applySubprocessSafety(cmd)

	var stderr bytes.Buffer
	cmd.Stderr = &stderr

	if err := cmd.Run(); err != nil {
		return nil, fmt.Errorf("ffmpeg scdet failed: %w\nstderr (tail): %s", err, tail(stderr.Bytes(), 500))
	}

	var boundaries []SceneBoundary
	scanner := bufio.NewScanner(&stderr)
	for scanner.Scan() {
		m := scdetRe.FindStringSubmatch(scanner.Text())
		if m == nil {
			continue
		}
		score, _ := strconv.ParseFloat(m[1], 64)
		timeSec, _ := strconv.ParseFloat(m[2], 64)
		boundaries = append(boundaries, SceneBoundary{
			TimeMs: int64(math.Round(timeSec * 1000)),
			Score:  score,
		})
	}

	sort.Slice(boundaries, func(i, j int) bool {
		return boundaries[i].TimeMs < boundaries[j].TimeMs
	})

	return boundaries, nil
}

// MergeBoundaries applies post-processing rules:
//  1. Boundaries within mergeGapMs of each other are collapsed (keep highest score).
//  2. Ensures time=0 is always a boundary (first scene start).
func MergeBoundaries(raw []SceneBoundary, mergeGapMs int64) []SceneBoundary {
	if len(raw) == 0 {
		return nil
	}

	var merged []SceneBoundary
	cur := raw[0]
	for i := 1; i < len(raw); i++ {
		if raw[i].TimeMs-cur.TimeMs <= mergeGapMs {
			if raw[i].Score > cur.Score {
				cur = raw[i]
			}
			continue
		}
		merged = append(merged, cur)
		cur = raw[i]
	}
	merged = append(merged, cur)

	return merged
}

func tail(b []byte, n int) string {
	if len(b) <= n {
		return string(b)
	}
	return string(b[len(b)-n:])
}
