package api

import (
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/arnaudcharles/doupro/internal/auth"
	"github.com/arnaudcharles/doupro/internal/events"
	"github.com/arnaudcharles/doupro/internal/store"
)

type accessResponse struct {
	Role                store.Role `json:"role"`
	ViewLogs            bool       `json:"view_logs"`
	ManageSchedules     bool       `json:"manage_schedules"`
	ManageNotifications bool       `json:"manage_notifications"`
	ViewStats           bool       `json:"view_stats"`
	Protected           bool       `json:"protected,omitempty"`
}

func accessDTO(a store.AccessControl) accessResponse {
	return accessResponse{Role: a.Role, ViewLogs: a.ViewLogs, ManageSchedules: a.ManageSchedules,
		ManageNotifications: a.ManageNotifications, ViewStats: a.ViewStats, Protected: a.Protected}
}

func (a accessResponse) model() store.AccessControl {
	return store.AccessControl{Role: a.Role, ViewLogs: a.ViewLogs, ManageSchedules: a.ManageSchedules,
		ManageNotifications: a.ManageNotifications, ViewStats: a.ViewStats}
}

type userResponse struct {
	ID           int64          `json:"id"`
	Username     string         `json:"username"`
	AuthProvider string         `json:"auth_provider"`
	CreatedAt    string         `json:"created_at"`
	Access       accessResponse `json:"access"`
}

func userDTO(u store.User) userResponse {
	return userResponse{ID: u.ID, Username: u.Username, AuthProvider: u.AuthProvider,
		CreatedAt: u.CreatedAt.Format(time.RFC3339), Access: accessDTO(u.Access)}
}

func handleListUsers(st *store.Store) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		users, err := st.ListUsers(r.Context())
		if err != nil {
			writeError(w, http.StatusInternalServerError, "internal_error", "failed to list users")
			return
		}
		out := make([]userResponse, 0, len(users))
		for _, u := range users {
			out = append(out, userDTO(u))
		}
		writeJSON(w, http.StatusOK, out)
	}
}

func handleCreateUser(st *store.Store, logger *events.Logger) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		var req struct {
			Username string         `json:"username"`
			Password string         `json:"password"`
			Access   accessResponse `json:"access"`
		}
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			writeError(w, http.StatusBadRequest, "invalid_request", "invalid JSON body")
			return
		}
		req.Username = strings.TrimSpace(req.Username)
		if req.Username == "" || len(req.Password) < 8 || !req.Access.model().Valid() {
			writeError(w, http.StatusBadRequest, "invalid_request", "username, password of at least 8 characters, and a valid role are required")
			return
		}
		hash, err := auth.HashPassword(req.Password)
		if err != nil {
			writeError(w, http.StatusInternalServerError, "internal_error", "failed to hash password")
			return
		}
		u, err := st.CreateUserWithAccess(r.Context(), req.Username, hash, false, req.Access.model())
		if err != nil {
			writeError(w, http.StatusConflict, "user_create_failed", err.Error())
			return
		}
		logger.Emit(r.Context(), events.Event{Level: events.LevelInfo, Type: "auth.user_created", Actor: events.ActorUser,
			ActorID: auth.ActorFromContext(r.Context()), Message: fmt.Sprintf("user %q created", u.Username)})
		writeJSON(w, http.StatusCreated, userDTO(u))
	}
}

func handleUpdateUserAccess(st *store.Store, logger *events.Logger) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		id, err := strconv.ParseInt(r.PathValue("id"), 10, 64)
		if err != nil {
			writeError(w, http.StatusBadRequest, "invalid_request", "invalid user id")
			return
		}
		var access accessResponse
		if err := json.NewDecoder(r.Body).Decode(&access); err != nil || !access.model().Valid() {
			writeError(w, http.StatusBadRequest, "invalid_request", "valid access assignment required")
			return
		}
		user, err := st.GetUserByID(r.Context(), id)
		if errors.Is(err, store.ErrNotFound) {
			writeError(w, http.StatusNotFound, "not_found", "user not found")
			return
		}
		if err != nil {
			writeError(w, http.StatusInternalServerError, "internal_error", "failed to load user")
			return
		}
		if user.Access.Protected {
			writeError(w, http.StatusConflict, "protected_admin", "the bootstrap administrator always has full access and cannot be reconfigured")
			return
		}
		if user.Access.Role == store.RoleAdmin && access.Role != store.RoleAdmin {
			count, countErr := st.AdminCount(r.Context())
			if countErr != nil {
				writeError(w, http.StatusInternalServerError, "internal_error", "failed to validate administrators")
				return
			}
			if count == 1 {
				writeError(w, http.StatusConflict, "last_admin", "the last administrator cannot be demoted")
				return
			}
		}
		if err := st.SetAccess(r.Context(), "user", id, access.model()); err != nil {
			writeError(w, http.StatusInternalServerError, "internal_error", "failed to update user access")
			return
		}
		user.Access = access.model()
		logger.Emit(r.Context(), events.Event{Level: events.LevelInfo, Type: "auth.user_access_updated", Actor: events.ActorUser,
			ActorID: auth.ActorFromContext(r.Context()), Message: fmt.Sprintf("access updated for user %q", user.Username)})
		writeJSON(w, http.StatusOK, userDTO(user))
	}
}

func handleDeleteUser(st *store.Store, logger *events.Logger) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		id, err := strconv.ParseInt(r.PathValue("id"), 10, 64)
		if err != nil {
			writeError(w, http.StatusBadRequest, "invalid_request", "invalid user id")
			return
		}
		user, err := st.GetUserByID(r.Context(), id)
		if errors.Is(err, store.ErrNotFound) {
			writeError(w, http.StatusNotFound, "not_found", "user not found")
			return
		}
		if err != nil {
			writeError(w, http.StatusInternalServerError, "internal_error", "failed to load user")
			return
		}
		if current, ok := auth.UserFromContext(r.Context()); ok && current.ID == id {
			writeError(w, http.StatusConflict, "self_delete", "you cannot delete your own account")
			return
		}
		if user.Access.Protected {
			writeError(w, http.StatusConflict, "protected_admin", "the bootstrap administrator cannot be deleted")
			return
		}
		if user.Access.Role == store.RoleAdmin {
			count, countErr := st.AdminCount(r.Context())
			if countErr != nil || count == 1 {
				writeError(w, http.StatusConflict, "last_admin", "the last administrator cannot be deleted")
				return
			}
		}
		if err := st.DeleteUser(r.Context(), id); err != nil {
			writeError(w, http.StatusInternalServerError, "internal_error", "failed to delete user")
			return
		}
		logger.Emit(r.Context(), events.Event{Level: events.LevelInfo, Type: "auth.user_deleted", Actor: events.ActorUser,
			ActorID: auth.ActorFromContext(r.Context()), Message: fmt.Sprintf("user %q deleted", user.Username)})
		writeJSON(w, http.StatusOK, map[string]string{"status": "ok"})
	}
}
