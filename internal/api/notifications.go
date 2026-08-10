package api

import (
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"strconv"
	"time"

	"github.com/arnaudcharles/doupro/internal/auth"
	"github.com/arnaudcharles/doupro/internal/events"
	"github.com/arnaudcharles/doupro/internal/notifier"
	"github.com/arnaudcharles/doupro/internal/store"
)

type notificationResponse struct {
	ID        int64  `json:"id"`
	Timestamp string `json:"timestamp"`
	Event     string `json:"event"`
	Target    string `json:"target,omitempty"`
	Channel   string `json:"channel"`
	Status    string `json:"status"`
	Error     string `json:"error,omitempty"`
	Message   string `json:"message,omitempty"`
}

type notificationJobResponse struct {
	ID            int64   `json:"id"`
	Event         string  `json:"event"`
	Target        string  `json:"target,omitempty"`
	Stack         string  `json:"stack,omitempty"`
	Channel       string  `json:"channel"`
	Message       string  `json:"message"`
	Status        string  `json:"status"`
	Attempts      int     `json:"attempts"`
	MaxAttempts   int     `json:"max_attempts"`
	NextAttemptAt string  `json:"next_attempt_at"`
	LastError     string  `json:"last_error,omitempty"`
	CreatedAt     string  `json:"created_at"`
	SentAt        *string `json:"sent_at,omitempty"`
}

func handleListNotificationQueue(st *store.Store) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		jobs, err := st.ListNotificationJobs(r.Context(), 100)
		if err != nil {
			writeError(w, http.StatusInternalServerError, "internal_error", "failed to list notification queue")
			return
		}
		resp := make([]notificationJobResponse, 0, len(jobs))
		for _, job := range jobs {
			item := notificationJobResponse{ID: job.ID, Event: job.Event, Target: job.Target, Stack: job.Stack,
				Channel: job.Channel, Message: job.Message, Status: job.Status, Attempts: job.Attempts,
				MaxAttempts: job.MaxAttempts, NextAttemptAt: job.NextAttemptAt.Format(time.RFC3339),
				LastError: job.LastError, CreatedAt: job.CreatedAt.Format(time.RFC3339)}
			if job.SentAt != nil {
				s := job.SentAt.Format(time.RFC3339)
				item.SentAt = &s
			}
			resp = append(resp, item)
		}
		writeJSON(w, http.StatusOK, resp)
	}
}

func handleRetryNotification(st *store.Store, logger *events.Logger) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		id, err := strconv.ParseInt(r.PathValue("id"), 10, 64)
		if err != nil || id <= 0 {
			writeError(w, http.StatusBadRequest, "invalid_request", "invalid notification queue id")
			return
		}
		if err := st.RetryNotification(r.Context(), id); err != nil {
			writeError(w, http.StatusConflict, "invalid_state", "notification is not in dead-letter")
			return
		}
		logger.Emit(r.Context(), events.Event{Level: events.LevelInfo, Type: "notification.requeued",
			Actor: events.ActorUser, Message: "dead-letter notification queued for manual retry",
			Metadata: map[string]any{"queue_id": id}})
		writeJSON(w, http.StatusAccepted, map[string]string{"status": "queued"})
	}
}

// handleListNotifications backs GET /api/v1/notifications — the delivery
// log described in docs/notifications.md, every attempt not just
// successes.
func handleListNotifications(st *store.Store) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		records, err := st.ListNotifications(r.Context(), 100)
		if err != nil {
			writeError(w, http.StatusInternalServerError, "internal_error", "failed to list notifications")
			return
		}
		resp := make([]notificationResponse, 0, len(records))
		for _, n := range records {
			resp = append(resp, notificationResponse{
				ID: n.ID, Timestamp: n.Timestamp.Format(time.RFC3339), Event: n.Event,
				Target: n.Target, Channel: n.Channel, Status: n.Status, Error: n.Error, Message: n.Message,
			})
		}
		writeJSON(w, http.StatusOK, resp)
	}
}

// handleGetNotificationChannels backs GET /api/v1/settings/notifications.
func handleGetNotificationChannels(notif *notifier.Notifier) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		channels, err := notif.PublicChannels(r.Context())
		if err != nil {
			writeError(w, http.StatusInternalServerError, "internal_error", "failed to load channels")
			return
		}
		if channels == nil {
			channels = []notifier.Channel{}
		}
		writeJSON(w, http.StatusOK, channels)
	}
}

// handleSetNotificationChannels backs PATCH /api/v1/settings/notifications
// — replaces the configured channel list wholesale. Channel URLs are
// write-only in spirit (this endpoint accepts them, GET returns them back
// as configured — see SECURITY.md on redaction gaps still to close before
// this is exposed beyond a trusted network).
func handleSetNotificationChannels(notif *notifier.Notifier, logger *events.Logger) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		var channels []notifier.Channel
		if err := json.NewDecoder(r.Body).Decode(&channels); err != nil {
			writeError(w, http.StatusBadRequest, "invalid_request", "invalid JSON body")
			return
		}
		for _, ch := range channels {
			if ch.Name == "" {
				writeError(w, http.StatusBadRequest, "invalid_request", "each channel needs a name")
				return
			}
		}
		if err := notif.SetChannels(r.Context(), channels); err != nil {
			if errors.Is(err, notifier.ErrChannelNeedsURL) {
				writeError(w, http.StatusBadRequest, "invalid_request", err.Error())
				return
			}
			writeError(w, http.StatusInternalServerError, "internal_error", "failed to save channels")
			return
		}
		public, err := notif.PublicChannels(r.Context())
		if err != nil {
			writeError(w, http.StatusInternalServerError, "internal_error", "failed to load channels")
			return
		}
		// Names only — never URLs/credentials, same redaction rule as every
		// other settings.*_changed event (see docs/logs.md).
		names := make([]string, len(channels))
		for i, ch := range channels {
			names[i] = ch.Name
		}
		logger.Emit(r.Context(), events.Event{
			Level: events.LevelInfo, Type: "settings.notifications_changed", Actor: events.ActorUser, ActorID: auth.ActorFromContext(r.Context()),
			Message:  fmt.Sprintf("notification channels updated: %d configured", len(channels)),
			Metadata: map[string]any{"count": len(channels), "names": names},
		})
		writeJSON(w, http.StatusOK, public)
	}
}

// handleTestNotificationChannel backs
// POST /api/v1/settings/notifications/test — sends a one-off test message
// to a single shoutrrr URL without persisting it as a configured channel,
// so an operator can verify a URL before saving it.
func handleTestNotificationChannel(notif *notifier.Notifier) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		var req struct {
			URL  string `json:"url"`
			Name string `json:"name"`
		}
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil || (req.URL == "" && req.Name == "") {
			writeError(w, http.StatusBadRequest, "invalid_request", "url or saved channel name is required")
			return
		}
		if err := notif.TestChannel(r.Context(), req.Name, req.URL); err != nil {
			writeError(w, http.StatusBadGateway, "send_failed", err.Error())
			return
		}
		writeJSON(w, http.StatusOK, map[string]string{"status": "ok"})
	}
}
