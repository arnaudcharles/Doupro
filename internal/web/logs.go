package web

import (
	"html/template"
	"net/http"
	"net/url"
	"strconv"

	"github.com/arnaudcharles/doupro/internal/store"
)

// logView is the display model for one log row.
type logView struct {
	Timestamp string
	Level     string
	Event     string
	Container string
	Message   string
}

// logFilterView carries the current filter selection back into the form
// (so a filtered page reload keeps the fields populated).
type logFilterView struct {
	Container string
	EventType string
	Level     string
}

func handleLogs(st *store.Store, tpl *template.Template, version string) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		pageSize, err := st.GetLogsPageSize(r.Context())
		if err != nil {
			pageSize = store.DefaultLogsPageSize
		}
		filter := store.EventFilter{
			Container: r.URL.Query().Get("container"),
			EventType: r.URL.Query().Get("event"),
			Level:     r.URL.Query().Get("level"),
			Limit:     pageSize,
		}

		records, err := st.ListEvents(r.Context(), filter)
		if err != nil {
			http.Error(w, "failed to load logs", http.StatusInternalServerError)
			return
		}

		logs := make([]logView, 0, len(records))
		for _, e := range records {
			logs = append(logs, logView{
				Timestamp: e.Timestamp.Format("2006-01-02 15:04:05"),
				Level:     e.Level,
				Event:     e.Type,
				Container: e.Container,
				Message:   e.Message,
			})
		}

		data := pageData{
			Title:     "Logs",
			Version:   version,
			Nav:       navItems("Logs", mustAccess(r)),
			Logs:      logs,
			Limit:     filter.Limit,
			ExportURL: exportLogsURL(filter),
			Filter: logFilterView{
				Container: filter.Container,
				EventType: filter.EventType,
				Level:     filter.Level,
			},
		}

		renderPage(w, r, tpl, "logs-content", data)
	}
}

// exportLogsURL builds the Export button's target: the same
// GET /api/v1/logs endpoint the page itself uses, carrying over the
// current filter, with download=1 (triggers a Content-Disposition
// attachment response — see handleListLogs) and store.MaxLogsPageSize
// (the same ceiling ListEvents enforces — see docs/logs.md) rather than
// the page's own, user-configurable display limit — an export is meant
// to grab everything available, not just what's currently on screen.
// Built server-side with url.Values so filter values with special
// characters can't break the link the way naive template string
// interpolation could.
func exportLogsURL(filter store.EventFilter) string {
	q := url.Values{}
	if filter.Container != "" {
		q.Set("container", filter.Container)
	}
	if filter.EventType != "" {
		q.Set("event", filter.EventType)
	}
	if filter.Level != "" {
		q.Set("level", filter.Level)
	}
	q.Set("limit", strconv.Itoa(store.MaxLogsPageSize))
	q.Set("download", "1")
	return "/api/v1/logs?" + q.Encode()
}
