package store

import (
	"context"
	"testing"
	"time"
)

func TestUpdateCandidateKeepsFirstDetectionUntilTargetChanges(t *testing.T) {
	ctx := context.Background()
	st, err := Open(ctx, ":memory:")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = st.Close() })
	first := time.Now().UTC().Add(-time.Hour)
	if err := st.ReplaceUpdateCandidates(ctx, "id", []UpdateCandidate{{Scope: "patch", Image: "app:1.2.4", Version: "1.2.4"}}, first); err != nil {
		t.Fatal(err)
	}
	if err := st.ReplaceUpdateCandidates(ctx, "id", []UpdateCandidate{{Scope: "patch", Image: "app:1.2.4", Version: "1.2.4"}}, time.Now()); err != nil {
		t.Fatal(err)
	}
	candidate, err := st.GetUpdateCandidate(ctx, "id", "patch")
	if err != nil || !candidate.DetectedAt.Equal(first) {
		t.Fatalf("candidate=%+v err=%v", candidate, err)
	}
	changedAt := time.Now().UTC()
	if err := st.ReplaceUpdateCandidates(ctx, "id", []UpdateCandidate{{Scope: "patch", Image: "app:1.2.5", Version: "1.2.5"}}, changedAt); err != nil {
		t.Fatal(err)
	}
	candidate, _ = st.GetUpdateCandidate(ctx, "id", "patch")
	if !candidate.DetectedAt.Equal(changedAt) {
		t.Fatalf("changed candidate=%+v", candidate)
	}
	if err := st.ReplaceUpdateCandidates(ctx, "id", nil, time.Now()); err != nil {
		t.Fatal(err)
	}
	if _, err := st.GetUpdateCandidate(ctx, "id", "patch"); err == nil {
		t.Fatal("stale candidate was not removed")
	}
}
