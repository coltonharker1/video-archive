package store

import (
	"fmt"

	"github.com/colton/video-archive/internal/model"
)

// InsertDetection inserts a face detection and returns its ID.
func (db *DB) InsertDetection(d *model.Detection) (int64, error) {
	result, err := db.conn.Exec(`
		INSERT INTO detections (frame_id, recording_id, timestamp_ms, bbox_x, bbox_y, bbox_w, bbox_h, confidence, landmarks, crop_path)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		d.FrameID, d.RecordingID, d.TimestampMs,
		d.BboxX, d.BboxY, d.BboxW, d.BboxH,
		d.Confidence, d.Landmarks, d.CropPath,
	)
	if err != nil {
		return 0, fmt.Errorf("inserting detection: %w", err)
	}
	return result.LastInsertId()
}

// CountDetections returns the number of detections for a recording.
func (db *DB) CountDetections(recordingID int64) (int, error) {
	var count int
	err := db.conn.QueryRow(`SELECT COUNT(*) FROM detections WHERE recording_id = ?`, recordingID).Scan(&count)
	return count, err
}

// ListDetections returns all detections for a recording, ordered by timestamp.
func (db *DB) ListDetections(recordingID int64) ([]model.Detection, error) {
	rows, err := db.conn.Query(`
		SELECT id, frame_id, recording_id, timestamp_ms,
		       bbox_x, bbox_y, bbox_w, bbox_h,
		       confidence, COALESCE(landmarks, ''), COALESCE(crop_path, '')
		FROM detections
		WHERE recording_id = ?
		ORDER BY timestamp_ms`, recordingID)
	if err != nil {
		return nil, fmt.Errorf("listing detections: %w", err)
	}
	defer rows.Close()

	var dets []model.Detection
	for rows.Next() {
		var d model.Detection
		if err := rows.Scan(&d.ID, &d.FrameID, &d.RecordingID, &d.TimestampMs,
			&d.BboxX, &d.BboxY, &d.BboxW, &d.BboxH,
			&d.Confidence, &d.Landmarks, &d.CropPath); err != nil {
			return nil, err
		}
		dets = append(dets, d)
	}
	return dets, rows.Err()
}
