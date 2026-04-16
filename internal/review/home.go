package review

import (
	"html/template"
	"net/http"
)

type homeRecording struct {
	ID           int64
	Slug         string
	Date         string
	DurationMs   int64
	Duration     string
	ClusterCount int
	SceneCount   int
}

type homePageData struct {
	Recordings      []homeRecording
	IdentityCount   int
	GroupCount      int
}

func (s *Server) handleHome(w http.ResponseWriter, r *http.Request) {
	// Only serve on exact /
	if r.URL.Path != "/" {
		http.NotFound(w, r)
		return
	}

	recs, _ := s.db.ListRecordings()
	identities, _ := s.db.ListIdentities()
	groups, _ := s.db.ListGroups()

	views := make([]homeRecording, 0, len(recs))
	for _, rec := range recs {
		clusterCount, _ := s.db.CountClusters(rec.ID)
		sceneCount, _ := s.db.CountScenes(rec.ID)
		views = append(views, homeRecording{
			ID:           rec.ID,
			Slug:         rec.Slug,
			Date:         rec.Date,
			DurationMs:   rec.DurationMs,
			Duration:     formatDurationMs(rec.DurationMs),
			ClusterCount: clusterCount,
			SceneCount:   sceneCount,
		})
	}

	data := homePageData{
		Recordings:    views,
		IdentityCount: len(identities),
		GroupCount:    len(groups),
	}

	tmpl, err := template.New("home").Parse(homeTemplate)
	if err != nil {
		http.Error(w, "template error: "+err.Error(), http.StatusInternalServerError)
		return
	}
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	tmpl.Execute(w, data)
}

var homeTemplate = `<!DOCTYPE html>
<html lang="en">
<head>
    <meta charset="UTF-8">
    <meta name="viewport" content="width=device-width, initial-scale=1.0">
    <title>video-archive</title>
    <style>` + sharedStyles + `
    .recordings-table {
        width: 100%;
        border-collapse: collapse;
    }
    .recordings-table th, .recordings-table td {
        padding: 10px 12px;
        text-align: left;
        border-bottom: 1px solid #0f3460;
        font-size: 0.9rem;
    }
    .recordings-table th {
        color: #888;
        font-weight: 500;
        font-size: 0.8rem;
        text-transform: uppercase;
        letter-spacing: 0.5px;
    }
    .recordings-table tr:hover td { background: #16213e; }
    .stats-row {
        display: flex;
        gap: 24px;
        margin-bottom: 24px;
    }
    .stat-card {
        background: #16213e;
        border: 1px solid #0f3460;
        border-radius: 10px;
        padding: 14px 20px;
        flex: 1;
    }
    .stat-card .value {
        font-size: 1.6rem;
        font-weight: 600;
        color: #fff;
    }
    .stat-card .label {
        font-size: 0.8rem;
        color: #888;
        margin-top: 2px;
    }
    </style>
</head>
<body>
    ` + renderNav("", 0) + `

    <h1>video-archive</h1>
    <p class="subtitle">Face recognition across home video footage.</p>

    <div class="stats-row">
        <div class="stat-card">
            <div class="value">{{len .Recordings}}</div>
            <div class="label">Recordings</div>
        </div>
        <div class="stat-card">
            <div class="value">{{.IdentityCount}}</div>
            <div class="label">Identities</div>
        </div>
        <div class="stat-card">
            <div class="value">{{.GroupCount}}</div>
            <div class="label">Groups</div>
        </div>
    </div>

    {{if not .Recordings}}
    <div class="empty-state">
        <p>No recordings yet.</p>
        <p>Start with: <code>va run /path/to/video.mp4</code></p>
    </div>
    {{else}}
    <h2>Recordings</h2>
    <table class="recordings-table">
        <thead>
            <tr>
                <th>ID</th>
                <th>Date</th>
                <th>Name</th>
                <th>Duration</th>
                <th>Clusters</th>
                <th>Scenes</th>
                <th></th>
            </tr>
        </thead>
        <tbody>
            {{range .Recordings}}
            <tr>
                <td>{{.ID}}</td>
                <td>{{.Date}}</td>
                <td>{{.Slug}}</td>
                <td>{{.Duration}}</td>
                <td>{{.ClusterCount}}</td>
                <td>{{.SceneCount}}</td>
                <td><a href="/review/{{.ID}}">Review</a> · <a href="/scenes/{{.ID}}">Scenes</a></td>
            </tr>
            {{end}}
        </tbody>
    </table>
    {{end}}
</body>
</html>`
