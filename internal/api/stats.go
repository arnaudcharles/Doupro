package api

import (
	"net/http"

	"github.com/arnaudcharles/doupro/internal/store"
)

type statsSummaryResponse struct {
	ContainersTotal     int            `json:"containers_total"`
	ContainersRunning   int            `json:"containers_running"`
	UpdatesAvailable    int            `json:"updates_available"`
	UpdatesSucceeded    int            `json:"updates_succeeded"`
	UpdatesFailed       int            `json:"updates_failed"`
	RollbacksAuto       int            `json:"rollbacks_auto"`
	RollbacksManual     int            `json:"rollbacks_manual"`
	NotificationsSent   int            `json:"notifications_sent"`
	NotificationsFailed int            `json:"notifications_failed"`
	SchedulesActive     int            `json:"schedules_active"`
	ContainersByStack   map[string]int `json:"containers_by_stack"`
}

// handleStatsSummary backs GET /api/v1/stats. See store.StatsSummary's
// doc comment for what this is (a current-state + recent-event snapshot)
// and isn't (a historical time series) yet.
func handleStatsSummary(st *store.Store) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		summary, err := st.Stats(r.Context())
		if err != nil {
			writeError(w, http.StatusInternalServerError, "internal_error", "failed to compute stats")
			return
		}
		writeJSON(w, http.StatusOK, statsSummaryResponse{
			ContainersTotal: summary.ContainersTotal, ContainersRunning: summary.ContainersRunning,
			UpdatesAvailable: summary.UpdatesAvailable, UpdatesSucceeded: summary.UpdatesSucceeded,
			UpdatesFailed: summary.UpdatesFailed, RollbacksAuto: summary.RollbacksAuto,
			RollbacksManual: summary.RollbacksManual, NotificationsSent: summary.NotificationsSent,
			NotificationsFailed: summary.NotificationsFailed, SchedulesActive: summary.SchedulesActive,
			ContainersByStack: summary.ContainersByStack,
		})
	}
}
