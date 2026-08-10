package web

import (
	"html/template"
	"net/http"

	"github.com/arnaudcharles/doupro/internal/store"
)

func handleStats(st *store.Store, tpl *template.Template, version string) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		summary, err := st.Stats(r.Context())
		if err != nil {
			http.Error(w, "failed to load stats", http.StatusInternalServerError)
			return
		}

		data := pageData{
			Title:   "Stats",
			Version: version,
			Nav:     navItems("Stats", mustAccess(r)),
			Stats:   summary,
		}
		renderPage(w, r, tpl, "stats-content", data)
	}
}
