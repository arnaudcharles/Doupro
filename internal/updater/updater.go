// Package updater implements the update/rollback state machine: snapshot
// current config, pull, recreate, verify health, commit or roll back
// (synchronously on a failed update, or on demand for a manual/automatic
// rollback). The only package allowed to mutate a container's running
// state. See docs/containers.md.
//
// Recreation preserves and validates the container configuration, including
// mounts, environment, labels, restart policy and networks. Post-update
// crash-loop monitoring is durable and automatically rolls a running
// container back after the configured threshold is reached.
package updater

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/arnaudcharles/doupro/internal/docker"
	"github.com/arnaudcharles/doupro/internal/events"
	"github.com/arnaudcharles/doupro/internal/metrics"
	"github.com/arnaudcharles/doupro/internal/notifier"
	"github.com/arnaudcharles/doupro/internal/registry"
	"github.com/arnaudcharles/doupro/internal/store"
)

// Default timing, matching the defaults documented in docs/settings.md.
const (
	DefaultHealthTimeout   = 2 * time.Minute
	DefaultStabilityWindow = 60 * time.Second
	DefaultStopTimeout     = 30 * time.Second
)

// Updater performs Update and Rollback for containers tracked in st,
// using docker to talk to the Engine API and logger for the structured
// events every state change must produce (see docs/logs.md).
type Updater struct {
	docker       *docker.Client
	store        *store.Store
	logger       *events.Logger
	notifier     *notifier.Notifier
	operations   *operationGuard
	selfBlockers func(context.Context, time.Time) ([]string, error)
	shutdown     func()
}

// ConfigureSelfUpdate supplies dependencies that are wired after Updater.
func (u *Updater) ConfigureSelfUpdate(blockers func(context.Context, time.Time) ([]string, error), shutdown func()) {
	u.selfBlockers = blockers
	u.shutdown = shutdown
}

// New builds an Updater. docker and store must already be open/connected.
func New(dockerClient *docker.Client, st *store.Store, logger *events.Logger, notif *notifier.Notifier) *Updater {
	return &Updater{docker: dockerClient, store: st, logger: logger, notifier: notif, operations: newOperationGuard()}
}

// Update pulls targetImage, recreates the container with its existing
// config otherwise untouched, and waits for it to come up healthy. On
// success the previous image is recorded (enabling Rollback) and
// update_available is cleared. On failure, DoUpRo reverts synchronously
// to the image the container was running before this call — an update
// that fails never leaves the operator on a broken container.
func (u *Updater) Update(ctx context.Context, containerID, targetImage string, actor events.Actor, actorID string) error {
	start := time.Now()
	var stack string
	defer func() { metrics.UpdateDurationSeconds.WithLabelValues(stack).Observe(time.Since(start).Seconds()) }()

	name := containerID
	if rec, err := u.store.GetContainer(ctx, containerID); err == nil {
		name = rec.Name
	}
	finish, err := u.beginOperation(name, "update", actor, actorID)
	if err != nil {
		return err
	}
	defer finish()

	before, err := u.docker.Inspect(ctx, containerID)
	if err != nil {
		return fmt.Errorf("inspect container before update: %w", err)
	}
	stack = stackOf(before.Config.Labels)
	name = trimSlash(before.Name)
	previousImage := before.Image // image ID (sha256:...) — see docs/containers.md; stays resolvable even after the tag moves on
	definedImage := targetImage
	if rec, getErr := u.store.GetContainer(ctx, containerID); getErr == nil {
		definedImage = stableImageReference(targetImage, rec.CurrentImage)
	}

	u.logger.Emit(ctx, events.Event{
		Level: events.LevelInfo, Type: "update.started", Container: name,
		FromVersion: before.Config.Image, ToVersion: targetImage, Actor: actor, ActorID: actorID,
		Message: fmt.Sprintf("updating %s: %s -> %s", name, before.Config.Image, targetImage),
	})

	result, err := u.recreate(ctx, containerID, before, targetImage)
	if err != nil {
		u.logger.Emit(ctx, events.Event{
			Level: events.LevelError, Type: "update.failed", Container: name,
			FromVersion: before.Config.Image, ToVersion: targetImage, Actor: actor, ActorID: actorID,
			Message: fmt.Sprintf("update failed for %s, reverting to previous image: %v", name, err),
		})
		if !result.originalRemoved {
			return u.failBeforeRemoval(ctx, "update", containerID, result, before, name, actor, actorID, err)
		}
		return u.revert(ctx, "update", containerID, result.ID, before, name, actor, actorID, err)
	}

	replacement := store.ContainerRecord{
		ID: result.ID, Name: name, Stack: stackOf(before.Config.Labels),
		State: result.State, Status: result.Status, CurrentImage: definedImage, PreviousImage: previousImage,
	}
	var commitErr error
	var crashThreshold, crashWindowMinutes int
	if result.State == "running" {
		crashThreshold, crashWindowMinutes, commitErr = u.store.GetCrashLoopSettings(ctx)
		if commitErr == nil {
			restartCount := 0
			if inspected, inspectErr := u.docker.Inspect(ctx, result.ID); inspectErr == nil {
				restartCount = inspected.RestartCount
			}
			commitErr = u.store.ReplaceContainerAndArmRollback(ctx, containerID, replacement,
				time.Now().UTC(), time.Duration(crashWindowMinutes)*time.Minute, crashThreshold, restartCount)
		}
	} else {
		commitErr = u.store.ReplaceContainer(ctx, containerID, replacement)
	}
	if commitErr != nil {
		commitErr = fmt.Errorf("record updated container: %w", commitErr)
		u.logger.Emit(ctx, events.Event{
			Level: events.LevelError, Type: "update.commit_failed", Container: name,
			FromVersion: before.Config.Image, ToVersion: targetImage, Actor: actor, ActorID: actorID,
			Message: fmt.Sprintf("update reached a healthy container but its durable commit failed for %s; reverting: %v", name, commitErr),
		})
		return u.revert(ctx, "update", containerID, result.ID, before, name, actor, actorID, commitErr)
	}
	if result.State == "running" {
		u.logger.Emit(ctx, events.Event{
			Level: events.LevelInfo, Type: "crashloop.armed", Container: name, Actor: events.ActorSystem,
			Message:  fmt.Sprintf("automatic rollback armed for %s: %d crashes within %d minutes", name, crashThreshold, crashWindowMinutes),
			Metadata: map[string]any{"threshold": crashThreshold, "window_minutes": crashWindowMinutes},
		})
	} else {
		_ = u.store.CompleteRollbackWatch(ctx, name, "not_running")
	}

	msg := fmt.Sprintf("%s updated %s -> %s", name, before.Config.Image, targetImage)
	u.logger.Emit(ctx, events.Event{
		Level: events.LevelInfo, Type: "update.succeeded", Container: name,
		FromVersion: before.Config.Image, ToVersion: targetImage, Actor: actor, ActorID: actorID,
		Message: msg,
	})
	u.notifier.Notify(ctx, "update.succeeded", name, stackOf(before.Config.Labels), msg)
	metrics.UpdatesTotal.WithLabelValues(stack, "success").Inc()
	return nil
}

// revert recreates the container back on its original image after a
// failed Update *or* Rollback (kind is "update" or "rollback", used only
// to make event types/messages/errors say which one) — so neither
// operation ever leaves the operator on a half-applied change. For a
// failed rollback, "original image" is the version being rolled back
// *from*: not the destination the operator wanted, but the last one
// actually confirmed running, which is the safer thing to land back on
// than an unconfirmed recreate.
//
// originalID is the container ID the store's row is still keyed under
// (nothing about it changes until this function commits one way or the
// other). liveID is whichever ID recreate() should target to stop/
// remove: the same as originalID if the failed attempt never got past
// pulling, or the (possibly unhealthy) *new* ID if it failed at the
// health-check stage — recreate() already stopped/removed originalID by
// then, so targeting it again fails with "no such container" — the exact
// bug this two-ID split fixes.
func (u *Updater) revert(ctx context.Context, kind, originalID, liveID string, before dockerInspect, name string, actor events.Actor, actorID string, cause error) error {
	result, revertErr := u.recreate(ctx, liveID, before, before.Config.Image)
	if revertErr != nil {
		// Worst case: neither the operation nor the revert succeeded. This
		// is logged as loudly as possible — there is no automatic recovery
		// left to attempt.
		msg := fmt.Sprintf("could not revert %s after a failed %s: %v (original failure: %v)", name, kind, revertErr, cause)
		u.logger.Emit(ctx, events.Event{
			Level: events.LevelError, Type: kind + ".revert_failed", Container: name,
			Actor: actor, ActorID: actorID, Message: msg,
		})
		u.notifier.Notify(ctx, kind+".revert_failed", name, stackOf(before.Config.Labels), msg)
		if kind == "update" {
			// doupro_rollbacks_total (docs/stats.md) has no failure
			// dimension yet — counting a failed rollback as a failed
			// *update* would misattribute it, so this only fires for the
			// update caller, which is what UpdatesTotal actually measures.
			metrics.UpdatesTotal.WithLabelValues(stackOf(before.Config.Labels), "failed").Inc()
		}
		return fmt.Errorf("%s failed and revert also failed: %s error: %w; revert error: %v", kind, kind, cause, revertErr)
	}

	// Preserve whatever previous_image was already on record — recreate()
	// only swaps the running image, it never changes what "previous"
	// means, so a revert must not silently blank out the Rollback
	// button's target for this container. Looked up by originalID: the
	// store has never heard of liveID yet when the two differ.
	var previousImage string
	currentImage := before.Config.Image
	if rec, err := u.store.GetContainer(ctx, originalID); err == nil {
		previousImage = rec.PreviousImage
		currentImage = stableImageReference(before.Config.Image, rec.CurrentImage)
	}

	if err := u.store.ReplaceContainer(ctx, originalID, store.ContainerRecord{
		ID: result.ID, Name: name, Stack: stackOf(before.Config.Labels),
		State: result.State, Status: result.Status, CurrentImage: currentImage, PreviousImage: previousImage,
	}); err != nil {
		return fmt.Errorf("record reverted container: %w", err)
	}
	u.notifier.Notify(ctx, kind+".failed", name, stackOf(before.Config.Labels), fmt.Sprintf("%s failed for %s, reverted to previous image: %v", kind, name, cause))
	if kind == "update" {
		metrics.UpdatesTotal.WithLabelValues(stackOf(before.Config.Labels), "failed").Inc()
	}
	return fmt.Errorf("%s failed, reverted to previous image: %w", kind, cause)
}

// failBeforeRemoval handles failures while the original container still
// exists. Pull/preflight failures require no mutation at all; if it had
// already been stopped before Remove failed, restart that exact container
// instead of destroying and recreating a healthy workload unnecessarily.
func (u *Updater) failBeforeRemoval(ctx context.Context, kind, originalID string, result recreationResult, before dockerInspect, name string, actor events.Actor, actorID string, cause error) error {
	if result.originalStopped {
		if err := u.docker.Start(ctx, originalID); err != nil {
			msg := fmt.Sprintf("could not restart %s after a failed %s: %v (original failure: %v)", name, kind, err, cause)
			u.logger.Emit(ctx, events.Event{Level: events.LevelError, Type: kind + ".revert_failed", Container: name, Actor: actor, ActorID: actorID, Message: msg})
			if kind == "update" {
				metrics.UpdatesTotal.WithLabelValues(stackOf(before.Config.Labels), "failed").Inc()
			}
			return fmt.Errorf("%s failed and original container could not be restarted: %w", kind, err)
		}
	}
	if kind == "update" {
		metrics.UpdatesTotal.WithLabelValues(stackOf(before.Config.Labels), "failed").Inc()
	}
	return fmt.Errorf("%s failed before the original container was removed: %w", kind, cause)
}

// stableImageReference enforces the store invariant that CurrentImage is
// the mutable operator-defined reference whenever one is known. Execution
// targets may legitimately be immutable (a one-off schedule pin or rollback
// digest), but persisting that target would lose the tag used for future
// update checks.
func stableImageReference(operationImage, storedImage string) string {
	if registry.IsDigestReference(operationImage) && storedImage != "" && !registry.IsDigestReference(storedImage) {
		return storedImage
	}
	return operationImage
}

// Rollback recreates containerID from its recorded previous image. The
// Rollback button is available any time a previous version is on record,
// independent of the container's current running state — see
// docs/containers.md.
func (u *Updater) Rollback(ctx context.Context, containerID string, actor events.Actor, actorID string) error {
	rec, err := u.store.GetContainer(ctx, containerID)
	if err != nil {
		return fmt.Errorf("look up container: %w", err)
	}
	if rec.PreviousImage == "" {
		return errors.New("no previous version recorded for this container")
	}
	finish, err := u.beginOperation(rec.Name, "rollback", actor, actorID)
	if err != nil {
		return err
	}
	defer finish()

	before, err := u.docker.Inspect(ctx, containerID)
	if err != nil {
		return fmt.Errorf("inspect container before rollback: %w", err)
	}
	if actor == events.ActorSystem {
		deadline := time.Now().Add(10 * time.Second)
		for before.State != nil && before.State.Status != "running" && before.State.Status != "exited" && before.State.Status != "created" && time.Now().Before(deadline) {
			select {
			case <-ctx.Done():
				return ctx.Err()
			case <-time.After(100 * time.Millisecond):
			}
			before, err = u.docker.Inspect(ctx, containerID)
			if err != nil {
				return fmt.Errorf("inspect crash-loop container before rollback: %w", err)
			}
		}
	}
	name := trimSlash(before.Name)

	// A manual rollback is a deliberate operator action, not something
	// worth flagging above routine activity — WARN reads as "something's
	// wrong" at the exact moment nothing is. An automatic rollback
	// (triggered by the crash-loop detector, not a human) stays WARN:
	// that one genuinely is worth a second look.
	eventType := "rollback.manual"
	level := events.LevelInfo
	if actor == events.ActorSystem {
		eventType = "rollback.auto"
		level = events.LevelWarn
	}
	u.logger.Emit(ctx, events.Event{
		Level: level, Type: eventType, Container: name,
		FromVersion: rec.CurrentImage, ToVersion: rec.PreviousImage, Actor: actor, ActorID: actorID,
		Message: fmt.Sprintf("rolling back %s: %s -> %s", name, rec.CurrentImage, rec.PreviousImage),
	})

	result, err := u.recreate(ctx, containerID, before, rec.PreviousImage)
	if err != nil {
		u.logger.Emit(ctx, events.Event{
			Level: events.LevelError, Type: eventType + ".failed", Container: name,
			FromVersion: rec.CurrentImage, ToVersion: rec.PreviousImage, Actor: actor, ActorID: actorID,
			Message: fmt.Sprintf("rollback failed for %s, reverting to current image: %v", name, err),
		})
		if !result.originalRemoved {
			return u.failBeforeRemoval(ctx, eventType, containerID, result, before, name, actor, actorID, err)
		}
		return u.revert(ctx, eventType, containerID, result.ID, before, name, actor, actorID, err)
	}

	// CurrentImage is left unchanged, same as Update(): it's the stable
	// compose-defined reference (a tag, e.g. "grav:latest"), which doesn't
	// change across an update/rollback cycle — only the digest actually
	// running under it does. PreviousImage becomes before.Image, the
	// image ID of whatever is being retired by this rollback right now
	// (mirroring how Update() captures its own previousImage the same
	// way) — a rollback can itself be undone the same way an update can.
	// Earlier this stored rec.CurrentImage (the tag) as PreviousImage and
	// rec.PreviousImage (a digest) as CurrentImage, which swapped a tag
	// and a digest into the wrong fields — CurrentImage would display as
	// a raw sha256 string next to the container name, and the "previous
	// image" chip would show a tag instead of a digest.
	watchStatus := "manual_rollback"
	if actor == events.ActorSystem {
		watchStatus = "automatic_rollback"
	}
	if err := u.store.ReplaceContainerAndCompleteRollback(ctx, containerID, store.ContainerRecord{
		ID: result.ID, Name: name, Stack: stackOf(before.Config.Labels),
		State: result.State, Status: result.Status, CurrentImage: rec.CurrentImage, PreviousImage: before.Image,
	}, watchStatus); err != nil {
		commitErr := fmt.Errorf("record rolled-back container: %w", err)
		u.logger.Emit(ctx, events.Event{
			Level: events.LevelError, Type: eventType + ".commit_failed", Container: name,
			FromVersion: rec.CurrentImage, ToVersion: rec.PreviousImage, Actor: actor, ActorID: actorID,
			Message: fmt.Sprintf("rollback recreated %s but its durable commit failed; restoring the pre-rollback container: %v", name, commitErr),
		})
		return u.revert(ctx, eventType, containerID, result.ID, before, name, actor, actorID, commitErr)
	}
	// ReplaceContainer always resets update_available to false — correct
	// for Update() (nothing newer is pending right after pulling the
	// latest content), wrong for Rollback: PreviousImage now holds
	// exactly the image just abandoned, which is by definition available
	// to switch back to, so the Update button should reappear alongside
	// Rollback rather than both being hidden. Best-effort: a failure here
	// only means a stale "no update" chip until the next periodic check,
	// not a reason to fail the rollback itself.
	if err := u.store.SetUpdateAvailable(ctx, result.ID, true); err != nil {
		u.logger.Emit(ctx, events.Event{
			Level: events.LevelWarn, Type: "container.sync_failed", Container: name,
			Actor: actor, ActorID: actorID,
			Message: fmt.Sprintf("rolled back %s but could not mark an update available again: %v", name, err),
		})
	}

	msg := fmt.Sprintf("%s rolled back to %s", name, rec.PreviousImage)
	u.logger.Emit(ctx, events.Event{
		Level: events.LevelInfo, Type: eventType + ".succeeded", Container: name,
		FromVersion: rec.CurrentImage, ToVersion: rec.PreviousImage, Actor: actor, ActorID: actorID,
		Message: msg,
	})
	// docs/notifications.md: automatic rollback can never be silenced at
	// the event-type level — Channel.matches special-cases "rollback.auto"
	// to bypass a channel's Events filter (it still respects the
	// stack/container scope filter, per an explicit maintainer decision).
	u.notifier.Notify(ctx, eventType+".succeeded", name, stackOf(before.Config.Labels), msg)
	trigger := "manual"
	if actor == events.ActorSystem {
		trigger = "auto"
	}
	metrics.RollbacksTotal.WithLabelValues(stackOf(before.Config.Labels), trigger).Inc()
	return nil
}
