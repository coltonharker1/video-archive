package cli

import (
	"encoding/json"
	"fmt"
	"strconv"

	"github.com/colton/video-archive/internal/model"
	"github.com/spf13/cobra"
)

var reportJSON bool

var reportCmd = &cobra.Command{
	Use:   "report [recording_id]",
	Short: "Show face analysis report for a recording",
	Long:  "Displays pipeline stats, cluster info, and per-person appearance timestamps.",
	Args:  cobra.ExactArgs(1),
	RunE:  runReport,
}

func init() {
	reportCmd.Flags().BoolVar(&reportJSON, "json", false, "output as JSON")
	RootCmd.AddCommand(reportCmd)
}

// personSummary is the JSON-serializable per-person report.
type personSummary struct {
	Name         string           `json:"name"`
	TotalTimeMs  int64            `json:"total_time_ms"`
	TotalTime    string           `json:"total_time"`
	SegmentCount int              `json:"segment_count"`
	Segments     []segmentOutput  `json:"segments"`
}

type segmentOutput struct {
	Start      string  `json:"start"`
	End        string  `json:"end"`
	DurationMs int64   `json:"duration_ms"`
	Confidence float64 `json:"confidence"`
}

type reportOutput struct {
	RecordingID int64           `json:"recording_id"`
	Slug        string          `json:"slug"`
	DurationMs  int64           `json:"duration_ms"`
	Frames      int             `json:"frames"`
	Detections  int             `json:"detections"`
	Tracks      int             `json:"tracks"`
	Clusters    int             `json:"clusters"`
	People      []personSummary `json:"people"`
}

func runReport(cmd *cobra.Command, args []string) error {
	id, err := strconv.ParseInt(args[0], 10, 64)
	if err != nil {
		return fmt.Errorf("invalid recording ID: %w", err)
	}

	cfg := getConfig()
	db, err := openDB(cfg)
	if err != nil {
		return err
	}
	defer db.Close()

	rec, err := db.GetRecording(id)
	if err != nil {
		return fmt.Errorf("recording not found: %w", err)
	}

	frameCount, _ := db.CountFrames(id, "")
	detCount, _ := db.CountDetections(id)
	trackCount, _ := db.CountTracks(id)
	clusterCount, _ := db.CountClusters(id)

	// Build per-person report from segments
	segments, _ := db.ListSegments(id)

	// Group segments by identity
	identitySegs := make(map[int64][]segmentOutput)
	identityTotal := make(map[int64]int64)
	for _, s := range segments {
		dur := s.EndMs - s.StartMs
		identitySegs[s.IdentityID] = append(identitySegs[s.IdentityID], segmentOutput{
			Start:      formatMs(s.StartMs),
			End:        formatMs(s.EndMs),
			DurationMs: dur,
			Confidence: s.Confidence,
		})
		identityTotal[s.IdentityID] += dur
	}

	// Resolve identity names
	var people []personSummary
	for identityID, segs := range identitySegs {
		ident, err := db.GetIdentity(identityID)
		name := fmt.Sprintf("Unknown #%d", identityID)
		if err == nil {
			name = ident.Name
		}
		people = append(people, personSummary{
			Name:         name,
			TotalTimeMs:  identityTotal[identityID],
			TotalTime:    formatDuration(identityTotal[identityID]),
			SegmentCount: len(segs),
			Segments:     segs,
		})
	}

	if reportJSON {
		out := reportOutput{
			RecordingID: id,
			Slug:        rec.Slug,
			DurationMs:  rec.DurationMs,
			Frames:      frameCount,
			Detections:  detCount,
			Tracks:      trackCount,
			Clusters:    clusterCount,
			People:      people,
		}
		data, _ := json.MarshalIndent(out, "", "  ")
		fmt.Println(string(data))
		return nil
	}

	// Human-readable output
	fmt.Printf("Report for recording %d: %s\n", id, rec.Slug)
	if rec.DurationMs > 0 {
		fmt.Printf("Duration: %s\n", formatDuration(rec.DurationMs))
	}
	fmt.Println("---")
	fmt.Printf("Frames sampled:   %d\n", frameCount)
	fmt.Printf("Faces detected:   %d\n", detCount)
	fmt.Printf("Tracks:           %d\n", trackCount)
	fmt.Printf("Clusters:         %d\n", clusterCount)

	if len(people) == 0 {
		// Show cluster overview if no segments yet
		clusters, err := db.ListClusters(id)
		if err == nil && len(clusters) > 0 {
			tracks, _ := db.ListTracks(id)
			trackTimeMap := make(map[int64]struct{ StartMs, EndMs int64 })
			for _, t := range tracks {
				trackTimeMap[t.ID] = struct{ StartMs, EndMs int64 }{t.StartMs, t.EndMs}
			}

			fmt.Println("\nClusters:")
			fmt.Printf("%-6s  %-10s  %-8s  %-20s  %s\n", "ID", "Status", "Tracks", "Time Range", "Thumbnail")
			fmt.Println("------  ----------  --------  --------------------  ---------")

			for _, c := range clusters {
				var trackIDs []int64
				json.Unmarshal([]byte(c.TrackIDs), &trackIDs)

				var minMs, maxMs int64
				first := true
				for _, tid := range trackIDs {
					if t, ok := trackTimeMap[tid]; ok {
						if first || t.StartMs < minMs {
							minMs = t.StartMs
						}
						if first || t.EndMs > maxMs {
							maxMs = t.EndMs
						}
						first = false
					}
				}

				timeRange := ""
				if !first {
					timeRange = fmt.Sprintf("%s - %s", formatMs(minMs), formatMs(maxMs))
				}

				identity := ""
				if c.IdentityID != nil {
					if ident, err := db.GetIdentity(*c.IdentityID); err == nil {
						identity = ident.Name
					}
				}

				thumb := c.ThumbnailPath
				if len(thumb) > 30 {
					thumb = "..." + thumb[len(thumb)-27:]
				}

				label := c.Status
				if identity != "" {
					label = identity
				}

				fmt.Printf("%-6d  %-10s  %-8d  %-20s  %s\n",
					c.ID, label, len(trackIDs), timeRange, thumb)
			}

			fmt.Println("\nRun 'va segments' after naming people in 'va review' to generate per-person timestamps.")
		}
		return nil
	}

	// Per-person output
	fmt.Printf("\nPeople found: %d\n", len(people))
	fmt.Println("===")

	for _, p := range people {
		fmt.Printf("\n%s  (%s total, %d segment%s)\n",
			p.Name, p.TotalTime, p.SegmentCount, plural(p.SegmentCount))
		for _, s := range p.Segments {
			fmt.Printf("  %s - %s  (%s)\n", s.Start, s.End, formatDuration(s.DurationMs))
		}
	}

	return nil
}

func formatMs(ms int64) string {
	sec := ms / 1000
	min := sec / 60
	sec = sec % 60
	hr := min / 60
	min = min % 60
	if hr > 0 {
		return fmt.Sprintf("%d:%02d:%02d", hr, min, sec)
	}
	return fmt.Sprintf("%d:%02d", min, sec)
}

func formatDuration(ms int64) string {
	sec := ms / 1000
	if sec < 60 {
		return fmt.Sprintf("%ds", sec)
	}
	min := sec / 60
	sec = sec % 60
	if min < 60 {
		return fmt.Sprintf("%dm%02ds", min, sec)
	}
	hr := min / 60
	min = min % 60
	return fmt.Sprintf("%dh%02dm%02ds", hr, min, sec)
}

func plural(n int) string {
	if n == 1 {
		return ""
	}
	return "s"
}

// unused import guard for model
var _ = func() { var _ model.Identity }
