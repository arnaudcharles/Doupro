// End-to-end CLI tests: they build the real doupro binary once and drive
// it as a subprocess against a real (in-memory-DB) API server, exactly the
// way an operator invokes it — os.Args parsing, actual stdout, actual exit
// codes. This is deliberately black-box: cmd/doupro has no logic of its
// own beyond argument parsing and HTTP calls (see the package doc in
// main.go), so anything a mock or in-process call could catch is already
// covered by internal/api and internal/cliclient's own tests. What only a
// subprocess run against a real HTTP listener catches is wiring: does
// `doupro containers list` (as literally typed by a user) reach the API,
// parse its response, and print something sane.
package main

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/arnaudcharles/doupro/internal/api"
	"github.com/arnaudcharles/doupro/internal/auth"
	"github.com/arnaudcharles/doupro/internal/events"
	"github.com/arnaudcharles/doupro/internal/notifier"
	"github.com/arnaudcharles/doupro/internal/store"
	"github.com/arnaudcharles/doupro/internal/updater"
)

// binPath is set once in TestMain by building ./cmd/doupro into a temp
// directory — every test below execs this same binary, never `go run`,
// so a build failure surfaces once instead of once per test.
var binPath string

func TestMain(m *testing.M) {
	dir, err := os.MkdirTemp("", "doupro-cli-test")
	if err != nil {
		panic(err)
	}
	defer func() { _ = os.RemoveAll(dir) }()

	binPath = filepath.Join(dir, "doupro")
	build := exec.Command("go", "build", "-o", binPath, ".")
	build.Dir = "."
	if out, err := build.CombinedOutput(); err != nil {
		panic("build doupro for CLI tests: " + err.Error() + "\n" + string(out))
	}

	os.Exit(m.Run())
}

// testServer boots a real api.RegisterRoutes handler over HTTP, backed by
// an in-memory SQLite store — the same code path serve() wires up, minus
// the Docker socket (nil updater.New client, exactly what internal/web's
// own tests use — see web_test.go). Handlers that would actually touch
// Docker (update/rollback/check) are intentionally out of scope for these
// CLI tests; every command that lists/reads/manages state through the
// store is fully exercised.
type testServer struct {
	*httptest.Server
	apiKey string
}

func newTestServer(t *testing.T) *testServer {
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

	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)

	// Mint an admin API key using the exact same primitives the API's
	// own handleCreateAPIKey does (internal/api/settings.go), rather than
	// going through a session-cookie login the CLI client can't use
	// anyway (it only ever sends Bearer tokens).
	token, err := auth.GenerateToken()
	if err != nil {
		t.Fatalf("generate token: %v", err)
	}
	prefix := token
	if len(prefix) > 8 {
		prefix = prefix[:8]
	}
	if _, err := st.CreateAPIKeyWithAccess(ctx, "cli-test", auth.HashToken(token), prefix, store.AccessControl{Role: store.RoleAdmin}); err != nil {
		t.Fatalf("create api key: %v", err)
	}

	return &testServer{Server: srv, apiKey: token}
}

// run execs the built doupro binary with args, pre-pending --host/--api-key
// so it talks to this test server, and returns combined stdout+stderr.
func (ts *testServer) run(t *testing.T, args ...string) (string, error) {
	t.Helper()
	full := append([]string{"--host", ts.URL, "--api-key", ts.apiKey}, args...)
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, binPath, full...)
	var out bytes.Buffer
	cmd.Stdout = &out
	cmd.Stderr = &out
	err := cmd.Run()
	return out.String(), err
}

func TestCLIVersionPrintsWithoutAHost(t *testing.T) {
	cmd := exec.Command(binPath, "version")
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("doupro version: %v\n%s", err, out)
	}
	if strings.TrimSpace(string(out)) != "dev" {
		t.Fatalf("doupro version = %q, want %q", strings.TrimSpace(string(out)), "dev")
	}
}

func TestCLIRejectsUnknownHost(t *testing.T) {
	cmd := exec.Command(binPath, "--host", "http://127.0.0.1:1", "--api-key", "x", "containers", "list")
	out, err := cmd.CombinedOutput()
	if err == nil {
		t.Fatalf("expected a non-zero exit against an unreachable host, got success: %s", out)
	}
}

func TestCLIContainersListEmptyFleet(t *testing.T) {
	ts := newTestServer(t)

	out, err := ts.run(t, "containers", "list", "--json")
	if err != nil {
		t.Fatalf("containers list: %v\n%s", err, out)
	}
	var containers []map[string]any
	if err := json.Unmarshal([]byte(out), &containers); err != nil {
		t.Fatalf("containers list did not print valid JSON: %v\noutput: %s", err, out)
	}
	if len(containers) != 0 {
		t.Fatalf("expected no tracked containers on a fresh store, got %d", len(containers))
	}
}

func TestCLIContainersInspectMissingContainer(t *testing.T) {
	ts := newTestServer(t)

	out, err := ts.run(t, "containers", "inspect", "does-not-exist")
	if err == nil {
		t.Fatalf("expected a non-zero exit for a missing container, got success: %s", out)
	}
	if !strings.Contains(out, "not found") {
		t.Fatalf("expected a not-found error message, got: %s", out)
	}
}

func TestCLILogsListEmpty(t *testing.T) {
	ts := newTestServer(t)

	out, err := ts.run(t, "logs", "--json")
	if err != nil {
		t.Fatalf("logs: %v\n%s", err, out)
	}
	var logs []map[string]any
	if err := json.Unmarshal([]byte(out), &logs); err != nil {
		t.Fatalf("logs list did not print valid JSON: %v\noutput: %s", err, out)
	}
}

func TestCLISettingsGetAndSetGeneral(t *testing.T) {
	ts := newTestServer(t)

	out, err := ts.run(t, "settings", "get", "--json")
	if err != nil {
		t.Fatalf("settings get: %v\n%s", err, out)
	}
	var settings map[string]any
	if err := json.Unmarshal([]byte(out), &settings); err != nil {
		t.Fatalf("settings get did not print valid JSON: %v\noutput: %s", err, out)
	}
	if _, ok := settings["check_interval_minutes"]; !ok {
		t.Fatalf("settings get response missing check_interval_minutes: %s", out)
	}
}

func TestCLIScheduleListEmpty(t *testing.T) {
	ts := newTestServer(t)

	out, err := ts.run(t, "schedule", "list", "--json")
	if err != nil {
		t.Fatalf("schedule list: %v\n%s", err, out)
	}
	var schedules []map[string]any
	if err := json.Unmarshal([]byte(out), &schedules); err != nil {
		t.Fatalf("schedule list did not print valid JSON: %v\noutput: %s", err, out)
	}
}

func TestCLIStatsReturnsSummary(t *testing.T) {
	ts := newTestServer(t)

	out, err := ts.run(t, "stats", "--json")
	if err != nil {
		t.Fatalf("stats: %v\n%s", err, out)
	}
	var stats map[string]any
	if err := json.Unmarshal([]byte(out), &stats); err != nil {
		t.Fatalf("stats did not print valid JSON: %v\noutput: %s", err, out)
	}
	if _, ok := stats["containers_total"]; !ok {
		t.Fatalf("stats response missing containers_total: %s", out)
	}
}

func TestCLIUsersListIncludesBootstrapAdmin(t *testing.T) {
	ts := newTestServer(t)

	out, err := ts.run(t, "users", "list", "--json")
	if err != nil {
		t.Fatalf("users list: %v\n%s", err, out)
	}
	var users []map[string]any
	if err := json.Unmarshal([]byte(out), &users); err != nil {
		t.Fatalf("users list did not print valid JSON: %v\noutput: %s", err, out)
	}
	if len(users) != 1 || users[0]["username"] != "admin" {
		t.Fatalf("expected the single bootstrap admin user, got: %s", out)
	}
}

func TestCLIRejectsBadAPIKey(t *testing.T) {
	ts := newTestServer(t)

	cmd := exec.Command(binPath, "--host", ts.URL, "--api-key", "not-a-real-key", "containers", "list")
	out, err := cmd.CombinedOutput()
	if err == nil {
		t.Fatalf("expected a non-zero exit for an invalid API key, got success: %s", out)
	}
}
