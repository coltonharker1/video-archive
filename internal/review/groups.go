package review

import (
	"html/template"
	"log/slog"
	"net/http"
	"strconv"
	"strings"

	"github.com/colton/video-archive/internal/model"
)

type groupMemberView struct {
	ID           int64
	Name         string
	ThumbnailURL string
}

type groupView struct {
	ID      int64
	Name    string
	Notes   string
	Members []groupMemberView
}

type identityPickerItem struct {
	ID           int64
	Name         string
	ThumbnailURL string
	InGroup      bool
}

type groupsPageData struct {
	Groups           []groupView
	AllIdentities    []identityPickerItem
	TotalGroups      int
	TotalIdentities  int
	SelectedGroupID  int64 // optional, from ?group=<id>
	SelectedMembers  map[int64]bool
}

func (s *Server) handleGroups(w http.ResponseWriter, r *http.Request) {
	groups, err := s.db.ListGroups()
	if err != nil {
		http.Error(w, "failed to load groups: "+err.Error(), http.StatusInternalServerError)
		return
	}

	// Build views with thumbnails for each member
	var groupViews []groupView
	for _, g := range groups {
		members, err := s.db.ListGroupMembers(g.ID)
		if err != nil {
			continue
		}
		memberViews := make([]groupMemberView, 0, len(members))
		for _, m := range members {
			memberViews = append(memberViews, groupMemberView{
				ID:           m.ID,
				Name:         m.Name,
				ThumbnailURL: identityThumbURL(s, m),
			})
		}
		groupViews = append(groupViews, groupView{
			ID:      g.ID,
			Name:    g.Name,
			Notes:   g.Notes,
			Members: memberViews,
		})
	}

	// Identity picker — all identities with their thumbnails, marked if they
	// belong to the currently-selected group.
	identities, _ := s.db.ListIdentitiesWithStats()
	selectedGroupID, _ := strconv.ParseInt(r.URL.Query().Get("group"), 10, 64)

	selectedMembers := make(map[int64]bool)
	if selectedGroupID > 0 {
		members, _ := s.db.ListGroupMembers(selectedGroupID)
		for _, m := range members {
			selectedMembers[m.ID] = true
		}
	}

	allIdents := make([]identityPickerItem, 0, len(identities))
	for _, st := range identities {
		thumbURL := ""
		if st.ThumbnailPath != "" {
			rel := strings.TrimPrefix(st.ThumbnailPath, "crops/")
			thumbURL = "/static/crops/" + rel
		}
		allIdents = append(allIdents, identityPickerItem{
			ID:           st.ID,
			Name:         st.Name,
			ThumbnailURL: thumbURL,
			InGroup:      selectedMembers[st.ID],
		})
	}

	data := groupsPageData{
		Groups:          groupViews,
		AllIdentities:   allIdents,
		TotalGroups:     len(groupViews),
		TotalIdentities: len(allIdents),
		SelectedGroupID: selectedGroupID,
		SelectedMembers: selectedMembers,
	}

	s.renderGroupsPage(w, data)
}

func (s *Server) renderGroupsPage(w http.ResponseWriter, data groupsPageData) {
	tmpl, err := template.New("groups").Funcs(template.FuncMap{
		"isSelected": func(id, selected int64) bool { return id == selected },
	}).Parse(groupsTemplate)
	if err != nil {
		http.Error(w, "template error: "+err.Error(), http.StatusInternalServerError)
		return
	}
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	tmpl.Execute(w, data)
}

// identityThumbURL resolves an identity to a crop URL by looking up the best
// confirmed cluster thumbnail.
func identityThumbURL(s *Server, ident model.Identity) string {
	if ident.ThumbnailPath != "" {
		rel := strings.TrimPrefix(ident.ThumbnailPath, "crops/")
		return "/static/crops/" + rel
	}
	// Fall back to looking up stats (which picks the best cluster thumbnail)
	stats, _ := s.db.ListIdentitiesWithStats()
	for _, st := range stats {
		if st.ID == ident.ID && st.ThumbnailPath != "" {
			rel := strings.TrimPrefix(st.ThumbnailPath, "crops/")
			return "/static/crops/" + rel
		}
	}
	return ""
}

// handleGroupCreate handles POST /groups/create
func (s *Server) handleGroupCreate(w http.ResponseWriter, r *http.Request) {
	r.ParseForm()
	name := strings.TrimSpace(r.FormValue("name"))
	if name == "" {
		http.Error(w, "name required", http.StatusBadRequest)
		return
	}

	if existing, err := s.db.FindGroupByName(name); err == nil && existing != nil {
		redirectOrHX(w, r, "/groups?group="+strconv.FormatInt(existing.ID, 10))
		return
	}

	id, err := s.db.CreateGroup(name)
	if err != nil {
		http.Error(w, "create failed: "+err.Error(), http.StatusInternalServerError)
		return
	}
	slog.Info("created group", "id", id, "name", name)

	redirectOrHX(w, r, "/groups?group="+strconv.FormatInt(id, 10))
}

// handleGroupRename handles POST /groups/{id}/rename
func (s *Server) handleGroupRename(w http.ResponseWriter, r *http.Request) {
	id, _ := strconv.ParseInt(r.PathValue("id"), 10, 64)
	r.ParseForm()
	name := strings.TrimSpace(r.FormValue("name"))
	if name == "" {
		http.Error(w, "name required", http.StatusBadRequest)
		return
	}
	if err := s.db.RenameGroup(id, name); err != nil {
		http.Error(w, "rename failed: "+err.Error(), http.StatusInternalServerError)
		return
	}
	redirectOrHX(w, r, "/groups?group="+strconv.FormatInt(id, 10))
}

// handleGroupDelete handles POST /groups/{id}/delete
func (s *Server) handleGroupDelete(w http.ResponseWriter, r *http.Request) {
	id, _ := strconv.ParseInt(r.PathValue("id"), 10, 64)
	if err := s.db.DeleteGroup(id); err != nil {
		http.Error(w, "delete failed: "+err.Error(), http.StatusInternalServerError)
		return
	}
	slog.Info("deleted group", "id", id)
	redirectOrHX(w, r, "/groups")
}

// handleGroupAddMember handles POST /groups/{id}/add
func (s *Server) handleGroupAddMember(w http.ResponseWriter, r *http.Request) {
	groupID, _ := strconv.ParseInt(r.PathValue("id"), 10, 64)
	r.ParseForm()
	identityID, err := strconv.ParseInt(r.FormValue("identity_id"), 10, 64)
	if err != nil {
		http.Error(w, "invalid identity_id", http.StatusBadRequest)
		return
	}
	if err := s.db.AddGroupMember(groupID, identityID); err != nil {
		http.Error(w, "add failed: "+err.Error(), http.StatusInternalServerError)
		return
	}
	redirectOrHX(w, r, "/groups?group="+strconv.FormatInt(groupID, 10))
}

// handleGroupRemoveMember handles POST /groups/{id}/remove/{identityID}
func (s *Server) handleGroupRemoveMember(w http.ResponseWriter, r *http.Request) {
	groupID, _ := strconv.ParseInt(r.PathValue("id"), 10, 64)
	identityID, _ := strconv.ParseInt(r.PathValue("identityID"), 10, 64)
	if err := s.db.RemoveGroupMember(groupID, identityID); err != nil {
		http.Error(w, "remove failed: "+err.Error(), http.StatusInternalServerError)
		return
	}
	redirectOrHX(w, r, "/groups?group="+strconv.FormatInt(groupID, 10))
}

var groupsTemplate = `<!DOCTYPE html>
<html lang="en">
<head>
    <meta charset="UTF-8">
    <meta name="viewport" content="width=device-width, initial-scale=1.0">
    <title>Groups · video-archive</title>
    <script src="https://unpkg.com/htmx.org@2.0.4"></script>
    <style>` + sharedStyles + `
    .layout {
        display: grid;
        grid-template-columns: 320px 1fr;
        gap: 24px;
        align-items: flex-start;
    }
    @media (max-width: 900px) {
        .layout { grid-template-columns: 1fr; }
    }
    .groups-sidebar {
        background: #16213e;
        border: 1px solid #0f3460;
        border-radius: 12px;
        padding: 14px;
    }
    .group-list { list-style: none; margin: 0; padding: 0; }
    .group-list li {
        padding: 10px 12px;
        border-radius: 8px;
        cursor: pointer;
        color: #c4b8ff;
        margin-bottom: 4px;
        border: 1px solid transparent;
    }
    .group-list li a {
        color: inherit;
        display: block;
    }
    .group-list li:hover { background: #0f3460; }
    .group-list li.active {
        background: #0f3460;
        border-color: #533483;
        color: #fff;
    }
    .group-list .member-count {
        font-size: 0.75rem;
        color: #888;
        float: right;
    }
    .create-form {
        margin-top: 14px;
        padding-top: 14px;
        border-top: 1px solid #0f3460;
        display: flex;
        gap: 6px;
    }
    .create-form input { flex: 1; }

    .group-detail {
        background: #16213e;
        border: 1px solid #0f3460;
        border-radius: 12px;
        padding: 20px;
    }
    .group-detail-header {
        display: flex;
        justify-content: space-between;
        align-items: center;
        margin-bottom: 16px;
        padding-bottom: 12px;
        border-bottom: 1px solid #0f3460;
    }
    .group-detail-header h2 { margin: 0; }
    .group-detail-actions { display: flex; gap: 8px; }

    .members-section { margin-bottom: 24px; }
    .members-grid {
        display: grid;
        grid-template-columns: repeat(auto-fill, minmax(140px, 1fr));
        gap: 12px;
    }
    .member-card {
        background: #0f3460;
        border-radius: 8px;
        padding: 10px;
        text-align: center;
        position: relative;
    }
    .member-card img, .member-card .empty-thumb {
        width: 80px;
        height: 80px;
        border-radius: 8px;
        object-fit: cover;
        margin: 0 auto 8px;
        display: block;
        background: #1a1a2e;
    }
    .member-card .empty-thumb {
        display: flex;
        align-items: center;
        justify-content: center;
        color: #533483;
        font-size: 1.5rem;
    }
    .member-card .name {
        font-size: 0.85rem;
        color: #fff;
        overflow: hidden;
        text-overflow: ellipsis;
        white-space: nowrap;
    }
    .member-card .remove-btn {
        position: absolute;
        top: 4px;
        right: 4px;
        background: rgba(231, 76, 60, 0.9);
        color: #fff;
        border: none;
        width: 22px;
        height: 22px;
        border-radius: 50%;
        cursor: pointer;
        font-size: 0.8rem;
        padding: 0;
        line-height: 1;
        opacity: 0;
        transition: opacity 0.15s;
    }
    .member-card:hover .remove-btn { opacity: 1; }

    .picker-section h3 {
        font-size: 0.9rem;
        color: #888;
        margin-bottom: 12px;
        font-weight: 500;
    }
    .picker-grid {
        display: grid;
        grid-template-columns: repeat(auto-fill, minmax(120px, 1fr));
        gap: 8px;
    }
    .picker-item {
        background: #0f3460;
        border: 1px solid transparent;
        border-radius: 8px;
        padding: 8px;
        text-align: center;
        cursor: pointer;
        transition: all 0.15s;
    }
    .picker-item:hover {
        border-color: #533483;
        background: #16213e;
    }
    .picker-item.in-group {
        opacity: 0.4;
        cursor: not-allowed;
    }
    .picker-item img, .picker-item .empty-thumb {
        width: 52px;
        height: 52px;
        border-radius: 6px;
        object-fit: cover;
        margin: 0 auto 6px;
        display: block;
        background: #1a1a2e;
    }
    .picker-item .empty-thumb {
        display: flex;
        align-items: center;
        justify-content: center;
        color: #533483;
    }
    .picker-item .name {
        font-size: 0.75rem;
        color: #fff;
        overflow: hidden;
        text-overflow: ellipsis;
        white-space: nowrap;
    }
    </style>
</head>
<body>
    ` + renderNav("groups", 0) + `

    <h1>Groups</h1>
    <p class="subtitle">Organize identities into families or collections. Groups let you find every scene where any member appears across your entire archive.</p>

    <div class="layout">
        <aside class="groups-sidebar">
            <h2>Groups</h2>
            {{if not .Groups}}
            <p class="muted" style="font-size: 0.85rem;">No groups yet. Create one below.</p>
            {{else}}
            <ul class="group-list">
                {{$selectedID := .SelectedGroupID}}
                {{range .Groups}}
                <li {{if isSelected .ID $selectedID}}class="active"{{end}}>
                    <a href="/groups?group={{.ID}}">
                        {{.Name}}
                        <span class="member-count">{{len .Members}}</span>
                    </a>
                </li>
                {{end}}
            </ul>
            {{end}}
            <form class="create-form" hx-post="/groups/create" hx-target="body">
                <input type="text" name="name" placeholder="New group name..." required>
                <button type="submit">Create</button>
            </form>
        </aside>

        <div>
        {{if eq .SelectedGroupID 0}}
            <div class="empty-state">
                {{if .Groups}}
                <p>Select a group to manage its members.</p>
                {{else}}
                <p>Create a group to get started.</p>
                <p class="muted" style="font-size: 0.85rem;">e.g., "Harker Family", "Pooles", "Kids"</p>
                {{end}}
            </div>
        {{else}}
            {{range .Groups}}
            {{if isSelected .ID $.SelectedGroupID}}
            <div class="group-detail">
                <div class="group-detail-header">
                    <h2>{{.Name}}</h2>
                    <div class="group-detail-actions">
                        <form hx-post="/groups/{{.ID}}/rename" hx-target="body" style="display: flex; gap: 6px;">
                            <input type="text" name="name" value="{{.Name}}" style="width: 180px;">
                            <button type="submit" class="btn-ghost">Rename</button>
                        </form>
                        <form hx-post="/groups/{{.ID}}/delete" hx-target="body"
                              hx-confirm="Delete this group? (Identities will be preserved.)">
                            <button type="submit" class="btn-danger">Delete</button>
                        </form>
                    </div>
                </div>

                <section class="members-section">
                    <h3 class="muted" style="font-size: 0.9rem; margin-bottom: 12px; font-weight: 500;">
                        Members ({{len .Members}})
                    </h3>
                    {{if .Members}}
                    <div class="members-grid">
                        {{$groupID := .ID}}
                        {{range .Members}}
                        <div class="member-card">
                            {{if .ThumbnailURL}}
                            <img src="{{.ThumbnailURL}}" alt="{{.Name}}">
                            {{else}}
                            <div class="empty-thumb">?</div>
                            {{end}}
                            <div class="name">{{.Name}}</div>
                            <form hx-post="/groups/{{$groupID}}/remove/{{.ID}}" hx-target="body" style="display:inline;">
                                <button type="submit" class="remove-btn" title="Remove from group">×</button>
                            </form>
                        </div>
                        {{end}}
                    </div>
                    {{else}}
                    <p class="muted" style="font-size: 0.85rem;">No members yet. Click identities below to add.</p>
                    {{end}}
                </section>

                <section class="picker-section">
                    <h3>Add identity to group</h3>
                    {{if not $.AllIdentities}}
                    <p class="muted" style="font-size: 0.85rem;">No identities available. Name clusters in a review page first.</p>
                    {{else}}
                    <div class="picker-grid">
                        {{$groupID := .ID}}
                        {{range $.AllIdentities}}
                        {{if not .InGroup}}
                        <form class="picker-item" hx-post="/groups/{{$groupID}}/add" hx-target="body" hx-trigger="click">
                            <input type="hidden" name="identity_id" value="{{.ID}}">
                            {{if .ThumbnailURL}}
                            <img src="{{.ThumbnailURL}}" alt="{{.Name}}">
                            {{else}}
                            <div class="empty-thumb">?</div>
                            {{end}}
                            <div class="name">{{.Name}}</div>
                        </form>
                        {{else}}
                        <div class="picker-item in-group" title="Already in group">
                            {{if .ThumbnailURL}}
                            <img src="{{.ThumbnailURL}}" alt="{{.Name}}">
                            {{else}}
                            <div class="empty-thumb">?</div>
                            {{end}}
                            <div class="name">{{.Name}}</div>
                        </div>
                        {{end}}
                        {{end}}
                    </div>
                    {{end}}
                </section>
            </div>
            {{end}}
            {{end}}
        {{end}}
        </div>
    </div>
</body>
</html>`
