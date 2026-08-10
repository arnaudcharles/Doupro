package store

import (
	"context"
	"database/sql"
	"fmt"
	"time"
)

// Schedule is a one-off or recurring update job, as described in
// docs/schedule.md. Which fields are meaningful depends on Kind:
//
//   - "once": RunAt and PinnedImage are set; fires exactly once against
//     ContainerName.
//   - "cron": CronExpr is set; targets ContainerName or Stack.
//   - "relative": RelativePolicy ("immediate"|"delayed") and, for
//     "delayed", RelativeAfter (e.g. "24h") are set; targets ContainerName
//     or Stack.
type Schedule struct {
	ID             int64
	Kind           string
	ContainerName  string
	Stack          string
	RunAt          *time.Time
	CronExpr       string
	RelativePolicy string
	RelativeAfter  string
	SemverPolicy   string
	PinnedImage    string
	Notify         bool
	Enabled        bool
	LastRunAt      *time.Time
	NextRunAt      *time.Time
	CreatedAt      time.Time
}

// Completed reports whether a one-off schedule has already fired. A disabled
// one-off with no LastRunAt was merely paused/cancelled before execution and is
// therefore not completed.
func (sc Schedule) Completed() bool {
	return sc.Kind == "once" && sc.LastRunAt != nil
}

// CreateSchedule inserts a new schedule and returns its ID.
func (s *Store) CreateSchedule(ctx context.Context, sc Schedule) (int64, error) {
	const q = `
INSERT INTO schedules (kind, container_id, stack, run_at, cron_expr, relative_policy,
                        relative_after, semver_policy, pinned_image, notify, enabled, next_run_at, created_at)
VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?);`

	res, err := s.db.ExecContext(ctx, q,
		sc.Kind, sc.ContainerName, sc.Stack, formatTimePtr(sc.RunAt), sc.CronExpr,
		sc.RelativePolicy, sc.RelativeAfter, sc.SemverPolicy, sc.PinnedImage, boolInt(sc.Notify), boolInt(sc.Enabled),
		formatTimePtr(sc.NextRunAt), time.Now().UTC().Format(time.RFC3339Nano),
	)
	if err != nil {
		return 0, fmt.Errorf("create schedule: %w", err)
	}
	return res.LastInsertId()
}

// ListSchedules returns every schedule, most recently created first.
func (s *Store) ListSchedules(ctx context.Context) ([]Schedule, error) {
	const q = `
SELECT id, kind, container_id, stack, run_at, cron_expr, relative_policy, relative_after, semver_policy,
       pinned_image, notify, enabled, last_run_at, next_run_at, created_at
FROM schedules ORDER BY id DESC;`

	rows, err := s.db.QueryContext(ctx, q)
	if err != nil {
		return nil, fmt.Errorf("list schedules: %w", err)
	}
	defer rows.Close() //nolint:errcheck // read-only query, rows drained fully below
	return scanSchedules(rows)
}

// GetSchedule returns one schedule by ID, or ErrNotFound.
func (s *Store) GetSchedule(ctx context.Context, id int64) (Schedule, error) {
	const q = `
SELECT id, kind, container_id, stack, run_at, cron_expr, relative_policy, relative_after, semver_policy,
       pinned_image, notify, enabled, last_run_at, next_run_at, created_at
FROM schedules WHERE id = ?;`

	rows, err := s.db.QueryContext(ctx, q, id)
	if err != nil {
		return Schedule{}, fmt.Errorf("get schedule %d: %w", id, err)
	}
	defer rows.Close() //nolint:errcheck // read-only query, rows drained fully below
	schedules, err := scanSchedules(rows)
	if err != nil {
		return Schedule{}, err
	}
	if len(schedules) == 0 {
		return Schedule{}, ErrNotFound
	}
	return schedules[0], nil
}

// DueSchedules returns every enabled schedule that hasn't run yet and
// whose next_run_at has passed — what the scheduler's tick executes.
func (s *Store) DueSchedules(ctx context.Context, now time.Time) ([]Schedule, error) {
	const q = `
SELECT id, kind, container_id, stack, run_at, cron_expr, relative_policy, relative_after, semver_policy,
       pinned_image, notify, enabled, last_run_at, next_run_at, created_at
FROM schedules
WHERE enabled = 1 AND next_run_at IS NOT NULL AND next_run_at <= ?
ORDER BY id;`

	rows, err := s.db.QueryContext(ctx, q, now.UTC().Format(time.RFC3339Nano))
	if err != nil {
		return nil, fmt.Errorf("list due schedules: %w", err)
	}
	defer rows.Close() //nolint:errcheck // read-only query, rows drained fully below
	return scanSchedules(rows)
}

func scanSchedules(rows *sql.Rows) ([]Schedule, error) {
	var out []Schedule
	for rows.Next() {
		var sc Schedule
		var runAt, lastRunAt, nextRunAt sql.NullString
		var createdAt string
		var notify, enabled int
		if err := rows.Scan(&sc.ID, &sc.Kind, &sc.ContainerName, &sc.Stack, &runAt, &sc.CronExpr,
			&sc.RelativePolicy, &sc.RelativeAfter, &sc.SemverPolicy, &sc.PinnedImage, &notify, &enabled,
			&lastRunAt, &nextRunAt, &createdAt); err != nil {
			return nil, fmt.Errorf("scan schedule: %w", err)
		}
		sc.Notify = notify != 0
		sc.Enabled = enabled != 0
		sc.RunAt = parseTimePtr(runAt)
		sc.LastRunAt = parseTimePtr(lastRunAt)
		sc.NextRunAt = parseTimePtr(nextRunAt)
		sc.CreatedAt, _ = time.Parse(time.RFC3339Nano, createdAt)
		out = append(out, sc)
	}
	return out, rows.Err()
}

// MarkScheduleRun records that a schedule fired: LastRunAt is set to now,
// and NextRunAt is updated to nextRun (nil disables it — a one-off
// schedule fires exactly once).
func (s *Store) MarkScheduleRun(ctx context.Context, id int64, nextRun *time.Time) error {
	const q = `UPDATE schedules SET last_run_at = ?, next_run_at = ?, enabled = ? WHERE id = ?;`
	enabled := 1
	if nextRun == nil {
		enabled = 0
	}
	_, err := s.db.ExecContext(ctx, q, time.Now().UTC().Format(time.RFC3339Nano), formatTimePtr(nextRun), enabled, id)
	if err != nil {
		return fmt.Errorf("mark schedule %d run: %w", id, err)
	}
	return nil
}

// TouchScheduleLastRun updates last_run_at without touching enabled or
// next_run_at — used by recurring "immediate" policies, which are
// evaluated live every tick rather than through a next_run_at due-check
// (see internal/scheduler).
func (s *Store) TouchScheduleLastRun(ctx context.Context, id int64) error {
	_, err := s.db.ExecContext(ctx, "UPDATE schedules SET last_run_at = ? WHERE id = ?;",
		time.Now().UTC().Format(time.RFC3339Nano), id)
	if err != nil {
		return fmt.Errorf("touch schedule %d last run: %w", id, err)
	}
	return nil
}

// SetScheduleEnabled toggles a schedule on/off without touching anything
// else — a disabled schedule is skipped by DueSchedules (its WHERE
// enabled = 1) but stays on record, unlike DeleteSchedule. ErrNotFound if
// id doesn't exist.
func (s *Store) SetScheduleEnabled(ctx context.Context, id int64, enabled bool) error {
	res, err := s.db.ExecContext(ctx, "UPDATE schedules SET enabled = ? WHERE id = ?;", boolInt(enabled), id)
	if err != nil {
		return fmt.Errorf("set schedule %d enabled=%v: %w", id, enabled, err)
	}
	n, _ := res.RowsAffected()
	if n == 0 {
		return ErrNotFound
	}
	return nil
}

// DeleteSchedule cancels a schedule.
func (s *Store) DeleteSchedule(ctx context.Context, id int64) error {
	res, err := s.db.ExecContext(ctx, "DELETE FROM schedules WHERE id = ?;", id)
	if err != nil {
		return fmt.Errorf("delete schedule %d: %w", id, err)
	}
	n, _ := res.RowsAffected()
	if n == 0 {
		return ErrNotFound
	}
	return nil
}

func formatTimePtr(t *time.Time) any {
	if t == nil {
		return nil
	}
	return t.UTC().Format(time.RFC3339Nano)
}

func parseTimePtr(ns sql.NullString) *time.Time {
	if !ns.Valid || ns.String == "" {
		return nil
	}
	t, err := time.Parse(time.RFC3339Nano, ns.String)
	if err != nil {
		return nil
	}
	return &t
}

func boolInt(b bool) int {
	if b {
		return 1
	}
	return 0
}
