package pipeline

import (
	"fmt"
	"log/slog"

	"github.com/colton/video-archive/internal/model"
	"github.com/colton/video-archive/internal/store"
)

// SceneMapResult describes the outcome of scene-people mapping.
type SceneMapResult struct {
	MappingCount int // total scene-person links created
	SceneCount   int
	PersonCount  int
}

// MinScenePeopleOverlapMs is the minimum segment/scene overlap (in ms)
// required to attribute a person to a scene. Segments that straddle a
// scene boundary by only a few hundred ms are artifacts of segment-merge
// time slack, not real presence — reject them.
const MinScenePeopleOverlapMs = 500

// MapScenePeople cross-references scenes with segments to populate the
// scene_people join table. Overwrites existing mappings for this recording.
func MapScenePeople(db *store.DB, recordingID int64) (*SceneMapResult, error) {
	scenes, err := db.ListScenes(recordingID)
	if err != nil {
		return nil, fmt.Errorf("listing scenes: %w", err)
	}
	if len(scenes) == 0 {
		return &SceneMapResult{}, nil
	}

	segments, err := db.ListSegments(recordingID)
	if err != nil {
		return nil, fmt.Errorf("listing segments: %w", err)
	}
	if len(segments) == 0 {
		slog.Info("no segments to map", "recording_id", recordingID)
		return &SceneMapResult{SceneCount: len(scenes)}, nil
	}

	if err := db.DeleteScenePeople(recordingID); err != nil {
		return nil, fmt.Errorf("clearing old scene_people: %w", err)
	}

	personSet := make(map[int64]bool)
	mappings := 0

	for _, scene := range scenes {
		for _, seg := range segments {
			// Check for overlap between scene and segment.
			if seg.EndMs <= scene.StartMs || seg.StartMs >= scene.EndMs {
				continue
			}

			overlapStart := max64(scene.StartMs, seg.StartMs)
			overlapEnd := min64(scene.EndMs, seg.EndMs)
			overlapMs := overlapEnd - overlapStart
			if overlapMs < MinScenePeopleOverlapMs {
				continue
			}

			err := db.InsertScenePerson(&model.ScenePerson{
				SceneID:           scene.ID,
				IdentityID:        seg.IdentityID,
				FirstAppearanceMs: overlapStart,
				TotalTimeMs:       overlapMs,
			})
			if err != nil {
				return nil, fmt.Errorf("inserting scene_person: %w", err)
			}
			personSet[seg.IdentityID] = true
			mappings++
		}
	}

	slog.Info("scene-people mapped",
		"recording_id", recordingID,
		"scenes", len(scenes),
		"people", len(personSet),
		"mappings", mappings,
	)

	return &SceneMapResult{
		MappingCount: mappings,
		SceneCount:   len(scenes),
		PersonCount:  len(personSet),
	}, nil
}

func max64(a, b int64) int64 {
	if a > b {
		return a
	}
	return b
}

func min64(a, b int64) int64 {
	if a < b {
		return a
	}
	return b
}
