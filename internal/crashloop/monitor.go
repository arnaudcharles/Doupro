// Package crashloop watches Docker lifecycle events and triggers the durable
// post-update automatic rollback policy documented in docs/containers.md.
package crashloop

import (
	"context"
	"errors"
	"fmt"
	"io"
	"strconv"
	"time"

	"github.com/arnaudcharles/doupro/internal/docker"
	"github.com/arnaudcharles/doupro/internal/events"
	"github.com/arnaudcharles/doupro/internal/metrics"
	"github.com/arnaudcharles/doupro/internal/store"
	"github.com/arnaudcharles/doupro/internal/updater"
)

type Monitor struct {
	docker  *docker.Client
	store   *store.Store
	updater *updater.Updater
	logger  *events.Logger
}

func New(client *docker.Client, st *store.Store, upd *updater.Updater, logger *events.Logger) *Monitor {
	return &Monitor{docker: client, store: st, updater: upd, logger: logger}
}

// Run reconciles restart counters first (covering daemon downtime), then
// keeps reconnecting to Docker's event stream until ctx is cancelled.
func (m *Monitor) Run(ctx context.Context) {
	m.recoverTriggered(ctx)
	backoff := time.Second
	for ctx.Err() == nil {
		since := m.reconcile(ctx)
		eventsCh, errs := m.docker.ContainerEvents(ctx, since)
		expiryTicker := time.NewTicker(time.Minute)
		streamEnded := false
		for !streamEnded {
			select {
			case <-ctx.Done():
				expiryTicker.Stop()
				return
			case <-expiryTicker.C:
				// Also reconciles RestartCount for active watches on the same
				// cadence — the Docker event stream can silently drop a "die"
				// event (reconnect gaps, exit-code parsing quirks), and
				// RestartCount is the only authoritative signal that doesn't
				// depend on having seen every event. Without this, a missed
				// crash is only ever backfilled when the stream reconnects,
				// which may not happen for a long time on a healthy connection.
				m.reconcile(ctx)
			case event, ok := <-eventsCh:
				if !ok {
					streamEnded = true
					continue
				}
				m.handleEvent(ctx, event)
			case err, ok := <-errs:
				if ok && err != nil && !errors.Is(err, context.Canceled) && !errors.Is(err, io.EOF) {
					m.logger.Emit(ctx, events.Event{Level: events.LevelWarn, Type: "crashloop.stream_failed", Actor: events.ActorSystem, Message: fmt.Sprintf("Docker event stream failed; reconnecting: %v", err)})
				}
				streamEnded = true
			}
		}
		expiryTicker.Stop()
		select {
		case <-ctx.Done():
			return
		case <-time.After(backoff):
		}
		if backoff < 30*time.Second {
			backoff *= 2
		}
	}
}

func (m *Monitor) recoverTriggered(ctx context.Context) {
	watches, err := m.store.ListTriggeredRollbackWatches(ctx)
	if err != nil {
		return
	}
	for _, watch := range watches {
		m.logger.Emit(ctx, events.Event{Level: events.LevelWarn, Type: "crashloop.rollback_recovered", Container: watch.ContainerName, Actor: events.ActorSystem, Message: fmt.Sprintf("resuming durable automatic rollback for %s after daemon restart", watch.ContainerName)})
		go m.rollback(context.WithoutCancel(ctx), watch)
	}
}

func (m *Monitor) reconcile(ctx context.Context) time.Time {
	now := time.Now().UTC()
	if expired, err := m.store.ExpireRollbackWatches(ctx, now); err == nil && expired > 0 {
		m.logger.Emit(ctx, events.Event{Level: events.LevelInfo, Type: "crashloop.expired", Actor: events.ActorSystem, Message: fmt.Sprintf("expired %d automatic rollback watch(es)", expired), Metadata: map[string]any{"count": expired}})
	}
	watches, err := m.store.ListActiveRollbackWatches(ctx)
	if err != nil || len(watches) == 0 {
		return now
	}
	since := watches[0].UpdateAppliedAt.Add(-time.Second)
	for _, watch := range watches {
		if watch.UpdateAppliedAt.Before(since) {
			since = watch.UpdateAppliedAt.Add(-time.Second)
		}
		info, inspectErr := m.docker.Inspect(ctx, watch.ContainerID)
		if inspectErr != nil {
			continue
		}
		if info.RestartCount > watch.ObservedRestartCount {
			delta := info.RestartCount - watch.ObservedRestartCount
			for i := 0; i < delta; i++ {
				m.recordCrash(ctx, watch.ContainerName, watch.ContainerID, now.UnixNano()+int64(i), now, "restart_counter")
			}
		}
		_ = m.store.SetRollbackWatchRestartCount(ctx, watch.ContainerName, info.RestartCount)
	}
	return since
}

func (m *Monitor) handleEvent(ctx context.Context, event docker.ContainerEvent) {
	if event.Action != "die" || event.Name == "" {
		return
	}
	exitCode, err := strconv.Atoi(event.ExitCode)
	if err == nil && exitCode == 0 {
		metrics.CrashEventsTotal.WithLabelValues("ignored").Inc()
		return
	}
	eventNano := event.TimeNano
	if eventNano == 0 {
		eventNano = event.Time.UnixNano()
	}
	m.recordCrash(ctx, event.Name, event.ContainerID, eventNano, event.Time, "docker_event")
}

func (m *Monitor) recordCrash(ctx context.Context, name, id string, eventNano int64, at time.Time, source string) {
	watch, counted, triggered, err := m.store.RecordCrash(ctx, name, id, eventNano, at)
	if errors.Is(err, store.ErrNotFound) {
		return
	}
	if err != nil {
		m.logger.Emit(ctx, events.Event{Level: events.LevelError, Type: "crashloop.record_failed", Container: name, Actor: events.ActorSystem, Message: fmt.Sprintf("failed to record crash for %s: %v", name, err)})
		return
	}
	if !counted {
		metrics.CrashEventsTotal.WithLabelValues("ignored").Inc()
		return
	}
	metrics.CrashEventsTotal.WithLabelValues("counted").Inc()
	m.logger.Emit(ctx, events.Event{
		Level: events.LevelWarn, Type: "crashloop.crash_detected", Container: name, Actor: events.ActorSystem,
		Message:  fmt.Sprintf("detected crash %d/%d for %s", watch.CrashCount, watch.Threshold, name),
		Metadata: map[string]any{"crash_count": watch.CrashCount, "threshold": watch.Threshold, "source": source},
	})
	if !triggered {
		return
	}
	m.logger.Emit(ctx, events.Event{
		Level: events.LevelWarn, Type: "crashloop.threshold_reached", Container: name, Actor: events.ActorSystem,
		Message:  fmt.Sprintf("%s crashed %d times after update; starting automatic rollback", name, watch.CrashCount),
		Metadata: map[string]any{"crash_count": watch.CrashCount, "threshold": watch.Threshold},
	})
	go m.rollback(context.WithoutCancel(ctx), watch)
}

func (m *Monitor) rollback(ctx context.Context, watch store.RollbackWatch) {
	rec, err := m.store.GetContainerByName(ctx, watch.ContainerName)
	if err == nil {
		err = m.updater.Rollback(ctx, rec.ID, events.ActorSystem, "crashloop")
	}
	if updater.IsMaintenanceError(err) {
		// Keep the durable watch in triggered state. The new daemon resumes it
		// after self-update; consuming it as a failure would lose the rollback.
		return
	}
	if err != nil {
		_ = m.store.CompleteRollbackWatch(ctx, watch.ContainerName, "rollback_failed")
		m.logger.Emit(ctx, events.Event{Level: events.LevelError, Type: "crashloop.rollback_failed", Container: watch.ContainerName, Actor: events.ActorSystem, Message: fmt.Sprintf("automatic rollback failed for %s: %v", watch.ContainerName, err)})
	}
}
