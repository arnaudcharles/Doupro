package api

import (
	"crypto/rand"
	"encoding/base64"
	"net/http"

	"github.com/arnaudcharles/doupro/internal/auth"
)

// SecurityHeaders wraps the whole mux (API + web UI) with headers that
// cost nothing to set and close off a few classes of attack SECURITY.md
// commits to defending against — most importantly clickjacking, given
// DoUpRo's web UI can recreate containers with root-equivalent access to
// the host.
//
// Content-Security-Policy uses a fresh per-request nonce for script-src:
// every inline <script> the web UI's templates render carries
// nonce="{{.CSPNonce}}" (see internal/web), so those specific inline
// blocks execute while any script an attacker manages to inject (which
// can't know the nonce in advance) is blocked by the browser. style-src
// stays 'unsafe-inline': the templates have no inline style attributes of
// their own, but the vendored Swagger UI bundle (web/static/swagger/,
// see docs/web-ui.md) injects its own <style> tags at runtime that a
// third-party bundle has no way to stamp with our nonce — tightening
// style-src further would just break /swagger for no real XSS benefit,
// since style-only injection can't execute script.
func SecurityHeaders(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		h := w.Header()
		h.Set("X-Content-Type-Options", "nosniff")
		h.Set("X-Frame-Options", "DENY")
		h.Set("Referrer-Policy", "same-origin")

		nonce := generateNonce()
		h.Set("Content-Security-Policy",
			"default-src 'self'; "+
				"script-src 'self' 'nonce-"+nonce+"'; "+
				"style-src 'self' 'unsafe-inline'; "+
				"img-src 'self' data:; "+
				"connect-src 'self'; "+
				"object-src 'none'; "+
				"base-uri 'self'; "+
				"frame-ancestors 'none'")

		next.ServeHTTP(w, r.WithContext(auth.WithCSPNonce(r.Context(), nonce)))
	})
}

// generateNonce returns a fresh random base64 value for the CSP nonce
// directive. Returning "" on a crypto/rand failure (practically never
// happens) just means that request's inline scripts get blocked, not a
// crash or a predictable nonce.
func generateNonce() string {
	b := make([]byte, 16)
	if _, err := rand.Read(b); err != nil {
		return ""
	}
	return base64.StdEncoding.EncodeToString(b)
}
