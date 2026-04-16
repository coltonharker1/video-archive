package review

import (
	"encoding/json"
	"fmt"
	"html/template"
	"log/slog"
	"net/http"
	"path/filepath"
	"strconv"
	"strings"

	"github.com/colton/video-archive/internal/model"
)

// thumbView is a single face-crop thumbnail with enough metadata for the
// review page to wire click → seek the video player AND → open a lightbox
// showing the full source frame with the detected bbox overlaid.
type thumbView struct {
	URL         string // crop image URL
	TimestampMs int64

	// Full source-frame lightbox info
	FrameURL string // full frame image URL, may be "" if frame missing
	FrameW   int
	FrameH   int
	BboxX    float64 // pixel coords in the source frame
	BboxY    float64
	BboxW    float64
	BboxH    float64
	Conf     float64
}

// clusterView is the data passed to the template for each cluster.
type clusterView struct {
	ID           int64
	Status       string
	IdentityName string
	TrackCount   int
	Thumbnails   []thumbView
	TimeRange    string
	StartMs      int64 // earliest detection timestamp — used for "jump to cluster" link
	TotalTimeMs  int64
	TrackIDs     []int64
}

// reviewData is the top-level template data.
type reviewData struct {
	RecordingID    int64
	RecordingSlug  string
	Clusters       []clusterView
	AllClusters    []clusterView // for merge dropdown
	Message        string
	NavHTML        template.HTML
	PendingCount   int
	ConfirmedCount int
	RejectedCount  int
	MasterURL      string // URL under /static/master/ for the <video> src, "" if unplayable
}

func (s *Server) handleReview(w http.ResponseWriter, r *http.Request) {
	recID, err := strconv.ParseInt(r.PathValue("recordingID"), 10, 64)
	if err != nil {
		http.Error(w, "invalid recording ID", http.StatusBadRequest)
		return
	}

	rec, err := s.db.GetRecording(recID)
	if err != nil {
		http.Error(w, "recording not found", http.StatusNotFound)
		return
	}

	clusters, err := s.db.ListClusters(recID)
	if err != nil {
		http.Error(w, "failed to load clusters", http.StatusInternalServerError)
		return
	}

	tracks, err := s.db.ListTracks(recID)
	if err != nil {
		http.Error(w, "failed to load tracks", http.StatusInternalServerError)
		return
	}
	trackMap := make(map[int64]model.Track)
	for _, t := range tracks {
		trackMap[t.ID] = t
	}

	// Load all detections for thumbnail lookup
	dets, _ := s.db.ListDetections(recID)
	detMap := make(map[int64]model.Detection)
	for _, d := range dets {
		detMap[d.ID] = d
	}

	// Load frames so thumbnails can open a lightbox of the original detection frame
	frames, _ := s.db.ListFrames(recID)
	frameMap := make(map[int64]model.FrameSample)
	for _, f := range frames {
		frameMap[f.ID] = f
	}

	var views []clusterView
	var pending, confirmed, rejected int
	for _, c := range clusters {
		cv := s.buildClusterView(c, trackMap, detMap, frameMap, rec)
		views = append(views, cv)
		switch cv.Status {
		case "pending":
			pending++
		case "confirmed":
			confirmed++
		case "rejected":
			rejected++
		}
	}

	masterURL := ""
	if rec.MasterPath != "" {
		rel := strings.TrimPrefix(rec.MasterPath, "masters/")
		ext := strings.ToLower(filepath.Ext(rel))
		// Only expose formats the browser can play natively.
		switch ext {
		case ".mp4", ".m4v", ".mov", ".webm":
			masterURL = "/static/master/" + rel
		}
	}

	data := reviewData{
		RecordingID:    recID,
		RecordingSlug:  rec.Slug,
		Clusters:       views,
		AllClusters:    views,
		NavHTML:        template.HTML(renderNav("review", recID)),
		PendingCount:   pending,
		ConfirmedCount: confirmed,
		RejectedCount:  rejected,
		MasterURL:      masterURL,
	}

	s.renderPage(w, data)
}

func (s *Server) buildClusterView(c model.Cluster, trackMap map[int64]model.Track, detMap map[int64]model.Detection, frameMap map[int64]model.FrameSample, rec *model.Recording) clusterView {
	var trackIDs []int64
	json.Unmarshal([]byte(c.TrackIDs), &trackIDs)

	// Collect thumbnails from track detections (up to 5 best)
	var thumbnails []thumbView
	var minMs, maxMs int64
	var totalMs int64
	first := true

	for _, tid := range trackIDs {
		t, ok := trackMap[tid]
		if !ok {
			continue
		}
		if first || t.StartMs < minMs {
			minMs = t.StartMs
		}
		if first || t.EndMs > maxMs {
			maxMs = t.EndMs
		}
		totalMs += t.EndMs - t.StartMs
		first = false

		// Get detection IDs for this track
		var detIDs []int64
		json.Unmarshal([]byte(t.DetectionIDs), &detIDs)

		for _, did := range detIDs {
			d, ok := detMap[did]
			if !ok || d.CropPath == "" {
				continue
			}
			tv := thumbView{
				URL:         "/static/crops/" + strings.TrimPrefix(d.CropPath, "crops/"),
				TimestampMs: d.TimestampMs,
				BboxX:       d.BboxX,
				BboxY:       d.BboxY,
				BboxW:       d.BboxW,
				BboxH:       d.BboxH,
				Conf:        d.Confidence,
				FrameW:      rec.Width,
				FrameH:      rec.Height,
			}
			if f, ok := frameMap[d.FrameID]; ok && f.FramePath != "" {
				tv.FrameURL = "/static/frames/" + strings.TrimPrefix(f.FramePath, "frames/")
				// Prefer the frame's own dims if stored, else fall back to recording dims
				if f.Width > 0 {
					tv.FrameW = f.Width
				}
				if f.Height > 0 {
					tv.FrameH = f.Height
				}
			}
			thumbnails = append(thumbnails, tv)
		}
	}

	// Limit to 5 thumbnails (spread evenly)
	if len(thumbnails) > 5 {
		step := len(thumbnails) / 5
		selected := make([]thumbView, 5)
		for i := 0; i < 5; i++ {
			selected[i] = thumbnails[i*step]
		}
		thumbnails = selected
	}

	timeRange := ""
	if !first {
		timeRange = fmt.Sprintf("%s - %s", formatMs(minMs), formatMs(maxMs))
	}

	identityName := ""
	if c.IdentityID != nil {
		if ident, err := s.db.GetIdentity(*c.IdentityID); err == nil {
			identityName = ident.Name
		}
	}

	return clusterView{
		ID:           c.ID,
		Status:       c.Status,
		IdentityName: identityName,
		TrackCount:   len(trackIDs),
		Thumbnails:   thumbnails,
		TimeRange:    timeRange,
		StartMs:      minMs,
		TotalTimeMs:  totalMs,
		TrackIDs:     trackIDs,
	}
}

func (s *Server) handleNameCluster(w http.ResponseWriter, r *http.Request) {
	recID, _ := strconv.ParseInt(r.PathValue("recordingID"), 10, 64)
	clusterID, _ := strconv.ParseInt(r.PathValue("clusterID"), 10, 64)

	r.ParseForm()
	name := strings.TrimSpace(r.FormValue("name"))
	if name == "" {
		http.Error(w, "name is required", http.StatusBadRequest)
		return
	}

	// Find or create identity
	identity, err := s.db.FindIdentityByName(name)
	if err != nil {
		// Create new identity
		id, err := s.db.CreateIdentity(name)
		if err != nil {
			http.Error(w, "failed to create identity", http.StatusInternalServerError)
			return
		}
		identity = &model.Identity{ID: id, Name: name}
	}

	// Link cluster to identity
	if err := s.db.UpdateClusterIdentity(clusterID, identity.ID); err != nil {
		http.Error(w, "failed to update cluster", http.StatusInternalServerError)
		return
	}

	slog.Info("named cluster", "cluster_id", clusterID, "identity", name)
	s.regenerateSegments(recID)

	// If HTMX request, re-render just this cluster card
	if r.Header.Get("HX-Request") == "true" {
		s.redirectToReview(w, r, recID)
		return
	}
	http.Redirect(w, r, fmt.Sprintf("/review/%d", recID), http.StatusSeeOther)
}

func (s *Server) handleRejectCluster(w http.ResponseWriter, r *http.Request) {
	recID, _ := strconv.ParseInt(r.PathValue("recordingID"), 10, 64)
	clusterID, _ := strconv.ParseInt(r.PathValue("clusterID"), 10, 64)

	if err := s.db.UpdateClusterStatus(clusterID, "rejected"); err != nil {
		http.Error(w, "failed to reject cluster", http.StatusInternalServerError)
		return
	}

	slog.Info("rejected cluster", "cluster_id", clusterID)
	s.regenerateSegments(recID)

	if r.Header.Get("HX-Request") == "true" {
		s.redirectToReview(w, r, recID)
		return
	}
	http.Redirect(w, r, fmt.Sprintf("/review/%d", recID), http.StatusSeeOther)
}

func (s *Server) handleMergeClusters(w http.ResponseWriter, r *http.Request) {
	recID, _ := strconv.ParseInt(r.PathValue("recordingID"), 10, 64)
	r.ParseForm()

	dstID, err := strconv.ParseInt(r.FormValue("dst"), 10, 64)
	if err != nil {
		http.Error(w, "invalid dst cluster", http.StatusBadRequest)
		return
	}
	srcID, err := strconv.ParseInt(r.FormValue("src"), 10, 64)
	if err != nil {
		http.Error(w, "invalid src cluster", http.StatusBadRequest)
		return
	}

	if dstID == srcID {
		http.Error(w, "cannot merge cluster with itself", http.StatusBadRequest)
		return
	}

	if err := s.db.MergeClusters(dstID, srcID); err != nil {
		http.Error(w, "merge failed: "+err.Error(), http.StatusInternalServerError)
		return
	}

	slog.Info("merged clusters", "dst", dstID, "src", srcID)
	s.regenerateSegments(recID)

	if r.Header.Get("HX-Request") == "true" {
		s.redirectToReview(w, r, recID)
		return
	}
	http.Redirect(w, r, fmt.Sprintf("/review/%d", recID), http.StatusSeeOther)
}

func (s *Server) redirectToReview(w http.ResponseWriter, r *http.Request, recID int64) {
	w.Header().Set("HX-Redirect", fmt.Sprintf("/review/%d", recID))
	w.WriteHeader(http.StatusOK)
}

func (s *Server) renderPage(w http.ResponseWriter, data reviewData) {
	tmpl, err := template.New("review").Funcs(template.FuncMap{
		"formatDuration": func(ms int64) string {
			sec := ms / 1000
			if sec < 60 {
				return fmt.Sprintf("%ds", sec)
			}
			return fmt.Sprintf("%d:%02d", sec/60, sec%60)
		},
	}).Parse(reviewTemplate)
	if err != nil {
		http.Error(w, "template error: "+err.Error(), http.StatusInternalServerError)
		return
	}
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	tmpl.Execute(w, data)
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

// Keep templates as a string constant to avoid file embedding complexity for now.
// Move to go:embed when templates stabilize.
var reviewTemplate = `<!DOCTYPE html>
<html lang="en">
<head>
    <meta charset="UTF-8">
    <meta name="viewport" content="width=device-width, initial-scale=1.0">
    <title>Review: {{.RecordingSlug}}</title>
    <script src="https://unpkg.com/htmx.org@2.0.4"></script>
    <style>
        * { box-sizing: border-box; margin: 0; padding: 0; }
        body {
            font-family: -apple-system, BlinkMacSystemFont, "Segoe UI", Roboto, sans-serif;
            background: #1a1a2e;
            color: #e0e0e0;
            padding: 20px;
            max-width: 1200px;
            margin: 0 auto;
        }
        h1 {
            font-size: 1.5rem;
            margin-bottom: 8px;
            color: #fff;
        }
        .subtitle {
            color: #888;
            margin-bottom: 24px;
            font-size: 0.9rem;
        }
        .clusters {
            display: grid;
            grid-template-columns: repeat(auto-fill, minmax(340px, 1fr));
            gap: 20px;
        }
        .cluster-card {
            background: #16213e;
            border: 1px solid #0f3460;
            border-radius: 12px;
            padding: 16px;
            transition: border-color 0.2s;
        }
        .cluster-card:hover { border-color: #533483; }
        .cluster-card.confirmed { border-color: #2ecc71; }
        .cluster-card.rejected { opacity: 0.4; border-color: #e74c3c; }
        .card-header {
            display: flex;
            justify-content: space-between;
            align-items: center;
            margin-bottom: 12px;
        }
        .cluster-id {
            font-size: 0.8rem;
            color: #666;
        }
        .status-badge {
            font-size: 0.7rem;
            padding: 2px 8px;
            border-radius: 10px;
            text-transform: uppercase;
            font-weight: 600;
        }
        .status-pending { background: #f39c12; color: #000; }
        .status-confirmed { background: #2ecc71; color: #000; }
        .status-rejected { background: #e74c3c; color: #fff; }
        .thumbnails {
            display: flex;
            gap: 6px;
            margin-bottom: 12px;
            flex-wrap: wrap;
        }
        .thumbnails img {
            width: 60px;
            height: 60px;
            object-fit: cover;
            border-radius: 6px;
            border: 2px solid transparent;
        }
        .thumbnails img:first-child {
            width: 80px;
            height: 80px;
            border-color: #533483;
        }
        .identity-name {
            font-size: 1.1rem;
            font-weight: 600;
            color: #2ecc71;
            margin-bottom: 8px;
        }
        .meta {
            font-size: 0.8rem;
            color: #888;
            margin-bottom: 12px;
        }
        .meta span { margin-right: 12px; }
        .actions {
            display: flex;
            gap: 8px;
            flex-wrap: wrap;
            align-items: center;
        }
        input[type="text"] {
            background: #0f3460;
            border: 1px solid #533483;
            color: #fff;
            padding: 6px 10px;
            border-radius: 6px;
            font-size: 0.85rem;
            width: 140px;
        }
        input[type="text"]::placeholder { color: #666; }
        button, select {
            background: #533483;
            color: #fff;
            border: none;
            padding: 6px 14px;
            border-radius: 6px;
            cursor: pointer;
            font-size: 0.8rem;
        }
        button:hover { background: #6c44a2; }
        .btn-reject {
            background: transparent;
            border: 1px solid #e74c3c;
            color: #e74c3c;
        }
        .btn-reject:hover { background: #e74c3c; color: #fff; }
        select {
            background: #0f3460;
            border: 1px solid #533483;
        }
        .merge-form { display: flex; gap: 6px; align-items: center; }
        .empty-state {
            text-align: center;
            padding: 60px 20px;
            color: #666;
        }
        .summary {
            background: #16213e;
            border-radius: 8px;
            padding: 12px 16px;
            margin-bottom: 20px;
            font-size: 0.85rem;
            display: flex;
            gap: 20px;
        }
        .summary .stat { color: #aaa; }
        .summary .stat strong { color: #fff; }
        .player-wrap {
            position: sticky;
            top: 0;
            background: #1a1a2e;
            padding: 8px 0 12px 0;
            margin-bottom: 16px;
            z-index: 10;
            border-bottom: 1px solid #0f3460;
        }
        .player-wrap video {
            width: 100%;
            max-height: 320px;
            background: #000;
            border-radius: 8px;
        }
        .player-hint {
            font-size: 0.75rem;
            color: #666;
            margin-top: 6px;
        }
        .player-missing {
            background: #16213e;
            padding: 10px 14px;
            border-radius: 8px;
            margin-bottom: 16px;
            font-size: 0.85rem;
            color: #888;
        }
        .thumbnails img.seekable { cursor: pointer; }
        .thumbnails img.seekable:hover { border-color: #9b8fd1 !important; }

        /* Source-frame lightbox */
        .frame-modal {
            position: fixed;
            inset: 0;
            background: rgba(0,0,0,0.88);
            display: none;
            align-items: center;
            justify-content: center;
            z-index: 1000;
            padding: 32px;
        }
        .frame-modal.open { display: flex; }
        .frame-stage {
            position: relative;
            max-width: 100%;
            max-height: 100%;
            display: inline-block;
        }
        .frame-stage img {
            display: block;
            max-width: 90vw;
            max-height: calc(100vh - 80px);
            object-fit: contain;
            border-radius: 4px;
        }
        .frame-bbox {
            position: absolute;
            border: 2px solid #2ecc71;
            box-shadow: 0 0 0 1px rgba(0,0,0,0.6), 0 0 12px rgba(46,204,113,0.7);
            pointer-events: none;
            border-radius: 2px;
        }
        .frame-caption {
            position: absolute;
            bottom: -30px;
            left: 0;
            right: 0;
            text-align: center;
            color: #aaa;
            font-size: 0.8rem;
            font-family: SF Mono, Menlo, monospace;
        }
        .frame-close {
            position: absolute;
            top: 16px;
            right: 20px;
            color: #fff;
            font-size: 1.4rem;
            background: transparent;
            border: none;
            cursor: pointer;
            opacity: 0.7;
        }
        .frame-close:hover { opacity: 1; background: transparent; }

        .cluster-card.selected {
            outline: 2px solid #c4b8ff;
            outline-offset: 3px;
        }
        .kb-hint {
            font-size: 0.75rem;
            color: #666;
            margin-bottom: 12px;
        }
        .kb-hint kbd {
            background: #0f3460;
            color: #c4b8ff;
            padding: 1px 6px;
            border-radius: 4px;
            font-family: SF Mono, Menlo, monospace;
            font-size: 0.75rem;
        }
        .kb-help {
            position: fixed;
            inset: 0;
            background: rgba(0,0,0,0.85);
            display: none;
            align-items: center;
            justify-content: center;
            z-index: 1100;
        }
        .kb-help.open { display: flex; }
        .kb-help-inner {
            background: #16213e;
            border: 1px solid #533483;
            border-radius: 12px;
            padding: 24px 28px;
            min-width: 360px;
            color: #e0e0e0;
        }
        .kb-help-inner h3 { margin-bottom: 12px; color: #fff; }
        .kb-help-inner dl {
            display: grid;
            grid-template-columns: 90px 1fr;
            gap: 6px 14px;
            font-size: 0.9rem;
        }
        .kb-help-inner dt { color: #c4b8ff; font-family: SF Mono, Menlo, monospace; }
        .kb-help-inner dd { color: #ccc; }
        .kb-help-close { margin-top: 16px; }
    </style>
</head>
<body>
    {{.NavHTML}}
    <h1>Review: {{.RecordingSlug}}</h1>
    <p class="subtitle">Assign names to identified people. Merge clusters that are the same person. Reject false positives.</p>

    {{if .MasterURL}}
    <div class="player-wrap">
        <video id="va-player" controls preload="metadata" src="{{.MasterURL}}"></video>
        <div class="player-hint">Click any thumbnail to jump the player to that moment.</div>
    </div>
    {{else}}
    <div class="player-missing">
        Video preview unavailable — master file is missing or in a format the browser can't play (mp4/mov/webm only).
    </div>
    {{end}}

    <div class="summary">
        <span class="stat"><strong>{{len .Clusters}}</strong> clusters</span>
        <span class="stat"><strong>{{.PendingCount}}</strong> pending</span>
        <span class="stat"><strong>{{.ConfirmedCount}}</strong> confirmed</span>
        <span class="stat"><strong>{{.RejectedCount}}</strong> rejected</span>
    </div>
    <div class="kb-hint">
        Shortcuts: <kbd>j</kbd>/<kbd>k</kbd> move · <kbd>n</kbd> name · <kbd>r</kbd> reject · <kbd>?</kbd> help
    </div>

    {{if not .Clusters}}
    <div class="empty-state">
        <p>No clusters found for this recording.</p>
        <p>Run the pipeline first: va run &lt;video&gt;</p>
    </div>
    {{else}}
    <div class="clusters">
        {{range .Clusters}}
        <div class="cluster-card {{.Status}}">
            <div class="card-header">
                <span class="cluster-id">Cluster #{{.ID}}</span>
                <span class="status-badge status-{{.Status}}">{{.Status}}</span>
            </div>

            {{if .IdentityName}}
            <div class="identity-name">{{.IdentityName}}</div>
            {{end}}

            <div class="thumbnails">
                {{range .Thumbnails}}
                <img src="{{.URL}}" alt="face" loading="lazy"
                     class="seekable" data-seek-ms="{{.TimestampMs}}"
                     data-frame-url="{{.FrameURL}}"
                     data-frame-w="{{.FrameW}}" data-frame-h="{{.FrameH}}"
                     data-bbox-x="{{.BboxX}}" data-bbox-y="{{.BboxY}}"
                     data-bbox-w="{{.BboxW}}" data-bbox-h="{{.BboxH}}"
                     data-conf="{{.Conf}}"
                     title="Click to inspect the source frame">
                {{end}}
                {{if not .Thumbnails}}
                <span style="color:#666; font-size:0.8rem;">No thumbnails</span>
                {{end}}
            </div>

            <div class="meta">
                <span>{{.TrackCount}} track{{if ne .TrackCount 1}}s{{end}}</span>
                {{if .TimeRange}}<span>{{.TimeRange}}</span>{{end}}
                {{if gt .TotalTimeMs 0}}<span>~{{formatDuration .TotalTimeMs}}</span>{{end}}
            </div>

            {{if ne .Status "rejected"}}
            <div class="actions">
                <form method="POST" action="/review/` + "{{$.RecordingID}}" + `/clusters/{{.ID}}/name"
                      hx-post="/review/` + "{{$.RecordingID}}" + `/clusters/{{.ID}}/name"
                      hx-target="body" style="display:flex;gap:6px;">
                    <input type="text" name="name" placeholder="Name..." value="{{.IdentityName}}">
                    <button type="submit">Save</button>
                </form>

                <form class="merge-form" method="POST" action="/review/` + "{{$.RecordingID}}" + `/clusters/merge"
                      hx-post="/review/` + "{{$.RecordingID}}" + `/clusters/merge"
                      hx-target="body">
                    <input type="hidden" name="src" value="{{.ID}}">
                    <select name="dst">
                        <option value="">Merge into...</option>
                        {{range $.AllClusters}}
                        {{if ne .Status "rejected"}}
                        <option value="{{.ID}}">{{if .IdentityName}}{{.IdentityName}}{{else}}Cluster #{{.ID}}{{end}}</option>
                        {{end}}
                        {{end}}
                    </select>
                    <button type="submit">Merge</button>
                </form>

                <form method="POST" action="/review/` + "{{$.RecordingID}}" + `/clusters/{{.ID}}/reject"
                      hx-post="/review/` + "{{$.RecordingID}}" + `/clusters/{{.ID}}/reject"
                      hx-target="body" hx-confirm="Reject this cluster?">
                    <button type="submit" class="btn-reject">Reject</button>
                </form>
            </div>
            {{end}}
        </div>
        {{end}}
    </div>
    {{end}}

    <div class="kb-help" id="kb-help">
        <div class="kb-help-inner">
            <h3>Keyboard shortcuts</h3>
            <dl>
                <dt>j / ↓</dt><dd>Next cluster</dd>
                <dt>k / ↑</dt><dd>Previous cluster</dd>
                <dt>n</dt><dd>Focus name input</dd>
                <dt>Enter</dt><dd>Save name (while focused)</dd>
                <dt>r</dt><dd>Reject current cluster</dd>
                <dt>Esc</dt><dd>Blur input / close overlay</dd>
                <dt>?</dt><dd>Toggle this help</dd>
            </dl>
            <button class="kb-help-close" onclick="document.getElementById('kb-help').classList.remove('open')">Close</button>
        </div>
    </div>

    <div class="frame-modal" id="frame-modal">
        <button class="frame-close" id="frame-close" aria-label="Close">✕</button>
        <div class="frame-stage" id="frame-stage">
            <img id="frame-img" alt="source frame">
            <div class="frame-bbox" id="frame-bbox"></div>
            <div class="frame-caption" id="frame-caption"></div>
        </div>
    </div>

    <script>
    (function() {
        const v = document.getElementById('va-player');

        function seek(ms, play) {
            if (!v) return;
            const t = ms / 1000;
            const doSeek = () => { v.currentTime = t; if (!play) v.pause(); };
            if (isFinite(v.duration) && t <= v.duration) {
                doSeek();
            } else {
                v.addEventListener('loadedmetadata', doSeek, { once: true });
            }
            if (play) v.play().catch(() => {});
        }

        // Lightbox state
        const modal = document.getElementById('frame-modal');
        const img = document.getElementById('frame-img');
        const box = document.getElementById('frame-bbox');
        const caption = document.getElementById('frame-caption');
        let current = null;

        function placeBox() {
            if (!current) return;
            const natW = img.naturalWidth, natH = img.naturalHeight;
            if (!natW || !natH) return;
            const r = img.getBoundingClientRect();
            const stageR = img.parentElement.getBoundingClientRect();
            // object-fit: contain — compute the displayed image rect inside the img element
            const scale = Math.min(r.width / natW, r.height / natH);
            const dispW = natW * scale;
            const dispH = natH * scale;
            const offX = (r.width - dispW) / 2 + (r.left - stageR.left);
            const offY = (r.height - dispH) / 2 + (r.top - stageR.top);
            // Bbox is stored in the frame image's native coord system — detection ran
            // on this exact image. No frame-vs-recording dim scaling needed.
            box.style.left = (offX + current.bx * scale) + 'px';
            box.style.top  = (offY + current.by * scale) + 'px';
            box.style.width  = (current.bw * scale) + 'px';
            box.style.height = (current.bh * scale) + 'px';
        }

        function openFrame(d) {
            current = d;
            const setCaption = () => {
                caption.textContent = 't=' + d.timestampMs + 'ms · conf=' + (d.conf).toFixed(2) + ' · ' + img.naturalWidth + '×' + img.naturalHeight;
            };
            img.onload = () => { setCaption(); placeBox(); };
            img.src = d.frameUrl;
            modal.classList.add('open');
            if (img.complete && img.naturalWidth) { setCaption(); placeBox(); }
        }

        function closeFrame() {
            modal.classList.remove('open');
            current = null;
            img.src = '';
        }

        modal.addEventListener('click', (e) => {
            if (e.target === modal) closeFrame();
        });
        document.getElementById('frame-close').addEventListener('click', closeFrame);
        document.addEventListener('keydown', (e) => {
            if (e.key === 'Escape' && modal.classList.contains('open')) closeFrame();
        });
        window.addEventListener('resize', placeBox);

        document.querySelectorAll('[data-seek-ms]').forEach(el => {
            el.addEventListener('click', (e) => {
                e.preventDefault();
                const ms = parseInt(el.dataset.seekMs, 10);
                seek(ms, false); // seek video, keep it paused
                const frameUrl = el.dataset.frameUrl;
                if (frameUrl) {
                    openFrame({
                        frameUrl: frameUrl,
                        frameW: parseInt(el.dataset.frameW, 10) || 720,
                        frameH: parseInt(el.dataset.frameH, 10) || 480,
                        bx: parseFloat(el.dataset.bboxX),
                        by: parseFloat(el.dataset.bboxY),
                        bw: parseFloat(el.dataset.bboxW),
                        bh: parseFloat(el.dataset.bboxH),
                        timestampMs: ms,
                        conf: parseFloat(el.dataset.conf) || 0,
                    });
                }
            });
        });

        // Hash-based jump from identity-detail segments: play through, no modal
        const m = location.hash.match(/^#t=(\d+)/);
        if (m) seek(parseInt(m[1], 10), true);

        // Keyboard navigation for the cluster grid.
        const cards = Array.from(document.querySelectorAll('.cluster-card'));
        const help = document.getElementById('kb-help');
        let selIdx = -1;

        function select(i) {
            if (!cards.length) return;
            if (i < 0) i = 0;
            if (i >= cards.length) i = cards.length - 1;
            if (selIdx >= 0 && cards[selIdx]) cards[selIdx].classList.remove('selected');
            selIdx = i;
            cards[selIdx].classList.add('selected');
            cards[selIdx].scrollIntoView({ block: 'nearest', behavior: 'smooth' });
        }

        // Start with the first non-rejected card, or first card overall.
        const firstLive = cards.findIndex(c => !c.classList.contains('rejected'));
        if (firstLive >= 0) select(firstLive);
        else if (cards.length) select(0);

        cards.forEach((c, i) => c.addEventListener('click', () => select(i)));

        document.addEventListener('keydown', (e) => {
            // Esc always closes modal/help or blurs input.
            if (e.key === 'Escape') {
                if (modal.classList.contains('open')) { closeFrame(); return; }
                if (help.classList.contains('open')) { help.classList.remove('open'); return; }
                if (document.activeElement && document.activeElement.blur) document.activeElement.blur();
                return;
            }

            // Skip shortcuts when typing in a form field.
            const t = e.target;
            if (t && (t.tagName === 'INPUT' || t.tagName === 'SELECT' || t.tagName === 'TEXTAREA')) return;
            if (e.ctrlKey || e.metaKey || e.altKey) return;

            if (e.key === '?' || (e.shiftKey && e.key === '/')) {
                e.preventDefault();
                help.classList.toggle('open');
                return;
            }
            if (e.key === 'j' || e.key === 'ArrowDown') {
                e.preventDefault();
                select(selIdx < 0 ? 0 : selIdx + 1);
                return;
            }
            if (e.key === 'k' || e.key === 'ArrowUp') {
                e.preventDefault();
                select(selIdx <= 0 ? 0 : selIdx - 1);
                return;
            }
            if (selIdx < 0) return;
            const card = cards[selIdx];
            if (e.key === 'n') {
                const input = card.querySelector('input[name="name"]');
                if (input) { e.preventDefault(); input.focus(); input.select(); }
                return;
            }
            if (e.key === 'r') {
                const rejectBtn = card.querySelector('form[hx-post$="/reject"] button');
                if (rejectBtn && confirm('Reject this cluster?')) {
                    e.preventDefault();
                    rejectBtn.click();
                }
                return;
            }
        });

        help.addEventListener('click', (e) => {
            if (e.target === help) help.classList.remove('open');
        });
    })();
    </script>
</body>
</html>`

