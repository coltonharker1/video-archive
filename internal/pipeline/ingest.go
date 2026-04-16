package pipeline

import (
	"context"
	"fmt"
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"strings"

	"github.com/colton/video-archive/internal/config"
	"github.com/colton/video-archive/internal/model"
	"github.com/colton/video-archive/internal/store"
	"github.com/colton/video-archive/internal/video"
)

// IngestLinkMode selects how the source file is placed into the archive.
type IngestLinkMode string

const (
	LinkModeCopy     IngestLinkMode = ""     // default — full byte copy
	LinkModeHardlink IngestLinkMode = "hard" // os.Link; same filesystem only, survives source deletion
	LinkModeSymlink  IngestLinkMode = "sym"  // os.Symlink; cross-FS ok, breaks if source moves
)

// IngestOptions controls how a source file is ingested into the archive.
type IngestOptions struct {
	SourcePath string
	Label      string         // user-provided description
	Date       string         // ISO YYYY-MM-DD; empty = today
	LinkMode   IngestLinkMode // how to place the file in the archive (default: copy)
}

// IngestResult describes the outcome of an ingest call.
type IngestResult struct {
	RecordingID int64
	ArchivePath string // absolute path to the master file in the archive
	Skipped     bool
	Meta        *model.VideoMeta
}

// IngestFile copies a source video file into the archive, extracts metadata,
// and creates a DB record. Idempotent — if the destination already exists,
// returns Skipped=true.
func IngestFile(ctx context.Context, cfg config.Config, db *store.DB, opts IngestOptions) (*IngestResult, error) {
	if _, err := os.Stat(opts.SourcePath); err != nil {
		return nil, fmt.Errorf("source file not found: %w", err)
	}

	if err := cfg.EnsureDirs(); err != nil {
		return nil, fmt.Errorf("creating data dirs: %w", err)
	}

	// Empty date persists as empty in the DB (unknown recording date). The
	// year subdirectory falls back to "unknown" so archives with historical
	// source material don't get mislabelled with today's date.
	date := opts.Date
	year := "unknown"
	if date != "" {
		year = date[:4]
	}

	slug := buildSlug(opts.SourcePath, opts.Label)
	archiveName := buildArchiveName(date, slug, filepath.Ext(opts.SourcePath))

	destDir := filepath.Join(cfg.MastersDir(), year)
	if err := os.MkdirAll(destDir, 0755); err != nil {
		return nil, fmt.Errorf("creating year dir: %w", err)
	}

	destPath := filepath.Join(destDir, archiveName)

	// Idempotency check
	if _, err := os.Stat(destPath); err == nil {
		rel, _ := filepath.Rel(cfg.DataDir, destPath)
		if recID, err := db.GetRecordingIDByMasterPath(rel); err == nil {
			return &IngestResult{RecordingID: recID, ArchivePath: destPath, Skipped: true}, nil
		}
		return &IngestResult{ArchivePath: destPath, Skipped: true}, nil
	}

	absSrc, err := filepath.Abs(opts.SourcePath)
	if err != nil {
		return nil, fmt.Errorf("resolving source path: %w", err)
	}

	switch opts.LinkMode {
	case LinkModeHardlink:
		if err := os.Link(absSrc, destPath); err != nil {
			return nil, fmt.Errorf("hardlinking to masters (source and archive must be on the same filesystem): %w", err)
		}
		slog.Info("hardlinked to masters", "path", destPath)
	case LinkModeSymlink:
		if err := os.Symlink(absSrc, destPath); err != nil {
			return nil, fmt.Errorf("symlinking to masters: %w", err)
		}
		slog.Info("symlinked to masters", "path", destPath, "target", absSrc)
	default:
		if err := copyFile(opts.SourcePath, destPath); err != nil {
			return nil, fmt.Errorf("copying to masters: %w", err)
		}
		slog.Info("copied to masters", "path", destPath)
	}

	meta, probeErr := video.Probe(ctx, cfg.Ffprobe, destPath)
	if probeErr != nil {
		slog.Warn("ffprobe failed, continuing without metadata", "error", probeErr)
	}

	relPath, _ := filepath.Rel(cfg.DataDir, destPath)
	rec := &model.Recording{
		Slug:       slug,
		Label:      opts.Label,
		Date:       date,
		MasterPath: relPath,
	}
	if meta != nil {
		rec.DurationMs = meta.DurationMs
		rec.Width = meta.Width
		rec.Height = meta.Height
		rec.FPS = meta.FPS
		rec.Codec = meta.Codec
		rec.Interlaced = meta.Interlaced
	}

	id, err := db.CreateRecording(rec)
	if err != nil {
		return nil, fmt.Errorf("saving to database: %w", err)
	}

	return &IngestResult{
		RecordingID: id,
		ArchivePath: destPath,
		Skipped:     false,
		Meta:        meta,
	}, nil
}

func buildSlug(filePath, label string) string {
	source := label
	if source == "" {
		source = strings.TrimSuffix(filepath.Base(filePath), filepath.Ext(filePath))
	}
	slug := strings.ToLower(source)
	slug = strings.ReplaceAll(slug, " ", "-")
	slug = strings.ReplaceAll(slug, "_", "-")
	var clean strings.Builder
	for _, r := range slug {
		if (r >= 'a' && r <= 'z') || (r >= '0' && r <= '9') || r == '-' {
			clean.WriteRune(r)
		}
	}
	result := clean.String()
	for strings.Contains(result, "--") {
		result = strings.ReplaceAll(result, "--", "-")
	}
	return strings.Trim(result, "-")
}

func buildArchiveName(date, slug, ext string) string {
	var parts []string
	if date != "" {
		parts = append(parts, date)
	}
	if slug != "" {
		parts = append(parts, slug)
	}
	if len(parts) == 0 {
		parts = []string{"unknown"}
	}
	return strings.Join(parts, "_") + ext
}

func copyFile(src, dst string) error {
	in, err := os.Open(src)
	if err != nil {
		return err
	}
	defer in.Close()

	out, err := os.Create(dst)
	if err != nil {
		return err
	}
	defer out.Close()

	if _, err := io.Copy(out, in); err != nil {
		return err
	}
	return out.Close()
}
