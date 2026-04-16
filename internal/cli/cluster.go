package cli

import (
	"context"
	"fmt"
	"strconv"

	"github.com/colton/video-archive/internal/config"
	"github.com/colton/video-archive/internal/mlclient"
	"github.com/colton/video-archive/internal/pipeline"
	"github.com/spf13/cobra"
)

var (
	clusterMergeThreshold float64
	clusterForce          bool
)

var clusterCmd = &cobra.Command{
	Use:   "cluster [recording_id]",
	Short: "Cluster tracks into identity groups (requires ML worker)",
	Long: `Groups face tracks into likely-same-person clusters using HDBSCAN,
then auto-merges clusters with similar face embeddings.

Use --merge-threshold to control how aggressively clusters merge:
  0.4  = very aggressive (may merge different people)
  0.55 = default (good for VHS quality)
  0.7  = conservative (more clusters, fewer mistakes)

Use --force to delete existing clusters and re-run.`,
	Args: cobra.ExactArgs(1),
	RunE: runCluster,
}

func init() {
	clusterCmd.Flags().Float64Var(&clusterMergeThreshold, "merge-threshold", 0.55,
		"cosine similarity threshold for auto-merging clusters (0.4=aggressive, 0.7=conservative)")
	clusterCmd.Flags().BoolVar(&clusterForce, "force", false,
		"delete existing clusters and re-run")
	RootCmd.AddCommand(clusterCmd)
}

func runCluster(cmd *cobra.Command, args []string) error {
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

	ml := mlclient.New(cfg.MLWorkerURL)

	// Check worker health
	health, err := ml.Health(context.Background())
	if err != nil {
		return fmt.Errorf("ML worker not reachable at %s: %w\nStart it with: cd worker && python worker.py", cfg.MLWorkerURL, err)
	}
	if !health.ModelsLoaded {
		return fmt.Errorf("ML worker models not loaded yet, try again shortly")
	}

	// Force mode: delete existing clusters
	if clusterForce {
		if err := db.DeleteClusters(id); err != nil {
			return fmt.Errorf("deleting existing clusters: %w", err)
		}
		fmt.Println("Deleted existing clusters")
	}

	opts := pipeline.ClusterOptions{
		MinClusterSize: 2,
		MergeThreshold: clusterMergeThreshold,
	}

	result, err := pipeline.ClusterTracks(context.Background(), cfg, db, ml, id, opts)
	if err != nil {
		return err
	}

	if result.Skipped {
		fmt.Printf("Clusters already exist for recording %d (%d clusters)\n", id, result.ClusterCount)
		fmt.Println("Use --force to delete and re-cluster")
		return nil
	}

	fmt.Printf("Created %d clusters for recording %d\n", result.ClusterCount, id)
	if result.MergedCount > 0 {
		fmt.Printf("  HDBSCAN produced %d initial clusters, auto-merge combined %d\n",
			result.BeforeMergeCount, result.MergedCount)
	}
	return nil
}
