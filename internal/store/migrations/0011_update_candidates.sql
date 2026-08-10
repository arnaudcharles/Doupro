CREATE TABLE IF NOT EXISTS update_candidates (
    container_id TEXT NOT NULL,
    scope         TEXT NOT NULL,
    image         TEXT NOT NULL,
    version       TEXT NOT NULL,
    detected_at   TEXT NOT NULL,
    updated_at    TEXT NOT NULL,
    PRIMARY KEY (container_id, scope)
);

CREATE INDEX IF NOT EXISTS idx_update_candidates_detected
    ON update_candidates (detected_at);
