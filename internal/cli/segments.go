package cli

import (
	"fmt"
	"strconv"

	"github.com/colton/video-archive/internal/config"
	"github.com/colton/video-archive/internal/pipeline"
	"github.com/spf13/cobra"
)

var segmentsCmd = &cobra.Command{
	Use:   "segments [recording_id]",
	Short: "Generate merged time segments for named people",
	Long:  "Takes confirmed clusters with assigned names and produces merged appearance segments per person.",
	Args:  cobra.ExactArgs(1),
	RunE:  runSegments,
}

func init() {
	RootCmd.AddCommand(segmentsCmd)
}

func runSegments(cmd *cobra.Command, args []string) error {
	id, err := strconv.ParseInt(args[0], 10, 64)
	if err != nil {
		return fmt.Errorf("invalid recording ID: %w", err)
	}

	cfg := config.Default()
	db, err := openDB(cfg)
	if err != nil {
		return err
	}
	defer db.Close()

	result, err := pipeline.GenerateSegments(cfg, db, id, pipeline.DefaultSegmentOptions())
	if err != nil {
		return err
	}

	if result.PersonCount == 0 {
		fmt.Println("No confirmed clusters with names found.")
		fmt.Println("Use 'va review' to name people first.")
		return nil
	}

	fmt.Printf("Generated %d segments for %d people in recording %d\n",
		result.SegmentCount, result.PersonCount, id)
	return nil
}
