package store

import (
	"fmt"

	"github.com/colton/video-archive/internal/model"
)

// InsertFrame inserts a frame sample record. Uses INSERT OR IGNORE to
// skip duplicates (recording_id, timestamp_ms is UNIQUE). The returned
// bool is true if a new row was written, false if an existing row with
// the same (recording_id, timestamp_ms) caused the insert to be skipped.
//
// LastInsertId is not reliable for distinguishing the ignored case on
// modernc/sqlite — it returns the *previous* successful insert's rowid
// rather than 0 when OR IGNORE skips. RowsAffected is the correct signal.
func (db *DB) InsertFrame(f *model.FrameSample) (int64, bool, error) {
	result, err := db.conn.Exec(`
		INSERT OR IGNORE INTO frames (recording_id, timestamp_ms, pass, frame_path, width, height)
		VALUES (?, ?, ?, ?, ?, ?)`,
		f.RecordingID, f.TimestampMs, f.Pass, f.FramePath, f.Width, f.Height,
	)
	if err != nil {
		return 0, false, fmt.Errorf("inserting frame: %w", err)
	}
	affected, err := result.RowsAffected()
	if err != nil {
		return 0, false, fmt.Errorf("checking rows affected: %w", err)
	}
	if affected == 0 {
		return 0, false, nil
	}
	id, err := result.LastInsertId()
	return id, true, err
}

// CountFrames returns the number of frames for a recording, optionally filtered by pass.
func (db *DB) CountFrames(recordingID int64, pass string) (int, error) {
	var count int
	var err error
	if pass == "" {
		err = db.conn.QueryRow(`SELECT COUNT(*) FROM frames WHERE recording_id = ?`, recordingID).Scan(&count)
	} else {
		err = db.conn.QueryRow(`SELECT COUNT(*) FROM frames WHERE recording_id = ? AND pass = ?`, recordingID, pass).Scan(&count)
	}
	if err != nil {
		return 0, fmt.Errorf("counting frames: %w", err)
	}
	return count, nil
}

// ListUnprocessedFrames returns frames that haven't been through face detection yet.
func (db *DB) ListUnprocessedFrames(recordingID int64) ([]model.FrameSample, error) {
	rows, err := db.conn.Query(`
		SELECT id, recording_id, timestamp_ms, pass, frame_path, COALESCE(width, 0), COALESCE(height, 0)
		FROM frames
		WHERE recording_id = ? AND processed = FALSE
		ORDER BY timestamp_ms`, recordingID)
	if err != nil {
		return nil, fmt.Errorf("listing unprocessed frames: %w", err)
	}
	defer rows.Close()

	var frames []model.FrameSample
	for rows.Next() {
		var f model.FrameSample
		if err := rows.Scan(&f.ID, &f.RecordingID, &f.TimestampMs, &f.Pass, &f.FramePath, &f.Width, &f.Height); err != nil {
			return nil, err
		}
		frames = append(frames, f)
	}
	return frames, rows.Err()
}

// MarkFrameProcessed sets processed=TRUE on a frame.
func (db *DB) MarkFrameProcessed(id int64) error {
	_, err := db.conn.Exec(`UPDATE frames SET processed = TRUE WHERE id = ?`, id)
	return err
}

// UpdateFrameDims sets the width/height of an existing frame row.
func (db *DB) UpdateFrameDims(id int64, w, h int) error {
	_, err := db.conn.Exec(`UPDATE frames SET width = ?, height = ? WHERE id = ?`, w, h, id)
	return err
}

// ListFramesMissingDims returns frames whose width or height is 0/NULL so they
// can be backfilled from the stored image files.
func (db *DB) ListFramesMissingDims() ([]model.FrameSample, error) {
	rows, err := db.conn.Query(`
		SELECT id, recording_id, timestamp_ms, pass, frame_path, COALESCE(width, 0), COALESCE(height, 0)
		FROM frames
		WHERE width IS NULL OR width = 0 OR height IS NULL OR height = 0
		ORDER BY id`)
	if err != nil {
		return nil, fmt.Errorf("listing frames missing dims: %w", err)
	}
	defer rows.Close()

	var frames []model.FrameSample
	for rows.Next() {
		var f model.FrameSample
		if err := rows.Scan(&f.ID, &f.RecordingID, &f.TimestampMs, &f.Pass, &f.FramePath, &f.Width, &f.Height); err != nil {
			return nil, err
		}
		frames = append(frames, f)
	}
	return frames, rows.Err()
}

// ListFrames returns all frames for a recording.
func (db *DB) ListFrames(recordingID int64) ([]model.FrameSample, error) {
	rows, err := db.conn.Query(`
		SELECT id, recording_id, timestamp_ms, pass, frame_path, COALESCE(width, 0), COALESCE(height, 0)
		FROM frames
		WHERE recording_id = ?
		ORDER BY timestamp_ms`, recordingID)
	if err != nil {
		return nil, fmt.Errorf("listing frames: %w", err)
	}
	defer rows.Close()

	var frames []model.FrameSample
	for rows.Next() {
		var f model.FrameSample
		if err := rows.Scan(&f.ID, &f.RecordingID, &f.TimestampMs, &f.Pass, &f.FramePath, &f.Width, &f.Height); err != nil {
			return nil, err
		}
		frames = append(frames, f)
	}
	return frames, rows.Err()
}
