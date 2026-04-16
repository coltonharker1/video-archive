package pipeline

import (
	"fmt"
	"sort"

	"github.com/colton/video-archive/internal/model"
	"github.com/colton/video-archive/internal/store"
)

// GroupReportOptions controls group report generation.
type GroupReportOptions struct {
	// SceneGapMs is the "within-scene" merge threshold. Segments from any
	// group member closer than this are merged into a single scene segment.
	// Default 20s — aggressive enough to join shots within a conversation,
	// conservative enough to distinguish genuinely different scenes.
	SceneGapMs int64

	// RecordingID optionally limits the report to a single recording.
	// 0 = all recordings.
	RecordingID int64
}

// DefaultGroupReportOptions returns sensible defaults.
func DefaultGroupReportOptions() GroupReportOptions {
	return GroupReportOptions{
		SceneGapMs: 20000,
	}
}

// GroupScene is a merged time range within a single recording showing which
// group members appeared in it.
type GroupScene struct {
	RecordingID   int64
	RecordingSlug string
	RecordingDate string
	StartMs       int64
	EndMs         int64
	DurationMs    int64
	Members       []string // names of members who appeared
	MemberIDs     []int64
	Confidence    float64 // average confidence of source segments
}

// GroupReport is the full cross-archive view of a group's appearances.
type GroupReport struct {
	GroupID     int64
	GroupName   string
	MemberCount int
	Scenes      []GroupScene
	TotalMs     int64
	VideoCount  int
}

// GenerateGroupReport computes group-level appearance scenes by taking every
// confirmed segment for any member of the group, then merging segments that
// fall within SceneGapMs of each other (per-recording).
//
// The result is computed on the fly — no new tables. Source of truth stays
// in the per-identity `segments` table.
func GenerateGroupReport(db *store.DB, groupID int64, opts GroupReportOptions) (*GroupReport, error) {
	group, err := db.GetGroup(groupID)
	if err != nil {
		return nil, fmt.Errorf("loading group: %w", err)
	}

	members, err := db.ListGroupMembers(groupID)
	if err != nil {
		return nil, fmt.Errorf("loading group members: %w", err)
	}
	if len(members) == 0 {
		return &GroupReport{
			GroupID:     groupID,
			GroupName:   group.Name,
			MemberCount: 0,
		}, nil
	}

	memberIDSet := make(map[int64]bool, len(members))
	memberNames := make(map[int64]string, len(members))
	for _, m := range members {
		memberIDSet[m.ID] = true
		memberNames[m.ID] = m.Name
	}

	// Gather all segments for all members, grouped by recording
	recordingSegments, err := collectSegmentsByRecording(db, memberIDSet, opts.RecordingID)
	if err != nil {
		return nil, fmt.Errorf("collecting segments: %w", err)
	}

	if len(recordingSegments) == 0 {
		return &GroupReport{
			GroupID:     groupID,
			GroupName:   group.Name,
			MemberCount: len(members),
		}, nil
	}

	// For each recording, merge overlapping/nearby member segments into scenes
	var scenes []GroupScene
	var totalMs int64

	for recID, segs := range recordingSegments {
		rec, err := db.GetRecording(recID)
		if err != nil {
			continue
		}

		mergedScenes := mergeMemberSegmentsIntoScenes(segs, opts.SceneGapMs, memberNames)
		for i := range mergedScenes {
			mergedScenes[i].RecordingID = recID
			mergedScenes[i].RecordingSlug = rec.Slug
			mergedScenes[i].RecordingDate = rec.Date
			scenes = append(scenes, mergedScenes[i])
			totalMs += mergedScenes[i].DurationMs
		}
	}

	// Sort scenes by recording date, then by start time within recording
	sort.Slice(scenes, func(i, j int) bool {
		if scenes[i].RecordingDate != scenes[j].RecordingDate {
			return scenes[i].RecordingDate < scenes[j].RecordingDate
		}
		if scenes[i].RecordingID != scenes[j].RecordingID {
			return scenes[i].RecordingID < scenes[j].RecordingID
		}
		return scenes[i].StartMs < scenes[j].StartMs
	})

	// Count distinct videos
	videoSet := make(map[int64]bool)
	for _, s := range scenes {
		videoSet[s.RecordingID] = true
	}

	return &GroupReport{
		GroupID:     groupID,
		GroupName:   group.Name,
		MemberCount: len(members),
		Scenes:      scenes,
		TotalMs:     totalMs,
		VideoCount:  len(videoSet),
	}, nil
}

// memberSegment is a segment tagged with which member it came from, used
// during scene merging.
type memberSegment struct {
	identityID int64
	startMs    int64
	endMs      int64
	confidence float64
}

// collectSegmentsByRecording groups all segments belonging to any member of the
// provided identity set, bucketed by recording ID.
func collectSegmentsByRecording(db *store.DB, memberIDs map[int64]bool, recordingFilter int64) (map[int64][]memberSegment, error) {
	// Iterate recordings and their segments. Could query directly by identity
	// but this reuses existing store methods. For a personal archive (dozens
	// of videos) the iteration cost is negligible.
	var recs []model.Recording
	if recordingFilter > 0 {
		rec, err := db.GetRecording(recordingFilter)
		if err != nil {
			return nil, err
		}
		recs = []model.Recording{*rec}
	} else {
		all, err := db.ListRecordings()
		if err != nil {
			return nil, err
		}
		recs = all
	}

	result := make(map[int64][]memberSegment)
	for _, rec := range recs {
		segs, err := db.ListSegments(rec.ID)
		if err != nil {
			continue
		}
		for _, s := range segs {
			if !memberIDs[s.IdentityID] {
				continue
			}
			result[rec.ID] = append(result[rec.ID], memberSegment{
				identityID: s.IdentityID,
				startMs:    s.StartMs,
				endMs:      s.EndMs,
				confidence: s.Confidence,
			})
		}
	}
	return result, nil
}

// mergeMemberSegmentsIntoScenes takes all member segments from a single recording
// and merges them into scene segments using the sceneGapMs threshold. Each
// output scene tracks which distinct members contributed to it.
func mergeMemberSegmentsIntoScenes(segs []memberSegment, sceneGapMs int64, memberNames map[int64]string) []GroupScene {
	if len(segs) == 0 {
		return nil
	}

	// Sort by start time
	sort.Slice(segs, func(i, j int) bool {
		return segs[i].startMs < segs[j].startMs
	})

	type openScene struct {
		startMs    int64
		endMs      int64
		memberIDs  map[int64]bool
		totalConf  float64
		segCount   int
	}

	newScene := func(s memberSegment) openScene {
		return openScene{
			startMs:   s.startMs,
			endMs:     s.endMs,
			memberIDs: map[int64]bool{s.identityID: true},
			totalConf: s.confidence,
			segCount:  1,
		}
	}

	current := newScene(segs[0])
	var scenes []openScene

	for i := 1; i < len(segs); i++ {
		s := segs[i]
		// If this segment starts within the scene gap of the current scene's
		// end, extend the scene. Otherwise close it and start a new one.
		if s.startMs <= current.endMs+sceneGapMs {
			if s.endMs > current.endMs {
				current.endMs = s.endMs
			}
			current.memberIDs[s.identityID] = true
			current.totalConf += s.confidence
			current.segCount++
		} else {
			scenes = append(scenes, current)
			current = newScene(s)
		}
	}
	scenes = append(scenes, current)

	// Finalize to GroupScene
	out := make([]GroupScene, 0, len(scenes))
	for _, sc := range scenes {
		memberIDList := make([]int64, 0, len(sc.memberIDs))
		memberNameList := make([]string, 0, len(sc.memberIDs))
		for id := range sc.memberIDs {
			memberIDList = append(memberIDList, id)
			memberNameList = append(memberNameList, memberNames[id])
		}
		sort.Strings(memberNameList)
		sort.Slice(memberIDList, func(i, j int) bool { return memberIDList[i] < memberIDList[j] })

		avgConf := sc.totalConf / float64(sc.segCount)
		out = append(out, GroupScene{
			StartMs:    sc.startMs,
			EndMs:      sc.endMs,
			DurationMs: sc.endMs - sc.startMs,
			MemberIDs:  memberIDList,
			Members:    memberNameList,
			Confidence: avgConf,
		})
	}
	return out
}
