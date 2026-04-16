package store

// migrate runs schema migrations using CREATE TABLE IF NOT EXISTS.
func (db *DB) migrate() error {
	_, err := db.conn.Exec(`
		CREATE TABLE IF NOT EXISTS recordings (
			id          INTEGER PRIMARY KEY,
			slug        TEXT NOT NULL UNIQUE,
			label       TEXT,
			date        TEXT,
			master_path TEXT NOT NULL,
			duration_ms INTEGER,
			width       INTEGER,
			height      INTEGER,
			fps         REAL,
			codec       TEXT,
			interlaced  BOOLEAN DEFAULT FALSE,
			created_at  TEXT DEFAULT (datetime('now')),
			updated_at  TEXT DEFAULT (datetime('now'))
		);

		CREATE TABLE IF NOT EXISTS jobs (
			id           INTEGER PRIMARY KEY,
			recording_id INTEGER NOT NULL REFERENCES recordings(id),
			type         TEXT NOT NULL,
			status       TEXT NOT NULL DEFAULT 'pending',
			attempt      INTEGER DEFAULT 0,
			max_retries  INTEGER DEFAULT 3,
			error        TEXT,
			progress     TEXT,
			created_at   TEXT DEFAULT (datetime('now')),
			started_at   TEXT,
			finished_at  TEXT
		);

		CREATE TABLE IF NOT EXISTS frames (
			id           INTEGER PRIMARY KEY,
			recording_id INTEGER NOT NULL REFERENCES recordings(id),
			timestamp_ms INTEGER NOT NULL,
			pass         TEXT NOT NULL,
			frame_path   TEXT NOT NULL,
			width        INTEGER,
			height       INTEGER,
			processed    BOOLEAN DEFAULT FALSE,
			created_at   TEXT DEFAULT (datetime('now')),
			UNIQUE(recording_id, timestamp_ms)
		);

		CREATE TABLE IF NOT EXISTS detections (
			id           INTEGER PRIMARY KEY,
			frame_id     INTEGER NOT NULL REFERENCES frames(id),
			recording_id INTEGER NOT NULL,
			timestamp_ms INTEGER NOT NULL,
			bbox_x       REAL NOT NULL,
			bbox_y       REAL NOT NULL,
			bbox_w       REAL NOT NULL,
			bbox_h       REAL NOT NULL,
			confidence   REAL NOT NULL,
			landmarks    TEXT,
			crop_path    TEXT,
			created_at   TEXT DEFAULT (datetime('now'))
		);

		CREATE TABLE IF NOT EXISTS embeddings (
			id           INTEGER PRIMARY KEY,
			detection_id INTEGER NOT NULL UNIQUE REFERENCES detections(id),
			recording_id INTEGER NOT NULL,
			vector       BLOB NOT NULL,
			model_used   TEXT NOT NULL,
			quality      REAL,
			created_at   TEXT DEFAULT (datetime('now'))
		);

		CREATE TABLE IF NOT EXISTS tracks (
			id            INTEGER PRIMARY KEY,
			recording_id  INTEGER NOT NULL,
			start_ms      INTEGER NOT NULL,
			end_ms        INTEGER NOT NULL,
			detection_ids TEXT NOT NULL,
			avg_embedding BLOB,
			frame_count   INTEGER,
			confidence    REAL,
			created_at    TEXT DEFAULT (datetime('now'))
		);

		CREATE TABLE IF NOT EXISTS clusters (
			id             INTEGER PRIMARY KEY,
			recording_id   INTEGER,
			track_ids      TEXT NOT NULL,
			centroid_emb   BLOB,
			thumbnail_path TEXT,
			identity_id    INTEGER REFERENCES identities(id),
			status         TEXT NOT NULL DEFAULT 'pending',
			created_at     TEXT DEFAULT (datetime('now'))
		);

		CREATE TABLE IF NOT EXISTS identities (
			id             INTEGER PRIMARY KEY,
			name           TEXT NOT NULL,
			reference_embs TEXT,
			thumbnail_path TEXT,
			notes          TEXT,
			created_at     TEXT DEFAULT (datetime('now')),
			updated_at     TEXT DEFAULT (datetime('now'))
		);

		CREATE TABLE IF NOT EXISTS segments (
			id           INTEGER PRIMARY KEY,
			recording_id INTEGER NOT NULL,
			identity_id  INTEGER NOT NULL REFERENCES identities(id),
			start_ms     INTEGER NOT NULL,
			end_ms       INTEGER NOT NULL,
			confidence   REAL,
			exported     BOOLEAN DEFAULT FALSE,
			clip_path    TEXT,
			created_at   TEXT DEFAULT (datetime('now'))
		);

		CREATE TABLE IF NOT EXISTS groups (
			id         INTEGER PRIMARY KEY,
			name       TEXT NOT NULL UNIQUE,
			notes      TEXT,
			created_at TEXT DEFAULT (datetime('now'))
		);

		CREATE TABLE IF NOT EXISTS group_members (
			group_id    INTEGER NOT NULL REFERENCES groups(id) ON DELETE CASCADE,
			identity_id INTEGER NOT NULL REFERENCES identities(id) ON DELETE CASCADE,
			PRIMARY KEY (group_id, identity_id)
		);

		CREATE INDEX IF NOT EXISTS idx_frames_recording ON frames(recording_id, timestamp_ms);
		CREATE INDEX IF NOT EXISTS idx_detections_frame ON detections(frame_id);
		CREATE INDEX IF NOT EXISTS idx_detections_recording ON detections(recording_id, timestamp_ms);
		CREATE INDEX IF NOT EXISTS idx_embeddings_detection ON embeddings(detection_id);
		CREATE INDEX IF NOT EXISTS idx_tracks_recording ON tracks(recording_id);
		CREATE INDEX IF NOT EXISTS idx_clusters_identity ON clusters(identity_id);
		CREATE INDEX IF NOT EXISTS idx_segments_recording ON segments(recording_id);
		CREATE INDEX IF NOT EXISTS idx_segments_identity ON segments(identity_id);

		CREATE TABLE IF NOT EXISTS scenes (
			id            INTEGER PRIMARY KEY,
			recording_id  INTEGER NOT NULL REFERENCES recordings(id),
			start_ms      INTEGER NOT NULL,
			end_ms        INTEGER NOT NULL,
			score         REAL,
			created_at    TEXT DEFAULT (datetime('now')),
			UNIQUE(recording_id, start_ms)
		);

		CREATE TABLE IF NOT EXISTS scene_people (
			scene_id          INTEGER NOT NULL REFERENCES scenes(id) ON DELETE CASCADE,
			identity_id       INTEGER NOT NULL REFERENCES identities(id) ON DELETE CASCADE,
			first_appearance_ms INTEGER NOT NULL,
			total_time_ms     INTEGER NOT NULL,
			PRIMARY KEY (scene_id, identity_id)
		);

		CREATE INDEX IF NOT EXISTS idx_scenes_recording ON scenes(recording_id, start_ms);
		CREATE INDEX IF NOT EXISTS idx_scene_people_identity ON scene_people(identity_id);
	`)
	return err
}
