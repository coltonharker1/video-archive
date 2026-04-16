package review

import (
	"fmt"
	"html/template"
	"net/http"
	"path/filepath"
	"strconv"
	"strings"

	"github.com/colton/video-archive/internal/model"
	"github.com/colton/video-archive/internal/store"
)

type scenePersonView struct {
	Name              string
	TotalTime         string
	TotalTimeMs       int64
	FirstAppearanceMs int64
	FirstAppearance   string
}

type sceneView struct {
	ID         int64
	Index      int // 1-based for display
	StartMs    int64
	EndMs      int64
	DurationMs int64
	Start      string
	End        string
	Duration   string
	Score      float64
	People     []scenePersonView
}

type scenesPageData struct {
	RecordingID   int64
	RecordingSlug string
	Scenes        []sceneView
	TotalScenes   int
	TotalPeople   int
	MasterURL     string
	NavHTML       template.HTML
}

func (s *Server) handleScenes(w http.ResponseWriter, r *http.Request) {
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

	scenes, err := s.db.ListScenes(recID)
	if err != nil {
		http.Error(w, "failed to load scenes", http.StatusInternalServerError)
		return
	}

	scenePeople, err := s.db.ListScenePeople(recID)
	if err != nil {
		http.Error(w, "failed to load scene people", http.StatusInternalServerError)
		return
	}

	// Group people by scene ID
	peopleByScene := make(map[int64][]store.ScenePersonView)
	for _, sp := range scenePeople {
		peopleByScene[sp.SceneID] = append(peopleByScene[sp.SceneID], sp)
	}

	allPeople := make(map[int64]bool)
	views := make([]sceneView, 0, len(scenes))
	for i, sc := range scenes {
		durMs := sc.EndMs - sc.StartMs
		sv := sceneView{
			ID:         sc.ID,
			Index:      i + 1,
			StartMs:    sc.StartMs,
			EndMs:      sc.EndMs,
			DurationMs: durMs,
			Start:      formatMs(sc.StartMs),
			End:        formatMs(sc.EndMs),
			Duration:   formatDurationMs(durMs),
			Score:      sc.Score,
		}

		for _, sp := range peopleByScene[sc.ID] {
			allPeople[sp.IdentityID] = true
			sv.People = append(sv.People, scenePersonView{
				Name:              sp.IdentityName,
				TotalTime:         formatDurationMs(sp.TotalTimeMs),
				TotalTimeMs:       sp.TotalTimeMs,
				FirstAppearanceMs: sp.FirstAppearanceMs,
				FirstAppearance:   formatMs(sp.FirstAppearanceMs),
			})
		}
		views = append(views, sv)
	}

	masterURL := ""
	if rec.MasterPath != "" {
		rel := strings.TrimPrefix(rec.MasterPath, "masters/")
		ext := strings.ToLower(filepath.Ext(rel))
		switch ext {
		case ".mp4", ".m4v", ".mov", ".webm":
			masterURL = "/static/master/" + rel
		}
	}

	data := scenesPageData{
		RecordingID:   recID,
		RecordingSlug: rec.Slug,
		Scenes:        views,
		TotalScenes:   len(views),
		TotalPeople:   len(allPeople),
		MasterURL:     masterURL,
		NavHTML:       template.HTML(renderNav("scenes", recID)),
	}

	tmpl, err := template.New("scenes").Funcs(template.FuncMap{
		"pct": func(partMs, totalMs int64) string {
			if totalMs <= 0 {
				return "0"
			}
			return fmt.Sprintf("%.0f", float64(partMs)*100/float64(totalMs))
		},
	}).Parse(scenesTemplate)
	if err != nil {
		http.Error(w, "template error: "+err.Error(), http.StatusInternalServerError)
		return
	}
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	tmpl.Execute(w, data)
}

// unused guard
var _ = model.Scene{}

var scenesTemplate = `<!DOCTYPE html>
<html lang="en">
<head>
    <meta charset="UTF-8">
    <meta name="viewport" content="width=device-width, initial-scale=1.0">
    <title>Scenes: {{.RecordingSlug}}</title>
    <style>` + sharedStyles + `
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
    .scene-list {
        display: flex;
        flex-direction: column;
        gap: 12px;
    }
    .scene-card {
        background: #16213e;
        border: 1px solid #0f3460;
        border-radius: 12px;
        padding: 16px;
        cursor: pointer;
        transition: border-color 0.2s;
    }
    .scene-card:hover { border-color: #533483; }
    .scene-card.selected { outline: 2px solid #c4b8ff; outline-offset: 3px; }
    .scene-header {
        display: flex;
        justify-content: space-between;
        align-items: center;
        margin-bottom: 8px;
    }
    .scene-header .scene-num {
        font-size: 0.75rem;
        color: #666;
    }
    .scene-header .scene-time {
        font-family: SF Mono, Menlo, monospace;
        font-size: 0.9rem;
        color: #c4b8ff;
    }
    .scene-header .scene-dur {
        font-size: 0.8rem;
        color: #888;
    }
    .scene-people {
        display: flex;
        flex-wrap: wrap;
        gap: 6px;
    }
    .scene-people .empty-hint {
        font-size: 0.8rem;
        color: #555;
        font-style: italic;
    }
    .person-chip {
        display: inline-flex;
        align-items: center;
        gap: 6px;
        background: #0f3460;
        border-radius: 8px;
        padding: 4px 10px;
        font-size: 0.8rem;
    }
    .person-chip .name { color: #fff; font-weight: 500; }
    .person-chip .time { color: #888; font-size: 0.7rem; }
    .person-chip .bar-wrap {
        width: 40px;
        height: 4px;
        background: #1a1a2e;
        border-radius: 2px;
        overflow: hidden;
    }
    .person-chip .bar {
        height: 100%;
        background: #533483;
        border-radius: 2px;
    }
    .jump-btn {
        font-size: 0.7rem;
        padding: 2px 8px;
        background: transparent;
        border: 1px solid #533483;
        color: #c4b8ff;
        border-radius: 4px;
        cursor: pointer;
        white-space: nowrap;
    }
    .jump-btn:hover { background: #16213e; }
    </style>
</head>
<body>
    {{.NavHTML}}
    <h1>Scenes: {{.RecordingSlug}}</h1>
    <p class="subtitle">Detected shot boundaries with people mapped from face analysis.</p>

    {{if .MasterURL}}
    <div class="player-wrap">
        <video id="va-player" controls preload="metadata" src="{{.MasterURL}}"></video>
        <div class="player-hint">Click any scene to play from that point. Click a person's "jump" to seek to their first appearance.</div>
    </div>
    {{else}}
    <div class="player-missing">
        Video preview unavailable — master file is missing or in a format the browser can't play.
    </div>
    {{end}}

    <div class="summary">
        <span class="stat"><strong>{{.TotalScenes}}</strong> scenes</span>
        <span class="stat"><strong>{{.TotalPeople}}</strong> people detected</span>
    </div>

    {{if not .Scenes}}
    <div class="empty-state">
        <p>No scenes detected yet.</p>
        <p>Run: <code>va scenes {{.RecordingID}}</code></p>
    </div>
    {{else}}
    <div class="scene-list">
        {{range .Scenes}}
        <div class="scene-card" data-start-ms="{{.StartMs}}" data-end-ms="{{.EndMs}}">
            <div class="scene-header">
                <span class="scene-num">Scene {{.Index}}</span>
                <span class="scene-time">{{.Start}} — {{.End}}</span>
                <span class="scene-dur">{{.Duration}}</span>
            </div>
            <div class="scene-people">
                {{if .People}}
                {{$sceneDurMs := .DurationMs}}
                {{range .People}}
                <div class="person-chip">
                    <span class="name">{{.Name}}</span>
                    <span class="time">{{.TotalTime}}</span>
                    <div class="bar-wrap"><div class="bar" style="width: {{pct .TotalTimeMs $sceneDurMs}}%"></div></div>
                    <button class="jump-btn" data-seek-ms="{{.FirstAppearanceMs}}" onclick="event.stopPropagation()">jump</button>
                </div>
                {{end}}
                {{else}}
                <span class="empty-hint">No identified people in this scene</span>
                {{end}}
            </div>
        </div>
        {{end}}
    </div>
    {{end}}

    <script>
    (function() {
        const v = document.getElementById('va-player');
        function seek(ms, play) {
            if (!v) return;
            const t = ms / 1000;
            const doSeek = () => { v.currentTime = t; if (play) v.play().catch(() => {}); else v.pause(); };
            if (isFinite(v.duration) && t <= v.duration) {
                doSeek();
            } else {
                v.addEventListener('loadedmetadata', doSeek, { once: true });
            }
        }

        // Click scene card → play from scene start
        const cards = document.querySelectorAll('.scene-card');
        let selected = null;
        cards.forEach(card => {
            card.addEventListener('click', () => {
                if (selected) selected.classList.remove('selected');
                card.classList.add('selected');
                selected = card;
                seek(parseInt(card.dataset.startMs, 10), true);
            });
        });

        // Click "jump" → seek to that person's first appearance
        document.querySelectorAll('.jump-btn[data-seek-ms]').forEach(btn => {
            btn.addEventListener('click', () => {
                seek(parseInt(btn.dataset.seekMs, 10), true);
            });
        });

        // Keyboard: j/k to move between scenes
        let selIdx = -1;
        const cardArr = Array.from(cards);
        function selectScene(i) {
            if (!cardArr.length) return;
            if (i < 0) i = 0;
            if (i >= cardArr.length) i = cardArr.length - 1;
            if (selected) selected.classList.remove('selected');
            selIdx = i;
            selected = cardArr[selIdx];
            selected.classList.add('selected');
            selected.scrollIntoView({ block: 'nearest', behavior: 'smooth' });
        }

        document.addEventListener('keydown', (e) => {
            const t = e.target;
            if (t && (t.tagName === 'INPUT' || t.tagName === 'SELECT' || t.tagName === 'TEXTAREA')) return;
            if (e.ctrlKey || e.metaKey || e.altKey) return;

            if (e.key === 'j' || e.key === 'ArrowDown') {
                e.preventDefault();
                selectScene(selIdx < 0 ? 0 : selIdx + 1);
                return;
            }
            if (e.key === 'k' || e.key === 'ArrowUp') {
                e.preventDefault();
                selectScene(selIdx <= 0 ? 0 : selIdx - 1);
                return;
            }
            if (e.key === 'Enter' && selIdx >= 0) {
                e.preventDefault();
                seek(parseInt(cardArr[selIdx].dataset.startMs, 10), true);
                return;
            }
        });

        // Hash-based jump
        const m = location.hash.match(/^#t=(\d+)/);
        if (m) seek(parseInt(m[1], 10), true);
    })();
    </script>
</body>
</html>`
