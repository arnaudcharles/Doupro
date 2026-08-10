package api

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"strings"
	"time"

	"github.com/arnaudcharles/doupro/internal/auth"
	"github.com/arnaudcharles/doupro/internal/events"
	"github.com/arnaudcharles/doupro/internal/store"
)

// Bootstrap ensures at least one local account exists before the server
// starts accepting requests. SECURITY.md requires auth to be on by
// default, so this never runs open: if DOUPRO_ADMIN_USER and
// DOUPRO_ADMIN_PASSWORD are both unset, it falls back to the zero-config
// admin/admin account instead of refusing to start — but that account is
// flagged must_change_password, so RequireAuth blocks it from reaching
// anything except the change-password flow until the operator sets a real
// password. An explicitly configured admin account is trusted as-is and
// not flagged.
func Bootstrap(ctx context.Context, st *store.Store, adminUser, adminPassword string) error {
	exists, err := st.HasAnyUser(ctx)
	if err != nil {
		return fmt.Errorf("check existing users: %w", err)
	}
	if exists {
		return nil
	}

	mustChangePassword := false
	switch {
	case adminUser == "" && adminPassword == "":
		adminUser, adminPassword = "admin", "admin"
		mustChangePassword = true
	case adminUser == "" || adminPassword == "":
		return errors.New("only one of DOUPRO_ADMIN_USER/DOUPRO_ADMIN_PASSWORD is set — set both, or leave both unset to use the admin/admin default (see .env.example)")
	case adminPassword == "CHANGE_ME" || len(adminPassword) < 8:
		return errors.New("DOUPRO_ADMIN_PASSWORD is still the .env.example placeholder or too short (8+ chars) — set a real password")
	}

	hash, err := auth.HashPassword(adminPassword)
	if err != nil {
		return err
	}
	return st.CreateUser(ctx, adminUser, hash, mustChangePassword)
}

type loginRequest struct {
	Username string `json:"username"`
	Password string `json:"password"`
}

func handleLogin(st *store.Store, logger *events.Logger, trustProxyHeaders bool) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		wantsJSON := isJSON(r)

		var req loginRequest
		if wantsJSON {
			if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
				writeError(w, http.StatusBadRequest, "invalid_request", "invalid JSON body")
				return
			}
		} else {
			if err := r.ParseForm(); err != nil {
				writeError(w, http.StatusBadRequest, "invalid_request", "invalid form body")
				return
			}
			req.Username = r.FormValue("username")
			req.Password = r.FormValue("password")
		}

		user, err := st.GetUserByUsername(r.Context(), req.Username)
		// Deliberately identical failure path — and identical timing, via
		// VerifyPasswordConstantTime — for "no such user" and "wrong
		// password", so a response can't be used to enumerate valid
		// usernames either by its content or by how long it took.
		if err != nil || !auth.VerifyPasswordConstantTime(user.PasswordHash, req.Password) {
			logger.Emit(r.Context(), events.Event{
				Level: events.LevelWarn, Type: "auth.login_failed", Actor: events.ActorUser, ActorID: req.Username,
				Message: fmt.Sprintf("failed login attempt for %q", req.Username),
			})
			if wantsJSON {
				writeError(w, http.StatusUnauthorized, "invalid_credentials", "invalid username or password")
				return
			}
			http.Redirect(w, r, "/login?error=1", http.StatusSeeOther)
			return
		}

		token, err := auth.GenerateToken()
		if err != nil {
			writeError(w, http.StatusInternalServerError, "internal_error", "failed to create session")
			return
		}
		if err := st.CreateSession(r.Context(), auth.HashToken(token), user.ID, auth.SessionTTL); err != nil {
			writeError(w, http.StatusInternalServerError, "internal_error", "failed to create session")
			return
		}
		csrfToken, err := auth.GenerateToken()
		if err != nil {
			writeError(w, http.StatusInternalServerError, "internal_error", "failed to create session")
			return
		}

		secure := auth.IsSecureRequest(r, trustProxyHeaders)
		http.SetCookie(w, &http.Cookie{
			Name:     auth.SessionCookieName,
			Value:    token,
			Path:     "/",
			HttpOnly: true,
			SameSite: http.SameSiteLaxMode,
			Secure:   secure,
			Expires:  time.Now().Add(auth.SessionTTL),
		})
		// Deliberately not HttpOnly — see auth.CSRFCookieName's doc comment.
		http.SetCookie(w, &http.Cookie{
			Name:     auth.CSRFCookieName,
			Value:    csrfToken,
			Path:     "/",
			HttpOnly: false,
			SameSite: http.SameSiteLaxMode,
			Secure:   secure,
			Expires:  time.Now().Add(auth.SessionTTL),
		})

		logger.Emit(r.Context(), events.Event{
			Level: events.LevelInfo, Type: "auth.login_succeeded", Actor: events.ActorUser, ActorID: user.Username,
			Message: fmt.Sprintf("%s logged in", user.Username),
		})

		if wantsJSON {
			writeJSON(w, http.StatusOK, map[string]string{"status": "ok"})
			return
		}
		http.Redirect(w, r, "/", http.StatusSeeOther)
	}
}

type changePasswordRequest struct {
	CurrentPassword string `json:"current_password"`
	NewPassword     string `json:"new_password"`
}

// handleChangePassword backs POST /api/v1/auth/password. Requires the
// current password even when called as part of the forced-first-login
// flow (the default admin/admin password still has to be typed once) —
// this also doubles as the voluntary "change my password" action from
// Settings, so there is exactly one code path for both.
func handleChangePassword(st *store.Store, logger *events.Logger) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		user, ok := auth.UserFromContext(r.Context())
		if !ok {
			writeError(w, http.StatusForbidden, "forbidden", "password changes require a browser session, not an API key")
			return
		}

		var req changePasswordRequest
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			writeError(w, http.StatusBadRequest, "invalid_request", "invalid JSON body")
			return
		}
		if !auth.VerifyPassword(user.PasswordHash, req.CurrentPassword) {
			writeError(w, http.StatusUnauthorized, "invalid_credentials", "current password is incorrect")
			return
		}
		if req.NewPassword == "CHANGE_ME" || len(req.NewPassword) < 8 {
			writeError(w, http.StatusBadRequest, "invalid_request", "new password must be at least 8 characters")
			return
		}

		hash, err := auth.HashPassword(req.NewPassword)
		if err != nil {
			writeError(w, http.StatusInternalServerError, "internal_error", "failed to hash password")
			return
		}
		if err := st.UpdatePassword(r.Context(), user.ID, hash); err != nil {
			writeError(w, http.StatusInternalServerError, "internal_error", "failed to update password")
			return
		}

		// A stolen session cookie must not survive its victim changing
		// their password. Keep the caller's own session alive (they just
		// proved they know the new password) and kill every other one.
		if c, err := r.Cookie(auth.SessionCookieName); err == nil {
			_ = st.DeleteOtherSessions(r.Context(), user.ID, auth.HashToken(c.Value))
		}
		logger.Emit(r.Context(), events.Event{
			Level: events.LevelInfo, Type: "auth.password_changed", Actor: events.ActorUser, ActorID: user.Username,
			Message: fmt.Sprintf("%s changed their password", user.Username),
		})
		writeJSON(w, http.StatusOK, map[string]string{"status": "ok"})
	}
}

// handleLogout is intentionally not behind RequireAuth — logging out with
// no valid session must still succeed (idempotent) — so it resolves the
// session itself rather than relying on middleware to have populated the
// request context.
func handleLogout(st *store.Store, logger *events.Logger) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if c, err := r.Cookie(auth.SessionCookieName); err == nil {
			tokenHash := auth.HashToken(c.Value)
			if user, err := st.SessionUser(r.Context(), tokenHash); err == nil {
				logger.Emit(r.Context(), events.Event{
					Level: events.LevelInfo, Type: "auth.logout", Actor: events.ActorUser, ActorID: user.Username,
					Message: fmt.Sprintf("%s logged out", user.Username),
				})
			}
			_ = st.DeleteSession(r.Context(), tokenHash)
		}
		http.SetCookie(w, &http.Cookie{Name: auth.SessionCookieName, Value: "", Path: "/", MaxAge: -1})
		http.SetCookie(w, &http.Cookie{Name: auth.CSRFCookieName, Value: "", Path: "/", MaxAge: -1})

		if isJSON(r) {
			writeJSON(w, http.StatusOK, map[string]string{"status": "ok"})
			return
		}
		http.Redirect(w, r, "/login", http.StatusSeeOther)
	}
}

func isJSON(r *http.Request) bool {
	return strings.HasPrefix(r.Header.Get("Content-Type"), "application/json") ||
		strings.HasPrefix(r.Header.Get("Accept"), "application/json")
}
