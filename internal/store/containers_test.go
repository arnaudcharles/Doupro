package store

import (
	"context"
	"fmt"
	"sync"
	"testing"

	"github.com/arnaudcharles/doupro/internal/events"
)

func TestOpenSerializesConcurrentSQLiteWriters(t *testing.T) {
	ctx := context.Background()
	st, err := Open(ctx, ":memory:")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = st.Close() })
	if got := st.db.Stats().MaxOpenConnections; got != 1 {
		t.Fatalf("max sqlite connections=%d, want 1", got)
	}

	const writers = 24
	errs := make(chan error, writers)
	var wg sync.WaitGroup
	for i := 0; i < writers; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			errs <- st.UpsertSeen(ctx, ContainerRecord{
				ID:           fmt.Sprintf("container-%d", i),
				Name:         fmt.Sprintf("container-%d", i),
				CurrentImage: "nginx:latest",
			})
		}(i)
	}
	wg.Wait()
	close(errs)
	for err := range errs {
		if err != nil {
			t.Fatalf("concurrent write failed: %v", err)
		}
	}

	var timeout int
	if err := st.db.QueryRowContext(ctx, "PRAGMA busy_timeout;").Scan(&timeout); err != nil {
		t.Fatal(err)
	}
	if timeout != int(sqliteBusyTimeout.Milliseconds()) {
		t.Fatalf("busy timeout=%dms want %dms", timeout, sqliteBusyTimeout.Milliseconds())
	}
}

func TestUpsertSeenPreservesTagAcrossDigestForms(t *testing.T) {
	ctx := context.Background()
	st, err := Open(ctx, ":memory:")
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = st.Close() }()

	const id = "container-id"
	if err := st.UpsertSeen(ctx, ContainerRecord{ID: id, Name: "grav", CurrentImage: "getgrav/grav:latest"}); err != nil {
		t.Fatal(err)
	}
	for _, observed := range []string{"sha256:old", "getgrav/grav@sha256:old"} {
		if err := st.UpsertSeen(ctx, ContainerRecord{ID: id, Name: "grav", CurrentImage: observed}); err != nil {
			t.Fatal(err)
		}
		rec, err := st.GetContainer(ctx, id)
		if err != nil {
			t.Fatal(err)
		}
		if rec.CurrentImage != "getgrav/grav:latest" {
			t.Fatalf("current image after observing %q = %q, want stable tag", observed, rec.CurrentImage)
		}
	}
}

func TestRecoverImageReferenceAndRepairLegacyRow(t *testing.T) {
	ctx := context.Background()
	st, err := Open(ctx, ":memory:")
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = st.Close() }()

	const id = "container-id"
	if err := st.UpsertSeen(ctx, ContainerRecord{ID: id, Name: "grav", CurrentImage: "getgrav/grav@sha256:new"}); err != nil {
		t.Fatal(err)
	}
	if err := st.InsertEvent(ctx, events.Event{
		Type: "rollback.manual", Container: "grav",
		FromVersion: "getgrav/grav:latest", ToVersion: "sha256:old",
	}); err != nil {
		t.Fatal(err)
	}
	if err := st.InsertEvent(ctx, events.Event{
		Type: "update.succeeded", Container: "grav",
		FromVersion: "sha256:old", ToVersion: "getgrav/grav@sha256:new",
	}); err != nil {
		t.Fatal(err)
	}

	recovered, err := st.RecoverImageReference(ctx, "grav")
	if err != nil {
		t.Fatal(err)
	}
	if recovered != "getgrav/grav:latest" {
		t.Fatalf("recovered image = %q, want %q", recovered, "getgrav/grav:latest")
	}
	if err := st.RepairCurrentImage(ctx, id, recovered); err != nil {
		t.Fatal(err)
	}
	rec, err := st.GetContainer(ctx, id)
	if err != nil {
		t.Fatal(err)
	}
	if rec.CurrentImage != recovered {
		t.Fatalf("current image = %q, want %q", rec.CurrentImage, recovered)
	}
}

// TestUpsertSeenPersistsExcluded guards against a regression where
// UpsertSeen's INSERT/ON CONFLICT UPDATE statement omitted the excluded
// column entirely — checkForUpdates computed the flag correctly in
// memory (and correctly skipped registry checks for it), but the value
// was silently dropped before it ever reached the containers table, so
// every container's persisted excluded flag stayed false forever
// regardless of Settings -> Exclusions.
func TestUpsertSeenPersistsExcluded(t *testing.T) {
	ctx := context.Background()
	st, err := Open(ctx, ":memory:")
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = st.Close() }()

	const id = "container-id"
	if err := st.UpsertSeen(ctx, ContainerRecord{ID: id, Name: "karakeep-meilisearch", CurrentImage: "getmeili/meilisearch:v1.11.3", Excluded: true}); err != nil {
		t.Fatal(err)
	}
	rec, err := st.GetContainer(ctx, id)
	if err != nil {
		t.Fatal(err)
	}
	if !rec.Excluded {
		t.Fatal("expected Excluded to be persisted as true, got false")
	}

	// A later UpsertSeen with Excluded: false (e.g. after the exclusion
	// is removed) must flip it back, not leave the stale true behind.
	if err := st.UpsertSeen(ctx, ContainerRecord{ID: id, Name: "karakeep-meilisearch", CurrentImage: "getmeili/meilisearch:v1.11.3", Excluded: false}); err != nil {
		t.Fatal(err)
	}
	rec, err = st.GetContainer(ctx, id)
	if err != nil {
		t.Fatal(err)
	}
	if rec.Excluded {
		t.Fatal("expected Excluded to be persisted as false after re-upsert, got true")
	}
}

// TestApplyExclusionsUpdatesAlreadyKnownContainersImmediately guards
// against Settings -> Exclusions only taking effect on the next periodic
// check (or a restart) instead of right away.
func TestApplyExclusionsUpdatesAlreadyKnownContainersImmediately(t *testing.T) {
	ctx := context.Background()
	st, err := Open(ctx, ":memory:")
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = st.Close() }()

	if err := st.UpsertSeen(ctx, ContainerRecord{ID: "a", Name: "karakeep-meilisearch", Stack: "personal", CurrentImage: "getmeili/meilisearch:v1.11.3"}); err != nil {
		t.Fatal(err)
	}
	if err := st.UpsertSeen(ctx, ContainerRecord{ID: "b", Name: "grav", Stack: "business", CurrentImage: "getgrav/grav:latest"}); err != nil {
		t.Fatal(err)
	}

	if err := st.ApplyExclusions(ctx, Exclusions{Containers: []string{"karakeep-meilisearch"}}); err != nil {
		t.Fatal(err)
	}

	a, err := st.GetContainer(ctx, "a")
	if err != nil {
		t.Fatal(err)
	}
	if !a.Excluded {
		t.Fatal("expected karakeep-meilisearch to be excluded immediately after ApplyExclusions")
	}
	b, err := st.GetContainer(ctx, "b")
	if err != nil {
		t.Fatal(err)
	}
	if b.Excluded {
		t.Fatal("expected grav to remain non-excluded")
	}

	// Removing the exclusion must flip it back immediately too.
	if err := st.ApplyExclusions(ctx, Exclusions{}); err != nil {
		t.Fatal(err)
	}
	a, err = st.GetContainer(ctx, "a")
	if err != nil {
		t.Fatal(err)
	}
	if a.Excluded {
		t.Fatal("expected karakeep-meilisearch to be un-excluded after clearing the exclusion list")
	}
}
