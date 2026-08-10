-- The bootstrap administrator is the instance owner/break-glass account.
-- It always has full access and cannot be reconfigured or deleted.
ALTER TABLE rbac_assignments ADD COLUMN protected INTEGER NOT NULL DEFAULT 0;

UPDATE rbac_assignments
SET protected = 1, role = 'admin'
WHERE principal_type = 'user'
  AND principal_id = (
    SELECT id FROM users WHERE auth_provider = 'local' ORDER BY id LIMIT 1
  );
