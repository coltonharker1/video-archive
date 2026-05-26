package cli

import (
	"context"
	"fmt"
	"log/slog"

	"github.com/colton/video-archive/internal/config"
	"github.com/colton/video-archive/internal/mlclient"
	"github.com/colton/video-archive/internal/pipeline"
	"github.com/spf13/cobra"
)

var (
	runLabel   string
	runDate    string
	runLink    bool
	runSymlink bool
)

var runCmd = &cobra.Command{
	Use:   "run [file]",
	Short: "Run the full pipeline: ingest -> sample -> detect -> track -> cluster",
	Args:  cobra.ExactArgs(1),
	RunE:  runPipeline,
}

func init() {
	runCmd.Flags().StringVar(&runLabel, "label", "", "description of the video")
	runCmd.Flags().StringVar(&runDate, "date", "", "recording date (YYYY-MM-DD)")
	runCmd.Flags().BoolVar(&runLink, "link", false, "hardlink the source into the archive instead of copying (same filesystem only)")
	runCmd.Flags().BoolVar(&runSymlink, "symlink", false, "symlink the source into the archive instead of copying (breaks if source moves)")
	runCmd.MarkFlagsMutuallyExclusive("link", "symlink")
	RootCmd.AddCommand(runCmd)
}

func runPipeline(cmd *cobra.Command, args []string) error {
	cfg := config.Default()

	db, err := openDB(cfg)
	if err != nil {
		return err
	}
	defer db.Close()

	// Step 1: Ingest
	fmt.Println("=== Step 1: Ingest ===")
	ingestResult, err := pipeline.IngestFile(context.Background(), cfg, db, pipeline.IngestOptions{
		SourcePath: args[0],
		Label:      runLabel,
		Date:       runDate,
		LinkMode:   linkModeFromFlags(runLink, runSymlink),
	})
	if err != nil {
		return fmt.Errorf("ingest failed: %w", err)
	}
	id := ingestResult.RecordingID
	idStr := fmt.Sprintf("%d", id)

	if ingestResult.Skipped {
		fmt.Printf("Already in archive (id=%d)\n", id)
	} else {
		fmt.Printf("Ingested (id=%d)\n", id)
		if ingestResult.Meta != nil {
			durationSec := float64(ingestResult.Meta.DurationMs) / 1000
			minutes := int(durationSec) / 60
			seconds := int(durationSec) % 60
			fmt.Printf("Duration: %d:%02d | %dx%d | %.2f fps | %s\n",
				minutes, seconds, ingestResult.Meta.Width, ingestResult.Meta.Height,
				ingestResult.Meta.FPS, ingestResult.Meta.Codec)
		}
	}

	// Step 2: Sample + Scene Detection (parallel-safe, both use ffmpeg)
	fmt.Println("\n=== Step 2: Sample ===")
	sampleResult, err := pipeline.SampleRecording(context.Background(), cfg, db, id)
	if err != nil {
		return fmt.Errorf("sample failed: %w", err)
	}
	if sampleResult.Skipped {
		fmt.Printf("Frames already exist (%d)\n", sampleResult.FrameCount)
	} else {
		fmt.Printf("Extracted %d frames\n", sampleResult.FrameCount)
	}

	fmt.Println("\n=== Step 2b: Scene Detection ===")
	sceneResult, err := pipeline.DetectScenes(context.Background(), cfg, db, id, pipeline.DefaultSceneOptions())
	if err != nil {
		slog.Warn("scene detection failed, you can retry with: va scenes "+idStr, "error", err)
	} else {
		fmt.Printf("Detected %d scenes from %d raw boundaries\n",
			sceneResult.SceneCount, sceneResult.BoundaryCount)
	}

	// Step 3+: ML pipeline (only if worker is available)
	ml := mlclient.New(cfg.MLWorkerURL)
	health, err := ml.Health(context.Background())
	if err != nil || !health.ModelsLoaded {
		fmt.Printf("\nML worker not available at %s — stopping after sample.\n", cfg.MLWorkerURL)
		fmt.Println("Start the worker and continue with:")
		fmt.Printf("  va detect %s\n  va track %s\n  va cluster %s\n  va report %s\n", idStr, idStr, idStr, idStr)
		return nil
	}

	// Step 3: Detect faces
	fmt.Println("\n=== Step 3: Detect ===")
	detectResult, err := pipeline.DetectFaces(context.Background(), cfg, db, ml, id)
	if err != nil {
		return fmt.Errorf("detect failed: %w", err)
	}
	if detectResult.Skipped {
		fmt.Println("All frames already processed")
	} else {
		fmt.Printf("Detected %d faces across %d frames\n", detectResult.FacesFound, detectResult.FramesDone)
	}

	if detectResult.FacesFound == 0 && !detectResult.Skipped {
		fmt.Println("\nNo faces found — pipeline complete.")
		return nil
	}

	// Step 3b: Fill-sample around detections, then re-detect on new frames
	fmt.Println("\n=== Step 3b: Fill sampling ===")
	fillResult, err := pipeline.FillSampleAroundDetections(context.Background(), cfg, db, id, pipeline.DefaultFillOptions())
	if err != nil {
		slog.Warn("fill sampling failed; continuing with scout-only detections", "error", err)
	} else if fillResult.Skipped {
		switch fillResult.SkipReason {
		case "tracks_exist":
			fmt.Println("Fill skipped — recording already has tracks (fill is pre-track only)")
		default:
			fmt.Printf("Fill frames already exist (%d)\n", fillResult.FrameCount)
		}
	} else if fillResult.FrameCount > 0 {
		fmt.Printf("Extracted %d fill frames across %d windows\n", fillResult.FrameCount, fillResult.WindowCount)
		fillDetect, err := pipeline.DetectFaces(context.Background(), cfg, db, ml, id)
		if err != nil {
			return fmt.Errorf("fill detect failed: %w", err)
		}
		if !fillDetect.Skipped {
			fmt.Printf("Fill detect: %d additional faces across %d frames\n", fillDetect.FacesFound, fillDetect.FramesDone)
		}
	}

	// Step 4: Track
	fmt.Println("\n=== Step 4: Track ===")
	trackResult, err := pipeline.TrackFaces(context.Background(), cfg, db, id)
	if err != nil {
		return fmt.Errorf("track failed: %w", err)
	}
	if trackResult.Skipped {
		fmt.Printf("Tracks already exist (%d)\n", trackResult.TrackCount)
	} else {
		fmt.Printf("Built %d tracks\n", trackResult.TrackCount)
	}

	if trackResult.TrackCount == 0 {
		fmt.Println("\nNo tracks — pipeline complete.")
		return nil
	}

	// Step 5: Cluster
	fmt.Println("\n=== Step 5: Cluster ===")
	clusterResult, err := pipeline.ClusterTracks(context.Background(), cfg, db, ml, id, pipeline.DefaultClusterOptions())
	if err != nil {
		slog.Warn("clustering failed, you can retry with: va cluster "+idStr, "error", err)
	} else if clusterResult.Skipped {
		fmt.Printf("Clusters already exist (%d)\n", clusterResult.ClusterCount)
	} else {
		fmt.Printf("Created %d clusters", clusterResult.ClusterCount)
		if clusterResult.MergedCount > 0 {
			fmt.Printf(" (auto-merged %d)", clusterResult.MergedCount)
		}
		fmt.Println()
	}

	// Step 6: Auto-match against known identities
	matchOpts := pipeline.DefaultMatchOptions()
	matchOpts.AutoApply = true
	matches, matchResult, err := pipeline.MatchIdentities(cfg, db, id, matchOpts)
	if err != nil {
		slog.Warn("identity matching failed", "error", err)
	} else if matchResult.Matched > 0 {
		fmt.Printf("\n=== Step 6: Identity Match ===\n")
		fmt.Printf("Auto-matched %d clusters to known people:\n", matchResult.Matched)
		for _, m := range matches {
			fmt.Printf("  Cluster #%-4d -> %s (%.0f%%)\n", m.ClusterID, m.IdentityName, m.Similarity*100)
		}
		if matchResult.Unmatched > 0 {
			fmt.Printf("  %d clusters unmatched (new people?)\n", matchResult.Unmatched)
		}
	}

	// Auto-generate segments if any clusters were auto-matched
	if matchResult != nil && matchResult.Matched > 0 {
		fmt.Println("\n=== Auto-generate Segments ===")
		segResult, err := pipeline.GenerateSegments(cfg, db, id, pipeline.DefaultSegmentOptions())
		if err != nil {
			slog.Warn("segment generation failed", "error", err)
		} else if segResult.SegmentCount > 0 {
			fmt.Printf("Generated %d segments for %d people\n", segResult.SegmentCount, segResult.PersonCount)

			// Merge adjacent scenes by people overlap, then map
			mergeResult, err := pipeline.MergeScenesByPeople(db, id, pipeline.DefaultSceneMergeOptions())
			if err != nil {
				slog.Warn("scene merge failed", "error", err)
			} else if mergeResult.Merged > 0 {
				fmt.Printf("Merged %d scene pairs (%d → %d scenes)\n",
					mergeResult.Merged, mergeResult.Before, mergeResult.After)
			}
			mapResult, err := pipeline.MapScenePeople(db, id)
			if err != nil {
				slog.Warn("scene-people mapping failed", "error", err)
			} else if mapResult.MappingCount > 0 {
				fmt.Printf("Mapped %d people across %d scenes\n", mapResult.PersonCount, mapResult.SceneCount)
			}
		}
	}

	fmt.Printf("\nPipeline complete for recording %s\n", idStr)
	fmt.Println("Next steps:")
	fmt.Printf("  va review %s    # review and name people\n", idStr)
	fmt.Printf("  va segments %s  # generate per-person timestamps\n", idStr)
	fmt.Printf("  va scene-map %s # map people into scenes\n", idStr)
	fmt.Printf("  va report %s    # view results\n", idStr)
	return nil
}
