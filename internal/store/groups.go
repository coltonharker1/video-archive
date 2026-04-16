package store

import (
	"database/sql"
	"fmt"

	"github.com/colton/video-archive/internal/model"
)

// CreateGroup inserts a new group and returns its ID.
func (db *DB) CreateGroup(name string) (int64, error) {
	result, err := db.conn.Exec(`INSERT INTO groups (name) VALUES (?)`, name)
	if err != nil {
		return 0, fmt.Errorf("creating group: %w", err)
	}
	return result.LastInsertId()
}

// GetGroup retrieves a group by ID.
func (db *DB) GetGroup(id int64) (*model.Group, error) {
	row := db.conn.QueryRow(`
		SELECT id, name, COALESCE(notes, '')
		FROM groups WHERE id = ?`, id)

	var g model.Group
	if err := row.Scan(&g.ID, &g.Name, &g.Notes); err != nil {
		return nil, fmt.Errorf("getting group: %w", err)
	}
	return &g, nil
}

// FindGroupByName looks up a group by exact name.
func (db *DB) FindGroupByName(name string) (*model.Group, error) {
	row := db.conn.QueryRow(`
		SELECT id, name, COALESCE(notes, '')
		FROM groups WHERE name = ?`, name)

	var g model.Group
	err := row.Scan(&g.ID, &g.Name, &g.Notes)
	if err != nil {
		return nil, err
	}
	return &g, nil
}

// ListGroups returns all groups with their member counts.
func (db *DB) ListGroups() ([]model.GroupSummary, error) {
	rows, err := db.conn.Query(`
		SELECT g.id, g.name, COALESCE(g.notes, ''),
		       (SELECT COUNT(*) FROM group_members gm WHERE gm.group_id = g.id) AS member_count
		FROM groups g
		ORDER BY g.name`)
	if err != nil {
		return nil, fmt.Errorf("listing groups: %w", err)
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

// DeleteGroup removes a group (CASCADE removes its members).
func (db *DB) DeleteGroup(id int64) error {
	_, err := db.conn.Exec(`DELETE FROM groups WHERE id = ?`, id)
	return err
}

// AddGroupMember links an identity to a group. Idempotent — ignores duplicates.
func (db *DB) AddGroupMember(groupID, identityID int64) error {
	_, err := db.conn.Exec(`
		INSERT OR IGNORE INTO group_members (group_id, identity_id) VALUES (?, ?)`,
		groupID, identityID)
	return err
}

// RemoveGroupMember unlinks an identity from a group.
func (db *DB) RemoveGroupMember(groupID, identityID int64) error {
	_, err := db.conn.Exec(`
		DELETE FROM group_members WHERE group_id = ? AND identity_id = ?`,
		groupID, identityID)
	return err
}

// ListGroupMembers returns all identities in a group.
func (db *DB) ListGroupMembers(groupID int64) ([]model.Identity, error) {
	rows, err := db.conn.Query(`
		SELECT i.id, i.name, COALESCE(i.thumbnail_path, ''), COALESCE(i.notes, '')
		FROM identities i
		INNER JOIN group_members gm ON gm.identity_id = i.id
		WHERE gm.group_id = ?
		ORDER BY i.name`, groupID)
	if err != nil {
		return nil, fmt.Errorf("listing group members: %w", err)
	}
	defer rows.Close()

	var members []model.Identity
	for rows.Next() {
		var i model.Identity
		if err := rows.Scan(&i.ID, &i.Name, &i.ThumbnailPath, &i.Notes); err != nil {
			return nil, err
		}
		members = append(members, i)
	}
	return members, rows.Err()
}

// FindIdentityByNameCaseInsensitive looks up an identity by case-insensitive
// name match. Used for ergonomic CLI lookups. Returns sql.ErrNoRows if not found.
func (db *DB) FindIdentityByNameCaseInsensitive(name string) (*model.Identity, error) {
	row := db.conn.QueryRow(`
		SELECT id, name, COALESCE(thumbnail_path, ''), COALESCE(notes, '')
		FROM identities WHERE LOWER(name) = LOWER(?)`, name)

	var i model.Identity
	err := row.Scan(&i.ID, &i.Name, &i.ThumbnailPath, &i.Notes)
	if err == sql.ErrNoRows {
		return nil, err
	}
	if err != nil {
		return nil, fmt.Errorf("finding identity: %w", err)
	}
	return &i, nil
}
