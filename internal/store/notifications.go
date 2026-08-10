package store

import (
	"context"
	"fmt"
	"time"
)

// NotificationRecord is one delivery attempt, sent or failed — every
// attempt is recorded, not just successes (docs/notifications.md).
type NotificationRecord struct {
	ID        int64
	Timestamp time.Time
	Event     string
	Target    string
	Channel   string
	Status    string // "sent" | "failed"
	Error     string
	Message   string
}

// InsertNotification records one delivery attempt.
func (s *Store) InsertNotification(ctx context.Context, n NotificationRecord) error {
	const q = `
INSERT INTO notifications (timestamp, event, target, channel, status, error, message)
VALUES (?, ?, ?, ?, ?, ?, ?);`
	_, err := s.db.ExecContext(ctx, q, time.Now().UTC().Format(time.RFC3339Nano),
		n.Event, n.Target, n.Channel, n.Status, n.Error, n.Message)
	if err != nil {
		return fmt.Errorf("insert notification: %w", err)
	}
	return nil
}

// ListNotifications returns delivery attempts, newest first, up to limit
// (default/max applied by the caller — see internal/api).
func (s *Store) ListNotifications(ctx context.Context, limit int) ([]NotificationRecord, error) {
	if limit <= 0 {
		limit = 100
	}
	const q = `
SELECT id, timestamp, event, target, channel, status, error, message
FROM notifications ORDER BY id DESC LIMIT ?;`

	rows, err := s.db.QueryContext(ctx, q, limit)
	if err != nil {
		return nil, fmt.Errorf("list notifications: %w", err)
	}
	defer rows.Close() //nolint:errcheck // read-only query, rows drained fully below

	var out []NotificationRecord
	for rows.Next() {
		var n NotificationRecord
		var ts string
		if err := rows.Scan(&n.ID, &ts, &n.Event, &n.Target, &n.Channel, &n.Status, &n.Error, &n.Message); err != nil {
			return nil, fmt.Errorf("scan notification: %w", err)
		}
		n.Timestamp, _ = time.Parse(time.RFC3339Nano, ts)
		out = append(out, n)
	}
	return out, rows.Err()
}
