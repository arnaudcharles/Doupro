package web

import (
	"bytes"
	"html/template"
	"io/fs"
	"net/http"
	"regexp"

	"github.com/yuin/goldmark"
)

// manualPage is one entry in the Manual sidebar, backed by a file in
// manuals/ (see /assets.go's ManualsFS and manuals/README.md for the
// canonical list this mirrors).
type manualPage struct {
	Slug  string // URL segment after /manual/, "" for the index
	File  string // filename under manuals/
	Title string
}

var manualPages = []manualPage{
	{"", "README.md", "Overview"},
	{"getting-started", "getting-started.md", "Getting started"},
	{"containers", "containers.md", "Containers"},
	{"schedule", "schedule.md", "Schedule"},
	{"notifications", "notifications.md", "Notifications"},
	{"logs", "logs.md", "Logs"},
	{"settings", "settings.md", "Settings"},
	{"stats", "stats.md", "Stats"},
	{"cli", "cli.md", "CLI"},
	{"faq", "faq.md", "FAQ"},
}

// manualLink rewrites same-directory Markdown links to other manual pages
// ("containers.md" -> "/manual/containers") so the rendered page is
// actually navigable. Links elsewhere in the repo (../docs/..., API
// examples) are left as plain relative paths — they render but don't
// resolve to an app route; the sidebar, not in-content links, is the
// primary navigation.
var manualLink = regexp.MustCompile(`\]\(([a-z-]+)\.md\)`)

func handleManual(manualsFS fs.FS, tpl *template.Template, version string) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		slug := r.PathValue("page")

		var page *manualPage
		for i := range manualPages {
			if manualPages[i].Slug == slug {
				page = &manualPages[i]
				break
			}
		}
		if page == nil {
			http.NotFound(w, r)
			return
		}

		raw, err := fs.ReadFile(manualsFS, "manuals/"+page.File)
		if err != nil {
			http.Error(w, "manual page not found", http.StatusNotFound)
			return
		}

		linked := manualLink.ReplaceAll(raw, []byte("](/manual/$1)"))

		var buf bytes.Buffer
		if err := goldmark.Convert(linked, &buf); err != nil {
			http.Error(w, "failed to render manual page", http.StatusInternalServerError)
			return
		}

		data := pageData{
			Title:       page.Title,
			Version:     version,
			Nav:         navItems("Manual", mustAccess(r)),
			ManualPages: manualPages,
			ManualSlug:  slug,
			ManualHTML:  template.HTML(buf.String()), //nolint:gosec // rendered from our own repo-shipped Markdown, not user input
		}
		renderPage(w, r, tpl, "manual-content", data)
	}
}
