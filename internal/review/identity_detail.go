package review

import (
	"html/template"
	"log/slog"
	"net/http"
	"sort"
	"strconv"
	"strings"

	"github.com/colton/video-archive/internal/model"
)

type idRecordingBreakdown struct {
	RecordingID  int64
	Slug         string
	Date         string
	ClusterCount int
	SegmentCount int
	TotalTime    string
	TotalTimeMs  int64
}

type idClusterView struct {
	ID            int64
	RecordingID   int64
	RecordingSlug string
	Status        string
	TrackCount    int
	ThumbnailURL  string
}

type idSegmentView struct {
	ID            int64
	RecordingID   int64
	RecordingSlug string
	RecordingDate string
	StartMs       int64
	Start         string
	End           string
	Duration      string
}

type identityDetailData struct {
	Identity     model.Identity
	ThumbnailURL string
	Groups       []model.GroupSummary
	Recordings   []idRecordingBreakdown
	Clusters     []idClusterView
	Segments     []idSegmentView
	VideoCount   int
	ClusterCount int
	SegmentCount int
	TotalTime    string
}

func cropPathToURL(p string) string {
	if p == "" {
		return ""
	}
	return "/static/crops/" + strings.TrimPrefix(p, "crops/")
}

func (s *Server) handleIdentityDetail(w http.ResponseWriter, r *http.Request) {
	id, err := strconv.ParseInt(r.PathValue("id"), 10, 64)
	if err != nil {
		http.Error(w, "invalid identity ID", http.StatusBadRequest)
		return
	}

	ident, err := s.db.GetIdentity(id)
	if err != nil {
		http.Error(w, "identity not found", http.StatusNotFound)
		return
	}

	groups, _ := s.db.ListGroupsForIdentity(id)
	clusters, err := s.db.ListClustersForIdentity(id)
	if err != nil {
		http.Error(w, "failed to load clusters: "+err.Error(), http.StatusInternalServerError)
		return
	}
	segments, err := s.db.ListSegmentsForIdentity(id)
	if err != nil {
		http.Error(w, "failed to load segments: "+err.Error(), http.StatusInternalServerError)
		return
	}

	// Per-recording aggregation — clusters contribute counts, segments contribute time.
	type recAgg struct {
		id           int64
		slug         string
		date         string
		clusterCount int
		segmentCount int
		totalTimeMs  int64
	}
	aggByRec := map[int64]*recAgg{}
	ensure := func(rid int64, slug, date string) *recAgg {
		a, ok := aggByRec[rid]
		if !ok {
			a = &recAgg{id: rid, slug: slug, date: date}
			aggByRec[rid] = a
		}
		return a
	}
	for _, c := range clusters {
		if c.RecordingID == nil {
			continue
		}
		ensure(*c.RecordingID, c.RecordingSlug, c.RecordingDate).clusterCount++
	}
	for _, seg := range segments {
		a := ensure(seg.RecordingID, seg.RecordingSlug, seg.RecordingDate)
		a.segmentCount++
		a.totalTimeMs += seg.EndMs - seg.StartMs
	}

	recs := make([]idRecordingBreakdown, 0, len(aggByRec))
	var totalMs int64
	for _, a := range aggByRec {
		totalMs += a.totalTimeMs
		recs = append(recs, idRecordingBreakdown{
			RecordingID:  a.id,
			Slug:         a.slug,
			Date:         a.date,
			ClusterCount: a.clusterCount,
			SegmentCount: a.segmentCount,
			TotalTime:    formatDurationMs(a.totalTimeMs),
			TotalTimeMs:  a.totalTimeMs,
		})
	}
	sort.Slice(recs, func(i, j int) bool {
		if recs[i].Date != recs[j].Date {
			return recs[i].Date > recs[j].Date
		}
		return recs[i].RecordingID > recs[j].RecordingID
	})

	clusterViews := make([]idClusterView, 0, len(clusters))
	for _, c := range clusters {
		trackCount := strings.Count(c.TrackIDs, ",") + 1
		if strings.TrimSpace(c.TrackIDs) == "" || c.TrackIDs == "[]" {
			trackCount = 0
		}
		var rid int64
		if c.RecordingID != nil {
			rid = *c.RecordingID
		}
		clusterViews = append(clusterViews, idClusterView{
			ID:            c.ID,
			RecordingID:   rid,
			RecordingSlug: c.RecordingSlug,
			Status:        c.Status,
			TrackCount:    trackCount,
			ThumbnailURL:  cropPathToURL(c.ThumbnailPath),
		})
	}

	segmentViews := make([]idSegmentView, 0, len(segments))
	for _, seg := range segments {
		segmentViews = append(segmentViews, idSegmentView{
			ID:            seg.ID,
			RecordingID:   seg.RecordingID,
			RecordingSlug: seg.RecordingSlug,
			RecordingDate: seg.RecordingDate,
			StartMs:       seg.StartMs,
			Start:         formatMs(seg.StartMs),
			End:           formatMs(seg.EndMs),
			Duration:      formatDurationMs(seg.EndMs - seg.StartMs),
		})
	}

	thumb := ident.ThumbnailPath
	if thumb == "" {
		for _, c := range clusters {
			if c.ThumbnailPath != "" && c.Status == "confirmed" {
				thumb = c.ThumbnailPath
				break
			}
		}
	}

	data := identityDetailData{
		Identity:     *ident,
		ThumbnailURL: cropPathToURL(thumb),
		Groups:       groups,
		Recordings:   recs,
		Clusters:     clusterViews,
		Segments:     segmentViews,
		VideoCount:   len(recs),
		ClusterCount: len(clusterViews),
		SegmentCount: len(segmentViews),
		TotalTime:    formatDurationMs(totalMs),
	}

	tmpl, err := template.New("identity_detail").Parse(identityDetailTemplate)
	if err != nil {
		http.Error(w, "template error: "+err.Error(), http.StatusInternalServerError)
		return
	}
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	tmpl.Execute(w, data)
}

// handleClusterDetach handles POST /identities/{id}/clusters/{clusterID}/detach.
// Unlinks the cluster from this identity and flips it back to pending review.
func (s *Server) handleClusterDetach(w http.ResponseWriter, r *http.Request) {
	identityID, err := strconv.ParseInt(r.PathValue("id"), 10, 64)
	if err != nil {
		http.Error(w, "invalid identity ID", http.StatusBadRequest)
		return
	}
	clusterID, err := strconv.ParseInt(r.PathValue("clusterID"), 10, 64)
	if err != nil {
		http.Error(w, "invalid cluster ID", http.StatusBadRequest)
		return
	}

	recID, _ := s.db.GetClusterRecordingID(clusterID)

	if err := s.db.DetachClusterFromIdentity(clusterID); err != nil {
		http.Error(w, "detach failed: "+err.Error(), http.StatusInternalServerError)
		return
	}
	slog.Info("detached cluster from identity", "cluster_id", clusterID, "identity_id", identityID)
	s.regenerateSegments(recID)

	redirectOrHX(w, r, "/identities/"+strconv.FormatInt(identityID, 10))
}

var identityDetailTemplate = `<!DOCTYPE html>
<html lang="en">
<head>
    <meta charset="UTF-8">
    <meta name="viewport" content="width=device-width, initial-scale=1.0">
    <title>{{.Identity.Name}} · video-archive</title>
    <script src="https://unpkg.com/htmx.org@2.0.4"></script>
    <style>` + sharedStyles + `
    .header-card {
        display: flex;
        gap: 20px;
        align-items: center;
        background: #16213e;
        border: 1px solid #0f3460;
        border-radius: 12px;
        padding: 20px;
        margin-bottom: 24px;
    }
    .header-thumb {
        width: 96px;
        height: 96px;
        border-radius: 12px;
        object-fit: cover;
        background: #0f3460;
        flex-shrink: 0;
    }
    .header-thumb-empty {
        width: 96px;
        height: 96px;
        border-radius: 12px;
        background: #0f3460;
        display: flex;
        align-items: center;
        justify-content: center;
        color: #533483;
        font-size: 2rem;
        flex-shrink: 0;
    }
    .header-body { flex: 1; }
    .header-name { font-size: 1.4rem; font-weight: 600; color: #fff; margin-bottom: 6px; }
    .header-stats {
        display: flex;
        gap: 18px;
        font-size: 0.85rem;
        color: #aaa;
        margin-bottom: 8px;
    }
    .header-stats strong { color: #fff; }

    .section { margin-bottom: 28px; }
    .section h2 { margin-bottom: 12px; }

    .rec-table { width: 100%; border-collapse: collapse; }
    .rec-table th, .rec-table td {
        padding: 10px 12px;
        text-align: left;
        border-bottom: 1px solid #0f3460;
        font-size: 0.9rem;
    }
    .rec-table th {
        color: #888;
        font-weight: 500;
        font-size: 0.8rem;
        text-transform: uppercase;
        letter-spacing: 0.5px;
    }
    .rec-table tr:hover td { background: #16213e; }

    .cluster-grid {
        display: grid;
        grid-template-columns: repeat(auto-fill, minmax(200px, 1fr));
        gap: 12px;
    }
    .cluster-tile {
        background: #16213e;
        border: 1px solid #0f3460;
        border-radius: 10px;
        padding: 10px;
    }
    .cluster-tile.pending { border-color: #f39c12; }
    .cluster-tile.rejected { opacity: 0.4; }
    .cluster-tile-thumb {
        width: 100%;
        aspect-ratio: 1;
        border-radius: 8px;
        background: #0f3460;
        object-fit: cover;
        margin-bottom: 8px;
    }
    .cluster-tile-thumb-empty {
        width: 100%;
        aspect-ratio: 1;
        border-radius: 8px;
        background: #0f3460;
        display: flex;
        align-items: center;
        justify-content: center;
        color: #533483;
        margin-bottom: 8px;
    }
    .cluster-tile-meta {
        font-size: 0.75rem;
        color: #aaa;
        margin-bottom: 8px;
    }
    .cluster-tile-meta a { color: #c4b8ff; }
    .cluster-tile-actions { display: flex; gap: 6px; }
    .cluster-tile-actions button {
        font-size: 0.7rem;
        padding: 3px 8px;
    }

    .seg-list {
        background: #16213e;
        border: 1px solid #0f3460;
        border-radius: 10px;
        padding: 8px 0;
        max-height: 360px;
        overflow-y: auto;
    }
    .seg-row {
        display: flex;
        gap: 12px;
        padding: 6px 14px;
        font-size: 0.85rem;
        font-family: -apple-system, SF Mono, Menlo, monospace;
        color: inherit;
        text-decoration: none;
    }
    .seg-row:hover { background: #0f3460; }
    .seg-row .rec { flex: 1; color: #c4b8ff; }
    .seg-row .times { color: #888; min-width: 140px; }
    .seg-row .dur { color: #aaa; min-width: 60px; text-align: right; }
    </style>
</head>
<body>
    ` + renderNav("identities", 0) + `

    <p><a href="/identities">← All identities</a></p>

    <div class="header-card">
        {{if .ThumbnailURL}}
        <img class="header-thumb" src="{{.ThumbnailURL}}" alt="{{.Identity.Name}}">
        {{else}}
        <div class="header-thumb-empty">?</div>
        {{end}}
        <div class="header-body">
            <div class="header-name">{{.Identity.Name}}</div>
            <div class="header-stats">
                <span><strong>{{.VideoCount}}</strong> video{{if ne .VideoCount 1}}s{{end}}</span>
                <span><strong>{{.ClusterCount}}</strong> cluster{{if ne .ClusterCount 1}}s{{end}}</span>
                <span><strong>{{.SegmentCount}}</strong> segment{{if ne .SegmentCount 1}}s{{end}}</span>
                <span><strong>{{.TotalTime}}</strong> screen time</span>
            </div>
            {{if .Groups}}
            <div>
                {{range .Groups}}<span class="pill">{{.Name}}</span>{{end}}
            </div>
            {{end}}
        </div>
    </div>

    {{if .Recordings}}
    <div class="section">
        <h2>Appearances by recording</h2>
        <table class="rec-table">
            <thead>
                <tr>
                    <th>Date</th>
                    <th>Recording</th>
                    <th>Clusters</th>
                    <th>Segments</th>
                    <th>Screen time</th>
                    <th></th>
                </tr>
            </thead>
            <tbody>
                {{range .Recordings}}
                <tr>
                    <td>{{if .Date}}{{.Date}}{{else}}<span class="muted">—</span>{{end}}</td>
                    <td>{{.Slug}}</td>
                    <td>{{.ClusterCount}}</td>
                    <td>{{.SegmentCount}}</td>
                    <td>{{.TotalTime}}</td>
                    <td><a href="/review/{{.RecordingID}}">Review →</a></td>
                </tr>
                {{end}}
            </tbody>
        </table>
    </div>
    {{else}}
    <div class="empty-state">
        <p>This identity has no confirmed clusters or segments yet.</p>
    </div>
    {{end}}

    {{if .Clusters}}
    <div class="section">
        <h2>Clusters ({{.ClusterCount}})</h2>
        <div class="cluster-grid">
            {{range .Clusters}}
            <div class="cluster-tile {{.Status}}">
                {{if .ThumbnailURL}}
                <img class="cluster-tile-thumb" src="{{.ThumbnailURL}}" alt="cluster {{.ID}}" loading="lazy">
                {{else}}
                <div class="cluster-tile-thumb-empty">no thumb</div>
                {{end}}
                <div class="cluster-tile-meta">
                    #{{.ID}} · <a href="/review/{{.RecordingID}}">{{.RecordingSlug}}</a> · {{.TrackCount}} track{{if ne .TrackCount 1}}s{{end}}
                </div>
                <div class="cluster-tile-actions">
                    <form hx-post="/identities/{{$.Identity.ID}}/clusters/{{.ID}}/detach" hx-target="body"
                          hx-confirm="Unlink this cluster from {{$.Identity.Name}}? It will return to pending review.">
                        <button type="submit" class="btn-ghost">Unlink</button>
                    </form>
                </div>
            </div>
            {{end}}
        </div>
    </div>
    {{end}}

    {{if .Segments}}
    <div class="section">
        <h2>Segments ({{.SegmentCount}})</h2>
        <div class="seg-list">
            {{range .Segments}}
            <a class="seg-row" href="/review/{{.RecordingID}}#t={{.StartMs}}">
                <span class="rec">{{.RecordingSlug}}{{if .RecordingDate}} <span class="muted">· {{.RecordingDate}}</span>{{end}}</span>
                <span class="times">{{.Start}} → {{.End}}</span>
                <span class="dur">{{.Duration}}</span>
            </a>
            {{end}}
        </div>
    </div>
    {{end}}
</body>
</html>`
