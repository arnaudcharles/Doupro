package api

import (
	"fmt"
	"net/http"
	"strings"
	"time"

	"golang.org/x/oauth2"

	"github.com/arnaudcharles/doupro/internal/auth"
	"github.com/arnaudcharles/doupro/internal/events"
	oidcauth "github.com/arnaudcharles/doupro/internal/oidc"
	"github.com/arnaudcharles/doupro/internal/store"
)

// oidcFlowCookie carries state+nonce+PKCE verifier between handleOIDCLogin
// and handleOIDCCallback. Same double-submit reasoning as auth.CSRFCookieName:
// the callback trusts these values only because they round-tripped through a
// cookie an attacker can't read or forge, not because the query string says
// so. Scoped to the OIDC flow's own path and short-lived — a stale one
// simply fails the callback's state check and sends the user back to /login.
const oidcFlowCookie = "doupro_oidc_flow"
const oidcFlowTTL = 10 * time.Minute

func handleOIDCLogin(prov *oidcauth.Provider, trustProxyHeaders bool) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		state, err := auth.GenerateToken()
		if err != nil {
			http.Error(w, "failed to start login", http.StatusInternalServerError)
			return
		}
		nonce, err := auth.GenerateToken()
		if err != nil {
			http.Error(w, "failed to start login", http.StatusInternalServerError)
			return
		}
		verifier := oauth2.GenerateVerifier()

		http.SetCookie(w, &http.Cookie{
			Name:     oidcFlowCookie,
			Value:    strings.Join([]string{state, nonce, verifier}, "|"),
			Path:     "/api/v1/auth/oidc",
			HttpOnly: true,
			SameSite: http.SameSiteLaxMode,
			Secure:   auth.IsSecureRequest(r, trustProxyHeaders),
			MaxAge:   int(oidcFlowTTL.Seconds()),
		})

		http.Redirect(w, r, prov.AuthCodeURL(state, nonce, verifier), http.StatusFound)
	}
}

func handleOIDCCallback(prov *oidcauth.Provider, st *store.Store, logger *events.Logger, trustProxyHeaders bool) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		clearFlowCookie := &http.Cookie{Name: oidcFlowCookie, Value: "", Path: "/api/v1/auth/oidc", MaxAge: -1}

		flowCookie, err := r.Cookie(oidcFlowCookie)
		if err != nil {
			http.SetCookie(w, clearFlowCookie)
			http.Redirect(w, r, "/login?error=1", http.StatusSeeOther)
			return
		}
		parts := strings.SplitN(flowCookie.Value, "|", 3)
		if len(parts) != 3 {
			http.SetCookie(w, clearFlowCookie)
			http.Redirect(w, r, "/login?error=1", http.StatusSeeOther)
			return
		}
		wantState, wantNonce, verifier := parts[0], parts[1], parts[2]
		http.SetCookie(w, clearFlowCookie)

		if r.URL.Query().Get("state") != wantState {
			http.Redirect(w, r, "/login?error=1", http.StatusSeeOther)
			return
		}

		code := r.URL.Query().Get("code")
		if code == "" {
			http.Redirect(w, r, "/login?error=1", http.StatusSeeOther)
			return
		}

		claims, err := prov.Exchange(r.Context(), code, verifier, wantNonce)
		if err != nil {
			logger.Emit(r.Context(), events.Event{
				Level: events.LevelWarn, Type: "auth.oidc_login_failed", Actor: events.ActorUser,
				Message: fmt.Sprintf("oidc login failed: %s", err),
			})
			http.Redirect(w, r, "/login?error=1", http.StatusSeeOther)
			return
		}

		if !prov.GroupAllowed(claims) {
			logger.Emit(r.Context(), events.Event{
				Level: events.LevelWarn, Type: "auth.oidc_login_denied", Actor: events.ActorUser, ActorID: claims.Username,
				Message:  fmt.Sprintf("%s authenticated via OIDC but is not in an allowed group", claims.Username),
				Metadata: map[string]any{"provider": "oidc", "subject": claims.Subject},
			})
			http.Redirect(w, r, "/login?error=1", http.StatusSeeOther)
			return
		}

		user, err := st.GetOrCreateOIDCUser(r.Context(), claims.Subject, claims.Username)
		if err != nil {
			logger.Emit(r.Context(), events.Event{
				Level: events.LevelError, Type: "auth.oidc_login_failed", Actor: events.ActorUser, ActorID: claims.Username,
				Message: fmt.Sprintf("failed to provision oidc user: %s", err),
			})
			http.Redirect(w, r, "/login?error=1", http.StatusSeeOther)
			return
		}

		token, err := auth.GenerateToken()
		if err != nil {
			http.Error(w, "failed to create session", http.StatusInternalServerError)
			return
		}
		if err := st.CreateSession(r.Context(), auth.HashToken(token), user.ID, auth.SessionTTL); err != nil {
			http.Error(w, "failed to create session", http.StatusInternalServerError)
			return
		}
		csrfToken, err := auth.GenerateToken()
		if err != nil {
			http.Error(w, "failed to create session", http.StatusInternalServerError)
			return
		}

		secure := auth.IsSecureRequest(r, trustProxyHeaders)
		http.SetCookie(w, &http.Cookie{
			Name: auth.SessionCookieName, Value: token, Path: "/", HttpOnly: true,
			SameSite: http.SameSiteLaxMode, Secure: secure, Expires: time.Now().Add(auth.SessionTTL),
		})
		http.SetCookie(w, &http.Cookie{
			Name: auth.CSRFCookieName, Value: csrfToken, Path: "/", HttpOnly: false,
			SameSite: http.SameSiteLaxMode, Secure: secure, Expires: time.Now().Add(auth.SessionTTL),
		})

		logger.Emit(r.Context(), events.Event{
			Level: events.LevelInfo, Type: "auth.login_succeeded", Actor: events.ActorUser, ActorID: user.Username,
			Message:  fmt.Sprintf("%s logged in via OIDC", user.Username),
			Metadata: map[string]any{"provider": "oidc", "subject": claims.Subject},
		})

		http.Redirect(w, r, "/", http.StatusSeeOther)
	}
}
