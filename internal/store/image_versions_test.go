package store

import (
	"context"
	"testing"
	"time"
)

func TestImageVersionCacheStoresResolvedAndUnresolved(t *testing.T) {
	st, err := Open(context.Background(), ":memory:")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = st.Close() })
	now := time.Now().UTC().Truncate(time.Second)
	for _, rec := range []ImageVersionRecord{
		{Registry: "registry.example", Repository: "team/app", Digest: "sha256:one", Version: "1.2.3", CheckedAt: now},
		{Registry: "registry.example", Repository: "team/app", Digest: "sha256:two", CheckedAt: now},
	} {
		if err := st.PutImageVersion(context.Background(), rec); err != nil {
			t.Fatal(err)
		}
	}
	resolved, ok, err := st.GetImageVersion(context.Background(), "registry.example", "team/app", "sha256:one")
	if err != nil || !ok || resolved.Status != "resolved" || resolved.Version != "1.2.3" {
		t.Fatalf("resolved=%+v ok=%v err=%v", resolved, ok, err)
	}
	unresolved, ok, err := st.GetImageVersion(context.Background(), "registry.example", "team/app", "sha256:two")
	if err != nil || !ok || unresolved.Status != "unresolved" || unresolved.Version != "" {
		t.Fatalf("unresolved=%+v ok=%v err=%v", unresolved, ok, err)
	}
}
