package web

import (
	"encoding/json"
	"html/template"
	"net/http"

	"github.com/arnaudcharles/doupro/internal/notifier"
	"github.com/arnaudcharles/doupro/internal/store"
)

type notificationView struct {
	Timestamp string
	Event     string
	Channel   string
	Status    string
	Message   string
}

type notificationJobView struct {
	ID          int64
	CreatedAt   string
	Event       string
	Channel     string
	Status      string
	Attempts    int
	MaxAttempts int
	NextAttempt string
	LastError   string
}

func handleNotifications(st *store.Store, notif *notifier.Notifier, tpl *template.Template, version string) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		records, err := st.ListNotifications(r.Context(), 100)
		if err != nil {
			http.Error(w, "failed to load notifications", http.StatusInternalServerError)
			return
		}
		views := make([]notificationView, 0, len(records))
		for _, n := range records {
			views = append(views, notificationView{
				Timestamp: n.Timestamp.Format("2006-01-02 15:04:05"),
				Event:     n.Event, Channel: n.Channel, Status: n.Status, Message: n.Message,
			})
		}
		jobs, err := st.ListNotificationJobs(r.Context(), 100)
		if err != nil {
			http.Error(w, "failed to load notification queue", http.StatusInternalServerError)
			return
		}
		jobViews := make([]notificationJobView, 0, len(jobs))
		for _, job := range jobs {
			jobViews = append(jobViews, notificationJobView{ID: job.ID, CreatedAt: job.CreatedAt.Format("2006-01-02 15:04:05"),
				Event: job.Event, Channel: job.Channel, Status: job.Status, Attempts: job.Attempts,
				MaxAttempts: job.MaxAttempts, NextAttempt: job.NextAttemptAt.Format("2006-01-02 15:04:05"), LastError: job.LastError})
		}

		channels, err := notif.PublicChannels(r.Context())
		if err != nil {
			http.Error(w, "failed to load channels", http.StatusInternalServerError)
			return
		}
		if channels == nil {
			channels = []notifier.Channel{}
		}
		channelsJSON, _ := json.Marshal(channels)

		data := pageData{
			Title:            "Notifications",
			Version:          version,
			Nav:              navItems("Notifications", mustAccess(r)),
			Notifications:    views,
			NotificationJobs: jobViews,
			Channels:         channels,
			ChannelsJSON:     template.JS(channelsJSON), //nolint:gosec // our own DB-sourced JSON, not user input at render time
		}
		renderPage(w, r, tpl, "notifications-content", data)
	}
}
