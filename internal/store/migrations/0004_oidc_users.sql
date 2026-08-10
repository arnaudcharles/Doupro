-- Backs OIDC/SSO login (see internal/oidc, auth.Bootstrap remains the
-- local-account path). auth_provider distinguishes a password-based
-- account from one auto-provisioned on first successful OIDC login;
-- oidc_subject is the IdP's stable `sub` claim, never reused across
-- identities, so it's the lookup key on every subsequent OIDC login.
ALTER TABLE users ADD COLUMN auth_provider TEXT NOT NULL DEFAULT 'local';
ALTER TABLE users ADD COLUMN oidc_subject TEXT;

CREATE UNIQUE INDEX IF NOT EXISTS idx_users_oidc_subject ON users (oidc_subject) WHERE oidc_subject IS NOT NULL;
