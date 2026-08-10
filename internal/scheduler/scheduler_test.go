package scheduler

import (
	"context"
	"testing"
	"time"

	"github.com/arnaudcharles/doupro/internal/store"
)

func TestRelativeRetryReadyThrottlesFailures(t *testing.T) {
	now := time.Now()
	if !relativeRetryReady(nil, 30*time.Minute, now) {
		t.Fatal("never-run policy should be ready")
	}
	recent := now.Add(-time.Minute)
	if relativeRetryReady(&recent, 30*time.Minute, now) {
		t.Fatal("recent failure should be throttled")
	}
	old := now.Add(-31 * time.Minute)
	if !relativeRetryReady(&old, 30*time.Minute, now) {
		t.Fatal("old failure should be ready")
	}
}

func TestSelfUpdateBlockersIncludesFiveMinuteHorizon(t *testing.T) {
	ctx := context.Background()
	st, err := store.Open(ctx, ":memory:")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = st.Close() })
	due := time.Now().UTC().Add(4 * time.Minute)
	if _, err := st.CreateSchedule(ctx, store.Schedule{Kind: "once", ContainerName: "demo", RunAt: &due, NextRunAt: &due, Enabled: true}); err != nil {
		t.Fatal(err)
	}
	s := New(nil, st, nil, nil, nil, time.Minute)
	blockers, err := s.SelfUpdateBlockers(ctx, time.Now().UTC().Add(5*time.Minute))
	if err != nil {
		t.Fatal(err)
	}
	if len(blockers) != 1 {
		t.Fatalf("blockers=%v, want one", blockers)
	}
}

func TestSelfUpdateBlockersIgnoresLaterAndDisabledSchedules(t *testing.T) {
	ctx := context.Background()
	st, err := store.Open(ctx, ":memory:")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = st.Close() })
	later := time.Now().UTC().Add(6 * time.Minute)
	soon := time.Now().UTC().Add(time.Minute)
	_, _ = st.CreateSchedule(ctx, store.Schedule{Kind: "once", ContainerName: "later", RunAt: &later, NextRunAt: &later, Enabled: true})
	_, _ = st.CreateSchedule(ctx, store.Schedule{Kind: "once", ContainerName: "disabled", RunAt: &soon, NextRunAt: &soon, Enabled: false})
	s := New(nil, st, nil, nil, nil, time.Minute)
	blockers, err := s.SelfUpdateBlockers(ctx, time.Now().UTC().Add(5*time.Minute))
	if err != nil {
		t.Fatal(err)
	}
	if len(blockers) != 0 {
		t.Fatalf("blockers=%v, want none", blockers)
	}
}
