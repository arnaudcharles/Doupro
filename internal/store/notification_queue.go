package store

import (
	"context"
	"database/sql"
	"fmt"
	"time"
)

const DefaultNotificationMaxAttempts = 6

type NotificationJob struct {
	ID            int64
	Event         string
	Target        string
	Stack         string
	Channel       string
	ChannelURL    string
	Message       string
	Status        string
	Attempts      int
	MaxAttempts   int
	NextAttemptAt time.Time
	LeaseUntil    *time.Time
	LastError     string
	CreatedAt     time.Time
	UpdatedAt     time.Time
	SentAt        *time.Time
}

func (s *Store) EnqueueNotification(ctx context.Context, job NotificationJob) (int64, error) {
	now := time.Now().UTC()
	if job.MaxAttempts <= 0 {
		job.MaxAttempts = DefaultNotificationMaxAttempts
	}
	if job.NextAttemptAt.IsZero() {
		job.NextAttemptAt = now
	}
	encryptedURL, err := s.encryptSecret(job.ChannelURL)
	if err != nil {
		return 0, fmt.Errorf("encrypt notification destination: %w", err)
	}
	res, err := s.db.ExecContext(ctx, `
INSERT INTO notification_queue
 (event,target,stack,channel,channel_url,message,status,attempts,max_attempts,next_attempt_at,created_at,updated_at)
VALUES (?,?,?,?,?,?,'pending',0,?,?,?,?)`, job.Event, job.Target, job.Stack, job.Channel,
		encryptedURL, job.Message, job.MaxAttempts, formatQueueTime(job.NextAttemptAt), formatQueueTime(now), formatQueueTime(now))
	if err != nil {
		return 0, fmt.Errorf("enqueue notification: %w", err)
	}
	id, err := res.LastInsertId()
	if err != nil {
		return 0, fmt.Errorf("get notification queue id: %w", err)
	}
	return id, nil
}

// ClaimNotification atomically leases the oldest ready job. Expired leases
// are reclaimed, providing durable at-least-once delivery after a crash.
func (s *Store) ClaimNotification(ctx context.Context, now time.Time, lease time.Duration) (*NotificationJob, error) {
	now = now.UTC()
	row := s.db.QueryRowContext(ctx, `
UPDATE notification_queue
SET status='processing', attempts=attempts+1, lease_until=?, updated_at=?
WHERE id = (
 SELECT id FROM notification_queue
 WHERE (status='pending' AND next_attempt_at<=?)
    OR (status='processing' AND lease_until IS NOT NULL AND lease_until<=?)
 ORDER BY next_attempt_at, id LIMIT 1
)
RETURNING id,event,target,stack,channel,channel_url,message,status,attempts,max_attempts,
 next_attempt_at,lease_until,last_error,created_at,updated_at,sent_at`,
		formatQueueTime(now.Add(lease)), formatQueueTime(now), formatQueueTime(now), formatQueueTime(now))
	job, err := s.scanNotificationJob(row)
	if err == sql.ErrNoRows {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("claim notification: %w", err)
	}
	return &job, nil
}

func (s *Store) CompleteNotification(ctx context.Context, id int64, sentAt time.Time) error {
	res, err := s.db.ExecContext(ctx, `UPDATE notification_queue
SET status='sent', sent_at=?, lease_until=NULL, last_error='', updated_at=?
WHERE id=? AND status='processing'`, formatQueueTime(sentAt), formatQueueTime(sentAt), id)
	return requireNotificationUpdate(res, err, "complete notification")
}

func (s *Store) FailNotification(ctx context.Context, id int64, retryAt time.Time, lastError string, dead bool) error {
	status := "pending"
	if dead {
		status = "dead_letter"
	}
	now := time.Now().UTC()
	res, err := s.db.ExecContext(ctx, `UPDATE notification_queue
SET status=?, next_attempt_at=?, lease_until=NULL, last_error=?, updated_at=?
WHERE id=? AND status='processing'`, status, formatQueueTime(retryAt), lastError, formatQueueTime(now), id)
	return requireNotificationUpdate(res, err, "fail notification")
}

func (s *Store) RetryNotification(ctx context.Context, id int64) error {
	now := time.Now().UTC()
	res, err := s.db.ExecContext(ctx, `UPDATE notification_queue
SET status='pending', attempts=0, next_attempt_at=?, lease_until=NULL, last_error='', updated_at=?
WHERE id=? AND status='dead_letter'`, formatQueueTime(now), formatQueueTime(now), id)
	return requireNotificationUpdate(res, err, "retry dead-letter notification")
}

func (s *Store) ListNotificationJobs(ctx context.Context, limit int) ([]NotificationJob, error) {
	if limit <= 0 || limit > 500 {
		limit = 100
	}
	rows, err := s.db.QueryContext(ctx, `SELECT id,event,target,stack,channel,channel_url,message,status,attempts,max_attempts,
 next_attempt_at,lease_until,last_error,created_at,updated_at,sent_at
FROM notification_queue ORDER BY id DESC LIMIT ?`, limit)
	if err != nil {
		return nil, fmt.Errorf("list notification queue: %w", err)
	}
	defer rows.Close() //nolint:errcheck // read-only query, rows drained fully below
	var jobs []NotificationJob
	for rows.Next() {
		job, scanErr := s.scanNotificationJob(rows)
		if scanErr != nil {
			return nil, fmt.Errorf("scan notification queue: %w", scanErr)
		}
		jobs = append(jobs, job)
	}
	return jobs, rows.Err()
}

func (s *Store) CountNotificationJobs(ctx context.Context, status string) (int, error) {
	var count int
	err := s.db.QueryRowContext(ctx, `SELECT COUNT(*) FROM notification_queue WHERE status=?`, status).Scan(&count)
	return count, err
}

func (s *Store) scanNotificationJob(row rowScanner) (NotificationJob, error) {
	var job NotificationJob
	var next, created, updated string
	var lease, sent sql.NullString
	err := row.Scan(&job.ID, &job.Event, &job.Target, &job.Stack, &job.Channel, &job.ChannelURL,
		&job.Message, &job.Status, &job.Attempts, &job.MaxAttempts, &next, &lease, &job.LastError, &created, &updated, &sent)
	if err != nil {
		return job, err
	}
	job.ChannelURL, err = s.decryptSecret(job.ChannelURL)
	if err != nil {
		return job, fmt.Errorf("decrypt notification destination: %w", err)
	}
	job.NextAttemptAt, _ = time.Parse(time.RFC3339Nano, next)
	job.CreatedAt, _ = time.Parse(time.RFC3339Nano, created)
	job.UpdatedAt, _ = time.Parse(time.RFC3339Nano, updated)
	if lease.Valid {
		t, _ := time.Parse(time.RFC3339Nano, lease.String)
		job.LeaseUntil = &t
	}
	if sent.Valid {
		t, _ := time.Parse(time.RFC3339Nano, sent.String)
		job.SentAt = &t
	}
	return job, nil
}

func formatQueueTime(t time.Time) string { return t.UTC().Format(time.RFC3339Nano) }

func requireNotificationUpdate(res sql.Result, err error, action string) error {
	if err != nil {
		return fmt.Errorf("%s: %w", action, err)
	}
	n, err := res.RowsAffected()
	if err != nil {
		return fmt.Errorf("%s rows affected: %w", action, err)
	}
	if n != 1 {
		return fmt.Errorf("%s: notification job is not in the required state", action)
	}
	return nil
}
