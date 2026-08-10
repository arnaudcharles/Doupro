-- Durable, at-least-once notification delivery queue. The destination URL
-- is snapshotted so editing/removing a channel cannot orphan an event that
-- was already accepted for delivery. It is encrypted by migration 0010.
CREATE TABLE IF NOT EXISTS notification_queue (
    id              INTEGER PRIMARY KEY AUTOINCREMENT,
    event           TEXT NOT NULL,
    target          TEXT NOT NULL DEFAULT '',
    stack           TEXT NOT NULL DEFAULT '',
    channel         TEXT NOT NULL,
    channel_url     TEXT NOT NULL,
    message         TEXT NOT NULL DEFAULT '',
    status          TEXT NOT NULL DEFAULT 'pending',
    attempts        INTEGER NOT NULL DEFAULT 0,
    max_attempts    INTEGER NOT NULL DEFAULT 6,
    next_attempt_at TEXT NOT NULL,
    lease_until     TEXT,
    last_error      TEXT NOT NULL DEFAULT '',
    created_at      TEXT NOT NULL,
    updated_at      TEXT NOT NULL,
    sent_at         TEXT
);

CREATE INDEX IF NOT EXISTS idx_notification_queue_ready
    ON notification_queue (status, next_attempt_at);
