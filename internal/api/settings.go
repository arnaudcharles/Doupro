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
	"github.com/arnaudcharles/doupro/internal/scheduler"
	"github.com/arnaudcharles/doupro/internal/store"
)

// --- General -------------------------------------------------------------

type generalSettingsResponse struct {
	CheckIntervalMinutes   int `json:"check_interval_minutes"`
	CrashLoopThreshold     int `json:"crash_loop_threshold"`
	CrashLoopWindowMinutes int `json:"crash_loop_window_minutes"`
	LogsPageSize           int `json:"logs_page_size"`
}

func handleGetGeneralSettings(st *store.Store) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		minutes, ok, err := st.GetCheckIntervalMinutes(r.Context())
		if err != nil {
			writeError(w, http.StatusInternalServerError, "internal_error", "failed to load settings")
			return
		}
		if !ok {
			minutes = int(scheduler.DefaultCheckInterval / time.Minute)
		}
		threshold, windowMinutes, err := st.GetCrashLoopSettings(r.Context())
		if err != nil {
			writeError(w, http.StatusInternalServerError, "internal_error", "failed to load crash-loop settings")
			return
		}
		logsPageSize, err := st.GetLogsPageSize(r.Context())
		if err != nil {
			writeError(w, http.StatusInternalServerError, "internal_error", "failed to load logs settings")
			return
		}
		writeJSON(w, http.StatusOK, generalSettingsResponse{
			CheckIntervalMinutes: minutes, CrashLoopThreshold: threshold, CrashLoopWindowMinutes: windowMinutes,
			LogsPageSize: logsPageSize,
		})
	}
}

func handleSetGeneralSettings(st *store.Store, logger *events.Logger) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		var req generalSettingsResponse
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			writeError(w, http.StatusBadRequest, "invalid_request", "invalid JSON body")
			return
		}
		if req.CheckIntervalMinutes == 0 {
			if current, ok, getErr := st.GetCheckIntervalMinutes(r.Context()); getErr == nil && ok {
				req.CheckIntervalMinutes = current
			} else {
				req.CheckIntervalMinutes = int(scheduler.DefaultCheckInterval / time.Minute)
			}
		}
		if req.CheckIntervalMinutes < 5 {
			writeError(w, http.StatusBadRequest, "invalid_request", "check_interval_minutes must be at least 5")
			return
		}
		currentThreshold, currentWindow, err := st.GetCrashLoopSettings(r.Context())
		if err != nil {
			writeError(w, http.StatusInternalServerError, "internal_error", "failed to load settings")
			return
		}
		if req.CrashLoopThreshold == 0 {
			req.CrashLoopThreshold = currentThreshold
		}
		if req.CrashLoopWindowMinutes == 0 {
			req.CrashLoopWindowMinutes = currentWindow
		}
		if req.CrashLoopThreshold < 1 || req.CrashLoopThreshold > 20 || req.CrashLoopWindowMinutes < 1 || req.CrashLoopWindowMinutes > 1440 {
			writeError(w, http.StatusBadRequest, "invalid_request", "crash-loop threshold must be 1-20 and window 1-1440 minutes")
			return
		}
		if req.LogsPageSize == 0 {
			if current, getErr := st.GetLogsPageSize(r.Context()); getErr == nil {
				req.LogsPageSize = current
			} else {
				req.LogsPageSize = store.DefaultLogsPageSize
			}
		}
		if req.LogsPageSize < store.MinLogsPageSize || req.LogsPageSize > store.MaxLogsPageSize {
			writeError(w, http.StatusBadRequest, "invalid_request",
				fmt.Sprintf("logs_page_size must be %d-%d", store.MinLogsPageSize, store.MaxLogsPageSize))
			return
		}
		if err := st.SetCheckIntervalMinutes(r.Context(), req.CheckIntervalMinutes); err != nil {
			writeError(w, http.StatusInternalServerError, "internal_error", "failed to save settings")
			return
		}
		if err := st.SetCrashLoopSettings(r.Context(), req.CrashLoopThreshold, req.CrashLoopWindowMinutes); err != nil {
			writeError(w, http.StatusInternalServerError, "internal_error", "failed to save crash-loop settings")
			return
		}
		if err := st.SetLogsPageSize(r.Context(), req.LogsPageSize); err != nil {
			writeError(w, http.StatusInternalServerError, "internal_error", "failed to save logs settings")
			return
		}
		logger.Emit(r.Context(), events.Event{Level: events.LevelInfo, Type: "settings.crashloop_changed", Actor: events.ActorUser, ActorID: auth.ActorFromContext(r.Context()), Message: fmt.Sprintf("automatic rollback policy set to %d crashes within %d minutes", req.CrashLoopThreshold, req.CrashLoopWindowMinutes), Metadata: map[string]any{"threshold": req.CrashLoopThreshold, "window_minutes": req.CrashLoopWindowMinutes}})
		writeJSON(w, http.StatusOK, req)
	}
}

// --- Exclusions ------------------------------------------------------------

func handleGetExclusions(st *store.Store) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		ex, err := st.GetExclusions(r.Context())
		if err != nil {
			writeError(w, http.StatusInternalServerError, "internal_error", "failed to load exclusions")
			return
		}
		if ex.Containers == nil {
			ex.Containers = []string{}
		}
		if ex.Stacks == nil {
			ex.Stacks = []string{}
		}
		writeJSON(w, http.StatusOK, ex)
	}
}

func handleSetExclusions(st *store.Store, logger *events.Logger) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		var ex store.Exclusions
		if err := json.NewDecoder(r.Body).Decode(&ex); err != nil {
			writeError(w, http.StatusBadRequest, "invalid_request", "invalid JSON body")
			return
		}
		if err := st.SetExclusions(r.Context(), ex); err != nil {
			writeError(w, http.StatusInternalServerError, "internal_error", "failed to save exclusions")
			return
		}
		// Recompute already-known containers' excluded flag right away,
		// rather than leaving it stale until the next periodic check (or a
		// restart) reaches UpsertSeen.
		if err := st.ApplyExclusions(r.Context(), ex); err != nil {
			writeError(w, http.StatusInternalServerError, "internal_error", "failed to apply exclusions")
			return
		}
		logger.Emit(r.Context(), events.Event{
			Level: events.LevelInfo, Type: "settings.exclusions_changed", Actor: events.ActorUser, ActorID: auth.ActorFromContext(r.Context()),
			Message:  fmt.Sprintf("exclusions updated: %d container(s), %d stack(s)", len(ex.Containers), len(ex.Stacks)),
			Metadata: map[string]any{"containers": len(ex.Containers), "stacks": len(ex.Stacks)},
		})
		writeJSON(w, http.StatusOK, ex)
	}
}

// --- Security / API keys ----------------------------------------------------

type apiKeyResponse struct {
	ID         int64   `json:"id"`
	Name       string  `json:"name"`
	KeyPrefix  string  `json:"key_prefix"`
	CreatedAt  string  `json:"created_at"`
	LastUsedAt *string `json:"last_used_at,omitempty"`
	// Key is only ever populated on creation — see handleCreateAPIKey.
	// The raw key is never recoverable afterward (SECURITY.md: hashed,
	// write-once).
	Key    string         `json:"key,omitempty"`
	Access accessResponse `json:"access"`
}

func handleListAPIKeys(st *store.Store) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		keys, err := st.ListAPIKeys(r.Context())
		if err != nil {
			writeError(w, http.StatusInternalServerError, "internal_error", "failed to list API keys")
			return
		}
		resp := make([]apiKeyResponse, 0, len(keys))
		for _, k := range keys {
			item := apiKeyResponse{ID: k.ID, Name: k.Name, KeyPrefix: k.KeyPrefix, CreatedAt: k.CreatedAt.Format(time.RFC3339), Access: accessDTO(k.Access)}
			if k.LastUsedAt != nil {
				s := k.LastUsedAt.Format(time.RFC3339)
				item.LastUsedAt = &s
			}
			resp = append(resp, item)
		}
		writeJSON(w, http.StatusOK, resp)
	}
}

func handleCreateAPIKey(st *store.Store, logger *events.Logger) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		var req struct {
			Name   string         `json:"name"`
			Access accessResponse `json:"access"`
		}
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			writeError(w, http.StatusBadRequest, "invalid_request", "invalid JSON body")
			return
		}
		if req.Access.Role == "" {
			req.Access.Role = store.RoleAdmin // compatibility with pre-RBAC clients
		}
		if req.Name == "" || !req.Access.model().Valid() {
			writeError(w, http.StatusBadRequest, "invalid_request", "name and a valid role are required")
			return
		}

		token, err := auth.GenerateToken()
		if err != nil {
			writeError(w, http.StatusInternalServerError, "internal_error", "failed to generate key")
			return
		}
		prefix := token
		if len(prefix) > 8 {
			prefix = prefix[:8]
		}

		id, err := st.CreateAPIKeyWithAccess(r.Context(), req.Name, auth.HashToken(token), prefix, req.Access.model())
		if err != nil {
			writeError(w, http.StatusInternalServerError, "internal_error", "failed to create API key")
			return
		}
		logger.Emit(r.Context(), events.Event{
			Level: events.LevelInfo, Type: "auth.api_key_created", Actor: events.ActorUser, ActorID: auth.ActorFromContext(r.Context()),
			Message: fmt.Sprintf("API key %q created", req.Name),
		})

		// The only time the raw key is ever returned — see docs/api.md and
		// SECURITY.md (API keys are stored hashed and are not recoverable).
		writeJSON(w, http.StatusCreated, apiKeyResponse{
			ID: id, Name: req.Name, KeyPrefix: prefix, CreatedAt: time.Now().UTC().Format(time.RFC3339), Key: token, Access: req.Access,
		})
	}
}

func handleRevokeAPIKey(st *store.Store, logger *events.Logger) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		id, err := strconv.ParseInt(r.PathValue("id"), 10, 64)
		if err != nil {
			writeError(w, http.StatusBadRequest, "invalid_request", "invalid api key id")
			return
		}
		if err := st.RevokeAPIKey(r.Context(), id); errors.Is(err, store.ErrNotFound) {
			writeError(w, http.StatusNotFound, "not_found", "api key not found")
			return
		} else if err != nil {
			writeError(w, http.StatusInternalServerError, "internal_error", "failed to revoke api key")
			return
		}
		logger.Emit(r.Context(), events.Event{
			Level: events.LevelInfo, Type: "auth.api_key_revoked", Actor: events.ActorUser, ActorID: auth.ActorFromContext(r.Context()),
			Message: fmt.Sprintf("API key #%d revoked", id),
		})
		writeJSON(w, http.StatusOK, map[string]string{"status": "ok"})
	}
}
