package store

import (
	"fmt"

	"github.com/colton/video-archive/internal/model"
)

// ClusterWithRecording is a cluster joined with its recording's slug and date.
type ClusterWithRecording struct {
	model.Cluster
	RecordingSlug string
	RecordingDate string
}

// ListClustersForIdentity returns every cluster linked to an identity across all
// recordings, joined with the recording's slug and date for display.
func (db *DB) ListClustersForIdentity(identityID int64) ([]ClusterWithRecording, error) {
	rows, err := db.conn.Query(`
		SELECT c.id, c.recording_id, c.track_ids, c.centroid_emb,
		       COALESCE(c.thumbnail_path, ''),
		       c.identity_id, c.status,
		       r.slug, COALESCE(r.date, '')
		FROM clusters c
		INNER JOIN recordings r ON r.id = c.recording_id
		WHERE c.identity_id = ?
		ORDER BY r.date DESC, r.id DESC, c.id`, identityID)
	if err != nil {
		return nil, fmt.Errorf("listing clusters for identity: %w", err)
	}
	defer rows.Close()

	var out []ClusterWithRecording
	for rows.Next() {
		var cw ClusterWithRecording
		if err := rows.Scan(
			&cw.ID, &cw.RecordingID, &cw.TrackIDs, &cw.CentroidEmb,
			&cw.ThumbnailPath, &cw.IdentityID, &cw.Status,
			&cw.RecordingSlug, &cw.RecordingDate,
		); err != nil {
			return nil, err
		}
		out = append(out, cw)
	}
	return out, rows.Err()
}

// SegmentWithRecording is a segment joined with its recording's slug and date.
type SegmentWithRecording struct {
	Segment
	RecordingSlug string
	RecordingDate string
}

// ListSegmentsForIdentity returns every segment for an identity across all
// recordings, joined with the recording's slug and date.
func (db *DB) ListSegmentsForIdentity(identityID int64) ([]SegmentWithRecording, error) {
	rows, err := db.conn.Query(`
		SELECT s.id, s.recording_id, s.identity_id, s.start_ms, s.end_ms,
		       COALESCE(s.confidence, 0),
		       r.slug, COALESCE(r.date, '')
		FROM segments s
		INNER JOIN recordings r ON r.id = s.recording_id
		WHERE s.identity_id = ?
		ORDER BY r.date DESC, r.id DESC, s.start_ms`, identityID)
	if err != nil {
		return nil, fmt.Errorf("listing segments for identity: %w", err)
	}
	defer rows.Close()

	var out []SegmentWithRecording
	for rows.Next() {
		var sw SegmentWithRecording
		if err := rows.Scan(
			&sw.ID, &sw.RecordingID, &sw.IdentityID, &sw.StartMs, &sw.EndMs,
			&sw.Confidence,
			&sw.RecordingSlug, &sw.RecordingDate,
		); err != nil {
			return nil, err
		}
		out = append(out, sw)
	}
	return out, rows.Err()
}

// DetachClusterFromIdentity unlinks a cluster from its identity and flips it
// back to 'pending' so it reappears in the per-recording review flow. Leaves
// the segments table alone — callers regenerate segments for the affected
// recording after mutation.
func (db *DB) DetachClusterFromIdentity(clusterID int64) error {
	_, err := db.conn.Exec(`
		UPDATE clusters SET identity_id = NULL, status = 'pending' WHERE id = ?`, clusterID)
	if err != nil {
		return fmt.Errorf("detaching cluster: %w", err)
	}
	return nil
}

// ListRecordingIDsForIdentity returns every recording_id where the identity
// has either a cluster or a segment. Used to know which recordings to
// regenerate segments for after identity-level mutations (merge, delete).
func (db *DB) ListRecordingIDsForIdentity(identityID int64) ([]int64, error) {
	rows, err := db.conn.Query(`
		SELECT DISTINCT recording_id FROM clusters
		WHERE identity_id = ? AND recording_id IS NOT NULL
		UNION
		SELECT DISTINCT recording_id FROM segments WHERE identity_id = ?`,
		identityID, identityID)
	if err != nil {
		return nil, fmt.Errorf("listing recordings for identity: %w", err)
	}
	defer rows.Close()

	var out []int64
	for rows.Next() {
		var rid int64
		if err := rows.Scan(&rid); err != nil {
			return nil, err
		}
		out = append(out, rid)
	}
	return out, rows.Err()
}
