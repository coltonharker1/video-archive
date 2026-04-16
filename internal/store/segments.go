package store

import (
	"fmt"
)

// Segment represents a continuous appearance of a person in a recording.
type Segment struct {
	ID          int64
	RecordingID int64
	IdentityID  int64
	StartMs     int64
	EndMs       int64
	Confidence  float64
}

// InsertSegment inserts a segment.
func (db *DB) InsertSegment(s *Segment) (int64, error) {
	result, err := db.conn.Exec(`
		INSERT INTO segments (recording_id, identity_id, start_ms, end_ms, confidence)
		VALUES (?, ?, ?, ?, ?)`,
		s.RecordingID, s.IdentityID, s.StartMs, s.EndMs, s.Confidence,
	)
	if err != nil {
		return 0, fmt.Errorf("inserting segment: %w", err)
	}
	return result.LastInsertId()
}

// ListSegments returns all segments for a recording, ordered by identity then start time.
func (db *DB) ListSegments(recordingID int64) ([]Segment, error) {
	rows, err := db.conn.Query(`
		SELECT s.id, s.recording_id, s.identity_id, s.start_ms, s.end_ms, COALESCE(s.confidence, 0)
		FROM segments s
		WHERE s.recording_id = ?
		ORDER BY s.identity_id, s.start_ms`, recordingID)
	if err != nil {
		return nil, fmt.Errorf("listing segments: %w", err)
	}
	defer rows.Close()

	var segs []Segment
	for rows.Next() {
		var s Segment
		if err := rows.Scan(&s.ID, &s.RecordingID, &s.IdentityID, &s.StartMs, &s.EndMs, &s.Confidence); err != nil {
			return nil, err
		}
		segs = append(segs, s)
	}
	return segs, rows.Err()
}

// CountSegments returns the number of segments for a recording.
func (db *DB) CountSegments(recordingID int64) (int, error) {
	var count int
	err := db.conn.QueryRow(`SELECT COUNT(*) FROM segments WHERE recording_id = ?`, recordingID).Scan(&count)
	return count, err
}

// DeleteSegments removes all segments for a recording (for regeneration).
func (db *DB) DeleteSegments(recordingID int64) error {
	_, err := db.conn.Exec(`DELETE FROM segments WHERE recording_id = ?`, recordingID)
	return err
}
