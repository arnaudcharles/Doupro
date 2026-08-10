package web

import (
	"fmt"
	"html/template"
	"net/http"

	"github.com/arnaudcharles/doupro/internal/store"
)

// scheduleView is the display model for one schedule row.
type scheduleView struct {
	ID        int64
	Type      string
	Target    string // container name or stack
	When      string // human summary of run_at/cron/policy
	Enabled   bool
	Inactive  bool
	Completed bool // a once schedule that has fired and is now historical
}

func handleSchedule(st *store.Store, tpl *template.Template, version string) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		schedules, err := st.ListSchedules(r.Context())
		if err != nil {
			http.Error(w, "failed to load schedules", http.StatusInternalServerError)
			return
		}

		views := make([]scheduleView, 0, len(schedules))
		var containerViews, recurringViews []scheduleView
		for _, sc := range schedules {
			v := scheduleView{
				ID:        sc.ID,
				Type:      sc.Kind,
				Target:    scheduleTarget(sc),
				When:      scheduleWhen(sc),
				Enabled:   sc.Enabled,
				Inactive:  false,
				Completed: sc.Completed(),
			}
			views = append(views, v)
			// "once" is always a one-off, per-container schedule; "cron"
			// and "relative" are the recurring policies applied to a
			// stack (or an individual container acting as its own group
			// of one) — see docs/schedule.md's two-list model.
			if sc.Kind == "once" {
				containerViews = append(containerViews, v)
			} else {
				recurringViews = append(recurringViews, v)
			}
		}

		data := pageData{
			Title:              "Schedule",
			Version:            version,
			Nav:                navItems("Schedule", mustAccess(r)),
			Schedules:          views,
			ContainerSchedules: containerViews,
			RecurringSchedules: recurringViews,
			PrefillContainer:   r.URL.Query().Get("container"),
		}
		renderPage(w, r, tpl, "schedule-content", data)
	}
}

func scheduleTarget(sc store.Schedule) string {
	if sc.ContainerName != "" {
		return sc.ContainerName
	}
	if sc.Stack != "" {
		return sc.Stack + " (stack)"
	}
	return "—"
}

func scheduleWhen(sc store.Schedule) string {
	switch sc.Kind {
	case "once":
		if sc.RunAt != nil {
			return "at " + sc.RunAt.Format("2006-01-02 15:04")
		}
	case "cron":
		if sc.NextRunAt != nil {
			return sc.CronExpr + " (next: " + sc.NextRunAt.Format("2006-01-02 15:04") + ", " + sc.SemverPolicy + ")"
		}
		return sc.CronExpr
	case "relative":
		if sc.RelativePolicy == "delayed" {
			return fmt.Sprintf("delayed by %s (%s)", sc.RelativeAfter, sc.SemverPolicy)
		}
		return "as soon as available (" + sc.SemverPolicy + ")"
	}
	return "—"
}
