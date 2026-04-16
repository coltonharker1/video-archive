package store

import (
	"fmt"

	"github.com/colton/video-archive/internal/model"
)

// InsertTrack inserts a face track.
func (db *DB) InsertTrack(t *model.Track) (int64, error) {
	result, err := db.conn.Exec(`
		INSERT INTO tracks (recording_id, start_ms, end_ms, detection_ids, avg_embedding, frame_count, confidence)
		VALUES (?, ?, ?, ?, ?, ?, ?)`,
		t.RecordingID, t.StartMs, t.EndMs, t.DetectionIDs, t.AvgEmbedding, t.FrameCount, t.Confidence,
	)
	if err != nil {
		return 0, fmt.Errorf("inserting track: %w", err)
	}
	return result.LastInsertId()
}

// CountTracks returns the number of tracks for a recording.
func (db *DB) CountTracks(recordingID int64) (int, error) {
	var count int
	err := db.conn.QueryRow(`SELECT COUNT(*) FROM tracks WHERE recording_id = ?`, recordingID).Scan(&count)
	return count, err
}

// ListTracks returns all tracks for a recording, ordered by start time.
func (db *DB) ListTracks(recordingID int64) ([]model.Track, error) {
	rows, err := db.conn.Query(`
		SELECT id, recording_id, start_ms, end_ms, detection_ids, avg_embedding, frame_count, confidence
		FROM tracks
		WHERE recording_id = ?
		ORDER BY start_ms`, recordingID)
	if err != nil {
		return nil, fmt.Errorf("listing tracks: %w", err)
	}
	defer rows.Close()

	var tracks []model.Track
	for rows.Next() {
		var t model.Track
		if err := rows.Scan(&t.ID, &t.RecordingID, &t.StartMs, &t.EndMs,
			&t.DetectionIDs, &t.AvgEmbedding, &t.FrameCount, &t.Confidence); err != nil {
			return nil, err
		}
		tracks = append(tracks, t)
	}
	return tracks, rows.Err()
}

// DeleteTracks removes all tracks for a recording (for re-tracking).
func (db *DB) DeleteTracks(recordingID int64) error {
	_, err := db.conn.Exec(`DELETE FROM tracks WHERE recording_id = ?`, recordingID)
	return err
}
