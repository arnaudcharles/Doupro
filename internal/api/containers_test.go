package api

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/arnaudcharles/doupro/internal/events"
	"github.com/arnaudcharles/doupro/internal/notifier"
	"github.com/arnaudcharles/doupro/internal/store"
	"github.com/arnaudcharles/doupro/internal/updater"
)

func TestOperationContextSurvivesRequestCancellation(t *testing.T) {
	requestCtx, cancelRequest := context.WithCancel(context.Background())
	opCtx, cancelOperation := operationContext(requestCtx)
	t.Cleanup(cancelOperation)

	cancelRequest()
	if err := requestCtx.Err(); err == nil {
		t.Fatal("request context was not canceled")
	}
	if err := opCtx.Err(); err != nil {
		t.Fatalf("operation context inherited client cancellation: %v", err)
	}
	deadline, ok := opCtx.Deadline()
	if !ok || time.Until(deadline) <= 0 {
		t.Fatal("operation context has no bounded deadline")
	}
}

func TestRespondActionUsesStableConflictCodeForContainerLock(t *testing.T) {
	req := httptest.NewRequest(http.MethodPost, "/api/v1/containers/demo/update", nil)
	req.Header.Set("Accept", "application/json")
	resp := httptest.NewRecorder()
	respondAction(resp, req, &updater.OperationInProgressError{
		Container: "demo",
		Current:   updater.Operation{Kind: "rollback", StartedAt: time.Now()},
	}, "update_failed")
	if resp.Code != http.StatusConflict {
		t.Fatalf("status = %d, want %d", resp.Code, http.StatusConflict)
	}
	if !strings.Contains(resp.Body.String(), `"code":"operation_in_progress"`) {
		t.Fatalf("unexpected response: %s", resp.Body.String())
	}
}

func TestSelectBulkUpdateTargets(t *testing.T) {
	records := []store.ContainerRecord{
		{Name: "a", Stack: "business", UpdateAvailable: true},
		{Name: "b", Stack: "business", UpdateAvailable: false},
		{Name: "c", Stack: "business", UpdateAvailable: true, Excluded: true},
		{Name: "d", Stack: "personal", UpdateAvailable: true},
	}

	all := selectBulkUpdateTargets(records, "")
	if got := names(all); !equalSets(got, []string{"a", "d"}) {
		t.Fatalf("no-stack filter = %v, want [a d]", got)
	}

	scoped := selectBulkUpdateTargets(records, "business")
	if got := names(scoped); !equalSets(got, []string{"a"}) {
		t.Fatalf("business-stack filter = %v, want [a]", got)
	}

	if got := selectBulkUpdateTargets(records, "nonexistent"); len(got) != 0 {
		t.Fatalf("unknown-stack filter = %v, want none", got)
	}
}

func newTestStoreAndUpdater(t *testing.T) (*store.Store, *updater.Updater) {
	t.Helper()
	st, err := store.Open(context.Background(), ":memory:")
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	t.Cleanup(func() { _ = st.Close() })
	logger := events.New("error")
	upd := updater.New(nil, st, logger, notifier.New(st, logger))
	return st, upd
}

func TestHandleGetContainerReturnsDetailForKnownContainer(t *testing.T) {
	st, upd := newTestStoreAndUpdater(t)
	if err := st.UpsertSeen(context.Background(), store.ContainerRecord{
		ID: "abc123", Name: "grav", Stack: "business", State: "running", CurrentImage: "getgrav/grav:latest",
	}); err != nil {
		t.Fatal(err)
	}

	req := httptest.NewRequest(http.MethodGet, "/api/v1/containers/grav", nil)
	req.SetPathValue("id", "grav")
	resp := httptest.NewRecorder()
	handleGetContainer(st, upd)(resp, req)

	if resp.Code != http.StatusOK {
		t.Fatalf("status = %d, want %d, body: %s", resp.Code, http.StatusOK, resp.Body.String())
	}
	if !strings.Contains(resp.Body.String(), `"name":"grav"`) {
		t.Fatalf("unexpected response: %s", resp.Body.String())
	}
}

func TestHandleGetContainerNotFound(t *testing.T) {
	st, upd := newTestStoreAndUpdater(t)

	req := httptest.NewRequest(http.MethodGet, "/api/v1/containers/nope", nil)
	req.SetPathValue("id", "nope")
	resp := httptest.NewRecorder()
	handleGetContainer(st, upd)(resp, req)

	if resp.Code != http.StatusNotFound {
		t.Fatalf("status = %d, want %d", resp.Code, http.StatusNotFound)
	}
}

func TestHandleCheckContainerNotFound(t *testing.T) {
	st, _ := newTestStoreAndUpdater(t)
	logger := events.New("error")
	notif := notifier.New(st, logger)

	req := httptest.NewRequest(http.MethodPost, "/api/v1/containers/nope/check", nil)
	req.SetPathValue("id", "nope")
	resp := httptest.NewRecorder()
	// A nil *docker.Client is safe here: CheckOneContainerNow resolves
	// store.ErrNotFound from st.GetContainerByName before it ever touches
	// the Docker client.
	handleCheckContainer(st, nil, logger, notif)(resp, req)

	if resp.Code != http.StatusNotFound {
		t.Fatalf("status = %d, want %d, body: %s", resp.Code, http.StatusNotFound, resp.Body.String())
	}
}

func names(records []store.ContainerRecord) []string {
	out := make([]string, len(records))
	for i, r := range records {
		out[i] = r.Name
	}
	return out
}

func TestHandlePreviewUpdateResolvesCandidateWithoutMutatingAnything(t *testing.T) {
	st, upd := newTestStoreAndUpdater(t)
	ctx := context.Background()
	if err := st.UpsertSeen(ctx, store.ContainerRecord{
		ID: "abc123", Name: "grav", CurrentImage: "getgrav/grav:latest", CurrentVersion: "1.2.3",
	}); err != nil {
		t.Fatal(err)
	}
	if err := st.SetUpdateAvailable(ctx, "abc123", true); err != nil {
		t.Fatal(err)
	}
	if err := st.ReplaceUpdateCandidates(ctx, "abc123", []store.UpdateCandidate{
		{ContainerID: "abc123", Scope: "major", Image: "getgrav/grav@sha256:deadbeef", Version: "1.3.0"},
	}, time.Now()); err != nil {
		t.Fatal(err)
	}

	req := httptest.NewRequest(http.MethodGet, "/api/v1/containers/grav/update", nil)
	req.SetPathValue("id", "grav")
	resp := httptest.NewRecorder()
	handlePreviewUpdate(st)(resp, req)

	if resp.Code != http.StatusOK {
		t.Fatalf("status = %d, want %d, body: %s", resp.Code, http.StatusOK, resp.Body.String())
	}
	body := resp.Body.String()
	for _, want := range []string{`"target_image":"getgrav/grav@sha256:deadbeef"`, `"target_version":"1.3.0"`, `"semver_scope":"major"`, `"update_available":true`} {
		if !strings.Contains(body, want) {
			t.Errorf("response missing %s: %s", want, body)
		}
	}

	// A preview must never touch Docker or mutate stored state — the
	// container record and its candidates must be exactly as seeded.
	rec, err := st.GetContainer(ctx, "abc123")
	if err != nil {
		t.Fatal(err)
	}
	if rec.CurrentImage != "getgrav/grav:latest" {
		t.Errorf("preview mutated current_image: %q", rec.CurrentImage)
	}
	if _, inProgress := upd.Operation("grav"); inProgress {
		t.Error("preview started a real operation")
	}
}

func TestHandlePreviewUpdateDoesNotShowARawFloatingTagAsATargetVersion(t *testing.T) {
	st, _ := newTestStoreAndUpdater(t)
	ctx := context.Background()
	if err := st.UpsertSeen(ctx, store.ContainerRecord{
		ID: "abc123", Name: "grav", CurrentImage: "getgrav/grav:latest", CurrentVersion: "1.7",
	}); err != nil {
		t.Fatal(err)
	}
	if err := st.SetUpdateAvailable(ctx, "abc123", true); err != nil {
		t.Fatal(err)
	}
	// checkOneContainer stamps every candidate's Version with the raw
	// configured tag as a placeholder, even for a floating tag — "latest"
	// here is not a real version and must not be shown as the resolved
	// target version (see the bug this caught live on the homelab: a
	// preview literally said "1.7 -> latest").
	if err := st.ReplaceUpdateCandidates(ctx, "abc123", []store.UpdateCandidate{
		{ContainerID: "abc123", Scope: "major", Image: "getgrav/grav:latest", Version: "latest"},
	}, time.Now()); err != nil {
		t.Fatal(err)
	}

	req := httptest.NewRequest(http.MethodGet, "/api/v1/containers/grav/update", nil)
	req.SetPathValue("id", "grav")
	resp := httptest.NewRecorder()
	handlePreviewUpdate(st)(resp, req)

	if strings.Contains(resp.Body.String(), `"target_version":"latest"`) {
		t.Fatalf("preview showed the raw floating tag as a target version: %s", resp.Body.String())
	}
}

func TestHandlePreviewUpdateRejectsBadSemverScope(t *testing.T) {
	st, _ := newTestStoreAndUpdater(t)
	if err := st.UpsertSeen(context.Background(), store.ContainerRecord{ID: "abc123", Name: "grav"}); err != nil {
		t.Fatal(err)
	}

	req := httptest.NewRequest(http.MethodGet, "/api/v1/containers/grav/update?semver=bogus", nil)
	req.SetPathValue("id", "grav")
	resp := httptest.NewRecorder()
	handlePreviewUpdate(st)(resp, req)

	if resp.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want %d", resp.Code, http.StatusBadRequest)
	}
}

func equalSets(got, want []string) bool {
	if len(got) != len(want) {
		return false
	}
	seen := make(map[string]bool, len(want))
	for _, w := range want {
		seen[w] = true
	}
	for _, g := range got {
		if !seen[g] {
			return false
		}
	}
	return true
}
