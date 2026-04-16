package review

import "fmt"

// sharedStyles contains CSS reused across all review UI pages.
const sharedStyles = `
* { box-sizing: border-box; margin: 0; padding: 0; }
body {
    font-family: -apple-system, BlinkMacSystemFont, "Segoe UI", Roboto, sans-serif;
    background: #1a1a2e;
    color: #e0e0e0;
    padding: 20px;
    max-width: 1400px;
    margin: 0 auto;
}
a { color: #9b8fd1; text-decoration: none; }
a:hover { color: #c4b8ff; }

nav.topnav {
    display: flex;
    gap: 4px;
    padding: 8px 0 20px 0;
    border-bottom: 1px solid #0f3460;
    margin-bottom: 24px;
    align-items: center;
}
nav.topnav a {
    padding: 6px 14px;
    border-radius: 6px;
    font-size: 0.9rem;
    color: #888;
}
nav.topnav a:hover { background: #16213e; color: #e0e0e0; }
nav.topnav a.active { background: #533483; color: #fff; }
nav.topnav .spacer { flex: 1; }
nav.topnav .muted { color: #666; font-size: 0.8rem; padding: 6px 0; }

h1 { font-size: 1.5rem; margin-bottom: 8px; color: #fff; }
h2 { font-size: 1.1rem; margin-bottom: 12px; color: #fff; }
.subtitle { color: #888; margin-bottom: 24px; font-size: 0.9rem; }

button, .btn {
    background: #533483;
    color: #fff;
    border: none;
    padding: 6px 14px;
    border-radius: 6px;
    cursor: pointer;
    font-size: 0.8rem;
    font-family: inherit;
}
button:hover, .btn:hover { background: #6c44a2; }
.btn-danger {
    background: transparent;
    border: 1px solid #e74c3c;
    color: #e74c3c;
}
.btn-danger:hover { background: #e74c3c; color: #fff; }
.btn-ghost {
    background: transparent;
    border: 1px solid #533483;
    color: #c4b8ff;
}
.btn-ghost:hover { background: #16213e; }

input[type="text"] {
    background: #0f3460;
    border: 1px solid #533483;
    color: #fff;
    padding: 6px 10px;
    border-radius: 6px;
    font-size: 0.85rem;
    font-family: inherit;
}
input[type="text"]::placeholder { color: #666; }
input[type="text"]:focus { outline: none; border-color: #9b8fd1; }

select {
    background: #0f3460;
    border: 1px solid #533483;
    color: #fff;
    padding: 6px 10px;
    border-radius: 6px;
    font-size: 0.8rem;
    font-family: inherit;
}

.empty-state {
    text-align: center;
    padding: 60px 20px;
    color: #666;
}
.empty-state p { margin-bottom: 8px; }
.empty-state code {
    background: #16213e;
    padding: 2px 6px;
    border-radius: 4px;
    color: #c4b8ff;
    font-size: 0.85rem;
}

.card {
    background: #16213e;
    border: 1px solid #0f3460;
    border-radius: 12px;
    padding: 16px;
}

.pill {
    display: inline-block;
    background: #0f3460;
    color: #c4b8ff;
    padding: 2px 8px;
    border-radius: 10px;
    font-size: 0.7rem;
    margin-right: 4px;
}

.hidden { display: none; }
.muted { color: #888; }
`

// renderNav returns the top navigation HTML with the active tab highlighted.
// active: "review", "identities", "groups", or empty string for no tab.
// If recordingID > 0, the Review link points to that recording; otherwise it's hidden.
func renderNav(active string, recordingID int64) string {
	cls := func(name string) string {
		if active == name {
			return ` class="active"`
		}
		return ""
	}

	recLinks := ""
	if recordingID > 0 {
		recLinks = fmt.Sprintf(`<a%s href="/review/%d">Review #%d</a><a%s href="/scenes/%d">Scenes</a>`,
			cls("review"), recordingID, recordingID,
			cls("scenes"), recordingID)
	}

	return fmt.Sprintf(`<nav class="topnav">
  <a%s href="/">Home</a>
  %s
  <a%s href="/identities">Identities</a>
  <a%s href="/groups">Groups</a>
  <div class="spacer"></div>
  <span class="muted">video-archive</span>
</nav>`, cls("home"), recLinks, cls("identities"), cls("groups"))
}
