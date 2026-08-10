-- Backs the forced-password-change flow: the zero-config admin/admin
-- bootstrap account (see auth.Bootstrap) is created with this flag set,
-- and every protected route redirects to /change-password until it's
-- cleared by a successful password change. Accounts created with an
-- explicit DOUPRO_ADMIN_USER/PASSWORD are not flagged.
ALTER TABLE users ADD COLUMN must_change_password INTEGER NOT NULL DEFAULT 0;
