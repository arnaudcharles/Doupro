package store

import (
	"context"
	"path/filepath"
	"testing"
	"time"
)

func TestRollbackWatchIsDurableAndTriggersExactlyOnce(t *testing.T) {
	ctx := context.Background()
	path := filepath.Join(t.TempDir(), "doupro.db")
	st, err := Open(ctx, path)
	if err != nil {
		t.Fatal(err)
	}
	applied := time.Now().UTC().Add(-time.Minute)
	if err := st.UpsertSeen(ctx, ContainerRecord{ID: "old", Name: "demo", State: "running", CurrentImage: "app:latest"}); err != nil {
		t.Fatal(err)
	}
	if err := st.ReplaceContainerAndArmRollback(ctx, "old", ContainerRecord{
		ID: "new", Name: "demo", State: "running", CurrentImage: "app:latest", PreviousImage: "sha256:old",
	}, applied, 10*time.Minute, 3, 0); err != nil {
		t.Fatal(err)
	}
	if err := st.Close(); err != nil {
		t.Fatal(err)
	}

	st, err = Open(ctx, path)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = st.Close() })
	watch, err := st.GetRollbackWatch(ctx, "demo")
	if err != nil {
		t.Fatal(err)
	}
	if watch.ContainerID != "new" || watch.Threshold != 3 || watch.Status != "active" {
		t.Fatalf("persisted watch = %+v", watch)
	}

	if _, counted, triggered, err := st.RecordCrash(ctx, "demo", "stale-id", 1, applied.Add(time.Minute)); err != nil || counted || triggered {
		t.Fatalf("stale ID result counted=%v triggered=%v err=%v", counted, triggered, err)
	}
	for i := int64(1); i <= 3; i++ {
		got, counted, triggered, err := st.RecordCrash(ctx, "demo", "new", i, applied.Add(time.Duration(i)*time.Minute))
		if err != nil {
			t.Fatal(err)
		}
		if !counted || triggered != (i == 3) || got.CrashCount != int(i) {
			t.Fatalf("crash %d = watch=%+v counted=%v triggered=%v", i, got, counted, triggered)
		}
		if got.ObservedRestartCount != int(i) {
			t.Fatalf("crash %d observed restart count = %d, want %d", i, got.ObservedRestartCount, i)
		}
		if _, duplicateCounted, _, err := st.RecordCrash(ctx, "demo", "new", i, applied.Add(time.Duration(i)*time.Minute)); err != nil || duplicateCounted {
			t.Fatalf("duplicate %d counted=%v err=%v", i, duplicateCounted, err)
		}
	}
	if _, counted, triggered, err := st.RecordCrash(ctx, "demo", "new", 4, applied.Add(4*time.Minute)); err != nil || counted || triggered {
		t.Fatalf("post-trigger event counted=%v triggered=%v err=%v", counted, triggered, err)
	}
	if err := st.ReplaceContainerAndCompleteRollback(ctx, "new", ContainerRecord{ID: "rolled-back", Name: "demo", State: "running", CurrentImage: "app:latest", PreviousImage: "sha256:new"}, "automatic_rollback"); err != nil {
		t.Fatal(err)
	}
	watch, err = st.GetRollbackWatch(ctx, "demo")
	if err != nil || watch.Status != "automatic_rollback" {
		t.Fatalf("completed watch=%+v err=%v", watch, err)
	}
	if rec, err := st.GetContainer(ctx, "rolled-back"); err != nil || rec.Name != "demo" {
		t.Fatalf("rolled-back container=%+v err=%v", rec, err)
	}
}

func TestRollbackWatchExpiresDurably(t *testing.T) {
	ctx := context.Background()
	st, err := Open(ctx, ":memory:")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = st.Close() })
	applied := time.Now().UTC().Add(-20 * time.Minute)
	if err := st.ArmRollbackWatch(ctx, "demo", "id", applied, 10*time.Minute, 3, 0); err != nil {
		t.Fatal(err)
	}
	if affected, err := st.ExpireRollbackWatches(ctx, time.Now()); err != nil || affected != 1 {
		t.Fatalf("expire affected=%d err=%v", affected, err)
	}
	watch, err := st.GetRollbackWatch(ctx, "demo")
	if err != nil || watch.Status != "expired" {
		t.Fatalf("watch=%+v err=%v", watch, err)
	}
}

func TestCrashLoopSettingsDefaultsAndPersistence(t *testing.T) {
	ctx := context.Background()
	st, err := Open(ctx, ":memory:")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = st.Close() })
	threshold, window, err := st.GetCrashLoopSettings(ctx)
	if err != nil || threshold != 3 || window != 10 {
		t.Fatalf("defaults=(%d,%d) err=%v", threshold, window, err)
	}
	if err := st.SetCrashLoopSettings(ctx, 5, 30); err != nil {
		t.Fatal(err)
	}
	threshold, window, err = st.GetCrashLoopSettings(ctx)
	if err != nil || threshold != 5 || window != 30 {
		t.Fatalf("saved=(%d,%d) err=%v", threshold, window, err)
	}
}
