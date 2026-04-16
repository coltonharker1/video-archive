package cli

import (
	"fmt"
	"strconv"

	"github.com/colton/video-archive/internal/config"
	"github.com/colton/video-archive/internal/pipeline"
	"github.com/spf13/cobra"
)

var (
	matchThreshold float64
	matchAutoApply bool
)

var matchCmd = &cobra.Command{
	Use:   "match [recording_id]",
	Short: "Match clusters against known identities from other videos",
	Long: `Compares pending clusters in this recording against all named people
from previously reviewed videos. Proposes or auto-applies matches.

Use --auto to automatically confirm matches above the threshold.
Use --threshold to control matching sensitivity (default 0.5).`,
	Args: cobra.ExactArgs(1),
	RunE: runMatch,
}

func init() {
	matchCmd.Flags().Float64Var(&matchThreshold, "threshold", 0.5,
		"cosine similarity threshold for matching (0.4=aggressive, 0.6=conservative)")
	matchCmd.Flags().BoolVar(&matchAutoApply, "auto", false,
		"automatically confirm matches above threshold")
	RootCmd.AddCommand(matchCmd)
}

func runMatch(cmd *cobra.Command, args []string) error {
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

	opts := pipeline.MatchOptions{
		Threshold: matchThreshold,
		AutoApply: matchAutoApply,
	}

	matches, result, err := pipeline.MatchIdentities(cfg, db, id, opts)
	if err != nil {
		return err
	}

	if result.Matched == 0 && result.Unmatched == 0 {
		fmt.Println("No pending clusters to match.")
		return nil
	}

	if len(matches) == 0 {
		fmt.Printf("No matches found among %d pending clusters.\n", result.Unmatched)
		fmt.Println("Name people in previous videos first, then re-run match.")
		return nil
	}

	if matchAutoApply {
		fmt.Printf("Auto-applied %d matches:\n", result.Matched)
	} else {
		fmt.Printf("Found %d potential matches (%d unmatched):\n", result.Matched, result.Unmatched)
	}

	for _, m := range matches {
		action := "suggested"
		if matchAutoApply {
			action = "applied"
		}
		fmt.Printf("  Cluster #%-4d -> %-20s  (%.0f%% similarity) [%s]\n",
			m.ClusterID, m.IdentityName, m.Similarity*100, action)
	}

	if !matchAutoApply && len(matches) > 0 {
		fmt.Printf("\nTo apply these matches automatically, re-run with --auto\n")
		fmt.Printf("Or review manually: va review %d\n", id)
	}

	return nil
}
