package store

import (
	"fmt"

	"github.com/colton/video-archive/internal/model"
)

// CreateRecording inserts a new recording and returns its ID.
func (db *DB) CreateRecording(rec *model.Recording) (int64, error) {
	result, err := db.conn.Exec(`
		INSERT INTO recordings (slug, label, date, master_path, duration_ms, width, height, fps, codec, interlaced)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		rec.Slug, rec.Label, rec.Date, rec.MasterPath,
		rec.DurationMs, rec.Width, rec.Height, rec.FPS, rec.Codec, rec.Interlaced,
	)
	if err != nil {
		return 0, fmt.Errorf("inserting recording: %w", err)
	}
	return result.LastInsertId()
}

// GetRecording retrieves a recording by ID.
func (db *DB) GetRecording(id int64) (*model.Recording, error) {
	row := db.conn.QueryRow(`
		SELECT id, slug, COALESCE(label, ''), COALESCE(date, ''),
		       master_path, COALESCE(duration_ms, 0),
		       COALESCE(width, 0), COALESCE(height, 0),
		       COALESCE(fps, 0), COALESCE(codec, ''),
		       COALESCE(interlaced, FALSE)
		FROM recordings WHERE id = ?`, id)

	rec := &model.Recording{}
	err := row.Scan(
		&rec.ID, &rec.Slug, &rec.Label, &rec.Date,
		&rec.MasterPath, &rec.DurationMs,
		&rec.Width, &rec.Height, &rec.FPS, &rec.Codec, &rec.Interlaced,
	)
	if err != nil {
		return nil, fmt.Errorf("getting recording: %w", err)
	}
	return rec, nil
}

// ListRecordings returns all recordings, newest first.
func (db *DB) ListRecordings() ([]model.Recording, error) {
	rows, err := db.conn.Query(`
		SELECT id, slug, COALESCE(label, ''), COALESCE(date, ''),
		       master_path, COALESCE(duration_ms, 0),
		       COALESCE(width, 0), COALESCE(height, 0)
		FROM recordings ORDER BY date DESC, id DESC`)
	if err != nil {
		return nil, fmt.Errorf("listing recordings: %w", err)
	}
	defer rows.Close()

	var recs []model.Recording
	for rows.Next() {
		var rec model.Recording
		if err := rows.Scan(&rec.ID, &rec.Slug, &rec.Label, &rec.Date,
			&rec.MasterPath, &rec.DurationMs,
			&rec.Width, &rec.Height); err != nil {
			return nil, fmt.Errorf("scanning recording: %w", err)
		}
		recs = append(recs, rec)
	}
	return recs, rows.Err()
}

// GetRecordingIDByMasterPath looks up a recording ID by its relative master path.
func (db *DB) GetRecordingIDByMasterPath(masterPath string) (int64, error) {
	var id int64
	err := db.conn.QueryRow(`SELECT id FROM recordings WHERE master_path = ?`, masterPath).Scan(&id)
	return id, err
}
