package cli

import (
	"context"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"

	"github.com/colton/video-archive/internal/config"
	"github.com/colton/video-archive/internal/pipeline"
	"github.com/spf13/cobra"
)

var (
	ingestDirLink    bool
	ingestDirSymlink bool
	ingestDirDryRun  bool
)

var ingestDirCmd = &cobra.Command{
	Use:   "ingest-dir [directory]",
	Short: "Ingest every video file under a directory (recursive, idempotent)",
	Long: `Recursively scans a directory for video files (.mov/.mp4/.m4v/.avi/.mkv)
and ingests each via the normal ingest pipeline. Already-ingested files are
skipped. Dates are extracted from filenames where possible
(YYYY-MM-DD, YYYY_MM_DD, or a bare YYYY prefix).

Recommended for the Home Videos archive:
  va ingest-dir ~/Desktop/Home\ Videos --link`,
	Args: cobra.ExactArgs(1),
	RunE: runIngestDir,
}

func init() {
	ingestDirCmd.Flags().BoolVar(&ingestDirLink, "link", false, "hardlink sources into the archive (same filesystem only)")
	ingestDirCmd.Flags().BoolVar(&ingestDirSymlink, "symlink", false, "symlink sources into the archive (breaks if source moves)")
	ingestDirCmd.Flags().BoolVar(&ingestDirDryRun, "dry-run", false, "scan and show what would be ingested without writing anything")
	ingestDirCmd.MarkFlagsMutuallyExclusive("link", "symlink")
	RootCmd.AddCommand(ingestDirCmd)
}

var (
	videoExts = map[string]bool{
		".mov": true, ".mp4": true, ".m4v": true,
		".avi": true, ".mkv": true, ".webm": true,
	}
	// YYYY-MM-DD or YYYY_MM_DD anywhere in filename.
	dateFullRe = regexp.MustCompile(`(\d{4})[-_](\d{2})[-_](\d{2})`)
	// Bare 19xx or 20xx year anywhere in filename (used as a fallback;
	// month-day default to 01-01).
	dateYearRe = regexp.MustCompile(`\b(19\d{2}|20\d{2})\b`)
)

func runIngestDir(cmd *cobra.Command, args []string) error {
	root := args[0]
	info, err := os.Stat(root)
	if err != nil {
		return fmt.Errorf("directory not found: %w", err)
	}
	if !info.IsDir() {
		return fmt.Errorf("not a directory: %s", root)
	}

	var files []string
	err = filepath.WalkDir(root, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() {
			return nil
		}
		if strings.HasPrefix(d.Name(), ".") {
			return nil
		}
		ext := strings.ToLower(filepath.Ext(path))
		if !videoExts[ext] {
			return nil
		}
		files = append(files, path)
		return nil
	})
	if err != nil {
		return fmt.Errorf("scanning directory: %w", err)
	}

	sort.Strings(files)
	fmt.Printf("Found %d video files under %s\n", len(files), root)

	if len(files) == 0 {
		return nil
	}

	if ingestDirDryRun {
		for _, f := range files {
			date := extractDateFromFilename(filepath.Base(f))
			dateLabel := date
			if dateLabel == "" {
				dateLabel = "(no date)"
			}
			fmt.Printf("  %s  [%s]\n", filepath.Base(f), dateLabel)
		}
		return nil
	}

	cfg := config.Default()
	db, err := openDB(cfg)
	if err != nil {
		return err
	}
	defer db.Close()

	linkMode := linkModeFromFlags(ingestDirLink, ingestDirSymlink)
	var ingested, skipped, failed int

	for i, path := range files {
		date := extractDateFromFilename(filepath.Base(path))
		fmt.Printf("[%d/%d] %s", i+1, len(files), filepath.Base(path))
		if date != "" {
			fmt.Printf("  (date=%s)", date)
		}
		fmt.Println()

		result, err := pipeline.IngestFile(context.Background(), cfg, db, pipeline.IngestOptions{
			SourcePath: path,
			Date:       date,
			LinkMode:   linkMode,
		})
		if err != nil {
			fmt.Printf("    ERROR: %v\n", err)
			failed++
			continue
		}
		if result.Skipped {
			fmt.Printf("    already in archive (id=%d)\n", result.RecordingID)
			skipped++
			continue
		}
		fmt.Printf("    ingested (id=%d)", result.RecordingID)
		if result.Meta != nil {
			dur := result.Meta.DurationMs / 1000
			fmt.Printf("  %d:%02d  %dx%d", dur/60, dur%60, result.Meta.Width, result.Meta.Height)
		}
		fmt.Println()
		ingested++
	}

	fmt.Printf("\nDone. ingested=%d skipped=%d failed=%d\n", ingested, skipped, failed)
	return nil
}

// extractDateFromFilename pulls a YYYY-MM-DD date out of a filename. Handles
// YYYY-MM-DD, YYYY_MM_DD, and bare YYYY (year-only) prefixes. Returns empty
// string if no date is recoverable.
func extractDateFromFilename(name string) string {
	if m := dateFullRe.FindStringSubmatch(name); m != nil {
		return fmt.Sprintf("%s-%s-%s", m[1], m[2], m[3])
	}
	if m := dateYearRe.FindStringSubmatch(name); m != nil {
		return fmt.Sprintf("%s-01-01", m[1])
	}
	return ""
}
