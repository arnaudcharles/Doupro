CREATE TABLE IF NOT EXISTS rollback_watches (
    container_name          TEXT PRIMARY KEY,
    container_id            TEXT NOT NULL,
    update_applied_at       TEXT NOT NULL,
    window_expires_at       TEXT NOT NULL,
    crash_count             INTEGER NOT NULL DEFAULT 0,
    threshold               INTEGER NOT NULL DEFAULT 3,
    observed_restart_count  INTEGER NOT NULL DEFAULT 0,
    last_event_nano         INTEGER NOT NULL DEFAULT 0,
    last_crash_at           TEXT,
    triggered_at            TEXT,
    status                  TEXT NOT NULL DEFAULT 'active',
    updated_at              TEXT NOT NULL
);

CREATE INDEX IF NOT EXISTS idx_rollback_watches_status_expiry
    ON rollback_watches (status, window_expires_at);
