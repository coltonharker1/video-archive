package store

import (
	"fmt"

	"github.com/colton/video-archive/internal/model"
)

// InsertCluster inserts a face cluster.
func (db *DB) InsertCluster(c *model.Cluster) (int64, error) {
	result, err := db.conn.Exec(`
		INSERT INTO clusters (recording_id, track_ids, centroid_emb, thumbnail_path, identity_id, status)
		VALUES (?, ?, ?, ?, ?, ?)`,
		c.RecordingID, c.TrackIDs, c.CentroidEmb, c.ThumbnailPath, c.IdentityID, c.Status,
	)
	if err != nil {
		return 0, fmt.Errorf("inserting cluster: %w", err)
	}
	return result.LastInsertId()
}

// CountClusters returns the number of clusters for a recording.
func (db *DB) CountClusters(recordingID int64) (int, error) {
	var count int
	err := db.conn.QueryRow(`SELECT COUNT(*) FROM clusters WHERE recording_id = ?`, recordingID).Scan(&count)
	return count, err
}

// ListClusters returns all clusters for a recording.
func (db *DB) ListClusters(recordingID int64) ([]model.Cluster, error) {
	rows, err := db.conn.Query(`
		SELECT id, recording_id, track_ids, centroid_emb, COALESCE(thumbnail_path, ''),
		       identity_id, status
		FROM clusters
		WHERE recording_id = ?
		ORDER BY id`, recordingID)
	if err != nil {
		return nil, fmt.Errorf("listing clusters: %w", err)
	}
	defer rows.Close()

	var clusters []model.Cluster
	for rows.Next() {
		var c model.Cluster
		if err := rows.Scan(&c.ID, &c.RecordingID, &c.TrackIDs, &c.CentroidEmb,
			&c.ThumbnailPath, &c.IdentityID, &c.Status); err != nil {
			return nil, err
		}
		clusters = append(clusters, c)
	}
	return clusters, rows.Err()
}

// DeleteClusters removes all clusters for a recording (for re-clustering).
func (db *DB) DeleteClusters(recordingID int64) error {
	_, err := db.conn.Exec(`DELETE FROM clusters WHERE recording_id = ?`, recordingID)
	return err
}

// UpdateClusterIdentity assigns an identity to a cluster.
func (db *DB) UpdateClusterIdentity(clusterID, identityID int64) error {
	_, err := db.conn.Exec(`
		UPDATE clusters SET identity_id = ?, status = 'confirmed' WHERE id = ?`,
		identityID, clusterID)
	return err
}

// UpdateClusterStatus updates the status of a cluster.
func (db *DB) UpdateClusterStatus(clusterID int64, status string) error {
	_, err := db.conn.Exec(`UPDATE clusters SET status = ? WHERE id = ?`, status, clusterID)
	return err
}

// GetClusterRecordingID returns the recording_id for a cluster, or 0 if the
// cluster is cross-video (recording_id IS NULL).
func (db *DB) GetClusterRecordingID(clusterID int64) (int64, error) {
	var rid *int64
	err := db.conn.QueryRow(`SELECT recording_id FROM clusters WHERE id = ?`, clusterID).Scan(&rid)
	if err != nil {
		return 0, fmt.Errorf("getting cluster recording: %w", err)
	}
	if rid == nil {
		return 0, nil
	}
	return *rid, nil
}
