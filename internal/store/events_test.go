package store

import (
	"context"
	"testing"

	"github.com/arnaudcharles/doupro/internal/events"
)

func TestListEventsFiltersContainerAndEventBySubstringCaseInsensitive(t *testing.T) {
	ctx := context.Background()
	st, err := Open(ctx, ":memory:")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = st.Close() })

	seed := []events.Event{
		{Level: events.LevelInfo, Type: "update.succeeded", Container: "unifi-os-server", Actor: events.ActorSystem, Message: "ok"},
		{Level: events.LevelError, Type: "update.failed", Container: "unifi-os-server", Actor: events.ActorSystem, Message: "boom"},
		{Level: events.LevelInfo, Type: "rollback.manual", Container: "traefik", Actor: events.ActorUser, Message: "ok"},
	}
	for _, e := range seed {
		if err := st.InsertEvent(ctx, e); err != nil {
			t.Fatal(err)
		}
	}

	cases := []struct {
		name   string
		filter EventFilter
		want   int
	}{
		{"container substring", EventFilter{Container: "unifi"}, 2},
		{"container case-insensitive", EventFilter{Container: "UNIFI-OS"}, 2},
		{"event type substring", EventFilter{EventType: "failed"}, 1},
		{"event type case-insensitive", EventFilter{EventType: "UPDATE."}, 2},
		{"no match", EventFilter{Container: "does-not-exist"}, 0},
		{"literal underscore not a wildcard", EventFilter{Container: "unifi_os"}, 0},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got, err := st.ListEvents(ctx, tc.filter)
			if err != nil {
				t.Fatal(err)
			}
			if len(got) != tc.want {
				t.Fatalf("len(got)=%d, want %d (%+v)", len(got), tc.want, got)
			}
		})
	}
}
