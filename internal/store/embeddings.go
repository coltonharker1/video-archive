package store

import (
	"fmt"

	"github.com/colton/video-archive/internal/model"
)

// InsertEmbedding inserts a face embedding.
func (db *DB) InsertEmbedding(e *model.Embedding) error {
	_, err := db.conn.Exec(`
		INSERT INTO embeddings (detection_id, recording_id, vector, model_used, quality)
		VALUES (?, ?, ?, ?, ?)`,
		e.DetectionID, e.RecordingID, e.Vector, e.ModelUsed, e.Quality,
	)
	if err != nil {
		return fmt.Errorf("inserting embedding: %w", err)
	}
	return nil
}

// CountEmbeddings returns the number of embeddings for a recording.
func (db *DB) CountEmbeddings(recordingID int64) (int, error) {
	var count int
	err := db.conn.QueryRow(`SELECT COUNT(*) FROM embeddings WHERE recording_id = ?`, recordingID).Scan(&count)
	return count, err
}

// GetEmbeddingByDetection returns the embedding for a specific detection.
func (db *DB) GetEmbeddingByDetection(detectionID int64) (*model.Embedding, error) {
	row := db.conn.QueryRow(`
		SELECT id, detection_id, recording_id, vector, model_used, COALESCE(quality, 0)
		FROM embeddings WHERE detection_id = ?`, detectionID)

	var e model.Embedding
	err := row.Scan(&e.ID, &e.DetectionID, &e.RecordingID, &e.Vector, &e.ModelUsed, &e.Quality)
	if err != nil {
		return nil, fmt.Errorf("getting embedding: %w", err)
	}
	return &e, nil
}

// ListEmbeddings returns all embeddings for a recording, joined with detection timestamps.
func (db *DB) ListEmbeddings(recordingID int64) ([]model.Embedding, error) {
	rows, err := db.conn.Query(`
		SELECT e.id, e.detection_id, e.recording_id, e.vector, e.model_used, COALESCE(e.quality, 0)
		FROM embeddings e
		WHERE e.recording_id = ?
		ORDER BY e.detection_id`, recordingID)
	if err != nil {
		return nil, fmt.Errorf("listing embeddings: %w", err)
	}
	defer rows.Close()

	var embs []model.Embedding
	for rows.Next() {
		var e model.Embedding
		if err := rows.Scan(&e.ID, &e.DetectionID, &e.RecordingID, &e.Vector, &e.ModelUsed, &e.Quality); err != nil {
			return nil, err
		}
		embs = append(embs, e)
	}
	return embs, rows.Err()
}
