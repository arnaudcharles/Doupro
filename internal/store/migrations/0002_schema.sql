-- Remaining core tables from docs/architecture.md: users/api_keys (auth),
-- events (structured log mirror), settings (runtime-editable config),
-- schedules (one-off + recurring), notifications (delivery log), and
-- image_history (append-only version trail per container).

CREATE TABLE IF NOT EXISTS users (
    id            INTEGER PRIMARY KEY AUTOINCREMENT,
    username      TEXT NOT NULL UNIQUE,
    password_hash TEXT NOT NULL,
    created_at    TEXT NOT NULL
);

CREATE TABLE IF NOT EXISTS sessions (
    token      TEXT PRIMARY KEY,
    user_id    INTEGER NOT NULL REFERENCES users(id) ON DELETE CASCADE,
    created_at TEXT NOT NULL,
    expires_at TEXT NOT NULL
);

CREATE TABLE IF NOT EXISTS api_keys (
    id         INTEGER PRIMARY KEY AUTOINCREMENT,
    name       TEXT NOT NULL,
    key_hash   TEXT NOT NULL UNIQUE,
    key_prefix TEXT NOT NULL, -- first chars shown in the UI list, never the full key
    created_at TEXT NOT NULL,
    last_used_at TEXT
);

CREATE TABLE IF NOT EXISTS events (
    id           INTEGER PRIMARY KEY AUTOINCREMENT,
    timestamp    TEXT NOT NULL,
    level        TEXT NOT NULL,
    event        TEXT NOT NULL,
    container    TEXT NOT NULL DEFAULT '',
    stack        TEXT NOT NULL DEFAULT '',
    from_version TEXT NOT NULL DEFAULT '',
    to_version   TEXT NOT NULL DEFAULT '',
    actor        TEXT NOT NULL DEFAULT '',
    actor_id     TEXT NOT NULL DEFAULT '',
    request_id   TEXT NOT NULL DEFAULT '',
    message      TEXT NOT NULL DEFAULT '',
    metadata     TEXT NOT NULL DEFAULT '{}'
);

CREATE INDEX IF NOT EXISTS idx_events_timestamp ON events (timestamp);
CREATE INDEX IF NOT EXISTS idx_events_event ON events (event);
CREATE INDEX IF NOT EXISTS idx_events_container ON events (container);

CREATE TABLE IF NOT EXISTS settings (
    key        TEXT PRIMARY KEY,
    value      TEXT NOT NULL,
    updated_at TEXT NOT NULL
);

CREATE TABLE IF NOT EXISTS schedules (
    id            INTEGER PRIMARY KEY AUTOINCREMENT,
    kind          TEXT NOT NULL, -- 'once' | 'cron' | 'relative'
    container_id  TEXT NOT NULL DEFAULT '',
    stack         TEXT NOT NULL DEFAULT '',
    run_at        TEXT,                       -- 'once' (nullable: unset for cron/relative)
    cron_expr     TEXT NOT NULL DEFAULT '',   -- 'cron'
    relative_policy TEXT NOT NULL DEFAULT '', -- 'relative': 'immediate' | 'delayed'
    relative_after  TEXT NOT NULL DEFAULT '', -- e.g. '24h', for 'delayed'
    pinned_image  TEXT NOT NULL DEFAULT '',   -- resolved target for 'once'
    notify        INTEGER NOT NULL DEFAULT 1,
    enabled       INTEGER NOT NULL DEFAULT 1,
    last_run_at   TEXT,
    next_run_at   TEXT,
    created_at    TEXT NOT NULL
);

CREATE INDEX IF NOT EXISTS idx_schedules_container ON schedules (container_id);
CREATE INDEX IF NOT EXISTS idx_schedules_stack ON schedules (stack);

CREATE TABLE IF NOT EXISTS notifications (
    id         INTEGER PRIMARY KEY AUTOINCREMENT,
    timestamp  TEXT NOT NULL,
    event      TEXT NOT NULL,
    target     TEXT NOT NULL DEFAULT '',
    channel    TEXT NOT NULL,
    status     TEXT NOT NULL, -- 'sent' | 'failed'
    error      TEXT NOT NULL DEFAULT '',
    message    TEXT NOT NULL DEFAULT ''
);

CREATE INDEX IF NOT EXISTS idx_notifications_timestamp ON notifications (timestamp);

CREATE TABLE IF NOT EXISTS image_history (
    id           INTEGER PRIMARY KEY AUTOINCREMENT,
    container_id TEXT NOT NULL,
    image        TEXT NOT NULL,
    digest       TEXT NOT NULL DEFAULT '',
    source       TEXT NOT NULL, -- 'detected' | 'updated-to' | 'rolled-back-to'
    timestamp    TEXT NOT NULL
);

CREATE INDEX IF NOT EXISTS idx_image_history_container ON image_history (container_id);
