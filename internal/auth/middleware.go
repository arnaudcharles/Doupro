package auth

import (
	"context"
	"net/http"
	"strings"
	"time"

	"github.com/arnaudcharles/doupro/internal/store"
)

// SessionCookieName is the cookie used for web UI sessions.
const SessionCookieName = "doupro_session"

// CSRFCookieName is the double-submit CSRF token: a random value set
// alongside the session cookie on login, deliberately *not* HttpOnly so
// page JS can read it and echo it back on every state-changing request
// (see web/static/csrf.js). A cross-site attacker can make a victim's
// browser send requests with the session cookie attached, but can't read
// this cookie's value to also produce a matching token — same-origin
// policy blocks that regardless of SameSite. Only checked for
// session-cookie-authenticated requests; a Bearer API key is never
// auto-attached by a browser, so it isn't vulnerable to CSRF in the first
// place.
const CSRFCookieName = "doupro_csrf"

// CSRFHeaderName is the header web/static/csrf.js's fetch patch sends the
// token back in; RequireCSRF also accepts it as a csrf_token form field
// for plain (non-fetch) <form method="post"> submissions.
const CSRFHeaderName = "X-CSRF-Token"

// SessionTTL is how long a session stays valid. Not yet configurable —
// see docs/settings.md (Security → session timeout).
const SessionTTL = 30 * 24 * time.Hour

type contextKey string

const actorContextKey contextKey = "actor"
const userContextKey contextKey = "user"
const accessContextKey contextKey = "access"
const trustedLocalContextKey contextKey = "trusted-local"
const cspNonceContextKey contextKey = "csp-nonce"

// WithCSPNonce attaches the per-request Content-Security-Policy nonce
// (generated once in api.SecurityHeaders) to ctx so the web package can
// render it into every inline <script nonce="..."> tag — the same value
// that went into the Content-Security-Policy response header, which is
// what lets those specific inline scripts run while any injected one
// (without the nonce) is blocked by the browser.
func WithCSPNonce(ctx context.Context, nonce string) context.Context {
	return context.WithValue(ctx, cspNonceContextKey, nonce)
}

// CSPNonceFromContext returns the nonce WithCSPNonce attached, or "" if
// none is present (e.g. a request that bypassed SecurityHeaders, such as
// in tests).
func CSPNonceFromContext(ctx context.Context) string {
	v, _ := ctx.Value(cspNonceContextKey).(string)
	return v
}

// WithTrustedLocalAccess marks ctx as coming from the local Unix-socket
// listener (see cmd/doupro/cmd_serve.go) — reachable only by something
// already inside the container's mount namespace (e.g. `docker exec`),
// which is root-equivalent access to the Docker socket DoUpRo itself
// holds, same as the trust already implied by that access level. Callers
// must only ever set this from server-side code that owns which listener
// accepted the connection; it must never be derived from anything a
// network client can send (a header, a query param, ...), since doing so
// would let a request over the real (API-key/session-gated) TCP listener
// forge full admin access.
func WithTrustedLocalAccess(ctx context.Context) context.Context {
	return context.WithValue(ctx, trustedLocalContextKey, true)
}

// ActorFromContext returns the username (or "api-key" for key-authenticated
// requests) RequireAuth attached to the request, or "" if called outside
// an authenticated request. Used to fill in events.Event.ActorID for
// actions a specific operator triggered (docs/logs.md).
func ActorFromContext(ctx context.Context) string {
	v, _ := ctx.Value(actorContextKey).(string)
	return v
}

// UserFromContext returns the session-authenticated user RequireAuth
// attached to the request, and whether one was present — false for
// API-key-authenticated requests, which have no store.User behind them.
func UserFromContext(ctx context.Context) (store.User, bool) {
	v, ok := ctx.Value(userContextKey).(store.User)
	return v, ok
}

// AccessFromContext returns the RBAC assignment attached by RequireAuth.
func AccessFromContext(ctx context.Context) (store.AccessControl, bool) {
	v, ok := ctx.Value(accessContextKey).(store.AccessControl)
	return v, ok
}

// RequirePermission rejects authenticated principals that lack permission.
// It must be wrapped inside RequireAuth so an access assignment is present.
func RequirePermission(permission store.Permission) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			access, ok := AccessFromContext(r.Context())
			if !ok || !access.Can(permission) {
				writeForbidden(w, r)
				return
			}
			next.ServeHTTP(w, r)
		})
	}
}

// changePasswordPaths are reachable by a session whose account is flagged
// must_change_password — everything else 403s/redirects until the
// password is changed (see api.handleChangePassword, web.handleChangePassword).
var changePasswordPaths = map[string]bool{
	"/change-password":      true,
	"/api/v1/auth/password": true,
	"/api/v1/auth/logout":   true,
}

// RequireAuth wraps a handler so it only runs for a valid session cookie
// or an `Authorization: Bearer <api-key>` header (docs/api.md). API/CLI
// callers (JSON Accept header or an /api/ path) get a 401 JSON body on
// failure; browser requests are redirected to /login instead, since a
// bare 401 has nowhere for an operator to go next. A session whose
// account has must_change_password set is only let through to the
// change-password page/endpoint and logout — see docs/settings.md
// (Security → forced password change).
func RequireAuth(st *store.Store) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if trusted, _ := r.Context().Value(trustedLocalContextKey).(bool); trusted {
				ctx := context.WithValue(r.Context(), actorContextKey, "local-socket")
				ctx = context.WithValue(ctx, accessContextKey, store.AccessControl{Role: store.RoleAdmin})
				next.ServeHTTP(w, r.WithContext(ctx))
				return
			}

			if header := r.Header.Get("Authorization"); strings.HasPrefix(header, "Bearer ") {
				key := strings.TrimPrefix(header, "Bearer ")
				if apiKey, err := st.AuthenticateAPIKey(r.Context(), HashToken(key)); err == nil {
					ctx := context.WithValue(r.Context(), actorContextKey, "api-key")
					ctx = context.WithValue(ctx, accessContextKey, apiKey.Access)
					next.ServeHTTP(w, r.WithContext(ctx))
					return
				}
				writeUnauthorized(w, r, "invalid or revoked API key")
				return
			}

			if c, err := r.Cookie(SessionCookieName); err == nil {
				if user, err := st.SessionUser(r.Context(), HashToken(c.Value)); err == nil {
					if user.MustChangePassword && !changePasswordPaths[r.URL.Path] {
						writePasswordChangeRequired(w, r)
						return
					}
					if !csrfSafeMethod(r.Method) && !validCSRF(r) {
						writeCSRFRequired(w, r)
						return
					}
					ctx := context.WithValue(r.Context(), actorContextKey, user.Username)
					ctx = context.WithValue(ctx, userContextKey, user)
					ctx = context.WithValue(ctx, accessContextKey, user.Access)
					next.ServeHTTP(w, r.WithContext(ctx))
					return
				}
			}

			writeUnauthorized(w, r, "login required")
		})
	}
}

func writeForbidden(w http.ResponseWriter, r *http.Request) {
	if strings.HasPrefix(r.URL.Path, "/api/") {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusForbidden)
		_, _ = w.Write([]byte(`{"error":{"code":"forbidden","message":"insufficient permission"}}`))
		return
	}
	http.Error(w, "insufficient permission", http.StatusForbidden)
}

// IsSecureRequest reports whether a request arrived over TLS, for setting
// the session/CSRF cookies' Secure flag. r.TLS is nil for every request
// when DoUpRo sits behind a reverse proxy that terminates TLS itself (the
// very common Traefik/nginx/Caddy homelab setup) — checking only r.TLS
// would silently make Secure permanently false in that deployment, not
// just when genuinely running plain HTTP. trustProxyHeaders (set via
// DOUPRO_TRUST_PROXY_HEADERS) must be explicitly opted into, since
// X-Forwarded-Proto is trivially spoofable by any client when there is no
// proxy actually stripping and re-setting it.
func IsSecureRequest(r *http.Request, trustProxyHeaders bool) bool {
	if r.TLS != nil {
		return true
	}
	if trustProxyHeaders && strings.EqualFold(r.Header.Get("X-Forwarded-Proto"), "https") {
		return true
	}
	return false
}

func csrfSafeMethod(method string) bool {
	return method == http.MethodGet || method == http.MethodHead || method == http.MethodOptions
}

// validCSRF implements the double-submit pattern: the request is legitimate
// only if the token it carries (header or form field — see
// web/static/csrf.js) matches the CSRFCookieName cookie value. A
// cross-site attacker can't produce a match without being able to read
// that cookie, which same-origin policy prevents regardless of SameSite
// mode.
func validCSRF(r *http.Request) bool {
	cookie, err := r.Cookie(CSRFCookieName)
	if err != nil || cookie.Value == "" {
		return false
	}
	submitted := r.Header.Get(CSRFHeaderName)
	if submitted == "" {
		submitted = r.FormValue("csrf_token")
	}
	return submitted != "" && TokensEqual(cookie.Value, submitted)
}

func writeCSRFRequired(w http.ResponseWriter, r *http.Request) {
	if strings.HasPrefix(r.URL.Path, "/api/") {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusForbidden)
		_, _ = w.Write([]byte(`{"error":{"code":"csrf_token_invalid","message":"missing or invalid CSRF token"}}`))
		return
	}
	http.Error(w, "missing or invalid CSRF token", http.StatusForbidden)
}

func writePasswordChangeRequired(w http.ResponseWriter, r *http.Request) {
	if strings.HasPrefix(r.URL.Path, "/api/") {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusForbidden)
		_, _ = w.Write([]byte(`{"error":{"code":"password_change_required","message":"the default password must be changed before continuing"}}`))
		return
	}
	http.Redirect(w, r, "/change-password", http.StatusSeeOther)
}

func writeUnauthorized(w http.ResponseWriter, r *http.Request, message string) {
	if strings.HasPrefix(r.URL.Path, "/api/") {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusUnauthorized)
		_, _ = w.Write([]byte(`{"error":{"code":"unauthenticated","message":"` + message + `"}}`))
		return
	}
	http.Redirect(w, r, "/login", http.StatusSeeOther)
}
