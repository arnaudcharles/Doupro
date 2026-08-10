package docker

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/docker/docker/api/types/container"
	dockerclient "github.com/docker/docker/client"

	"github.com/arnaudcharles/doupro/internal/registry"
)

func TestStackName(t *testing.T) {
	cases := []struct {
		name   string
		labels map[string]string
		want   string
	}{
		{"compose project wins", map[string]string{"com.docker.compose.project": "business", "doupro.stack": "other"}, "business"},
		{"falls back to doupro.stack", map[string]string{"doupro.stack": "personal"}, "personal"},
		{"neither label present", map[string]string{"unrelated": "x"}, ""},
		{"no labels at all", nil, ""},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := StackName(tc.labels); got != tc.want {
				t.Errorf("StackName(%v) = %q, want %q", tc.labels, got, tc.want)
			}
		})
	}
}

func TestStatusOf(t *testing.T) {
	noState := container.InspectResponse{ContainerJSONBase: &container.ContainerJSONBase{}}
	if got := statusOf(noState); got != "unknown" {
		t.Errorf("statusOf(nil State) = %q, want %q", got, "unknown")
	}
	info := container.InspectResponse{
		ContainerJSONBase: &container.ContainerJSONBase{
			State: &container.State{Status: "running"},
		},
	}
	if got := statusOf(info); got != "running" {
		t.Errorf("statusOf(running) = %q, want %q", got, "running")
	}
}

func TestParseRepoDigest(t *testing.T) {
	cases := []struct {
		in         string
		wantDigest string
		wantOK     bool
	}{
		{"nginx@sha256:abcd1234", "sha256:abcd1234", true},
		{"ghcr.io/org/app@sha256:deadbeef", "sha256:deadbeef", true},
		{"no-at-sign-here", "", false},
		{"", "", false},
	}
	for _, tc := range cases {
		digest, ok := parseRepoDigest(tc.in)
		if digest != tc.wantDigest || ok != tc.wantOK {
			t.Errorf("parseRepoDigest(%q) = (%q, %v), want (%q, %v)", tc.in, digest, ok, tc.wantDigest, tc.wantOK)
		}
	}
}

func TestPullAuth(t *testing.T) {
	t.Run("explicit registry config takes priority over Docker Hub credentials", func(t *testing.T) {
		if err := registry.Configure([]registry.Config{{Host: "registry.example.com", Username: "reg-user", Password: "reg-pass"}}); err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { _ = registry.Configure(nil) })

		ref := registry.Ref{Registry: "registry.example.com", Repository: "app", Tag: "latest"}
		user, pass := pullAuth(ref, "hub-user", "hub-pass")
		if user != "reg-user" || pass != "reg-pass" {
			t.Errorf("pullAuth = (%q, %q), want per-registry credentials (reg-user, reg-pass)", user, pass)
		}
	})

	t.Run("Docker Hub credentials only apply to Docker Hub", func(t *testing.T) {
		if err := registry.Configure(nil); err != nil {
			t.Fatal(err)
		}
		hub := registry.Ref{Registry: "registry-1.docker.io", Repository: "library/nginx", Tag: "latest"}
		user, pass := pullAuth(hub, "hub-user", "hub-pass")
		if user != "hub-user" || pass != "hub-pass" {
			t.Errorf("pullAuth(docker hub) = (%q, %q), want (hub-user, hub-pass)", user, pass)
		}

		other := registry.Ref{Registry: "ghcr.io", Repository: "org/app", Tag: "latest"}
		user, pass = pullAuth(other, "hub-user", "hub-pass")
		if user != "" || pass != "" {
			t.Errorf("pullAuth(non-Hub registry) = (%q, %q), want empty — Docker Hub credentials must never leak to another registry", user, pass)
		}
	})

	t.Run("no credentials configured at all", func(t *testing.T) {
		if err := registry.Configure(nil); err != nil {
			t.Fatal(err)
		}
		ref := registry.Ref{Registry: "registry-1.docker.io", Repository: "library/nginx", Tag: "latest"}
		user, pass := pullAuth(ref, "", "")
		if user != "" || pass != "" {
			t.Errorf("pullAuth with nothing configured = (%q, %q), want empty", user, pass)
		}
	})
}

func TestContainerFromSummary(t *testing.T) {
	cases := []struct {
		name string
		in   container.Summary
		want Container
	}{
		{
			name: "name trimmed of leading slash",
			in:   container.Summary{ID: "abc123", Names: []string{"/grav"}, Image: "getgrav/grav:latest", State: "running", Status: "Up 3 hours"},
			want: Container{ID: "abc123", Name: "grav", Image: "getgrav/grav:latest", State: "running", Status: "Up 3 hours", Stack: "", Excluded: false},
		},
		{
			name: "falls back to ID when Names is empty",
			in:   container.Summary{ID: "def456", State: "exited"},
			want: Container{ID: "def456", Name: "def456", State: "exited"},
		},
		{
			name: "doupro.enable=false marks Excluded",
			in:   container.Summary{ID: "ghi789", Names: []string{"/svc"}, Labels: map[string]string{"doupro.enable": "false"}},
			want: Container{ID: "ghi789", Name: "svc", Excluded: true, Labels: map[string]string{"doupro.enable": "false"}},
		},
		{
			name: "stack resolved from compose project label",
			in:   container.Summary{ID: "jkl012", Names: []string{"/db"}, Labels: map[string]string{"com.docker.compose.project": "business"}},
			want: Container{ID: "jkl012", Name: "db", Stack: "business", Labels: map[string]string{"com.docker.compose.project": "business"}},
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := containerFromSummary(tc.in)
			if got.ID != tc.want.ID || got.Name != tc.want.Name || got.Stack != tc.want.Stack || got.Excluded != tc.want.Excluded {
				t.Errorf("containerFromSummary(%+v) = %+v, want %+v", tc.in, got, tc.want)
			}
		})
	}
}

// fakeEngineClient wires a *Client at a real docker/docker/client to an
// httptest server standing in for the Docker Engine API — no real daemon
// needed to exercise the request/response plumbing List and Ping depend on.
func fakeEngineClient(t *testing.T, handler http.HandlerFunc) *Client {
	t.Helper()
	server := httptest.NewServer(handler)
	t.Cleanup(server.Close)

	host := "tcp://" + strings.TrimPrefix(server.URL, "http://")
	cli, err := dockerclient.NewClientWithOpts(
		dockerclient.WithHost(host),
		dockerclient.WithVersion("1.47"), // pin the API version so no /version negotiation call is needed
	)
	if err != nil {
		t.Fatalf("create fake docker client: %v", err)
	}
	return &Client{cli: cli, socketPath: "unix:///dev/null"}
}

func TestClientPing(t *testing.T) {
	client := fakeEngineClient(t, func(w http.ResponseWriter, r *http.Request) {
		if !strings.Contains(r.URL.Path, "_ping") {
			http.NotFound(w, r)
			return
		}
		w.Header().Set("Api-Version", "1.47")
		w.WriteHeader(http.StatusOK)
	})
	if err := client.Ping(context.Background()); err != nil {
		t.Fatalf("Ping() = %v, want nil", err)
	}
}

func TestClientList(t *testing.T) {
	fixture := []container.Summary{
		{
			ID: "abc123", Names: []string{"/grav"}, Image: "getgrav/grav:latest", ImageID: "sha256:deadbeef",
			State: "running", Status: "Up 3 hours",
			Labels: map[string]string{"com.docker.compose.project": "business"},
		},
		{
			ID: "def456", Names: []string{"/idle"}, Image: "alpine:latest",
			State: "exited", Status: "Exited (0) 2 days ago",
			Labels: map[string]string{"doupro.enable": "false"},
		},
	}

	client := fakeEngineClient(t, func(w http.ResponseWriter, r *http.Request) {
		if !strings.Contains(r.URL.Path, "/containers/json") {
			http.NotFound(w, r)
			return
		}
		if r.URL.Query().Get("all") != "1" {
			t.Errorf("List() did not request all=1 (stopped containers would be missed): got query %q", r.URL.RawQuery)
		}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(fixture)
	})

	got, err := client.List(context.Background())
	if err != nil {
		t.Fatalf("List() error = %v", err)
	}
	if len(got) != 2 {
		t.Fatalf("List() returned %d containers, want 2", len(got))
	}
	if got[0].Name != "grav" || got[0].Stack != "business" || got[0].Excluded {
		t.Errorf("first container = %+v, want name=grav stack=business excluded=false", got[0])
	}
	if got[1].Name != "idle" || !got[1].Excluded {
		t.Errorf("second container = %+v, want name=idle excluded=true", got[1])
	}
}
