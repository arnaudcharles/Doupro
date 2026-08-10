package cliclient

import (
	"context"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"testing"
)

// shortTempDir returns a short-lived temp directory outside t.TempDir()'s
// deeply nested default location — a Unix socket path is limited to ~104
// bytes (sockaddr_un) on macOS, and t.TempDir()'s path plus a filename
// routinely exceeds that, failing Listen with "invalid argument" for
// reasons that have nothing to do with what these tests actually check.
func shortTempDir(t *testing.T) string {
	t.Helper()
	dir, err := os.MkdirTemp("", "duc")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(dir) })
	return dir
}

func TestLocalSocketReachable(t *testing.T) {
	if got := LocalSocketReachable(""); got {
		t.Error("empty path reported reachable")
	}

	dir := shortTempDir(t)
	missing := filepath.Join(dir, "does-not-exist.sock")
	if got := LocalSocketReachable(missing); got {
		t.Error("nonexistent path reported reachable")
	}

	regularFile := filepath.Join(dir, "not-a-socket")
	if err := os.WriteFile(regularFile, []byte("x"), 0o600); err != nil {
		t.Fatal(err)
	}
	if got := LocalSocketReachable(regularFile); got {
		t.Error("regular file reported reachable as a socket")
	}

	socketPath := filepath.Join(dir, "doupro.sock")
	listener, err := net.Listen("unix", socketPath)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = listener.Close() }()
	if got := LocalSocketReachable(socketPath); !got {
		t.Error("a real Unix socket was not reported reachable")
	}
}

func TestNewLocalDialsTheGivenSocket(t *testing.T) {
	dir := shortTempDir(t)
	socketPath := filepath.Join(dir, "doupro.sock")
	listener, err := net.Listen("unix", socketPath)
	if err != nil {
		t.Fatal(err)
	}

	mux := http.NewServeMux()
	mux.HandleFunc("/api/v1/ping", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"ok":true}`))
	})
	srv := &http.Server{Handler: mux}
	go func() { _ = srv.Serve(listener) }()
	defer func() { _ = srv.Close() }()

	c := NewLocal(socketPath)
	var result struct {
		OK bool `json:"ok"`
	}
	if err := c.do(context.Background(), "GET", "/api/v1/ping", nil, nil, &result); err != nil {
		t.Fatalf("do() over local socket = %v", err)
	}
	if !result.OK {
		t.Fatal("did not reach the handler behind the socket")
	}
}
