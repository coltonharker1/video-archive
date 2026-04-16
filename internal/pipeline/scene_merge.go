package pipeline

import (
	"fmt"
	"log/slog"

	"github.com/colton/video-archive/internal/model"
	"github.com/colton/video-archive/internal/store"
)

// SceneMergeOptions controls face-informed scene merging.
type SceneMergeOptions struct {
	MinOverlap          float64 // Jaccard similarity threshold (default 0.5)
	MinSegmentOverlapMs int64   // minimum segment/scene overlap to count as "present" (default 500ms)
}

// DefaultSceneMergeOptions returns sensible defaults.
//
// MinSegmentOverlapMs guards against boundary-crossing artifacts: a segment
// that ends 200ms after a real scene cut would otherwise attribute that
// person to both scenes, falsely merging them. 500ms is long enough to
// reject these artifacts but short enough to keep genuine brief appearances.
func DefaultSceneMergeOptions() SceneMergeOptions {
	return SceneMergeOptions{
		MinOverlap:          0.5,
		MinSegmentOverlapMs: 500,
	}
}

// SceneMergeResult describes the outcome of face-informed merging.
type SceneMergeResult struct {
	Before  int // scene count before merging
	After   int // scene count after merging
	Merged  int // number of merge operations performed
}

// MergeScenesByPeople merges adjacent scenes that share a high proportion of
// people (by Jaccard similarity of identity sets). This catches camera wobble,
// pan-triggered false cuts, and other over-segmentation where the same group
// of people is continuously present.
//
// Requires both scenes and segments to exist. Empty scenes (no identified
// people) act as merge barriers — they're never merged into neighbours.
func MergeScenesByPeople(db *store.DB, recordingID int64, opts SceneMergeOptions) (*SceneMergeResult, error) {
	scenes, err := db.ListScenes(recordingID)
	if err != nil {
		return nil, fmt.Errorf("listing scenes: %w", err)
	}
	if len(scenes) <= 1 {
		return &SceneMergeResult{Before: len(scenes), After: len(scenes)}, nil
	}

	segments, err := db.ListSegments(recordingID)
	if err != nil {
		return nil, fmt.Errorf("listing segments: %w", err)
	}

	// Build identity set for each scene by checking segment overlap.
	// Reject tiny overlaps (segments that straddle a scene boundary by only a
	// few hundred ms) — those cause false merges of genuinely different scenes.
	type idSet = map[int64]bool
	scenePeople := make([]idSet, len(scenes))
	for i, sc := range scenes {
		ids := make(idSet)
		for _, seg := range segments {
			overlap := min64(sc.EndMs, seg.EndMs) - max64(sc.StartMs, seg.StartMs)
			if overlap < opts.MinSegmentOverlapMs {
				continue
			}
			ids[seg.IdentityID] = true
		}
		scenePeople[i] = ids
		slog.Debug("scene people built", "scene_idx", i, "start_ms", sc.StartMs, "end_ms", sc.EndMs, "identities", ids)
	}

	// Walk adjacent pairs, greedily merge when overlap exceeds threshold.
	type merged struct {
		startMs int64
		endMs   int64
		score   float64
		people  idSet
	}

	result := []merged{{
		startMs: scenes[0].StartMs,
		endMs:   scenes[0].EndMs,
		score:   scenes[0].Score,
		people:  scenePeople[0],
	}}

	mergeCount := 0
	for i := 1; i < len(scenes); i++ {
		cur := &result[len(result)-1]
		nextPeople := scenePeople[i]

		shouldMerge := false
		if len(cur.people) > 0 && len(nextPeople) > 0 {
			j := jaccard(cur.people, nextPeople)
			if j >= opts.MinOverlap {
				shouldMerge = true
			}
		}

		if shouldMerge {
			slog.Debug("merging scenes", "cur_start", cur.startMs, "cur_end", cur.endMs, "next_start", scenes[i].StartMs, "next_end", scenes[i].EndMs, "cur_people", cur.people, "next_people", nextPeople)
			cur.endMs = scenes[i].EndMs
			for id := range nextPeople {
				cur.people[id] = true
			}
			mergeCount++
		} else {
			slog.Debug("NOT merging scenes", "cur_start", cur.startMs, "cur_end", cur.endMs, "next_start", scenes[i].StartMs, "next_end", scenes[i].EndMs, "cur_people", cur.people, "next_people", nextPeople)
			result = append(result, merged{
				startMs: scenes[i].StartMs,
				endMs:   scenes[i].EndMs,
				score:   scenes[i].Score,
				people:  nextPeople,
			})
		}
	}

	if mergeCount == 0 {
		slog.Debug("no scenes merged by people overlap", "recording_id", recordingID)
		return &SceneMergeResult{Before: len(scenes), After: len(scenes)}, nil
	}

	// Rewrite scenes in DB.
	if err := db.DeleteScenes(recordingID); err != nil {
		return nil, fmt.Errorf("clearing scenes for rewrite: %w", err)
	}
	for _, m := range result {
		if _, err := db.InsertScene(&model.Scene{
			RecordingID: recordingID,
			StartMs:     m.startMs,
			EndMs:       m.endMs,
			Score:       m.score,
		}); err != nil {
			return nil, fmt.Errorf("inserting merged scene: %w", err)
		}
	}

	slog.Debug("scenes merged by people overlap",
		"recording_id", recordingID,
		"before", len(scenes),
		"after", len(result),
		"merges", mergeCount,
	)

	return &SceneMergeResult{
		Before: len(scenes),
		After:  len(result),
		Merged: mergeCount,
	}, nil
}

// jaccard computes the Jaccard similarity of two identity sets:
// |A ∩ B| / |A ∪ B|.
func jaccard(a, b map[int64]bool) float64 {
	if len(a) == 0 && len(b) == 0 {
		return 0
	}
	intersection := 0
	for id := range a {
		if b[id] {
			intersection++
		}
	}
	union := len(a)
	for id := range b {
		if !a[id] {
			union++
		}
	}
	if union == 0 {
		return 0
	}
	return float64(intersection) / float64(union)
}
