-- Resolved human-readable versions (see internal/registry.ResolveVersion),
-- distinct from current_image/previous_image which stay raw tag/digest
-- strings. Both are best-effort and empty when resolution didn't find a
-- match (non-Hub registry, no matching tag, Hub API failure) — the UI
-- falls back to the raw tag/image in that case, never fabricates one.
ALTER TABLE containers ADD COLUMN current_version TEXT NOT NULL DEFAULT '';
ALTER TABLE containers ADD COLUMN available_version TEXT NOT NULL DEFAULT '';
