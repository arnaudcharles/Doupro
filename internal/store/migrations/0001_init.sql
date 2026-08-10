-- Initial schema: only the containers table needed to back the first
-- vertical slice (discovery -> store -> GET /api/v1/containers). The full
-- schema documented in docs/architecture.md (image_history, schedules,
-- events, notifications, settings, users, api_keys) lands with the
-- subsystems that need it.

CREATE TABLE IF NOT EXISTS containers (
    id               TEXT PRIMARY KEY,
    name             TEXT NOT NULL,
    stack            TEXT NOT NULL DEFAULT '',
    state            TEXT NOT NULL DEFAULT '',
    status           TEXT NOT NULL DEFAULT '',
    current_image    TEXT NOT NULL DEFAULT '',
    previous_image   TEXT NOT NULL DEFAULT '',
    update_available INTEGER NOT NULL DEFAULT 0,
    excluded         INTEGER NOT NULL DEFAULT 0,
    last_checked_at  TEXT,
    updated_at       TEXT NOT NULL
);

CREATE INDEX IF NOT EXISTS idx_containers_stack ON containers (stack);
