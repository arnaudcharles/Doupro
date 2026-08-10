package crashloop

import (
	"context"
	"testing"
	"time"

	"github.com/arnaudcharles/doupro/internal/docker"
	"github.com/arnaudcharles/doupro/internal/events"
	"github.com/arnaudcharles/doupro/internal/store"
)

func TestHandleEventCountsOnlyUnexpectedUniqueExit(t *testing.T) {
	ctx := context.Background()
	st, err := store.Open(ctx, ":memory:")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = st.Close() })
	if err := st.ArmRollbackWatch(ctx, "demo", "container-id", time.Now().Add(-time.Minute), 10*time.Minute, 5, 0); err != nil {
		t.Fatal(err)
	}
	m := New(nil, st, nil, events.New("error"))
	now := time.Now().UTC()
	m.handleEvent(ctx, docker.ContainerEvent{ContainerID: "container-id", Name: "demo", Action: "die", ExitCode: "0", Time: now, TimeNano: 1})
	m.handleEvent(ctx, docker.ContainerEvent{ContainerID: "container-id", Name: "demo", Action: "die", ExitCode: "137", Time: now, TimeNano: 2})
	m.handleEvent(ctx, docker.ContainerEvent{ContainerID: "container-id", Name: "demo", Action: "die", ExitCode: "137", Time: now, TimeNano: 2})
	m.handleEvent(ctx, docker.ContainerEvent{ContainerID: "other-id", Name: "demo", Action: "die", ExitCode: "1", Time: now, TimeNano: 3})
	watch, err := st.GetRollbackWatch(ctx, "demo")
	if err != nil {
		t.Fatal(err)
	}
	if watch.CrashCount != 1 {
		t.Fatalf("crash count = %d, want 1", watch.CrashCount)
	}
}
