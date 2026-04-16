package store

import (
	"database/sql"
	"fmt"

	_ "modernc.org/sqlite"
)

// DB wraps the SQLite database connection.
type DB struct {
	conn *sql.DB
	// backoffSecondsPerAttempt controls retry backoff in ClaimNextPendingJob.
	// Multiplied by the job's attempt count. Defaults to 30.
	backoffSecondsPerAttempt int
}

// Open opens (or creates) a SQLite database at the given path and runs migrations.
func Open(dbPath string) (*DB, error) {
	conn, err := sql.Open("sqlite", dbPath+"?_journal_mode=WAL&_busy_timeout=5000&_foreign_keys=on")
	if err != nil {
		return nil, fmt.Errorf("opening database: %w", err)
	}

	// Single writer to eliminate SQLITE_BUSY. WAL mode allows concurrent reads.
	conn.SetMaxOpenConns(1)

	db := &DB{
		conn:                    conn,
		backoffSecondsPerAttempt: 30,
	}
	if err := db.migrate(); err != nil {
		return nil, fmt.Errorf("running migrations: %w", err)
	}

	return db, nil
}

// SetBackoffSeconds overrides the retry backoff multiplier (for tests).
func (db *DB) SetBackoffSeconds(seconds int) {
	db.backoffSecondsPerAttempt = seconds
}

// Close closes the database connection.
func (db *DB) Close() error {
	return db.conn.Close()
}
