package store

import (
	"fmt"

	"github.com/colton/video-archive/internal/model"
)

// InsertScene inserts a scene boundary.
func (db *DB) InsertScene(s *model.Scene) (int64, error) {
	result, err := db.conn.Exec(`
		INSERT INTO scenes (recording_id, start_ms, end_ms, score)
		VALUES (?, ?, ?, ?)`,
		s.RecordingID, s.StartMs, s.EndMs, s.Score)
	if err != nil {
		return 0, fmt.Errorf("inserting scene: %w", err)
	}
	return result.LastInsertId()
}

// DeleteScenes removes all scenes (and cascade-deletes scene_people) for a recording.
func (db *DB) DeleteScenes(recordingID int64) error {
	// scene_people has ON DELETE CASCADE, so deleting scenes cleans up the join table.
	_, err := db.conn.Exec(`DELETE FROM scenes WHERE recording_id = ?`, recordingID)
	return err
}

// ListScenes returns all scenes for a recording, ordered by start time.
func (db *DB) ListScenes(recordingID int64) ([]model.Scene, error) {
	rows, err := db.conn.Query(`
		SELECT id, recording_id, start_ms, end_ms, COALESCE(score, 0)
		FROM scenes
		WHERE recording_id = ?
		ORDER BY start_ms`, recordingID)
	if err != nil {
		return nil, fmt.Errorf("listing scenes: %w", err)
	}
	defer rows.Close()

	var scenes []model.Scene
	for rows.Next() {
		var s model.Scene
		if err := rows.Scan(&s.ID, &s.RecordingID, &s.StartMs, &s.EndMs, &s.Score); err != nil {
			return nil, err
		}
		scenes = append(scenes, s)
	}
	return scenes, rows.Err()
}

// CountScenes returns the number of scenes for a recording.
func (db *DB) CountScenes(recordingID int64) (int, error) {
	var count int
	err := db.conn.QueryRow(`SELECT COUNT(*) FROM scenes WHERE recording_id = ?`, recordingID).Scan(&count)
	return count, err
}

// InsertScenePerson links a person to a scene.
func (db *DB) InsertScenePerson(sp *model.ScenePerson) error {
	_, err := db.conn.Exec(`
		INSERT INTO scene_people (scene_id, identity_id, first_appearance_ms, total_time_ms)
		VALUES (?, ?, ?, ?)
		ON CONFLICT(scene_id, identity_id) DO UPDATE SET
			first_appearance_ms = MIN(excluded.first_appearance_ms, scene_people.first_appearance_ms),
			total_time_ms = scene_people.total_time_ms + excluded.total_time_ms`,
		sp.SceneID, sp.IdentityID, sp.FirstAppearanceMs, sp.TotalTimeMs)
	if err != nil {
		return fmt.Errorf("inserting scene_person: %w", err)
	}
	return nil
}

// DeleteScenePeople removes all scene-person links for a recording (via scene IDs).
func (db *DB) DeleteScenePeople(recordingID int64) error {
	_, err := db.conn.Exec(`
		DELETE FROM scene_people
		WHERE scene_id IN (SELECT id FROM scenes WHERE recording_id = ?)`, recordingID)
	return err
}

// ScenePersonView is a scene_people row joined with identity name for display.
type ScenePersonView struct {
	model.ScenePerson
	IdentityName string
}

// ListScenePeople returns all people in all scenes for a recording.
func (db *DB) ListScenePeople(recordingID int64) ([]ScenePersonView, error) {
	rows, err := db.conn.Query(`
		SELECT sp.scene_id, sp.identity_id, sp.first_appearance_ms, sp.total_time_ms,
		       i.name
		FROM scene_people sp
		INNER JOIN scenes s ON s.id = sp.scene_id
		INNER JOIN identities i ON i.id = sp.identity_id
		WHERE s.recording_id = ?
		ORDER BY s.start_ms, sp.first_appearance_ms`, recordingID)
	if err != nil {
		return nil, fmt.Errorf("listing scene people: %w", err)
	}
	defer rows.Close()

	var out []ScenePersonView
	for rows.Next() {
		var v ScenePersonView
		if err := rows.Scan(&v.SceneID, &v.IdentityID, &v.FirstAppearanceMs,
			&v.TotalTimeMs, &v.IdentityName); err != nil {
			return nil, err
		}
		out = append(out, v)
	}
	return out, rows.Err()
}
