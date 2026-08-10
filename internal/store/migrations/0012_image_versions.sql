CREATE TABLE IF NOT EXISTS image_versions (
    registry   TEXT NOT NULL,
    repository TEXT NOT NULL,
    digest     TEXT NOT NULL,
    version    TEXT NOT NULL DEFAULT '',
    status     TEXT NOT NULL CHECK (status IN ('resolved', 'unresolved')),
    checked_at TEXT NOT NULL,
    PRIMARY KEY (registry, repository, digest)
);

CREATE INDEX IF NOT EXISTS idx_image_versions_checked_at
    ON image_versions(checked_at);
