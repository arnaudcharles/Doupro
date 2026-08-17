package web_test

// Smoke tests for the documentation surface a public repository leans on
// most: that the OpenAPI document served at runtime is valid, matches what
// Swagger UI actually renders, and that Swagger UI itself is reachable and
// wired at the URL it advertises. internal/api/contract_test.go separately
// guards that the document's *content* matches the registered routes; this
// file guards that the document and the UI serving it are actually live
// over HTTP, end to end, the way a browser or curl would hit them.

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/cookiejar"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"

	doupro "github.com/arnaudcharles/doupro"
	"github.com/arnaudcharles/doupro/internal/api"
	"github.com/arnaudcharles/doupro/internal/events"
	"github.com/arnaudcharles/doupro/internal/notifier"
	"github.com/arnaudcharles/doupro/internal/store"
	"github.com/arnaudcharles/doupro/internal/updater"
	"github.com/arnaudcharles/doupro/internal/web"
)

// newSmokeServer boots the same mux serve() wires up in production (API +
// web UI on one ServeMux, one HTTP server — see docs/architecture.md),
// backed by an in-memory store and a nil Docker client (this suite only
// exercises HTTP routing/rendering, never Docker).
func newSmokeServer(t *testing.T) (*httptest.Server, *http.Client) {
	t.Helper()
	ctx := context.Background()

	st, err := store.Open(ctx, ":memory:")
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	t.Cleanup(func() { _ = st.Close() })
	if err := api.Bootstrap(ctx, st, "admin", "admin-test-password"); err != nil {
		t.Fatalf("bootstrap admin: %v", err)
	}

	logger := events.New("error")
	logger.SetSink(st)
	notif := notifier.New(st, logger)
	upd := updater.New(nil, st, logger, notif)

	mux := http.NewServeMux()
	api.RegisterRoutes(mux, st, upd, notif, logger, false, nil, nil, nil)
	web.RegisterRoutes(mux, st, upd, notif, "test", doupro.TemplatesFS, doupro.StaticFS, doupro.ManualsFS, false)

	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)

	jar, err := cookiejar.New(nil)
	if err != nil {
		t.Fatalf("build cookie jar: %v", err)
	}
	client := &http.Client{Jar: jar}

	// Real login over HTTP (form-encoded, exactly how the browser's
	// /login page submits) rather than minting a session directly —
	// this exercises handleLogin too, not just the pages behind it.
	resp, err := client.PostForm(srv.URL+"/api/v1/auth/login", url.Values{
		"username": {"admin"},
		"password": {"admin-test-password"},
	})
	if err != nil {
		t.Fatalf("login: %v", err)
	}
	_, _ = io.Copy(io.Discard, resp.Body)
	_ = resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("login status = %d, want %d", resp.StatusCode, http.StatusOK)
	}

	return srv, client
}

func TestSwaggerUISmoke(t *testing.T) {
	srv, client := newSmokeServer(t)

	resp, err := client.Get(srv.URL + "/swagger")
	if err != nil {
		t.Fatalf("GET /swagger: %v", err)
	}
	body, _ := io.ReadAll(resp.Body)
	_ = resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		t.Fatalf("GET /swagger status = %d, want %d\nbody: %s", resp.StatusCode, http.StatusOK, body)
	}
	html := string(body)
	if !strings.Contains(html, `url: "/api/v1/openapi.json"`) {
		t.Fatal("swagger page does not point Swagger UI at /api/v1/openapi.json")
	}
	if !strings.Contains(html, "SwaggerUIBundle") {
		t.Fatal("swagger page did not render the vendored Swagger UI bundle")
	}
}

func TestSwaggerUIRequiresAuth(t *testing.T) {
	srv, _ := newSmokeServer(t)

	// A plain client that does not follow redirects: RequireAuth sends an
	// unauthenticated browser request to /login via 303 (see
	// auth.writeUnauthorized), which a redirect-following client would
	// mask as a 200 on /login — checking the first hop is what actually
	// proves /swagger itself refused the request.
	client := &http.Client{CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
	resp, err := client.Get(srv.URL + "/swagger")
	if err != nil {
		t.Fatalf("GET /swagger (unauthenticated): %v", err)
	}
	_, _ = io.Copy(io.Discard, resp.Body)
	_ = resp.Body.Close()
	if resp.StatusCode != http.StatusSeeOther && resp.StatusCode != http.StatusFound {
		t.Fatalf("unauthenticated GET /swagger status = %d, want a redirect to /login", resp.StatusCode)
	}
	if loc := resp.Header.Get("Location"); loc != "/login" {
		t.Fatalf("unauthenticated GET /swagger redirected to %q, want /login", loc)
	}
}

func TestOpenAPIDocumentIsServedAndWellFormed(t *testing.T) {
	srv, _ := newSmokeServer(t)

	resp, err := http.Get(srv.URL + "/api/v1/openapi.json")
	if err != nil {
		t.Fatalf("GET /api/v1/openapi.json: %v", err)
	}
	body, _ := io.ReadAll(resp.Body)
	_ = resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		t.Fatalf("GET /api/v1/openapi.json status = %d, want %d", resp.StatusCode, http.StatusOK)
	}
	if ct := resp.Header.Get("Content-Type"); !strings.Contains(ct, "json") {
		t.Fatalf("Content-Type = %q, want a JSON content type", ct)
	}

	var spec struct {
		OpenAPI string                    `json:"openapi"`
		Paths   map[string]map[string]any `json:"paths"`
		Info    map[string]any            `json:"info"`
		Servers []map[string]any          `json:"servers"`
	}
	if err := json.Unmarshal(body, &spec); err != nil {
		t.Fatalf("openapi.json served at runtime is not valid JSON: %v", err)
	}
	if !strings.HasPrefix(spec.OpenAPI, "3.") {
		t.Fatalf("openapi version = %q, want a 3.x document", spec.OpenAPI)
	}
	if len(spec.Paths) == 0 {
		t.Fatal("openapi.json document has no documented paths")
	}
	if _, ok := spec.Paths["/api/v1/containers"]; !ok {
		t.Fatal("openapi.json is missing the well-known /api/v1/containers path — served document may be stale or truncated")
	}
}

// TestOpenAPIDocumentIsUnauthenticated guards the reason /api/v1/openapi.json
// itself has no auth gate even though /swagger does: external tooling
// (and Swagger UI's own JS, running in the browser after the page auth
// check already happened) needs to fetch it without a session.
func TestOpenAPIDocumentIsUnauthenticated(t *testing.T) {
	srv, _ := newSmokeServer(t)

	resp, err := http.Get(srv.URL + "/api/v1/openapi.json")
	if err != nil {
		t.Fatalf("GET /api/v1/openapi.json (unauthenticated): %v", err)
	}
	_, _ = io.Copy(io.Discard, resp.Body)
	_ = resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("unauthenticated GET /api/v1/openapi.json status = %d, want %d", resp.StatusCode, http.StatusOK)
	}
}
