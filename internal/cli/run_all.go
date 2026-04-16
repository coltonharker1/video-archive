package cli

import (
	"context"
	"fmt"
	"log/slog"
	"sort"

	"github.com/colton/video-archive/internal/config"
	"github.com/colton/video-archive/internal/mlclient"
	"github.com/colton/video-archive/internal/pipeline"
	"github.com/colton/video-archive/internal/store"
	"github.com/spf13/cobra"
)

var (
	runAllLimit      int
	runAllSkipMatch  bool
	runAllDryRun     bool
)

var runAllCmd = &cobra.Command{
	Use:   "run-all",
	Short: "Run the full pipeline on every recording that isn't fully processed",
	Long: `Iterates every recording and runs: sample → scenes → detect → embed →
track → cluster → match → segments → scene-map. Each stage is idempotent, so
already-done work is skipped. Recordings with existing clusters are treated
as processed and only the downstream match/segments/scene-map steps run.

Recommended for batch processing after va ingest-dir:
  caffeinate -dis go run ./cmd/va run-all`,
	Args: cobra.NoArgs,
	RunE: runRunAll,
}

func init() {
	runAllCmd.Flags().IntVar(&runAllLimit, "limit", 0, "process at most N recordings (0 = no limit)")
	runAllCmd.Flags().BoolVar(&runAllSkipMatch, "skip-match", false, "skip cross-video identity matching step")
	runAllCmd.Flags().BoolVar(&runAllDryRun, "dry-run", false, "list recordings that would be processed without running anything")
	RootCmd.AddCommand(runAllCmd)
}

func runRunAll(cmd *cobra.Command, args []string) error {
	cfg := config.Default()
	db, err := openDB(cfg)
	if err != nil {
		return err
	}
	defer db.Close()

	recs, err := db.ListRecordings()
	if err != nil {
		return fmt.Errorf("listing recordings: %w", err)
	}
	if len(recs) == 0 {
		fmt.Println("No recordings in archive. Run 'va ingest' or 'va ingest-dir' first.")
		return nil
	}

	// Process smaller files first so user sees progress fast. Rough proxy:
	// duration_ms ascending.
	sort.Slice(recs, func(i, j int) bool {
		return recs[i].DurationMs < recs[j].DurationMs
	})

	if runAllLimit > 0 && len(recs) > runAllLimit {
		recs = recs[:runAllLimit]
	}

	if runAllDryRun {
		fmt.Printf("Would process %d recordings (shortest first):\n", len(recs))
		for _, r := range recs {
			dur := r.DurationMs / 1000
			fmt.Printf("  [%d] %s  %d:%02d\n", r.ID, r.Slug, dur/60, dur%60)
		}
		return nil
	}

	// Probe ML worker once. Without it we can still run sample+scenes.
	ml := mlclient.New(cfg.MLWorkerURL)
	health, mlErr := ml.Health(context.Background())
	mlAvailable := mlErr == nil && health != nil && health.ModelsLoaded
	if !mlAvailable {
		fmt.Printf("Warning: ML worker not available at %s — will run sample+scenes only.\n\n", cfg.MLWorkerURL)
	}

	total := len(recs)
	var done, failed int
	for i, rec := range recs {
		fmt.Printf("=== [%d/%d] %s (id=%d) ===\n", i+1, total, rec.Slug, rec.ID)
		if err := processOne(cfg, db, ml, mlAvailable, rec.ID); err != nil {
			fmt.Printf("  ERROR: %v\n", err)
			failed++
			continue
		}
		done++
	}

	fmt.Printf("\nBatch complete. processed=%d failed=%d total=%d\n", done, failed, total)
	return nil
}

// processOne runs every idempotent stage of the pipeline on a single recording.
// Stages that have nothing to do log and return cleanly.
func processOne(cfg config.Config, db *store.DB, ml *mlclient.Client, mlAvailable bool, id int64) error {
	ctx := context.Background()

	// Sample (frames)
	sampleResult, err := pipeline.SampleRecording(ctx, cfg, db, id)
	if err != nil {
		return fmt.Errorf("sample: %w", err)
	}
	if sampleResult.Skipped {
		fmt.Printf("  sample: %d frames (cached)\n", sampleResult.FrameCount)
	} else {
		fmt.Printf("  sample: %d frames\n", sampleResult.FrameCount)
	}

	// Scene detection (no ML needed)
	sceneResult, err := pipeline.DetectScenes(ctx, cfg, db, id, pipeline.DefaultSceneOptions())
	if err != nil {
		slog.Warn("scene detection failed; continuing", "recording_id", id, "err", err)
	} else {
		fmt.Printf("  scenes: %d detected (%d raw)\n", sceneResult.SceneCount, sceneResult.BoundaryCount)
	}

	if !mlAvailable {
		return nil
	}

	// Detect faces
	detectResult, err := pipeline.DetectFaces(ctx, cfg, db, ml, id)
	if err != nil {
		return fmt.Errorf("detect: %w", err)
	}
	if detectResult.Skipped {
		fmt.Printf("  detect: cached\n")
	} else {
		fmt.Printf("  detect: %d faces across %d frames\n", detectResult.FacesFound, detectResult.FramesDone)
	}
	if detectResult.FacesFound == 0 && !detectResult.Skipped {
		fmt.Printf("  no faces — skipping downstream\n")
		return nil
	}

	// (Embedding happens inside DetectFaces — the ML worker returns both
	// detections and embeddings in one pass.)

	// Track
	trackResult, err := pipeline.TrackFaces(ctx, cfg, db, id)
	if err != nil {
		return fmt.Errorf("track: %w", err)
	}
	if trackResult.Skipped {
		fmt.Printf("  track: %d tracks (cached)\n", trackResult.TrackCount)
	} else {
		fmt.Printf("  track: %d tracks\n", trackResult.TrackCount)
	}
	if trackResult.TrackCount == 0 {
		return nil
	}

	// Cluster
	clusterResult, err := pipeline.ClusterTracks(ctx, cfg, db, ml, id, pipeline.DefaultClusterOptions())
	if err != nil {
		slog.Warn("clustering failed; continuing", "recording_id", id, "err", err)
	} else if clusterResult.Skipped {
		fmt.Printf("  cluster: %d clusters (cached)\n", clusterResult.ClusterCount)
	} else {
		fmt.Printf("  cluster: %d clusters\n", clusterResult.ClusterCount)
	}

	// Match against known identities
	if !runAllSkipMatch {
		matchOpts := pipeline.DefaultMatchOptions()
		matchOpts.AutoApply = true
		matches, matchResult, err := pipeline.MatchIdentities(cfg, db, id, matchOpts)
		if err != nil {
			slog.Warn("match failed; continuing", "recording_id", id, "err", err)
		} else if matchResult != nil {
			fmt.Printf("  match: %d auto-matched, %d unmatched\n", matchResult.Matched, matchResult.Unmatched)
			for _, m := range matches {
				fmt.Printf("    cluster #%d -> %s (%.0f%%)\n", m.ClusterID, m.IdentityName, m.Similarity*100)
			}
		}

		// Regenerate segments + scene-people if anything got matched
		if matchResult != nil && matchResult.Matched > 0 {
			if _, err := pipeline.GenerateSegments(cfg, db, id, pipeline.DefaultSegmentOptions()); err != nil {
				slog.Warn("segments failed", "err", err)
			}
			if _, err := pipeline.MergeScenesByPeople(db, id, pipeline.DefaultSceneMergeOptions()); err != nil {
				slog.Warn("scene merge failed", "err", err)
			}
			if _, err := pipeline.MapScenePeople(db, id); err != nil {
				slog.Warn("scene-map failed", "err", err)
			}
		}
	}

	return nil
}
