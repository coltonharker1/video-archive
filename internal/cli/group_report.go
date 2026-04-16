package cli

import (
	"encoding/json"
	"fmt"
	"strings"

	"github.com/colton/video-archive/internal/pipeline"
	"github.com/spf13/cobra"
)

var (
	grpReportJSON        bool
	grpReportRecordingID int64
	grpReportSceneGapMs  int64
)

var groupReportCmd = &cobra.Command{
	Use:   "group-report [group-name]",
	Short: "Show all appearances of a group across the archive (merged into scenes)",
	Long: `Produces a reviewable list of scenes where any member of a group appears.
Overlapping or nearby member segments in the same recording merge into a single
scene entry, tagged with which members appeared.

Use --scene-gap to tune the scene-merge threshold (default 20s):
  10s  = conservative (more, shorter scenes)
  20s  = default
  30s  = aggressive (fewer, longer scenes)

Use --recording <id> to report on a single video.`,
	Args: cobra.ExactArgs(1),
	RunE: runGroupReport,
}

func init() {
	groupReportCmd.Flags().BoolVar(&grpReportJSON, "json", false, "output as JSON")
	groupReportCmd.Flags().Int64Var(&grpReportRecordingID, "recording", 0, "limit to a single recording ID")
	groupReportCmd.Flags().Int64Var(&grpReportSceneGapMs, "scene-gap", 20000, "scene-merge gap threshold in ms")
	RootCmd.AddCommand(groupReportCmd)
}

type groupSceneOutput struct {
	Recording     string   `json:"recording"`
	RecordingID   int64    `json:"recording_id"`
	RecordingDate string   `json:"recording_date"`
	Start         string   `json:"start"`
	End           string   `json:"end"`
	DurationMs    int64    `json:"duration_ms"`
	Duration      string   `json:"duration"`
	Members       []string `json:"members"`
	Confidence    float64  `json:"confidence"`
}

type groupReportOutput struct {
	Group       string             `json:"group"`
	GroupID     int64              `json:"group_id"`
	MemberCount int                `json:"member_count"`
	VideoCount  int                `json:"video_count"`
	SceneCount  int                `json:"scene_count"`
	TotalMs     int64              `json:"total_ms"`
	TotalTime   string             `json:"total_time"`
	Scenes      []groupSceneOutput `json:"scenes"`
}

func runGroupReport(cmd *cobra.Command, args []string) error {
	groupName := args[0]

	db, err := openDB(getConfig())
	if err != nil {
		return err
	}
	defer db.Close()

	group, err := db.FindGroupByName(groupName)
	if err != nil {
		return fmt.Errorf("group %q not found. Available: va group list", groupName)
	}

	opts := pipeline.GroupReportOptions{
		SceneGapMs:  grpReportSceneGapMs,
		RecordingID: grpReportRecordingID,
	}

	report, err := pipeline.GenerateGroupReport(db, group.ID, opts)
	if err != nil {
		return err
	}

	if grpReportJSON {
		return emitGroupReportJSON(report)
	}

	return emitGroupReportText(report, opts)
}

func emitGroupReportJSON(report *pipeline.GroupReport) error {
	scenes := make([]groupSceneOutput, 0, len(report.Scenes))
	for _, s := range report.Scenes {
		scenes = append(scenes, groupSceneOutput{
			Recording:     s.RecordingSlug,
			RecordingID:   s.RecordingID,
			RecordingDate: s.RecordingDate,
			Start:         formatMs(s.StartMs),
			End:           formatMs(s.EndMs),
			DurationMs:    s.DurationMs,
			Duration:      formatDuration(s.DurationMs),
			Members:       s.Members,
			Confidence:    s.Confidence,
		})
	}

	out := groupReportOutput{
		Group:       report.GroupName,
		GroupID:     report.GroupID,
		MemberCount: report.MemberCount,
		VideoCount:  report.VideoCount,
		SceneCount:  len(report.Scenes),
		TotalMs:     report.TotalMs,
		TotalTime:   formatDuration(report.TotalMs),
		Scenes:      scenes,
	}

	data, _ := json.MarshalIndent(out, "", "  ")
	fmt.Println(string(data))
	return nil
}

func emitGroupReportText(report *pipeline.GroupReport, opts pipeline.GroupReportOptions) error {
	fmt.Printf("%s — appearances across %d recording%s\n",
		report.GroupName, report.VideoCount, plural(report.VideoCount))

	if report.MemberCount == 0 {
		fmt.Printf("\nGroup has no members. Add some: va group add %q <name> [<name>...]\n", report.GroupName)
		return nil
	}

	if len(report.Scenes) == 0 {
		fmt.Println("\nNo scenes found.")
		fmt.Println("Make sure you've:")
		fmt.Println("  1. Named clusters in 'va review'")
		fmt.Println("  2. Generated segments with 'va segments <id>'")
		fmt.Println("  3. Added those identities to the group via 'va group add'")
		return nil
	}

	// Group scenes by recording
	currentRec := ""
	for _, s := range report.Scenes {
		recLabel := fmt.Sprintf("%s (%s)", s.RecordingSlug, s.RecordingDate)
		if recLabel != currentRec {
			fmt.Printf("\n%s\n", recLabel)
			currentRec = recLabel
		}
		fmt.Printf("  %-7s - %-7s  (%-8s)  %s\n",
			formatMs(s.StartMs),
			formatMs(s.EndMs),
			formatDuration(s.DurationMs),
			strings.Join(s.Members, ", "),
		)
	}

	fmt.Printf("\nTotal: %d scenes across %d video%s, %s combined runtime\n",
		len(report.Scenes), report.VideoCount, plural(report.VideoCount), formatDuration(report.TotalMs))
	fmt.Printf("(scene-gap: %ds — tune with --scene-gap <ms>)\n", opts.SceneGapMs/1000)
	return nil
}
