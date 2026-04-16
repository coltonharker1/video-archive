package store

import (
	"fmt"

	"github.com/colton/video-archive/internal/model"
)

// CreateIdentity inserts a named identity and returns its ID.
func (db *DB) CreateIdentity(name string) (int64, error) {
	result, err := db.conn.Exec(`
		INSERT INTO identities (name) VALUES (?)`, name)
	if err != nil {
		return 0, fmt.Errorf("creating identity: %w", err)
	}
	return result.LastInsertId()
}

// GetIdentity retrieves an identity by ID.
func (db *DB) GetIdentity(id int64) (*model.Identity, error) {
	row := db.conn.QueryRow(`
		SELECT id, name, COALESCE(reference_embs, ''), COALESCE(thumbnail_path, ''),
		       COALESCE(notes, ''), created_at
		FROM identities WHERE id = ?`, id)

	var i model.Identity
	var createdAt string
	err := row.Scan(&i.ID, &i.Name, &i.ReferenceEmbs, &i.ThumbnailPath, &i.Notes, &createdAt)
	if err != nil {
		return nil, fmt.Errorf("getting identity: %w", err)
	}
	return &i, nil
}

// ListIdentities returns all identities.
func (db *DB) ListIdentities() ([]model.Identity, error) {
	rows, err := db.conn.Query(`
		SELECT id, name, COALESCE(thumbnail_path, ''), COALESCE(notes, '')
		FROM identities ORDER BY name`)
	if err != nil {
		return nil, fmt.Errorf("listing identities: %w", err)
	}
	defer rows.Close()

	var identities []model.Identity
	for rows.Next() {
		var i model.Identity
		if err := rows.Scan(&i.ID, &i.Name, &i.ThumbnailPath, &i.Notes); err != nil {
			return nil, err
		}
		identities = append(identities, i)
	}
	return identities, rows.Err()
}

// FindIdentityByName looks up an identity by exact name. Returns nil if not found.
func (db *DB) FindIdentityByName(name string) (*model.Identity, error) {
	row := db.conn.QueryRow(`SELECT id, name FROM identities WHERE name = ?`, name)
	var i model.Identity
	err := row.Scan(&i.ID, &i.Name)
	if err != nil {
		return nil, err
	}
	return &i, nil
}

// IdentityStats holds aggregated stats for one identity across the archive.
type IdentityStats struct {
	ID            int64
	Name          string
	ClusterCount  int
	VideoCount    int
	SegmentCount  int
	TotalTimeMs   int64
	ThumbnailPath string // best confirmed-cluster thumbnail
}

// ListIdentitiesWithStats returns all identities with aggregated counts from
// confirmed clusters and segments. Used by the identity management UI.
func (db *DB) ListIdentitiesWithStats() ([]IdentityStats, error) {
	// One query per metric keeps it readable and fast enough for a personal archive.
	identities, err := db.ListIdentities()
	if err != nil {
		return nil, err
	}

	result := make([]IdentityStats, 0, len(identities))
	for _, ident := range identities {
		stats := IdentityStats{
			ID:            ident.ID,
			Name:          ident.Name,
			ThumbnailPath: ident.ThumbnailPath,
		}

		// Cluster count + distinct video count + best thumbnail
		row := db.conn.QueryRow(`
			SELECT COUNT(*), COUNT(DISTINCT recording_id)
			FROM clusters WHERE identity_id = ? AND status = 'confirmed'`, ident.ID)
		_ = row.Scan(&stats.ClusterCount, &stats.VideoCount)

		// Segment count + total time
		row2 := db.conn.QueryRow(`
			SELECT COUNT(*), COALESCE(SUM(end_ms - start_ms), 0)
			FROM segments WHERE identity_id = ?`, ident.ID)
		_ = row2.Scan(&stats.SegmentCount, &stats.TotalTimeMs)

		// If the identity has no saved thumbnail, pick the best confirmed cluster's thumbnail
		if stats.ThumbnailPath == "" {
			_ = db.conn.QueryRow(`
				SELECT COALESCE(thumbnail_path, '')
				FROM clusters
				WHERE identity_id = ? AND status = 'confirmed' AND thumbnail_path IS NOT NULL AND thumbnail_path != ''
				ORDER BY id
				LIMIT 1`, ident.ID).Scan(&stats.ThumbnailPath)
		}

		result = append(result, stats)
	}
	return result, nil
}

// RenameIdentity updates an identity's name.
func (db *DB) RenameIdentity(id int64, newName string) error {
	_, err := db.conn.Exec(`
		UPDATE identities SET name = ?, updated_at = datetime('now') WHERE id = ?`,
		newName, id)
	if err != nil {
		return fmt.Errorf("renaming identity: %w", err)
	}
	return nil
}

// MergeIdentities merges srcID into dstID: reassigns all clusters and segments
// from src to dst, then deletes src. Non-destructive wrt the underlying clusters
// (they just point to a different identity_id afterwards).
func (db *DB) MergeIdentities(dstID, srcID int64) error {
	if dstID == srcID {
		return fmt.Errorf("cannot merge identity with itself")
	}

	tx, err := db.conn.Begin()
	if err != nil {
		return fmt.Errorf("begin tx: %w", err)
	}
	defer tx.Rollback()

	// Reassign clusters
	if _, err := tx.Exec(`UPDATE clusters SET identity_id = ? WHERE identity_id = ?`, dstID, srcID); err != nil {
		return fmt.Errorf("reassigning clusters: %w", err)
	}

	// Reassign segments
	if _, err := tx.Exec(`UPDATE segments SET identity_id = ? WHERE identity_id = ?`, dstID, srcID); err != nil {
		return fmt.Errorf("reassigning segments: %w", err)
	}

	// Move group memberships (preserving dst's existing memberships, dropping duplicates)
	if _, err := tx.Exec(`
		INSERT OR IGNORE INTO group_members (group_id, identity_id)
		SELECT group_id, ? FROM group_members WHERE identity_id = ?`, dstID, srcID); err != nil {
		return fmt.Errorf("merging group memberships: %w", err)
	}
	if _, err := tx.Exec(`DELETE FROM group_members WHERE identity_id = ?`, srcID); err != nil {
		return fmt.Errorf("removing old group memberships: %w", err)
	}

	// Delete the src identity
	if _, err := tx.Exec(`DELETE FROM identities WHERE id = ?`, srcID); err != nil {
		return fmt.Errorf("deleting src identity: %w", err)
	}

	return tx.Commit()
}

// DeleteIdentity removes an identity and detaches all clusters/segments from it.
// Clusters revert to status='pending' with identity_id=NULL so they reappear
// in the review flow.
func (db *DB) DeleteIdentity(id int64) error {
	tx, err := db.conn.Begin()
	if err != nil {
		return fmt.Errorf("begin tx: %w", err)
	}
	defer tx.Rollback()

	if _, err := tx.Exec(`
		UPDATE clusters SET identity_id = NULL, status = 'pending' WHERE identity_id = ?`, id); err != nil {
		return fmt.Errorf("detaching clusters: %w", err)
	}
	if _, err := tx.Exec(`DELETE FROM segments WHERE identity_id = ?`, id); err != nil {
		return fmt.Errorf("deleting segments: %w", err)
	}
	if _, err := tx.Exec(`DELETE FROM group_members WHERE identity_id = ?`, id); err != nil {
		return fmt.Errorf("removing group memberships: %w", err)
	}
	if _, err := tx.Exec(`DELETE FROM identities WHERE id = ?`, id); err != nil {
		return fmt.Errorf("deleting identity: %w", err)
	}
	return tx.Commit()
}

// ListGroupsForIdentity returns the groups an identity belongs to.
func (db *DB) ListGroupsForIdentity(identityID int64) ([]model.GroupSummary, error) {
	rows, err := db.conn.Query(`
		SELECT g.id, g.name, COALESCE(g.notes, ''),
		       (SELECT COUNT(*) FROM group_members gm2 WHERE gm2.group_id = g.id) AS member_count
		FROM groups g
		INNER JOIN group_members gm ON gm.group_id = g.id
		WHERE gm.identity_id = ?
		ORDER BY g.name`, identityID)
	if err != nil {
		return nil, fmt.Errorf("listing groups for identity: %w", err)
	}
	defer rows.Close()

	var groups []model.GroupSummary
	for rows.Next() {
		var g model.GroupSummary
		if err := rows.Scan(&g.ID, &g.Name, &g.Notes, &g.MemberCount); err != nil {
			return nil, err
		}
		groups = append(groups, g)
	}
	return groups, rows.Err()
}

// RenameGroup updates a group's name.
func (db *DB) RenameGroup(id int64, newName string) error {
	_, err := db.conn.Exec(`UPDATE groups SET name = ? WHERE id = ?`, newName, id)
	return err
}

// MergeClusters merges srcClusterID into dstClusterID by moving all track IDs
// from src to dst, then deleting src.
func (db *DB) MergeClusters(dstID, srcID int64) error {
	// Get both clusters' track IDs
	var dstTracks, srcTracks string
	if err := db.conn.QueryRow(`SELECT track_ids FROM clusters WHERE id = ?`, dstID).Scan(&dstTracks); err != nil {
		return fmt.Errorf("getting dst cluster: %w", err)
	}
	if err := db.conn.QueryRow(`SELECT track_ids FROM clusters WHERE id = ?`, srcID).Scan(&srcTracks); err != nil {
		return fmt.Errorf("getting src cluster: %w", err)
	}

	// Parse and merge JSON arrays
	// Simple approach: combine the raw JSON arrays
	// "[1,2]" + "[3,4]" -> "[1,2,3,4]"
	merged := dstTracks[:len(dstTracks)-1] + "," + srcTracks[1:]

	if _, err := db.conn.Exec(`UPDATE clusters SET track_ids = ? WHERE id = ?`, merged, dstID); err != nil {
		return fmt.Errorf("updating dst cluster: %w", err)
	}
	if _, err := db.conn.Exec(`DELETE FROM clusters WHERE id = ?`, srcID); err != nil {
		return fmt.Errorf("deleting src cluster: %w", err)
	}
	return nil
}
