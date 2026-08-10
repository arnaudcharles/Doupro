// Package events is the single place that emits structured log lines to
// stdout and (once internal/store exists) persists the same events for the
// in-app Logs page, with centralized secret redaction. No other package
// writes to stdout or the events table directly. See docs/logs.md for the
// field schema this must stay in sync with.
package events

import (
	"context"
	"log/slog"
	"os"
	"strings"
)

// Level mirrors the severities documented in docs/logs.md.
type Level = slog.Level

const (
	LevelDebug = slog.LevelDebug
	LevelInfo  = slog.LevelInfo
	LevelWarn  = slog.LevelWarn
	LevelError = slog.LevelError
)

// Actor identifies who or what triggered an event.
type Actor string

const (
	ActorSystem   Actor = "system"
	ActorUser     Actor = "user"
	ActorSchedule Actor = "schedule"
)

// Event is the structured record documented in docs/logs.md. Fields left
// at their zero value are omitted from the emitted JSON line.
type Event struct {
	Level       Level
	Type        string // "<domain>.<action>", e.g. "update.succeeded"
	Container   string
	Stack       string
	FromVersion string
	ToVersion   string
	Actor       Actor
	ActorID     string
	RequestID   string
	Message     string
	Metadata    map[string]any
}

// redactedKeys lists metadata keys whose values are never logged in the
// clear, regardless of caller intent. Keep this in sync with the secret
// handling rules in SECURITY.md.
var redactedKeys = map[string]struct{}{
	"password": {}, "token": {}, "api_key": {}, "apikey": {},
	"secret": {}, "credential": {}, "webhook_url": {},
}

// Sink persists an event alongside the stdout line (see docs/logs.md —
// events are written to both). internal/store.Store implements this.
type Sink interface {
	InsertEvent(ctx context.Context, e Event) error
}

// Logger emits events to stdout as single-line JSON and is safe for
// concurrent use.
type Logger struct {
	slog *slog.Logger
	sink Sink
}

// New builds a Logger writing to stdout at the given minimum level.
// levelName accepts "debug", "info", "warn" or "error" (see
// DOUPRO_LOG_LEVEL in .env.example); unrecognized values fall back to info.
// It has no persistence sink until SetSink is called — useful during the
// startup window before the store is open.
func New(levelName string) *Logger {
	handler := slog.NewJSONHandler(os.Stdout, &slog.HandlerOptions{
		Level: parseLevel(levelName),
	})
	return &Logger{slog: slog.New(handler)}
}

// SetSink attaches (or replaces) the persistence sink. Safe to call once
// the store is ready; events emitted before this call are still written
// to stdout, just not persisted.
func (l *Logger) SetSink(sink Sink) {
	l.sink = sink
}

// Emit writes one event to stdout and, if a sink is attached, persists it.
// It never returns an error: a logging failure must not interrupt the
// operation being logged — a persistence failure is itself logged to
// stdout instead of propagated.
func (l *Logger) Emit(ctx context.Context, e Event) {
	if l.sink != nil {
		if err := l.sink.InsertEvent(ctx, e); err != nil {
			l.slog.Error("failed to persist event", slog.String("error", err.Error()))
		}
	}
	attrs := make([]any, 0, 12)
	attrs = append(attrs, slog.String("event", e.Type))

	if e.Container != "" {
		attrs = append(attrs, slog.String("container", e.Container))
	}
	if e.Stack != "" {
		attrs = append(attrs, slog.String("stack", e.Stack))
	}
	if e.FromVersion != "" {
		attrs = append(attrs, slog.String("from_version", e.FromVersion))
	}
	if e.ToVersion != "" {
		attrs = append(attrs, slog.String("to_version", e.ToVersion))
	}
	if e.Actor != "" {
		attrs = append(attrs, slog.String("actor", string(e.Actor)))
	}
	if e.ActorID != "" {
		attrs = append(attrs, slog.String("actor_id", e.ActorID))
	}
	if e.RequestID != "" {
		attrs = append(attrs, slog.String("request_id", e.RequestID))
	}
	for k, v := range redact(e.Metadata) {
		attrs = append(attrs, slog.Any("metadata."+k, v))
	}

	l.slog.LogAttrs(ctx, e.Level, e.Message, toAttrs(attrs)...)
}

func toAttrs(kvs []any) []slog.Attr {
	attrs := make([]slog.Attr, 0, len(kvs))
	for _, v := range kvs {
		if a, ok := v.(slog.Attr); ok {
			attrs = append(attrs, a)
		}
	}
	return attrs
}

// redact returns a copy of metadata with known-sensitive keys replaced by
// "[REDACTED]", so a call site that forgets to scrub a secret can't leak
// it through the event log (see SECURITY.md).
func redact(metadata map[string]any) map[string]any {
	if metadata == nil {
		return nil
	}
	out := make(map[string]any, len(metadata))
	for k, v := range metadata {
		if _, sensitive := redactedKeys[strings.ToLower(k)]; sensitive {
			out[k] = "[REDACTED]"
			continue
		}
		out[k] = v
	}
	return out
}

func parseLevel(name string) slog.Level {
	switch strings.ToLower(name) {
	case "debug":
		return LevelDebug
	case "warn", "warning":
		return LevelWarn
	case "error":
		return LevelError
	default:
		return LevelInfo
	}
}
