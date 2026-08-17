package store

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"time"

	"github.com/arnaudcharles/doupro/internal/events"
)

// InsertEvent persists one structured event, making Store an
// events.Sink. See docs/logs.md for the field schema this mirrors.
func (s *Store) InsertEvent(ctx context.Context, e events.Event) error {
	metadataJSON, err := json.Marshal(e.Metadata)
	if err != nil {
		return fmt.Errorf("marshal event metadata: %w", err)
	}

	const q = `
INSERT INTO events (timestamp, level, event, container, stack, from_version,
                     to_version, actor, actor_id, request_id, message, metadata)
VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?);`

	_, err = s.db.ExecContext(ctx, q,
		time.Now().UTC().Format(time.RFC3339Nano),
		e.Level.String(), e.Type, e.Container, e.Stack,
		e.FromVersion, e.ToVersion, string(e.Actor), e.ActorID, e.RequestID,
		e.Message, string(metadataJSON),
	)
	if err != nil {
		return fmt.Errorf("insert event: %w", err)
	}
	return nil
}

// EventRecord is a persisted event as returned by ListEvents. See
// docs/logs.md for field meanings.
type EventRecord struct {
	ID          int64
	Timestamp   time.Time
	Level       string
	Type        string
	Container   string
	Stack       string
	FromVersion string
	ToVersion   string
	Actor       string
	ActorID     string
	RequestID   string
	Message     string
	Metadata    map[string]any
}

// EventFilter narrows ListEvents. Zero values mean "no filter" on that
// field. Limit is clamped to [1, MaxLogsPageSize], defaulting to
// DefaultLogsPageSize.
type EventFilter struct {
	EventType string
	Container string
	Level     string
	Limit     int
}

// escapeLike escapes SQL LIKE wildcards (% and _) in user-supplied filter
// text so they're matched literally instead of acting as pattern
// metacharacters, before the caller wraps the result in its own %...%.
func escapeLike(s string) string {
	r := strings.NewReplacer(`\`, `\\`, "%", `\%`, "_", `\_`)
	return r.Replace(s)
}

// ListEvents returns events newest-first, matching the filters the Logs
// page and GET /api/v1/logs expose (see docs/logs.md).
func (s *Store) ListEvents(ctx context.Context, f EventFilter) ([]EventRecord, error) {
	limit := f.Limit
	if limit <= 0 {
		limit = DefaultLogsPageSize
	}
	if limit > MaxLogsPageSize {
		limit = MaxLogsPageSize
	}

	q := strings.Builder{}
	q.WriteString(`SELECT id, timestamp, level, event, container, stack, from_version,
	                       to_version, actor, actor_id, request_id, message, metadata
	                FROM events WHERE 1=1`)
	args := make([]any, 0, 4)

	if f.EventType != "" {
		q.WriteString(" AND event LIKE ? ESCAPE '\\'")
		args = append(args, "%"+escapeLike(f.EventType)+"%")
	}
	if f.Container != "" {
		q.WriteString(" AND container LIKE ? ESCAPE '\\'")
		args = append(args, "%"+escapeLike(f.Container)+"%")
	}
	if f.Level != "" {
		q.WriteString(" AND level = ?")
		args = append(args, f.Level)
	}
	q.WriteString(" ORDER BY id DESC LIMIT ?")
	args = append(args, limit)

	rows, err := s.db.QueryContext(ctx, q.String(), args...)
	if err != nil {
		return nil, fmt.Errorf("list events: %w", err)
	}
	defer rows.Close() //nolint:errcheck // read-only query, rows drained fully below

	var out []EventRecord
	for rows.Next() {
		var r EventRecord
		var ts, metadataJSON string
		if err := rows.Scan(&r.ID, &ts, &r.Level, &r.Type, &r.Container, &r.Stack,
			&r.FromVersion, &r.ToVersion, &r.Actor, &r.ActorID, &r.RequestID,
			&r.Message, &metadataJSON); err != nil {
			return nil, fmt.Errorf("scan event: %w", err)
		}
		r.Timestamp, _ = time.Parse(time.RFC3339Nano, ts)
		if metadataJSON != "" {
			_ = json.Unmarshal([]byte(metadataJSON), &r.Metadata)
		}
		out = append(out, r)
	}
	return out, rows.Err()
}
