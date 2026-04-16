package review

import (
	"encoding/json"
	"fmt"
	"html/template"
	"log/slog"
	"net/http"
	"net/url"
	"strconv"
	"strings"

	"github.com/colton/video-archive/internal/model"
	"github.com/colton/video-archive/internal/store"
)

// identityView is a single identity as rendered in the UI.
type identityView struct {
	ID            int64
	Name          string
	ThumbnailURL  string
	VideoCount    int
	ClusterCount  int
	SegmentCount  int
	TotalTime     string
	Groups        []string
}

type identitiesPageData struct {
	Identities []identityView
	Total      int
}

func (s *Server) handleIdentities(w http.ResponseWriter, r *http.Request) {
	stats, err := s.db.ListIdentitiesWithStats()
	if err != nil {
		http.Error(w, "failed to load identities: "+err.Error(), http.StatusInternalServerError)
		return
	}

	views := make([]identityView, 0, len(stats))
	for _, st := range stats {
		// Resolve thumbnail to a URL (crops are under /static/crops/)
		thumbURL := ""
		if st.ThumbnailPath != "" {
			rel := strings.TrimPrefix(st.ThumbnailPath, "crops/")
			thumbURL = "/static/crops/" + rel
		}

		groupNames := []string{}
		if groups, err := s.db.ListGroupsForIdentity(st.ID); err == nil {
			for _, g := range groups {
				groupNames = append(groupNames, g.Name)
			}
		}

		views = append(views, identityView{
			ID:           st.ID,
			Name:         st.Name,
			ThumbnailURL: thumbURL,
			VideoCount:   st.VideoCount,
			ClusterCount: st.ClusterCount,
			SegmentCount: st.SegmentCount,
			TotalTime:    formatDurationMs(st.TotalTimeMs),
			Groups:       groupNames,
		})
	}

	data := identitiesPageData{
		Identities: views,
		Total:      len(views),
	}

	s.renderIdentitiesPage(w, data)
}

func (s *Server) renderIdentitiesPage(w http.ResponseWriter, data identitiesPageData) {
	tmpl, err := template.New("identities").Parse(identitiesTemplate)
	if err != nil {
		http.Error(w, "template error: "+err.Error(), http.StatusInternalServerError)
		return
	}
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	tmpl.Execute(w, data)
}

// handleIdentityRename handles POST /identities/{id}/rename
func (s *Server) handleIdentityRename(w http.ResponseWriter, r *http.Request) {
	id, err := strconv.ParseInt(r.PathValue("id"), 10, 64)
	if err != nil {
		http.Error(w, "invalid identity ID", http.StatusBadRequest)
		return
	}

	r.ParseForm()
	newName := strings.TrimSpace(r.FormValue("name"))
	if newName == "" {
		http.Error(w, "name is required", http.StatusBadRequest)
		return
	}

	// If another identity already has this name, we merge src into them.
	existing, _ := s.db.FindIdentityByNameCaseInsensitive(newName)
	if existing != nil && existing.ID != id {
		// Bounce to a dedicated confirm page instead of merging silently.
		if r.FormValue("confirm") != "1" {
			confirmURL := fmt.Sprintf("/identities/%d/rename-confirm?name=%s",
				id, url.QueryEscape(newName))
			redirectOrHX(w, r, confirmURL)
			return
		}

		srcRecs, _ := s.db.ListRecordingIDsForIdentity(id)
		dstRecs, _ := s.db.ListRecordingIDsForIdentity(existing.ID)
		if err := s.db.MergeIdentities(existing.ID, id); err != nil {
			http.Error(w, "merge failed: "+err.Error(), http.StatusInternalServerError)
			return
		}
		slog.Info("merged identity via rename", "src", id, "dst", existing.ID, "name", newName)
		s.regenerateSegments(append(srcRecs, dstRecs...)...)
	} else {
		if err := s.db.RenameIdentity(id, newName); err != nil {
			http.Error(w, "rename failed: "+err.Error(), http.StatusInternalServerError)
			return
		}
		slog.Info("renamed identity", "id", id, "new_name", newName)
	}

	redirectOrHX(w, r, "/identities")
}

// handleIdentityDelete handles POST /identities/{id}/delete
func (s *Server) handleIdentityDelete(w http.ResponseWriter, r *http.Request) {
	id, err := strconv.ParseInt(r.PathValue("id"), 10, 64)
	if err != nil {
		http.Error(w, "invalid identity ID", http.StatusBadRequest)
		return
	}

	recs, _ := s.db.ListRecordingIDsForIdentity(id)
	if err := s.db.DeleteIdentity(id); err != nil {
		http.Error(w, "delete failed: "+err.Error(), http.StatusInternalServerError)
		return
	}
	slog.Info("deleted identity", "id", id)
	s.regenerateSegments(recs...)

	redirectOrHX(w, r, "/identities")
}

// handleIdentityMerge handles POST /identities/merge (dst=..., src=...)
func (s *Server) handleIdentityMerge(w http.ResponseWriter, r *http.Request) {
	r.ParseForm()
	dstID, err := strconv.ParseInt(r.FormValue("dst"), 10, 64)
	if err != nil {
		http.Error(w, "invalid dst identity", http.StatusBadRequest)
		return
	}
	srcID, err := strconv.ParseInt(r.FormValue("src"), 10, 64)
	if err != nil {
		http.Error(w, "invalid src identity", http.StatusBadRequest)
		return
	}

	srcRecs, _ := s.db.ListRecordingIDsForIdentity(srcID)
	dstRecs, _ := s.db.ListRecordingIDsForIdentity(dstID)
	if err := s.db.MergeIdentities(dstID, srcID); err != nil {
		http.Error(w, "merge failed: "+err.Error(), http.StatusInternalServerError)
		return
	}
	slog.Info("merged identities", "dst", dstID, "src", srcID)
	s.regenerateSegments(append(srcRecs, dstRecs...)...)

	redirectOrHX(w, r, "/identities")
}

// handleIdentityRenameConfirm renders a confirmation page when renaming an
// identity to a name that already belongs to another identity (which would
// silently merge them). Reached via HX-Redirect from the rename handler.
func (s *Server) handleIdentityRenameConfirm(w http.ResponseWriter, r *http.Request) {
	srcID, err := strconv.ParseInt(r.PathValue("id"), 10, 64)
	if err != nil {
		http.Error(w, "invalid identity ID", http.StatusBadRequest)
		return
	}
	newName := strings.TrimSpace(r.URL.Query().Get("name"))
	if newName == "" {
		http.Redirect(w, r, "/identities", http.StatusSeeOther)
		return
	}

	src, err := s.db.GetIdentity(srcID)
	if err != nil {
		http.Error(w, "identity not found", http.StatusNotFound)
		return
	}

	dst, err := s.db.FindIdentityByNameCaseInsensitive(newName)
	// If the collision is gone (identity deleted/renamed elsewhere), just do the rename.
	if err != nil || dst == nil || dst.ID == srcID {
		http.Redirect(w, r, "/identities", http.StatusSeeOther)
		return
	}

	data := struct {
		SrcID, DstID     int64
		SrcName, DstName string
		Name             string
	}{
		SrcID: srcID, DstID: dst.ID,
		SrcName: src.Name, DstName: dst.Name,
		Name: newName,
	}

	tmpl, err := template.New("merge_confirm").Parse(mergeConfirmTemplate)
	if err != nil {
		http.Error(w, "template error: "+err.Error(), http.StatusInternalServerError)
		return
	}
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	tmpl.Execute(w, data)
}

// unused import guard for model — kept since other handlers reference it
var _ = model.Identity{}

// redirectOrHX sends an HX-Redirect for HTMX requests, or a 303 redirect otherwise.
func redirectOrHX(w http.ResponseWriter, r *http.Request, path string) {
	if r.Header.Get("HX-Request") == "true" {
		w.Header().Set("HX-Redirect", path)
		w.WriteHeader(http.StatusOK)
		return
	}
	http.Redirect(w, r, path, http.StatusSeeOther)
}

// unused guard
var _ = fmt.Sprintf
var _ = json.Marshal
var _ = store.IdentityStats{}

var identitiesTemplate = `<!DOCTYPE html>
<html lang="en">
<head>
    <meta charset="UTF-8">
    <meta name="viewport" content="width=device-width, initial-scale=1.0">
    <title>Identities · video-archive</title>
    <script src="https://unpkg.com/htmx.org@2.0.4"></script>
    <style>` + sharedStyles + `
    .identities {
        display: grid;
        grid-template-columns: repeat(auto-fill, minmax(260px, 1fr));
        gap: 16px;
    }
    .ident-card {
        background: #16213e;
        border: 1px solid #0f3460;
        border-radius: 12px;
        padding: 12px;
        display: flex;
        gap: 12px;
        align-items: flex-start;
    }
    .ident-card:hover { border-color: #533483; }
    .ident-thumb {
        width: 64px;
        height: 64px;
        border-radius: 8px;
        object-fit: cover;
        background: #0f3460;
        flex-shrink: 0;
    }
    .ident-thumb-empty {
        width: 64px;
        height: 64px;
        border-radius: 8px;
        background: #0f3460;
        display: flex;
        align-items: center;
        justify-content: center;
        color: #533483;
        font-size: 1.5rem;
        flex-shrink: 0;
    }
    .ident-body { flex: 1; min-width: 0; }
    .ident-name {
        font-size: 1rem;
        font-weight: 600;
        color: #fff;
        margin-bottom: 4px;
        overflow: hidden;
        text-overflow: ellipsis;
        white-space: nowrap;
    }
    .ident-stats {
        font-size: 0.75rem;
        color: #888;
        margin-bottom: 6px;
    }
    .ident-groups {
        margin-bottom: 8px;
    }
    .ident-actions {
        display: flex;
        gap: 6px;
        flex-wrap: wrap;
    }
    .ident-actions form { display: inline; }
    .ident-actions input[type="text"] {
        width: 120px;
        font-size: 0.8rem;
        padding: 4px 8px;
    }
    .ident-actions button {
        font-size: 0.75rem;
        padding: 4px 10px;
    }
    .summary-bar {
        background: #16213e;
        border-radius: 8px;
        padding: 10px 14px;
        margin-bottom: 20px;
        font-size: 0.85rem;
        color: #aaa;
    }
    </style>
</head>
<body>
    ` + renderNav("identities", 0) + `

    <h1>Identities</h1>
    <p class="subtitle">Everyone you've named. Rename to fix typos, delete to send clusters back to review, merge duplicates.</p>

    <div class="summary-bar">
        <strong>{{.Total}}</strong> named {{if eq .Total 1}}person{{else}}people{{end}}
    </div>

    {{if not .Identities}}
    <div class="empty-state">
        <p>No identities yet.</p>
        <p>Name people in <code>/review/&lt;id&gt;</code> first.</p>
    </div>
    {{else}}
    <div class="identities">
        {{range .Identities}}
        <div class="ident-card" data-id="{{.ID}}">
            <a href="/identities/{{.ID}}" style="flex-shrink:0;">
            {{if .ThumbnailURL}}
            <img class="ident-thumb" src="{{.ThumbnailURL}}" alt="{{.Name}}">
            {{else}}
            <div class="ident-thumb-empty">?</div>
            {{end}}
            </a>
            <div class="ident-body">
                <div class="ident-name"><a href="/identities/{{.ID}}" style="color:#fff;">{{.Name}}</a></div>
                <div class="ident-stats">
                    {{.VideoCount}} video{{if ne .VideoCount 1}}s{{end}} · {{.SegmentCount}} segment{{if ne .SegmentCount 1}}s{{end}} · {{.TotalTime}}
                </div>
                {{if .Groups}}
                <div class="ident-groups">
                    {{range .Groups}}<span class="pill">{{.}}</span>{{end}}
                </div>
                {{end}}
                <div class="ident-actions">
                    <form hx-post="/identities/{{.ID}}/rename" hx-target="body">
                        <input type="text" name="name" placeholder="New name..." value="{{.Name}}">
                        <button type="submit">Save</button>
                    </form>
                    <form hx-post="/identities/{{.ID}}/delete" hx-target="body"
                          hx-confirm="Delete this identity? Clusters will go back to pending review.">
                        <button type="submit" class="btn-danger">Delete</button>
                    </form>
                </div>
            </div>
        </div>
        {{end}}
    </div>
    {{end}}
</body>
</html>`

var mergeConfirmTemplate = `<!DOCTYPE html>
<html lang="en">
<head>
    <meta charset="UTF-8">
    <meta name="viewport" content="width=device-width, initial-scale=1.0">
    <title>Confirm merge · video-archive</title>
    <script src="https://unpkg.com/htmx.org@2.0.4"></script>
    <style>` + sharedStyles + `
    .confirm-card {
        background: #16213e;
        border: 1px solid #0f3460;
        border-radius: 12px;
        padding: 24px;
        max-width: 560px;
        margin: 40px auto;
    }
    .confirm-card h2 { margin-bottom: 12px; }
    .confirm-card p { margin-bottom: 12px; color: #ccc; }
    .confirm-card .warn {
        background: #0f3460;
        border-left: 3px solid #f39c12;
        padding: 10px 14px;
        border-radius: 6px;
        margin-bottom: 16px;
        font-size: 0.9rem;
        color: #ddd;
    }
    .confirm-card .actions {
        display: flex;
        gap: 10px;
        margin-top: 16px;
    }
    .confirm-card .actions form { display: inline; }
    </style>
</head>
<body>
    ` + renderNav("identities", 0) + `
    <div class="confirm-card">
        <h2>Merge into "{{.DstName}}"?</h2>
        <p>
            You're renaming <strong>{{.SrcName}}</strong> (#{{.SrcID}}) to
            <strong>{{.Name}}</strong>, but another identity already has that name:
            <strong>{{.DstName}}</strong> (#{{.DstID}}).
        </p>
        <div class="warn">
            Confirming will merge <strong>{{.SrcName}}</strong>'s clusters, segments, and
            group memberships into <strong>{{.DstName}}</strong>. <strong>{{.SrcName}}</strong>
            will be deleted. This cannot be undone.
        </div>
        <div class="actions">
            <form method="POST" action="/identities/{{.SrcID}}/rename">
                <input type="hidden" name="name" value="{{.Name}}">
                <input type="hidden" name="confirm" value="1">
                <button type="submit">Merge</button>
            </form>
            <a href="/identities"><button type="button" class="btn-ghost">Cancel</button></a>
        </div>
    </div>
</body>
</html>`
