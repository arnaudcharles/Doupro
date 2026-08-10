-- RBAC assignments are kept in a separate table so this migration remains
-- idempotent under DoUpRo's migration runner (every migration is replayed on
-- every startup). Existing users and API keys retain their pre-RBAC full
-- access; newly-created principals receive an explicit assignment at creation.

CREATE TABLE IF NOT EXISTS rbac_assignments (
    principal_type       TEXT NOT NULL CHECK (principal_type IN ('user', 'api_key')),
    principal_id         INTEGER NOT NULL,
    role                 TEXT NOT NULL CHECK (role IN ('admin', 'write', 'read')),
    view_logs            INTEGER NOT NULL DEFAULT 0,
    manage_schedules     INTEGER NOT NULL DEFAULT 0,
    manage_notifications INTEGER NOT NULL DEFAULT 0,
    view_stats           INTEGER NOT NULL DEFAULT 0,
    PRIMARY KEY (principal_type, principal_id)
);

INSERT OR IGNORE INTO rbac_assignments (principal_type, principal_id, role)
SELECT 'user', id, 'admin' FROM users;

INSERT OR IGNORE INTO rbac_assignments (principal_type, principal_id, role)
SELECT 'api_key', id, 'admin' FROM api_keys;
