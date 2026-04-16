package pipeline

import (
	"context"
	"fmt"
	"log/slog"

	"github.com/colton/video-archive/internal/config"
	"github.com/colton/video-archive/internal/store"
	"github.com/colton/video-archive/internal/tracking"
)

// TrackResult describes the outcome of face tracking.
type TrackResult struct {
	TrackCount int
	Skipped    bool
}

// TrackFaces builds face tracks from detections and embeddings.
// Pure Go — no ML worker needed.
func TrackFaces(_ context.Context, _ config.Config, db *store.DB, recordingID int64) (*TrackResult, error) {
	// Check if tracks already exist
	existing, err := db.CountTracks(recordingID)
	if err != nil {
		return nil, fmt.Errorf("counting tracks: %w", err)
	}
	if existing > 0 {
		slog.Info("tracks already exist, skipping", "recording_id", recordingID, "count", existing)
		return &TrackResult{TrackCount: existing, Skipped: true}, nil
	}

	// Load detections
	detections, err := db.ListDetections(recordingID)
	if err != nil {
		return nil, fmt.Errorf("listing detections: %w", err)
	}
	if len(detections) == 0 {
		slog.Info("no detections found", "recording_id", recordingID)
		return &TrackResult{TrackCount: 0}, nil
	}

	// Load embeddings into a map keyed by detection ID
	embList, err := db.ListEmbeddings(recordingID)
	if err != nil {
		return nil, fmt.Errorf("listing embeddings: %w", err)
	}
	embMap := make(map[int64][]float64, len(embList))
	for _, e := range embList {
		embMap[e.DetectionID] = bytesToFloat64s(e.Vector)
	}

	// Build tracks
	cfg := tracking.DefaultConfig()
	tracks := tracking.BuildTracks(recordingID, detections, embMap, cfg)

	// Merge close tracks
	tracks = tracking.MergeTracks(tracks, cfg.MaxGapMs, 0.7)

	// Save to DB
	for _, t := range tracks {
		if _, err := db.InsertTrack(&t); err != nil {
			return nil, fmt.Errorf("inserting track: %w", err)
		}
	}

	slog.Info("tracking complete",
		"recording_id", recordingID,
		"detections", len(detections),
		"tracks", len(tracks),
	)

	return &TrackResult{TrackCount: len(tracks)}, nil
}
