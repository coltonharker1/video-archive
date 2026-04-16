package cli

import (
	"fmt"
	"os"
	"path/filepath"

	"github.com/colton/video-archive/internal/config"
	"github.com/colton/video-archive/internal/store"
)

func getConfig() config.Config {
	return config.Default()
}

func openDB(cfg config.Config) (*store.DB, error) {
	// Ensure the parent directory exists before opening SQLite
	if err := os.MkdirAll(filepath.Dir(cfg.DBPath), 0755); err != nil {
		return nil, fmt.Errorf("creating data dir: %w", err)
	}
	db, err := store.Open(cfg.DBPath)
	if err != nil {
		return nil, fmt.Errorf("opening database: %w", err)
	}
	return db, nil
}
