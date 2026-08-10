package api

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/arnaudcharles/doupro/internal/auth"
)

func TestSecurityHeadersSetsCSPWithAPerRequestNonceAlsoReachableFromContext(t *testing.T) {
	var seenNonce string
	inner := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		seenNonce = auth.CSPNonceFromContext(r.Context())
	})

	rec := httptest.NewRecorder()
	SecurityHeaders(inner).ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/", nil))

	csp := rec.Header().Get("Content-Security-Policy")
	if csp == "" {
		t.Fatal("expected a Content-Security-Policy header, got none")
	}
	if seenNonce == "" {
		t.Fatal("expected a nonce to be attached to the request context")
	}
	if !strings.Contains(csp, "'nonce-"+seenNonce+"'") {
		t.Fatalf("CSP header %q does not contain the context nonce %q", csp, seenNonce)
	}
	if !strings.Contains(csp, "frame-ancestors 'none'") {
		t.Fatalf("CSP header %q missing frame-ancestors directive", csp)
	}
}

func TestSecurityHeadersUsesADifferentNonceOnEachRequest(t *testing.T) {
	inner := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {})
	handler := SecurityHeaders(inner)

	rec1 := httptest.NewRecorder()
	handler.ServeHTTP(rec1, httptest.NewRequest(http.MethodGet, "/", nil))
	rec2 := httptest.NewRecorder()
	handler.ServeHTTP(rec2, httptest.NewRequest(http.MethodGet, "/", nil))

	if rec1.Header().Get("Content-Security-Policy") == rec2.Header().Get("Content-Security-Policy") {
		t.Fatal("expected a fresh nonce (and thus a different CSP header) per request")
	}
}
