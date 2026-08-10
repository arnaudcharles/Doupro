package scheduler

import (
	"context"
	"fmt"
	"time"

	cron "github.com/robfig/cron/v3"

	"github.com/arnaudcharles/doupro/internal/docker"
	"github.com/arnaudcharles/doupro/internal/events"
	"github.com/arnaudcharles/doupro/internal/metrics"
	"github.com/arnaudcharles/doupro/internal/notifier"
	"github.com/arnaudcharles/doupro/internal/store"
	"github.com/arnaudcharles/doupro/internal/updater"
)

// DefaultCheckInterval matches the default documented in docs/settings.md,
// used until Settings -> General's check interval is ever set (see
// currentCheckInterval).
const DefaultCheckInterval = 30 * time.Minute

// dueTickInterval is how often the scheduler looks for due one-off/cron
// schedules and re-evaluates "immediate" recurring policies. Independent
// of DefaultCheckInterval: a schedule firing should not have to wait for
// the next full registry check to be noticed.
const dueTickInterval = 30 * time.Second

// Scheduler runs the periodic registry check and executes one-off and
// recurring schedules. See docs/schedule.md.
type Scheduler struct {
	docker        *docker.Client
	store         *store.Store
	updater       *updater.Updater
	logger        *events.Logger
	notifier      *notifier.Notifier
	checkInterval time.Duration
}

// New builds a Scheduler. All dependencies must already be open/connected.
func New(cli *docker.Client, st *store.Store, upd *updater.Updater, logger *events.Logger, notif *notifier.Notifier, checkInterval time.Duration) *Scheduler {
	if checkInterval <= 0 {
		checkInterval = DefaultCheckInterval
	}
	return &Scheduler{docker: cli, store: st, updater: upd, logger: logger, notifier: notif, checkInterval: checkInterval}
}

// Run blocks, running the registry check on the configured interval and
// evaluating due/immediate schedules on a tighter tick, until ctx is
// cancelled. The check interval is a time.Timer re-armed after every fire
// (rather than a time.Ticker) specifically so it can pick up a changed
// Settings -> General check interval (docs/settings.md) without a
// restart: a Ticker's period can't be changed once created.
func (s *Scheduler) Run(ctx context.Context) {
	dueTicker := time.NewTicker(dueTickInterval)
	defer dueTicker.Stop()

	checkTimer := time.NewTimer(s.currentCheckInterval(ctx))
	defer checkTimer.Stop()

	for {
		select {
		case <-ctx.Done():
			return
		case <-checkTimer.C:
			Check(ctx, s.logger, s.docker, s.store, s.notifier)
			s.runRelativePolicies(ctx)
			checkTimer.Reset(s.currentCheckInterval(ctx))
		case <-dueTicker.C:
			s.runDueSchedules(ctx)
			s.runRelativePolicies(ctx)
		}
	}
}

// currentCheckInterval reads Settings -> General's check interval, or
// falls back to the value passed to New if it's never been set.
func (s *Scheduler) currentCheckInterval(ctx context.Context) time.Duration {
	minutes, ok, err := s.store.GetCheckIntervalMinutes(ctx)
	if err != nil || !ok {
		return s.checkInterval
	}
	return time.Duration(minutes) * time.Minute
}

func (s *Scheduler) runDueSchedules(ctx context.Context) {
	if s.updater.MaintenanceActive() {
		return
	}
	due, err := s.store.DueSchedules(ctx, time.Now())
	if err != nil {
		s.logger.Emit(ctx, events.Event{
			Level: events.LevelError, Type: "schedule.list_failed", Actor: events.ActorSystem,
			Message: fmt.Sprintf("could not list due schedules: %v", err),
		})
		return
	}
	for _, sc := range due {
		switch sc.Kind {
		case "once":
			s.executeOnce(ctx, sc)
		case "cron":
			s.executeCron(ctx, sc)
		}
	}
}

// executeOnce applies a one-off schedule (the per-container Schedule
// button, docs/containers.md) to its pinned target image, then disables
// itself — it fires exactly once.
func (s *Scheduler) executeOnce(ctx context.Context, sc store.Schedule) {
	actorID := fmt.Sprintf("schedule:%d", sc.ID)

	rec, err := s.store.GetContainerByName(ctx, sc.ContainerName)
	if err != nil {
		s.logExecuted(ctx, sc, fmt.Errorf("look up container %q: %w", sc.ContainerName, err))
		_ = s.store.MarkScheduleRun(ctx, sc.ID, nil)
		return
	}

	target := sc.PinnedImage
	if target == "" {
		target = rec.CurrentImage
	}

	err = s.updater.Update(ctx, rec.ID, target, events.ActorSchedule, actorID)
	if updater.IsMaintenanceError(err) {
		return
	}
	s.logExecuted(ctx, sc, err)
	_ = s.store.MarkScheduleRun(ctx, sc.ID, nil)
}

// executeCron applies a recurring cron policy to every target container
// that currently has an update available, then computes and records the
// next run time.
func (s *Scheduler) executeCron(ctx context.Context, sc store.Schedule) {
	if _, paused := s.applyToTargets(ctx, sc, 0); paused {
		return
	}

	next, _ := NextCronRun(sc.CronExpr) // already validated at creation (see api.handleCreateSchedule)
	if err := s.store.MarkScheduleRun(ctx, sc.ID, next); err != nil {
		s.logger.Emit(ctx, events.Event{
			Level: events.LevelError, Type: "schedule.update_failed", Actor: events.ActorSystem,
			Message: fmt.Sprintf("could not record next run for schedule #%d: %v", sc.ID, err),
		})
	}
}

// runRelativePolicies evaluates immediate and delayed policies against the
// durable first-detected timestamp of each selected semver candidate.
func (s *Scheduler) runRelativePolicies(ctx context.Context) {
	if s.updater.MaintenanceActive() {
		return
	}
	schedules, err := s.store.ListSchedules(ctx)
	if err != nil {
		s.logger.Emit(ctx, events.Event{
			Level: events.LevelError, Type: "schedule.list_failed", Actor: events.ActorSystem,
			Message: fmt.Sprintf("could not list schedules: %v", err),
		})
		return
	}
	for _, sc := range schedules {
		if !sc.Enabled || sc.Kind != "relative" {
			continue
		}
		// A failed provider/update must not turn the 30-second due ticker into
		// an aggressive retry loop. Re-evaluate at most once per check interval.
		if !relativeRetryReady(sc.LastRunAt, s.currentCheckInterval(ctx), time.Now()) {
			continue
		}
		var delay time.Duration
		if sc.RelativePolicy == "delayed" {
			delay, err = time.ParseDuration(sc.RelativeAfter)
			if err != nil {
				continue
			}
		}
		if applied, paused := s.applyToTargets(ctx, sc, delay); paused {
			return
		} else if applied > 0 {
			_ = s.store.TouchScheduleLastRun(ctx, sc.ID)
		}
	}
}

func relativeRetryReady(lastRun *time.Time, interval time.Duration, now time.Time) bool {
	return lastRun == nil || !now.Before(lastRun.Add(interval))
}

// SelfUpdateBlockers returns every enabled schedule that can mutate a
// container before horizon. It is called only after updater has closed the
// global maintenance gate, so no schedule can race the decision.
func (s *Scheduler) SelfUpdateBlockers(ctx context.Context, horizon time.Time) ([]string, error) {
	triggered, err := s.store.ListTriggeredRollbackWatches(ctx)
	if err != nil {
		return nil, err
	}
	if len(triggered) != 0 {
		blockers := make([]string, 0, len(triggered))
		for _, watch := range triggered {
			blockers = append(blockers, fmt.Sprintf("automatic rollback for %s is pending", watch.ContainerName))
		}
		return blockers, nil
	}
	schedules, err := s.store.ListSchedules(ctx)
	if err != nil {
		return nil, err
	}
	now := time.Now().UTC()
	var blockers []string
	for _, sc := range schedules {
		if !sc.Enabled {
			continue
		}
		if sc.NextRunAt != nil && !sc.NextRunAt.After(horizon) {
			blockers = append(blockers, fmt.Sprintf("schedule #%d is due at %s", sc.ID, sc.NextRunAt.UTC().Format(time.RFC3339)))
			continue
		}
		if sc.Kind != "relative" {
			continue
		}
		targets, resolveErr := s.resolveTargets(ctx, sc)
		if resolveErr != nil {
			return nil, resolveErr
		}
		for _, rec := range targets {
			if !rec.UpdateAvailable || rec.Excluded {
				continue
			}
			due := now
			if sc.RelativePolicy == "delayed" {
				delay, parseErr := time.ParseDuration(sc.RelativeAfter)
				if parseErr != nil {
					return nil, fmt.Errorf("parse schedule #%d delay: %w", sc.ID, parseErr)
				}
				scope := sc.SemverPolicy
				if scope == "" {
					scope = "major"
				}
				candidate, candidateErr := s.store.GetUpdateCandidate(ctx, rec.ID, scope)
				if candidateErr != nil {
					continue
				}
				due = candidate.DetectedAt.Add(delay)
			}
			if !due.After(horizon) {
				blockers = append(blockers, fmt.Sprintf("relative schedule #%d for %s is due at %s", sc.ID, rec.Name, due.UTC().Format(time.RFC3339)))
			}
		}
	}
	return blockers, nil
}

// applyToTargets updates every container in sc's target (a single
// container or a whole stack) that currently has an update available,
// returning how many updates were attempted.
func (s *Scheduler) applyToTargets(ctx context.Context, sc store.Schedule, delay time.Duration) (int, bool) {
	actorID := fmt.Sprintf("schedule:%d", sc.ID)
	targets, err := s.resolveTargets(ctx, sc)
	if err != nil {
		s.logger.Emit(ctx, events.Event{
			Level: events.LevelError, Type: "schedule.list_failed", Actor: events.ActorSystem,
			Message: fmt.Sprintf("could not resolve targets for schedule #%d: %v", sc.ID, err),
		})
		return 0, false
	}

	applied := 0
	for _, rec := range targets {
		if !rec.UpdateAvailable || rec.Excluded {
			continue
		}
		scope := sc.SemverPolicy
		if scope == "" {
			scope = "major"
		}
		candidate, candidateErr := s.store.GetUpdateCandidate(ctx, rec.ID, scope)
		if candidateErr != nil {
			continue
		}
		if delay > 0 && time.Now().Before(candidate.DetectedAt.Add(delay)) {
			continue
		}
		err := s.updater.Update(ctx, rec.ID, candidate.Image, events.ActorSchedule, actorID)
		if updater.IsMaintenanceError(err) {
			return applied, true
		}
		s.logExecuted(ctx, sc, err)
		if err == nil {
			_ = s.store.ClearUpdateCandidates(ctx, rec.ID)
		}
		applied++
	}
	return applied, false
}

func (s *Scheduler) resolveTargets(ctx context.Context, sc store.Schedule) ([]store.ContainerRecord, error) {
	if sc.ContainerName != "" {
		rec, err := s.store.GetContainerByName(ctx, sc.ContainerName)
		if err != nil {
			return nil, err
		}
		return []store.ContainerRecord{rec}, nil
	}

	all, err := s.store.ListContainers(ctx)
	if err != nil {
		return nil, err
	}
	out := make([]store.ContainerRecord, 0, len(all))
	for _, rec := range all {
		if rec.Stack == sc.Stack {
			out = append(out, rec)
		}
	}
	return out, nil
}

func (s *Scheduler) logExecuted(ctx context.Context, sc store.Schedule, err error) {
	level, msg := events.LevelInfo, fmt.Sprintf("schedule #%d executed", sc.ID)
	if err != nil {
		level, msg = events.LevelError, fmt.Sprintf("schedule #%d execution failed: %v", sc.ID, err)
	}
	s.logger.Emit(ctx, events.Event{
		Level: level, Type: "schedule.executed", Actor: events.ActorSchedule,
		ActorID: fmt.Sprintf("%d", sc.ID), Container: sc.ContainerName, Stack: sc.Stack, Message: msg,
		Metadata: map[string]any{"kind": sc.Kind, "semver_policy": sc.SemverPolicy},
	})
	status := "success"
	if err != nil {
		status = "failed"
	}
	metrics.ScheduleExecutionsTotal.WithLabelValues(sc.Kind, sc.SemverPolicy, status).Inc()
	// "Recurring policy executed" (docs/notifications.md) only applies to
	// cron/relative policies, not a one-off "once" schedule firing — that
	// case already has its own update.succeeded/failed notification from
	// updater.Update, so a second notification here would just be an
	// unlabeled duplicate. sc.Notify is the per-schedule opt-out set at
	// creation time (previously stored but never read anywhere).
	if sc.Kind != "once" && sc.Notify {
		s.notifier.Notify(ctx, "schedule.executed", sc.ContainerName, sc.Stack, msg)
	}
}

// NextCronRun computes the next run time for a standard 5-field cron
// expression. Exported so internal/api can validate an expression (and
// show the caller what it means) at schedule-creation time, before it
// ever reaches the scheduler loop.
func NextCronRun(expr string) (*time.Time, error) {
	schedule, err := cron.ParseStandard(expr)
	if err != nil {
		return nil, fmt.Errorf("invalid cron expression %q: %w", expr, err)
	}
	t := schedule.Next(time.Now())
	return &t, nil
}
