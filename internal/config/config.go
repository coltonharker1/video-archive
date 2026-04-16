package config

import (
	"os"
	"path/filepath"
)

// Config holds application configuration.
type Config struct {
	DataDir     string  // root directory for all archive data
	DBPath      string  // path to SQLite database
	Ffmpeg      string  // path to ffmpeg binary
	Ffprobe     string  // path to ffprobe binary
	MLWorkerURL string  // URL of the Python ML worker
	SampleFPS   float64 // frames per second for sampling (default 0.5)
}

// Default returns a Config with sensible defaults.
func Default() Config {
	home, _ := os.UserHomeDir()
	dataDir := filepath.Join(home, ".video-archive")

	return Config{
		DataDir:     dataDir,
		DBPath:      filepath.Join(dataDir, "va.db"),
		Ffmpeg:      "ffmpeg",
		Ffprobe:     "ffprobe",
		MLWorkerURL: "http://localhost:8089",
		SampleFPS:   0.5,
	}
}

// MastersDir returns the path to the masters directory.
func (c Config) MastersDir() string {
	return filepath.Join(c.DataDir, "masters")
}

// FramesDir returns the path to the frames directory.
func (c Config) FramesDir() string {
	return filepath.Join(c.DataDir, "frames")
}

// CropsDir returns the path to the face crops directory.
func (c Config) CropsDir() string {
	return filepath.Join(c.DataDir, "crops")
}

// ThumbnailsDir returns the path to the thumbnails directory.
func (c Config) ThumbnailsDir() string {
	return filepath.Join(c.DataDir, "thumbnails")
}

// ClipsDir returns the path to the exported clips directory.
func (c Config) ClipsDir() string {
	return filepath.Join(c.DataDir, "clips")
}

// ModelsDir returns the path to the ML models directory.
func (c Config) ModelsDir() string {
	return filepath.Join(c.DataDir, "models")
}

// LogsDir returns the path to the logs directory.
func (c Config) LogsDir() string {
	return filepath.Join(c.DataDir, "logs")
}

// EnsureDirs creates all required data directories.
func (c Config) EnsureDirs() error {
	dirs := []string{
		c.DataDir,
		c.MastersDir(),
		c.FramesDir(),
		c.CropsDir(),
		c.ThumbnailsDir(),
		c.ClipsDir(),
		c.ModelsDir(),
		c.LogsDir(),
	}
	for _, d := range dirs {
		if err := os.MkdirAll(d, 0755); err != nil {
			return err
		}
	}
	return nil
}
