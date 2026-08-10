package notifier

import (
	"context"
	"errors"
	"testing"
	"time"

	appsecrets "github.com/arnaudcharles/doupro/internal/secrets"
	"github.com/arnaudcharles/doupro/internal/store"
)

func configureNotifierTestSecrets(t *testing.T, ctx context.Context, st *store.Store) {
	t.Helper()
	cipher, err := appsecrets.New(make([]byte, 32))
	if err != nil {
		t.Fatal(err)
	}
	if err := st.ConfigureSecrets(ctx, cipher); err != nil {
		t.Fatal(err)
	}
}

func TestChannelMatches(t *testing.T) {
	cases := []struct {
		name      string
		ch        Channel
		eventType string
		container string
		stack     string
		want      bool
	}{
		{
			name:      "unfiltered channel matches everything",
			ch:        Channel{Name: "all"},
			eventType: "update.succeeded", container: "grav", stack: "business",
			want: true,
		},
		{
			name:      "event filter excludes non-listed category",
			ch:        Channel{Name: "updates-only", Events: []string{"update.applied", "update.failed"}},
			eventType: "rollback.manual.succeeded", container: "grav", stack: "business",
			want: false,
		},
		{
			name:      "event filter includes listed category",
			ch:        Channel{Name: "updates-only", Events: []string{"update.applied", "update.failed"}},
			eventType: "update.succeeded", container: "grav", stack: "business",
			want: true,
		},
		{
			name:      "update.failed category also covers update.revert_failed",
			ch:        Channel{Name: "updates-only", Events: []string{"update.failed"}},
			eventType: "update.revert_failed", container: "grav", stack: "business",
			want: true,
		},
		{
			name:      "rollback.manual outcomes collapse into one category",
			ch:        Channel{Name: "rollbacks-only", Events: []string{"rollback.manual"}},
			eventType: "rollback.manual.revert_failed", container: "grav", stack: "business",
			want: true,
		},
		{
			name:      "rollback.auto bypasses the event filter",
			ch:        Channel{Name: "updates-only", Events: []string{"update.succeeded"}},
			eventType: "rollback.auto.succeeded", container: "grav", stack: "business",
			want: true,
		},
		{
			name:      "scope filter excludes a container not in either list",
			ch:        Channel{Name: "business-only", Stacks: []string{"business"}},
			eventType: "update.succeeded", container: "adguardhome", stack: "infra",
			want: false,
		},
		{
			name:      "scope filter includes via matching stack",
			ch:        Channel{Name: "business-only", Stacks: []string{"business"}},
			eventType: "update.succeeded", container: "grav", stack: "business",
			want: true,
		},
		{
			name:      "scope filter includes via matching container even in a different stack",
			ch:        Channel{Name: "grav-watcher", Containers: []string{"grav"}},
			eventType: "update.succeeded", container: "grav", stack: "business",
			want: true,
		},
		{
			name:      "scope filter is a union of stacks and containers",
			ch:        Channel{Name: "mixed", Stacks: []string{"infra"}, Containers: []string{"grav"}},
			eventType: "update.succeeded", container: "grav", stack: "business",
			want: true,
		},
		{
			name:      "rollback.auto still respects the scope filter",
			ch:        Channel{Name: "infra-only", Stacks: []string{"infra"}},
			eventType: "rollback.auto.succeeded", container: "grav", stack: "business",
			want: false,
		},
		{
			name:      "scoped channel still receives policy-level events with no container/stack",
			ch:        Channel{Name: "business-only", Stacks: []string{"business"}},
			eventType: "check.failed", container: "", stack: "",
			want: true,
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := tc.ch.matches(tc.eventType, tc.container, tc.stack); got != tc.want {
				t.Errorf("matches(%q, %q, %q) = %v, want %v", tc.eventType, tc.container, tc.stack, got, tc.want)
			}
		})
	}
}

func TestNotifyQueuesThenRetriesAndDeadLetters(t *testing.T) {
	ctx := context.Background()
	st, err := store.Open(ctx, ":memory:")
	if err != nil {
		t.Fatal(err)
	}
	configureNotifierTestSecrets(t, ctx, st)
	t.Cleanup(func() { _ = st.Close() })
	n := New(st)
	n.baseBackoff, n.maxBackoff = time.Millisecond, time.Millisecond
	if err := n.SetChannels(ctx, []Channel{{Name: "broken", URL: "generic://broken"}}); err != nil {
		t.Fatal(err)
	}
	n.Notify(ctx, "update.succeeded", "demo", "infra", "updated")

	attempts := 0
	n.send = func(_, _ string) error { attempts++; return errors.New("offline") }
	for i := 0; i < store.DefaultNotificationMaxAttempts; i++ {
		worked, err := n.deliverOne(ctx)
		if err != nil || !worked {
			t.Fatalf("attempt %d worked=%v err=%v", i+1, worked, err)
		}
		time.Sleep(2 * time.Millisecond)
	}
	jobs, err := st.ListNotificationJobs(ctx, 10)
	if err != nil || len(jobs) != 1 {
		t.Fatalf("jobs=%+v err=%v", jobs, err)
	}
	if attempts != store.DefaultNotificationMaxAttempts || jobs[0].Status != "dead_letter" {
		t.Fatalf("attempts=%d job=%+v", attempts, jobs[0])
	}
	history, err := st.ListNotifications(ctx, 10)
	if err != nil || len(history) != store.DefaultNotificationMaxAttempts {
		t.Fatalf("history=%d err=%v", len(history), err)
	}
}

func TestSuccessfulDeliveryCompletesJob(t *testing.T) {
	ctx := context.Background()
	st, err := store.Open(ctx, ":memory:")
	if err != nil {
		t.Fatal(err)
	}
	configureNotifierTestSecrets(t, ctx, st)
	t.Cleanup(func() { _ = st.Close() })
	n := New(st)
	if err := n.SetChannels(ctx, []Channel{{Name: "ops", URL: "generic://ops"}}); err != nil {
		t.Fatal(err)
	}
	n.Notify(ctx, "rollback.auto.succeeded", "demo", "infra", "restored")
	n.send = func(_, message string) error {
		if message != "restored" {
			t.Fatalf("message=%q", message)
		}
		return nil
	}
	worked, err := n.deliverOne(ctx)
	if err != nil || !worked {
		t.Fatalf("worked=%v err=%v", worked, err)
	}
	jobs, _ := st.ListNotificationJobs(ctx, 10)
	if len(jobs) != 1 || jobs[0].Status != "sent" || jobs[0].SentAt == nil {
		t.Fatalf("job=%+v", jobs)
	}
}

func TestPublicChannelsAreWriteOnlyAndPatchRetainsURL(t *testing.T) {
	ctx := context.Background()
	st, err := store.Open(ctx, ":memory:")
	if err != nil {
		t.Fatal(err)
	}
	configureNotifierTestSecrets(t, ctx, st)
	t.Cleanup(func() { _ = st.Close() })
	n := New(st)
	secretURL := "telegram://bot:secret@telegram?chats=1"
	if err := n.SetChannels(ctx, []Channel{{Name: "ops", URL: secretURL, Events: []string{"update.applied"}}}); err != nil {
		t.Fatal(err)
	}
	public, err := n.PublicChannels(ctx)
	if err != nil || len(public) != 1 {
		t.Fatalf("public=%+v err=%v", public, err)
	}
	if public[0].URL != "" || !public[0].Configured || public[0].Provider != "telegram" {
		t.Fatalf("public=%+v", public[0])
	}
	if err := n.SetChannels(ctx, []Channel{{Name: "ops", Events: []string{"rollback.manual"}, Configured: true}}); err != nil {
		t.Fatal(err)
	}
	internal, err := n.Channels(ctx)
	if err != nil || len(internal) != 1 || internal[0].URL != secretURL {
		t.Fatalf("internal=%+v err=%v", internal, err)
	}
}

// TestSetChannelsRenamingWithoutAURLPreservesItByPosition covers the web
// UI's Modify flow: the full current channel list, with exactly one
// entry renamed and its URL left blank (write-only, never sent back to
// prefill — see PublicChannels). Since the name no longer matches
// anything stored, SetChannels falls back to matching by position — the
// list is the same length, and nothing else moved — so the rename
// succeeds and the original destination survives under the new name.
// Works the same regardless of provider (Telegram here is arbitrary).
func TestSetChannelsRenamingWithoutAURLPreservesItByPosition(t *testing.T) {
	ctx := context.Background()
	st, err := store.Open(ctx, ":memory:")
	if err != nil {
		t.Fatal(err)
	}
	configureNotifierTestSecrets(t, ctx, st)
	t.Cleanup(func() { _ = st.Close() })
	n := New(st)
	secretURL := "telegram://bot:secret@telegram?chats=1"
	if err := n.SetChannels(ctx, []Channel{
		{Name: "ops", URL: secretURL},
		{Name: "ntfy-alerts", URL: "ntfy://ntfy.sh/doupro"},
	}); err != nil {
		t.Fatal(err)
	}

	if err := n.SetChannels(ctx, []Channel{
		{Name: "ops (renamed)"},
		{Name: "ntfy-alerts", URL: "ntfy://ntfy.sh/doupro"},
	}); err != nil {
		t.Fatalf("SetChannels: %v", err)
	}

	internal, err := n.Channels(ctx)
	if err != nil || len(internal) != 2 {
		t.Fatalf("internal=%+v err=%v", internal, err)
	}
	if internal[0].Name != "ops (renamed)" || internal[0].URL != secretURL {
		t.Fatalf("renamed channel = %+v, want name %q with the preserved URL", internal[0], "ops (renamed)")
	}
}

// TestSetChannelsRenamingWithAnAddedChannelReturnsErrChannelNeedsURL
// covers what the position fallback deliberately does not attempt:
// renaming one channel while ALSO adding a new one in the same request
// changes the list length, so position no longer reliably means "the
// same channel" — the new entry needs its own URL, and reports
// ErrChannelNeedsURL (a 400 an operator can act on) rather than a
// generic persistence failure, or worse, silently matching the wrong
// channel's URL to the wrong name.
func TestSetChannelsRenamingWithAnAddedChannelReturnsErrChannelNeedsURL(t *testing.T) {
	ctx := context.Background()
	st, err := store.Open(ctx, ":memory:")
	if err != nil {
		t.Fatal(err)
	}
	configureNotifierTestSecrets(t, ctx, st)
	t.Cleanup(func() { _ = st.Close() })
	n := New(st)
	if err := n.SetChannels(ctx, []Channel{{Name: "ops", URL: "telegram://bot:secret@telegram?chats=1"}}); err != nil {
		t.Fatal(err)
	}

	err = n.SetChannels(ctx, []Channel{{Name: "ops (renamed)"}, {Name: "brand new"}})
	if !errors.Is(err, ErrChannelNeedsURL) {
		t.Fatalf("SetChannels error = %v, want ErrChannelNeedsURL", err)
	}

	// The failed request must not have partially applied.
	internal, err := n.Channels(ctx)
	if err != nil || len(internal) != 1 || internal[0].Name != "ops" {
		t.Fatalf("internal=%+v err=%v", internal, err)
	}
}
