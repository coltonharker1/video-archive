package cli

import (
	"fmt"
	"strconv"

	"github.com/colton/video-archive/internal/config"
	"github.com/colton/video-archive/internal/pipeline"
	"github.com/spf13/cobra"
)

var (
	sceneMapNoMerge  bool
	sceneMapOverlap  float64
)

var sceneMapCmd = &cobra.Command{
	Use:   "scene-map [recording_id]",
	Short: "Merge scenes by people overlap, then map people into scenes",
	Long: `First merges adjacent scenes that share people (face-informed merging),
then cross-references scenes with per-person segments to populate the
scene-people relationship table.

Run after both 'va scenes' and 'va segments'.`,
	Args: cobra.ExactArgs(1),
	RunE: runSceneMap,
}

func init() {
	sceneMapCmd.Flags().BoolVar(&sceneMapNoMerge, "no-merge", false, "skip face-informed scene merging")
	sceneMapCmd.Flags().Float64Var(&sceneMapOverlap, "overlap", 0.5, "Jaccard similarity threshold for merging (0.0-1.0)")
	RootCmd.AddCommand(sceneMapCmd)
}

func runSceneMap(cmd *cobra.Command, args []string) error {
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

	_ = cfg

	// Step 1: Face-informed scene merging
	if !sceneMapNoMerge {
		mergeOpts := pipeline.DefaultSceneMergeOptions()
		mergeOpts.MinOverlap = sceneMapOverlap
		mergeResult, err := pipeline.MergeScenesByPeople(db, id, mergeOpts)
		if err != nil {
			return fmt.Errorf("scene merge failed: %w", err)
		}
		if mergeResult.Merged > 0 {
			fmt.Printf("Merged %d scene pairs (%d → %d scenes)\n",
				mergeResult.Merged, mergeResult.Before, mergeResult.After)
		}
	}

	// Step 2: Map people into (possibly merged) scenes
	result, err := pipeline.MapScenePeople(db, id)
	if err != nil {
		return err
	}

	if result.MappingCount == 0 {
		fmt.Println("No mappings created. Ensure scenes and segments both exist.")
		fmt.Println("Run 'va scenes <id>' and 'va segments <id>' first.")
		return nil
	}

	fmt.Printf("Mapped %d people across %d scenes (%d links) in recording %d\n",
		result.PersonCount, result.SceneCount, result.MappingCount, id)
	return nil
}
