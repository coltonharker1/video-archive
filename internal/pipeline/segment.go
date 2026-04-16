package pipeline

import (
	"encoding/json"
	"fmt"
	"log/slog"
	"sort"

	"github.com/colton/video-archive/internal/config"
	"github.com/colton/video-archive/internal/store"
)

// SegmentOptions controls segment generation.
type SegmentOptions struct {
	GapThresholdMs int64 // merge segments closer than this (default 3000ms)
}

// DefaultSegmentOptions returns sensible defaults.
// GapThresholdMs is the within-person merge threshold. At 0.5 fps sampling,
// track breaks and brief face-away moments routinely produce 2-4 second gaps
// even when a person is continuously present. 5s is conservative enough to
// bridge those without merging genuinely separate appearances.
func DefaultSegmentOptions() SegmentOptions {
	return SegmentOptions{
		GapThresholdMs: 5000,
	}
}

// SegmentResult describes the outcome of segment generation.
type SegmentResult struct {
	SegmentCount int
	PersonCount  int
}

// timeRange is a time interval with a confidence score.
type timeRange struct {
	startMs    int64
	endMs      int64
	confidence float64
}

// GenerateSegments builds merged time segments for each named person from
// their confirmed clusters and tracks. Overwrites existing segments.
func GenerateSegments(_ config.Config, db *store.DB, recordingID int64, opts SegmentOptions) (*SegmentResult, error) {
	// Always regenerate
	if err := db.DeleteSegments(recordingID); err != nil {
		return nil, fmt.Errorf("deleting existing segments: %w", err)
	}

	clusters, err := db.ListClusters(recordingID)
	if err != nil {
		return nil, fmt.Errorf("listing clusters: %w", err)
	}

	tracks, err := db.ListTracks(recordingID)
	if err != nil {
		return nil, fmt.Errorf("listing tracks: %w", err)
	}
	trackMap := make(map[int64]struct{ StartMs, EndMs int64 })
	trackConfMap := make(map[int64]float64)
	for _, t := range tracks {
		trackMap[t.ID] = struct{ StartMs, EndMs int64 }{t.StartMs, t.EndMs}
		trackConfMap[t.ID] = t.Confidence
	}

	// Group time ranges by identity
	identityRanges := make(map[int64][]timeRange)

	for _, c := range clusters {
		if c.Status != "confirmed" || c.IdentityID == nil {
			continue
		}

		var trackIDs []int64
		json.Unmarshal([]byte(c.TrackIDs), &trackIDs)

		for _, tid := range trackIDs {
			if t, ok := trackMap[tid]; ok {
				identityRanges[*c.IdentityID] = append(identityRanges[*c.IdentityID], timeRange{
					startMs:    t.StartMs,
					endMs:      t.EndMs,
					confidence: trackConfMap[tid],
				})
			}
		}
	}

	if len(identityRanges) == 0 {
		slog.Info("no confirmed clusters with identities", "recording_id", recordingID)
		return &SegmentResult{}, nil
	}

	totalSegments := 0
	for identityID, ranges := range identityRanges {
		merged := mergeTimeRanges(ranges, opts.GapThresholdMs)

		for _, m := range merged {
			_, err := db.InsertSegment(&store.Segment{
				RecordingID: recordingID,
				IdentityID:  identityID,
				StartMs:     m.startMs,
				EndMs:       m.endMs,
				Confidence:  m.confidence,
			})
			if err != nil {
				return nil, fmt.Errorf("inserting segment: %w", err)
			}
			totalSegments++
		}
	}

	slog.Info("segments generated",
		"recording_id", recordingID,
		"people", len(identityRanges),
		"segments", totalSegments,
	)

	return &SegmentResult{
		SegmentCount: totalSegments,
		PersonCount:  len(identityRanges),
	}, nil
}

// mergeTimeRanges sorts ranges by start time and merges those that overlap
// or are within gapMs of each other.
func mergeTimeRanges(ranges []timeRange, gapMs int64) []timeRange {
	if len(ranges) == 0 {
		return nil
	}

	sort.Slice(ranges, func(i, j int) bool {
		return ranges[i].startMs < ranges[j].startMs
	})

	merged := []timeRange{ranges[0]}

	for i := 1; i < len(ranges); i++ {
		last := &merged[len(merged)-1]
		curr := ranges[i]

		if curr.startMs <= last.endMs+gapMs {
			if curr.endMs > last.endMs {
				last.endMs = curr.endMs
			}
			last.confidence = (last.confidence + curr.confidence) / 2
		} else {
			merged = append(merged, curr)
		}
	}

	return merged
}
