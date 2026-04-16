package review

import (
	"log/slog"

	"github.com/colton/video-archive/internal/pipeline"
)

// regenerateSegments rebuilds segments and scene-people mappings for every
// given recording ID, dedupes, and logs (but does not fail the request on)
// errors. Called after any cluster/identity mutation so segments and scene
// attributions stay fresh without manual re-runs.
func (s *Server) regenerateSegments(recIDs ...int64) {
	seen := make(map[int64]bool, len(recIDs))
	opts := pipeline.DefaultSegmentOptions()
	for _, id := range recIDs {
		if id == 0 || seen[id] {
			continue
		}
		seen[id] = true
		if _, err := pipeline.GenerateSegments(s.cfg, s.db, id, opts); err != nil {
			slog.Error("auto-regen segments failed", "recording_id", id, "err", err)
			continue
		}
		// Only run scene merge + map if scenes exist for this recording.
		if count, _ := s.db.CountScenes(id); count > 0 {
			pipeline.MergeScenesByPeople(s.db, id, pipeline.DefaultSceneMergeOptions())
			if _, err := pipeline.MapScenePeople(s.db, id); err != nil {
				slog.Error("auto-regen scene-people failed", "recording_id", id, "err", err)
			}
		}
	}
}
