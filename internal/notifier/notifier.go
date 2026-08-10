// Package notifier turns domain events into outbound notifications via
// containrrr/shoutrrr's unified URL scheme (Telegram, Discord, Slack,
// ntfy, Gotify, generic webhooks, ...), and records every delivery
// attempt — sent or failed. See docs/notifications.md.
//
// Channel configuration is stored as JSON under the
// "notifications.channels" settings key rather than its own table: it's a
// small, cohesive blob edited as a whole from Settings, not queried
// piecemeal. Quiet hours (docs/settings.md) aren't implemented yet.
package notifier

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"slices"
	"strings"
	"time"

	"github.com/containrrr/shoutrrr"

	"github.com/arnaudcharles/doupro/internal/events"
	"github.com/arnaudcharles/doupro/internal/metrics"
	"github.com/arnaudcharles/doupro/internal/store"
)

const channelsSettingsKey = "notifications.channels"

// Category is one of the 8 event categories documented in the "Event
// types" table of docs/notifications.md — the granularity a channel's
// Events filter is expressed in. The event types actually emitted via
// Notify are finer-grained ("update.succeeded", "rollback.auto.failed",
// "rollback.manual.revert_failed", ...), since the operation/outcome
// split matters for logging and metrics; categorize() collapses a raw
// event type down to the one category the UI shows a checkbox for.
type Category string

const (
	CategoryUpdateAvailable Category = "update.available"
	CategoryUpdateApplied   Category = "update.applied"
	CategoryUpdateFailed    Category = "update.failed"
	CategoryRollbackAuto    Category = "rollback.auto"
	CategoryRollbackManual  Category = "rollback.manual"
	CategoryScheduleChanged Category = "schedule.changed"
	CategoryPolicyExecuted  Category = "schedule.executed"
	CategoryCheckFailed     Category = "check.failed"
)

// categorize maps a raw Notify eventType to the Category its channel
// filter checkbox lives under. Falls back to eventType itself for
// anything unrecognized, so a future event type without a mapping here
// still round-trips through an exact-match filter rather than silently
// matching nothing.
func categorize(eventType string) Category {
	switch {
	case eventType == "update.available":
		return CategoryUpdateAvailable
	case eventType == "update.succeeded":
		return CategoryUpdateApplied
	case strings.HasPrefix(eventType, "update."):
		return CategoryUpdateFailed // update.failed, update.revert_failed
	case strings.HasPrefix(eventType, "rollback.auto"):
		return CategoryRollbackAuto
	case strings.HasPrefix(eventType, "rollback.manual"):
		return CategoryRollbackManual
	case eventType == "schedule.created", eventType == "schedule.modified", eventType == "schedule.cancelled":
		return CategoryScheduleChanged
	case eventType == "schedule.executed":
		return CategoryPolicyExecuted
	case eventType == "check.failed":
		return CategoryCheckFailed
	default:
		return Category(eventType)
	}
}

// Channel is one configured notification destination: Name is a
// human label shown in the UI ("Telegram — Username"), URL is a shoutrrr
// service URL.
//
// Events, Stacks, and Containers are independent filters, all empty by
// default (a freshly added channel receives everything, matching the
// behavior before these filters existed):
//   - Events: which Category values this channel receives, as strings.
//     Empty means every category. CategoryRollbackAuto is documented as
//     never silenceable at the event-type level (docs/notifications.md)
//     — enforced in matches by always treating it as included regardless
//     of this list.
//   - Stacks / Containers: which containers this channel cares about,
//     matched as a union (OR) — a container is in scope if its stack is
//     in Stacks OR its name is in Containers. Both empty means every
//     container (unscoped). Events with no specific container (e.g. a
//     stack-wide "recurring policy executed" summary, or none at all)
//     always pass the scope filter, since there is nothing to match
//     against.
type Channel struct {
	Name       string   `json:"name"`
	URL        string   `json:"url,omitempty"`
	Events     []string `json:"events,omitempty"`
	Stacks     []string `json:"stacks,omitempty"`
	Containers []string `json:"containers,omitempty"`
	Configured bool     `json:"configured,omitempty"`
	Provider   string   `json:"provider,omitempty"`
}

// matches reports whether ch should receive an event of type eventType
// for the given container/stack (either may be empty — a policy-level
// notification has a stack but no single container, for instance).
func (ch Channel) matches(eventType, container, stack string) bool {
	category := categorize(eventType)
	if len(ch.Events) > 0 && category != CategoryRollbackAuto && !slices.Contains(ch.Events, string(category)) {
		return false
	}
	if len(ch.Stacks) == 0 && len(ch.Containers) == 0 {
		return true
	}
	if container == "" && stack == "" {
		return true
	}
	if stack != "" && slices.Contains(ch.Stacks, stack) {
		return true
	}
	if container != "" && slices.Contains(ch.Containers, container) {
		return true
	}
	return false
}

// Notifier dispatches events to every configured channel.
type Notifier struct {
	store        *store.Store
	logger       *events.Logger
	send         func(string, string) error
	pollInterval time.Duration
	lease        time.Duration
	baseBackoff  time.Duration
	maxBackoff   time.Duration
}

// New builds a Notifier backed by st for channel config and the
// notification log.
func New(st *store.Store, loggers ...*events.Logger) *Notifier {
	var logger *events.Logger
	if len(loggers) > 0 {
		logger = loggers[0]
	}
	return &Notifier{store: st, logger: logger, send: shoutrrr.Send,
		pollInterval: time.Second, lease: 2 * time.Minute,
		baseBackoff: time.Minute, maxBackoff: time.Hour}
}

// Channels returns the currently configured channels.
func (n *Notifier) Channels(ctx context.Context) ([]Channel, error) {
	raw, ok, err := n.store.GetSecretSetting(ctx, channelsSettingsKey)
	if err != nil {
		return nil, err
	}
	if !ok || raw == "" {
		return nil, nil
	}
	var channels []Channel
	if err := json.Unmarshal([]byte(raw), &channels); err != nil {
		return nil, fmt.Errorf("parse stored notification channels: %w", err)
	}
	return channels, nil
}

// ErrChannelNeedsURL is returned by SetChannels when an entry has no URL
// of its own and none can be carried over from an existing channel of the
// same name — see the doc comment on SetChannels below. Callers (the API
// handler) use errors.Is to report this as a 400 the caller can act on,
// distinct from a genuine persistence failure (500).
var ErrChannelNeedsURL = errors.New("channel needs a URL")

// SetChannels replaces the configured channels wholesale. An entry with
// no URL of its own inherits an existing channel's URL, tried two ways so
// the web UI's Modify flow can save a rename (any provider, not just one)
// without ever handling the write-only URL itself (see PublicChannels):
//
//  1. By Name — the common case, unaffected by a rename since the name
//     didn't change.
//  2. By position — when (1) misses and the incoming list is the same
//     length as what's stored, position i is assumed to be the same
//     channel as existing[i] renamed in place, exactly what a Modify
//     submission looks like (the full current list, one entry mutated,
//     nothing reordered). A length change (add/remove) skips this, since
//     position no longer means "same channel."
//
// A request that still can't resolve a URL this way — a genuinely new
// channel with no destination, or a rename combined with a reorder or a
// count change — fails with ErrChannelNeedsURL rather than silently
// dropping the channel's destination.
func (n *Notifier) SetChannels(ctx context.Context, channels []Channel) error {
	existing, err := n.Channels(ctx)
	if err != nil {
		return err
	}
	byName := make(map[string]string, len(existing))
	for _, channel := range existing {
		byName[channel.Name] = channel.URL
	}
	samePositions := len(channels) == len(existing)
	for i := range channels {
		if channels[i].URL == "" {
			channels[i].URL = byName[channels[i].Name]
		}
		if channels[i].URL == "" && samePositions {
			channels[i].URL = existing[i].URL
		}
		if channels[i].URL == "" {
			return fmt.Errorf("%w: %q (fill in the destination fields to set one)", ErrChannelNeedsURL, channels[i].Name)
		}
		channels[i].Configured = false
		channels[i].Provider = ""
	}
	b, err := json.Marshal(channels)
	if err != nil {
		return fmt.Errorf("marshal notification channels: %w", err)
	}
	return n.store.SetSecretSetting(ctx, channelsSettingsKey, string(b))
}

// PublicChannels is safe for API and HTML responses: it preserves filters
// and identity while never returning the credential-bearing URL.
func (n *Notifier) PublicChannels(ctx context.Context) ([]Channel, error) {
	channels, err := n.Channels(ctx)
	if err != nil {
		return nil, err
	}
	for i := range channels {
		channels[i].Provider = strings.SplitN(channels[i].URL, "://", 2)[0]
		channels[i].URL = ""
		channels[i].Configured = true
	}
	return channels, nil
}

func (n *Notifier) TestChannel(ctx context.Context, name, rawURL string) error {
	if rawURL != "" {
		return shoutrrr.Send(rawURL, "DoUpRo test notification — if you can read this, the channel works.")
	}
	channels, err := n.Channels(ctx)
	if err != nil {
		return err
	}
	for _, channel := range channels {
		if channel.Name == name {
			return shoutrrr.Send(channel.URL, "DoUpRo test notification — if you can read this, the channel works.")
		}
	}
	return fmt.Errorf("notification channel %q not found", name)
}

// Notify durably enqueues message for every configured channel whose filters
// match. Delivery is at-least-once and happens asynchronously in Run.
// target is usually a container name but may be empty for a policy-level
// event with no single container (e.g. a schedule lifecycle change);
// stack is empty when the event isn't scoped to a stack at all. Never
// returns an error — a notification failure must not interrupt the
// operation that triggered it (docs/notifications.md: delivery failure
// never blocks the underlying action).
func (n *Notifier) Notify(ctx context.Context, eventType, target, stack, message string) {
	channels, err := n.Channels(ctx)
	if err != nil || len(channels) == 0 {
		return
	}

	for _, ch := range channels {
		if !ch.matches(eventType, target, stack) {
			continue
		}
		id, enqueueErr := n.store.EnqueueNotification(ctx, store.NotificationJob{
			Event: eventType, Target: target, Stack: stack, Channel: ch.Name,
			ChannelURL: ch.URL, Message: message,
		})
		if enqueueErr != nil {
			n.emit(ctx, events.LevelError, "notification.enqueue_failed", target, stack, ch.Name, 0, enqueueErr.Error())
			continue
		}
		n.emit(ctx, events.LevelInfo, "notification.queued", target, stack, ch.Name, id, "notification queued")
	}
}

// Run delivers queued notifications until ctx is cancelled. A job whose
// processing lease expires is automatically reclaimed after a process crash.
func (n *Notifier) Run(ctx context.Context) {
	ticker := time.NewTicker(n.pollInterval)
	defer ticker.Stop()
	for {
		worked, err := n.deliverOne(ctx)
		if err != nil {
			n.emit(ctx, events.LevelError, "notification.worker_failed", "", "", "", 0, err.Error())
		}
		if worked {
			continue
		}
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
		}
	}
}

func (n *Notifier) deliverOne(ctx context.Context) (bool, error) {
	job, err := n.store.ClaimNotification(ctx, time.Now(), n.lease)
	if err != nil || job == nil {
		return false, err
	}
	sendErr := n.send(job.ChannelURL, job.Message)
	status, errText := "sent", ""
	if sendErr == nil {
		now := time.Now().UTC()
		if err := n.store.CompleteNotification(ctx, job.ID, now); err != nil {
			return true, err
		}
		n.emit(ctx, events.LevelInfo, "notification.sent", job.Target, job.Stack, job.Channel, job.ID, "notification delivered")
	} else {
		status, errText = "failed", sendErr.Error()
		dead := job.Attempts >= job.MaxAttempts
		retryAt := time.Now().UTC().Add(n.backoff(job.Attempts))
		if err := n.store.FailNotification(ctx, job.ID, retryAt, errText, dead); err != nil {
			return true, err
		}
		if dead {
			metrics.NotificationDeadLettersTotal.WithLabelValues(job.Channel).Inc()
			n.emit(ctx, events.LevelError, "notification.dead_lettered", job.Target, job.Stack, job.Channel, job.ID, errText)
		} else {
			metrics.NotificationRetriesTotal.WithLabelValues(job.Channel).Inc()
			n.emit(ctx, events.LevelWarn, "notification.retry_scheduled", job.Target, job.Stack, job.Channel, job.ID, errText)
		}
	}
	_ = n.store.InsertNotification(ctx, store.NotificationRecord{
		Event: job.Event, Target: job.Target, Channel: job.Channel,
		Status: status, Error: errText, Message: job.Message,
	})
	metrics.NotificationsTotal.WithLabelValues(job.Channel, status).Inc()
	return true, nil
}

func (n *Notifier) backoff(attempt int) time.Duration {
	seconds := float64(n.baseBackoff) * math.Pow(2, float64(max(attempt-1, 0)))
	d := time.Duration(seconds)
	if d > n.maxBackoff {
		return n.maxBackoff
	}
	return d
}

func (n *Notifier) emit(ctx context.Context, level events.Level, kind, target, stack, channel string, id int64, message string) {
	if n.logger == nil {
		return
	}
	n.logger.Emit(ctx, events.Event{Level: level, Type: kind, Container: target, Stack: stack,
		Actor: events.ActorSystem, Message: message, Metadata: map[string]any{"channel": channel, "queue_id": id}})
}
