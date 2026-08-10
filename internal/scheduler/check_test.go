package scheduler

import (
	"context"
	"net/http"
	"testing"
	"time"

	"github.com/arnaudcharles/doupro/internal/registry"
	"github.com/arnaudcharles/doupro/internal/store"
)

func TestResolveVersionsAfterRollback(t *testing.T) {
	const (
		currentDigest = "sha256:old"
		remoteDigest  = "sha256:new"
	)
	ref := registry.ParseRef("actualbudget/actual-server:latest")
	cache := map[string]map[string]string{
		ref.Registry + "/" + ref.Repository: {
			"26.7.0": currentDigest,
			"26.8.0": remoteDigest,
		},
	}

	current, available, err := resolveVersions(
		context.Background(), http.DefaultClient, cache, ref,
		currentDigest, remoteDigest, true, versionHints{},
	)
	if err != nil {
		t.Fatal(err)
	}

	if current != "26.7.0" {
		t.Fatalf("current version = %q, want %q", current, "26.7.0")
	}
	if available != "26.8.0" {
		t.Fatalf("available version = %q, want %q", available, "26.8.0")
	}
}

func TestLoadVersionHintsUsesDurablePositiveAndNegativeCache(t *testing.T) {
	ctx := context.Background()
	st, err := store.Open(ctx, ":memory:")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = st.Close() })
	ref := registry.ParseRef("ghcr.io/team/app:latest")
	if err := st.PutImageVersion(ctx, store.ImageVersionRecord{Registry: ref.Registry, Repository: ref.Repository,
		Digest: "sha256:current", Version: "1.2.3", CheckedAt: time.Now()}); err != nil {
		t.Fatal(err)
	}
	if err := st.PutImageVersion(ctx, store.ImageVersionRecord{Registry: ref.Registry, Repository: ref.Repository,
		Digest: "sha256:remote", Status: "unresolved", CheckedAt: time.Now()}); err != nil {
		t.Fatal(err)
	}
	hints := loadVersionHints(ctx, st, ref, "missing-container", "sha256:current", "sha256:remote", true, "")
	if hints.current != "1.2.3" || hints.skipCurrent || hints.available != "" || !hints.skipAvailable {
		t.Fatalf("hints=%+v", hints)
	}
}

func TestResolveVersionsPrefersImmutableImageVersionLabel(t *testing.T) {
	ref := registry.ParseRef("ghcr.io/example/app:latest")
	current, available, err := resolveVersions(context.Background(), http.DefaultClient, map[string]map[string]string{}, ref,
		"sha256:old", "sha256:old", false, versionHints{current: "2.4.1"})
	if err != nil {
		t.Fatal(err)
	}
	if current != "2.4.1" || available != "2.4.1" {
		t.Fatalf("current=%q available=%q", current, available)
	}
}

func TestRegistryImageRefAfterRollback(t *testing.T) {
	const (
		rawDigest       = "sha256:247f7abff9f7097bbdab57df76fedd124d1e24a6ec4944fb5ef0ad128997ce05"
		canonicalDigest = "getgrav/grav@sha256:929cd343ecf32db4c04cd775bedb000f55e40d8c871c3f1dbadefc71cd68c044"
	)
	tests := []struct {
		name   string
		live   string
		stored string
		want   string
	}{
		{
			name:   "raw running digest uses stored compose reference",
			live:   rawDigest,
			stored: "actualbudget/actual-server:latest",
			want:   "actualbudget/actual-server:latest",
		},
		{
			name:   "canonical running digest uses stored compose reference",
			live:   canonicalDigest,
			stored: "getgrav/grav:latest",
			want:   "getgrav/grav:latest",
		},
		{
			name:   "normal live tag remains authoritative",
			live:   "actualbudget/actual-server:latest",
			stored: "actualbudget/actual-server:latest",
			want:   "actualbudget/actual-server:latest",
		},
		{
			name: "brand new digest-only container remains unchanged",
			live: rawDigest,
			want: rawDigest,
		},
		{
			name: "brand new canonical digest remains unchanged",
			live: canonicalDigest,
			want: canonicalDigest,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := registryImageRef(tt.live, tt.stored); got != tt.want {
				t.Fatalf("registryImageRef(%q, %q) = %q, want %q", tt.live, tt.stored, got, tt.want)
			}
		})
	}
}
