package api

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/arnaudcharles/doupro/internal/events"
	"github.com/arnaudcharles/doupro/internal/notifier"
	appsecrets "github.com/arnaudcharles/doupro/internal/secrets"
	"github.com/arnaudcharles/doupro/internal/store"
)

// TestHandleSetExclusionsEmitsSettingsChangedEvent guards a real gap: the
// handler used to persist exclusions without logging anything at all,
// unlike every other Settings section (registries, crash-loop). See
// docs/logs.md's settings.exclusions_changed entry.
func TestHandleSetExclusionsEmitsSettingsChangedEvent(t *testing.T) {
	st, err := store.Open(context.Background(), ":memory:")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = st.Close() })
	logger := events.New("error")
	logger.SetSink(st)

	req := httptest.NewRequest(http.MethodPatch, "/api/v1/settings/exclusions",
		strings.NewReader(`{"containers":["karakeep-meilisearch"],"stacks":[]}`))
	resp := httptest.NewRecorder()
	handleSetExclusions(st, logger)(resp, req)

	if resp.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200, body: %s", resp.Code, resp.Body.String())
	}

	logs, err := st.ListEvents(context.Background(), store.EventFilter{EventType: "settings.exclusions_changed"})
	if err != nil {
		t.Fatal(err)
	}
	if len(logs) != 1 {
		t.Fatalf("logged events = %d, want 1", len(logs))
	}
}

// TestHandleSetNotificationChannelsEmitsSettingsChangedEvent is the same
// guard for the Notifications channel list — see
// docs/logs.md's settings.notifications_changed entry.
func TestHandleSetNotificationChannelsEmitsSettingsChangedEvent(t *testing.T) {
	st, err := store.Open(context.Background(), ":memory:")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = st.Close() })
	cipher, err := appsecrets.New(make([]byte, 32))
	if err != nil {
		t.Fatal(err)
	}
	if err := st.ConfigureSecrets(context.Background(), cipher); err != nil {
		t.Fatal(err)
	}
	logger := events.New("error")
	logger.SetSink(st)
	notif := notifier.New(st, logger)

	req := httptest.NewRequest(http.MethodPatch, "/api/v1/settings/notifications",
		strings.NewReader(`[{"name":"ops","url":"generic+https://example.com/webhook"}]`))
	resp := httptest.NewRecorder()
	handleSetNotificationChannels(notif, logger)(resp, req)

	if resp.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200, body: %s", resp.Code, resp.Body.String())
	}

	logs, err := st.ListEvents(context.Background(), store.EventFilter{EventType: "settings.notifications_changed"})
	if err != nil {
		t.Fatal(err)
	}
	if len(logs) != 1 {
		t.Fatalf("logged events = %d, want 1", len(logs))
	}
	if !strings.Contains(logs[0].Message, "1 configured") {
		t.Fatalf("message = %q, want it to mention the channel count", logs[0].Message)
	}
}
