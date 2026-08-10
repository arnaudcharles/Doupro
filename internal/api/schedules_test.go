package api

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/arnaudcharles/doupro/internal/notifier"
	"github.com/arnaudcharles/doupro/internal/store"
)

func TestUpdateCompletedOnceScheduleIsRejected(t *testing.T) {
	st, err := store.Open(context.Background(), ":memory:")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = st.Close() })
	runAt := time.Now().Add(-time.Minute)
	id, err := st.CreateSchedule(context.Background(), store.Schedule{
		Kind: "once", ContainerName: "demo", RunAt: &runAt, Enabled: true,
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := st.MarkScheduleRun(context.Background(), id, nil); err != nil {
		t.Fatal(err)
	}

	req := httptest.NewRequest(http.MethodPatch, "/api/v1/schedules/1", strings.NewReader(`{"enabled":true}`))
	req.SetPathValue("id", "1")
	resp := httptest.NewRecorder()
	handleUpdateSchedule(st, notifier.New(st)).ServeHTTP(resp, req)
	if resp.Code != http.StatusConflict {
		t.Fatalf("status = %d, want %d; body=%s", resp.Code, http.StatusConflict, resp.Body.String())
	}
	if !strings.Contains(resp.Body.String(), `"code":"schedule_completed"`) {
		t.Fatalf("unexpected body: %s", resp.Body.String())
	}
}

func TestPinImageDoesNotParseAnExistingDigestAsATag(t *testing.T) {
	const pinned = "nginx@sha256:c7a6ad68be85142c7fe1089e48faa1e7c7166a194caa9180ddea66345876b9d2"
	if got := pinImage(context.Background(), pinned); got != pinned {
		t.Fatalf("pinImage(%q) = %q, want unchanged digest", pinned, got)
	}
}

func TestCreateDelayedScheduleValidatesDurationAndSemver(t *testing.T) {
	st, err := store.Open(context.Background(), ":memory:")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = st.Close() })
	notif := notifier.New(st)

	bad := httptest.NewRequest(http.MethodPost, "/api/v1/schedules", strings.NewReader(`{"type":"relative","container":"demo","policy":"delayed","after":"tomorrow","semver":"patch"}`))
	badResp := httptest.NewRecorder()
	handleCreateSchedule(st, notif).ServeHTTP(badResp, bad)
	if badResp.Code != http.StatusBadRequest {
		t.Fatalf("bad duration status=%d body=%s", badResp.Code, badResp.Body.String())
	}

	good := httptest.NewRequest(http.MethodPost, "/api/v1/schedules", strings.NewReader(`{"type":"relative","container":"demo","policy":"delayed","after":"24h","semver":"minor"}`))
	goodResp := httptest.NewRecorder()
	handleCreateSchedule(st, notif).ServeHTTP(goodResp, good)
	if goodResp.Code != http.StatusCreated {
		t.Fatalf("status=%d body=%s", goodResp.Code, goodResp.Body.String())
	}
	schedules, err := st.ListSchedules(context.Background())
	if err != nil || len(schedules) != 1 || schedules[0].SemverPolicy != "minor" || schedules[0].RelativeAfter != "24h" {
		t.Fatalf("schedules=%+v err=%v", schedules, err)
	}
}
