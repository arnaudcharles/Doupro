package store

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"time"
)

const (
	DefaultCrashLoopThreshold = 3
	DefaultCrashLoopWindow    = 10 * time.Minute
)

type RollbackWatch struct {
	ContainerName        string     `json:"container_name"`
	ContainerID          string     `json:"container_id"`
	UpdateAppliedAt      time.Time  `json:"update_applied_at"`
	WindowExpiresAt      time.Time  `json:"window_expires_at"`
	CrashCount           int        `json:"crash_count"`
	Threshold            int        `json:"threshold"`
	ObservedRestartCount int        `json:"observed_restart_count"`
	LastEventNano        int64      `json:"-"`
	LastCrashAt          *time.Time `json:"last_crash_at,omitempty"`
	TriggeredAt          *time.Time `json:"triggered_at,omitempty"`
	Status               string     `json:"status"`
}

func (s *Store) ArmRollbackWatch(ctx context.Context, containerName, containerID string, appliedAt time.Time, window time.Duration, threshold, restartCount int) error {
	now := time.Now().UTC()
	_, err := s.db.ExecContext(ctx, `
INSERT INTO rollback_watches
  (container_name, container_id, update_applied_at, window_expires_at, crash_count, threshold,
   observed_restart_count, last_event_nano, status, updated_at)
VALUES (?, ?, ?, ?, 0, ?, ?, 0, 'active', ?)
ON CONFLICT(container_name) DO UPDATE SET
  container_id = excluded.container_id,
  update_applied_at = excluded.update_applied_at,
  window_expires_at = excluded.window_expires_at,
  crash_count = 0,
  threshold = excluded.threshold,
  observed_restart_count = excluded.observed_restart_count,
  last_event_nano = 0,
  last_crash_at = NULL,
  triggered_at = NULL,
  status = 'active',
  updated_at = excluded.updated_at;`,
		containerName, containerID, appliedAt.UTC().Format(time.RFC3339Nano),
		appliedAt.Add(window).UTC().Format(time.RFC3339Nano), threshold, restartCount,
		now.Format(time.RFC3339Nano))
	if err != nil {
		return fmt.Errorf("arm rollback watch for %s: %w", containerName, err)
	}
	return nil
}

func (s *Store) GetRollbackWatch(ctx context.Context, name string) (RollbackWatch, error) {
	return scanRollbackWatch(s.db.QueryRowContext(ctx, `
SELECT container_name, container_id, update_applied_at, window_expires_at,
       crash_count, threshold, observed_restart_count, last_event_nano, last_crash_at,
       triggered_at, status
FROM rollback_watches WHERE container_name = ?;`, name))
}

func (s *Store) ListActiveRollbackWatches(ctx context.Context) ([]RollbackWatch, error) {
	return s.listRollbackWatches(ctx, "active")
}

func (s *Store) ListTriggeredRollbackWatches(ctx context.Context) ([]RollbackWatch, error) {
	return s.listRollbackWatches(ctx, "triggered")
}

func (s *Store) listRollbackWatches(ctx context.Context, status string) ([]RollbackWatch, error) {
	rows, err := s.db.QueryContext(ctx, `
SELECT container_name, container_id, update_applied_at, window_expires_at,
       crash_count, threshold, observed_restart_count, last_event_nano, last_crash_at,
       triggered_at, status
FROM rollback_watches WHERE status = ? ORDER BY update_applied_at;`, status)
	if err != nil {
		return nil, fmt.Errorf("list active rollback watches: %w", err)
	}
	defer rows.Close() //nolint:errcheck // read-only query, rows drained fully below
	var out []RollbackWatch
	for rows.Next() {
		watch, err := scanRollbackWatch(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, watch)
	}
	return out, rows.Err()
}

type rowScanner interface{ Scan(...any) error }

func scanRollbackWatch(row rowScanner) (RollbackWatch, error) {
	var w RollbackWatch
	var applied, expires string
	var lastCrash, triggered sql.NullString
	err := row.Scan(&w.ContainerName, &w.ContainerID, &applied, &expires,
		&w.CrashCount, &w.Threshold, &w.ObservedRestartCount, &w.LastEventNano, &lastCrash,
		&triggered, &w.Status)
	if errors.Is(err, sql.ErrNoRows) {
		return RollbackWatch{}, ErrNotFound
	}
	if err != nil {
		return RollbackWatch{}, fmt.Errorf("scan rollback watch: %w", err)
	}
	w.UpdateAppliedAt, _ = time.Parse(time.RFC3339Nano, applied)
	w.WindowExpiresAt, _ = time.Parse(time.RFC3339Nano, expires)
	if lastCrash.Valid {
		t, _ := time.Parse(time.RFC3339Nano, lastCrash.String)
		w.LastCrashAt = &t
	}
	if triggered.Valid {
		t, _ := time.Parse(time.RFC3339Nano, triggered.String)
		w.TriggeredAt = &t
	}
	return w, nil
}

// RecordCrash durably increments a watch and atomically claims the threshold
// transition. Duplicate/replayed Docker events and expired watches are not
// counted. triggered is true for exactly one caller.
func (s *Store) RecordCrash(ctx context.Context, name, containerID string, eventNano int64, at time.Time) (watch RollbackWatch, counted, triggered bool, err error) {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return watch, false, false, err
	}
	defer tx.Rollback() //nolint:errcheck
	watch, err = scanRollbackWatch(tx.QueryRowContext(ctx, `
SELECT container_name, container_id, update_applied_at, window_expires_at,
       crash_count, threshold, observed_restart_count, last_event_nano, last_crash_at,
       triggered_at, status FROM rollback_watches WHERE container_name = ?;`, name))
	if err != nil {
		return watch, false, false, err
	}
	if watch.Status != "active" || watch.ContainerID != containerID || !at.Before(watch.WindowExpiresAt) || eventNano <= watch.LastEventNano {
		return watch, false, false, tx.Commit()
	}
	watch.CrashCount++
	watch.ObservedRestartCount++
	watch.LastEventNano = eventNano
	watch.LastCrashAt = &at
	status := "active"
	var triggeredAt any
	if watch.CrashCount >= watch.Threshold {
		status = "triggered"
		watch.Status = status
		watch.TriggeredAt = &at
		triggeredAt = at.UTC().Format(time.RFC3339Nano)
		triggered = true
	}
	_, err = tx.ExecContext(ctx, `UPDATE rollback_watches SET crash_count=?, observed_restart_count=?, last_event_nano=?,
last_crash_at=?, triggered_at=?, status=?, updated_at=? WHERE container_name=?;`,
		watch.CrashCount, watch.ObservedRestartCount, eventNano, at.UTC().Format(time.RFC3339Nano), triggeredAt,
		status, time.Now().UTC().Format(time.RFC3339Nano), name)
	if err != nil {
		return watch, false, false, err
	}
	return watch, true, triggered, tx.Commit()
}

func (s *Store) SetRollbackWatchRestartCount(ctx context.Context, name string, count int) error {
	_, err := s.db.ExecContext(ctx, `UPDATE rollback_watches SET observed_restart_count=?, updated_at=? WHERE container_name=?;`, count, time.Now().UTC().Format(time.RFC3339Nano), name)
	return err
}

func (s *Store) CompleteRollbackWatch(ctx context.Context, name, status string) error {
	_, err := s.db.ExecContext(ctx, `UPDATE rollback_watches SET status=?, updated_at=? WHERE container_name=?;`, status, time.Now().UTC().Format(time.RFC3339Nano), name)
	return err
}

func (s *Store) ExpireRollbackWatches(ctx context.Context, now time.Time) (int64, error) {
	res, err := s.db.ExecContext(ctx, `UPDATE rollback_watches SET status='expired', updated_at=? WHERE status='active' AND window_expires_at <= ?;`, now.UTC().Format(time.RFC3339Nano), now.UTC().Format(time.RFC3339Nano))
	if err != nil {
		return 0, err
	}
	return res.RowsAffected()
}

func (s *Store) CountActiveRollbackWatches(ctx context.Context) (int, error) {
	var count int
	err := s.db.QueryRowContext(ctx, `SELECT COUNT(*) FROM rollback_watches WHERE status='active';`).Scan(&count)
	return count, err
}
