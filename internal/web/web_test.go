package web_test

import (
	"context"
	"io/fs"
	"net/http"
	"strings"
	"testing"

	doupro "github.com/arnaudcharles/doupro"
	"github.com/arnaudcharles/doupro/internal/events"
	"github.com/arnaudcharles/doupro/internal/notifier"
	"github.com/arnaudcharles/doupro/internal/store"
	"github.com/arnaudcharles/doupro/internal/updater"
	"github.com/arnaudcharles/doupro/internal/web"
)

func TestRegisterRoutesParsesEmbeddedTemplates(t *testing.T) {
	st, err := store.Open(context.Background(), ":memory:")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = st.Close() })

	mux := http.NewServeMux()
	web.RegisterRoutes(mux, st, updater.New(nil, st, events.New("error"), notifier.New(st)), notifier.New(st), "test", doupro.TemplatesFS, doupro.StaticFS, doupro.ManualsFS, false)
}

func TestNotificationProviderFieldToggleDoesNotHideSavedBadges(t *testing.T) {
	raw, err := fs.ReadFile(doupro.TemplatesFS, "web/templates/notifications.html")
	if err != nil {
		t.Fatal(err)
	}
	template := string(raw)
	if !strings.Contains(template, "querySelectorAll('#channel-form [data-provider]')") {
		t.Fatal("provider field toggle is not scoped to the add-channel form")
	}
	if strings.Contains(template, "querySelectorAll('[data-provider]')") {
		t.Fatal("global provider selector would hide saved non-Telegram badges")
	}
	for _, provider := range []string{"telegram", "ntfy", "slack", "discord", "gotify"} {
		if !strings.Contains(template, provider+":") {
			t.Fatalf("missing badge metadata for %s", provider)
		}
	}
}
