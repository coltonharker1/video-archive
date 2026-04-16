package cli

import (
	"context"
	"fmt"

	"github.com/colton/video-archive/internal/config"
	"github.com/colton/video-archive/internal/pipeline"
	"github.com/spf13/cobra"
)

var (
	ingestLabel   string
	ingestDate    string
	ingestLink    bool
	ingestSymlink bool
)

var ingestCmd = &cobra.Command{
	Use:   "ingest [file]",
	Short: "Ingest a video file into the archive",
	Long:  "Copies the video file to the masters directory, extracts metadata, and creates a database record.",
	Args:  cobra.ExactArgs(1),
	RunE:  runIngest,
}

func init() {
	ingestCmd.Flags().StringVar(&ingestLabel, "label", "", "description of the video")
	ingestCmd.Flags().StringVar(&ingestDate, "date", "", "recording date (YYYY-MM-DD)")
	ingestCmd.Flags().BoolVar(&ingestLink, "link", false, "hardlink the source into the archive instead of copying (same filesystem only)")
	ingestCmd.Flags().BoolVar(&ingestSymlink, "symlink", false, "symlink the source into the archive instead of copying (breaks if source moves)")
	ingestCmd.MarkFlagsMutuallyExclusive("link", "symlink")
	RootCmd.AddCommand(ingestCmd)
}

func linkModeFromFlags(link, symlink bool) pipeline.IngestLinkMode {
	switch {
	case link:
		return pipeline.LinkModeHardlink
	case symlink:
		return pipeline.LinkModeSymlink
	default:
		return pipeline.LinkModeCopy
	}
}

func runIngest(cmd *cobra.Command, args []string) error {
	cfg := config.Default()

	db, err := openDB(cfg)
	if err != nil {
		return err
	}
	defer db.Close()

	result, err := pipeline.IngestFile(context.Background(), cfg, db, pipeline.IngestOptions{
		SourcePath: args[0],
		Label:      ingestLabel,
		Date:       ingestDate,
		LinkMode:   linkModeFromFlags(ingestLink, ingestSymlink),
	})
	if err != nil {
		return err
	}

	if result.Skipped {
		fmt.Printf("Already in archive: %s (id=%d)\n", result.ArchivePath, result.RecordingID)
		return nil
	}

	fmt.Printf("Ingested: %s (id=%d)\n", result.ArchivePath, result.RecordingID)
	if result.Meta != nil {
		durationSec := float64(result.Meta.DurationMs) / 1000
		minutes := int(durationSec) / 60
		seconds := int(durationSec) % 60
		fmt.Printf("Duration: %d:%02d | %dx%d | %.2f fps | %s",
			minutes, seconds, result.Meta.Width, result.Meta.Height, result.Meta.FPS, result.Meta.Codec)
		if result.Meta.Interlaced {
			fmt.Print(" (interlaced)")
		}
		fmt.Println()
	}
	return nil
}
