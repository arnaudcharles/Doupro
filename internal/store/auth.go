package store

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"strings"
	"time"
)

// ErrNotFound is returned by lookups that find no matching row.
var ErrNotFound = errors.New("not found")

// Role is the broad access level assigned to a user or API key.
type Role string

const (
	RoleAdmin Role = "admin"
	RoleWrite Role = "write"
	RoleRead  Role = "read"
)

// Permission is a capability checked by the API and web route guards.
type Permission string

const (
	PermissionReadContainers      Permission = "read_containers"
	PermissionWriteContainers     Permission = "write_containers"
	PermissionViewLogs            Permission = "view_logs"
	PermissionManageSchedules     Permission = "manage_schedules"
	PermissionManageNotifications Permission = "manage_notifications"
	PermissionViewStats           Permission = "view_stats"
	PermissionManageSettings      Permission = "manage_settings"
	PermissionManageUsers         Permission = "manage_users"
)

// AccessControl combines one broad role with the four independently
// configurable capabilities agreed for RBAC. Admin always has every
// permission, regardless of the stored checkbox values.
type AccessControl struct {
	Role                Role `json:"role"`
	ViewLogs            bool `json:"view_logs"`
	ManageSchedules     bool `json:"manage_schedules"`
	ManageNotifications bool `json:"manage_notifications"`
	ViewStats           bool `json:"view_stats"`
	Protected           bool `json:"-"`
}

func (a AccessControl) Valid() bool {
	return a.Role == RoleAdmin || a.Role == RoleWrite || a.Role == RoleRead
}

func (a AccessControl) Can(permission Permission) bool {
	if a.Role == RoleAdmin {
		return true
	}
	switch permission {
	case PermissionReadContainers:
		return a.Role == RoleWrite || a.Role == RoleRead
	case PermissionWriteContainers:
		return a.Role == RoleWrite
	case PermissionViewLogs:
		return a.ViewLogs
	case PermissionManageSchedules:
		return a.ManageSchedules
	case PermissionManageNotifications:
		return a.ManageNotifications
	case PermissionViewStats:
		return a.ViewStats
	case PermissionManageSettings, PermissionManageUsers:
		return false
	default:
		return false
	}
}

// User is a local or OIDC account with an RBAC assignment. AuthProvider is
// "local" (password) or "oidc" (auto-provisioned on first SSO login, see
// GetOrCreateOIDCUser); OIDCSubject is only set for the latter.
type User struct {
	ID                 int64
	Username           string
	PasswordHash       string
	MustChangePassword bool
	AuthProvider       string
	OIDCSubject        string
	CreatedAt          time.Time
	Access             AccessControl
}

func (s *Store) accessFor(ctx context.Context, principalType string, principalID int64) (AccessControl, error) {
	const q = `SELECT role, view_logs, manage_schedules, manage_notifications, view_stats, protected
FROM rbac_assignments WHERE principal_type = ? AND principal_id = ?;`
	var access AccessControl
	err := s.db.QueryRowContext(ctx, q, principalType, principalID).Scan(&access.Role, &access.ViewLogs,
		&access.ManageSchedules, &access.ManageNotifications, &access.ViewStats, &access.Protected)
	if errors.Is(err, sql.ErrNoRows) {
		// Defensive compatibility for a principal inserted by an older binary
		// after migration 0006 ran but before this binary started.
		access.Role = RoleAdmin
		if setErr := s.SetAccess(ctx, principalType, principalID, access); setErr != nil {
			return AccessControl{}, setErr
		}
		return access, nil
	}
	if err != nil {
		return AccessControl{}, fmt.Errorf("get %s %d access: %w", principalType, principalID, err)
	}
	return access, nil
}

// SetAccess creates or replaces the RBAC assignment for a user or API key.
func (s *Store) SetAccess(ctx context.Context, principalType string, principalID int64, access AccessControl) error {
	if (principalType != "user" && principalType != "api_key") || !access.Valid() {
		return fmt.Errorf("invalid RBAC assignment")
	}
	current, err := s.accessForExisting(ctx, principalType, principalID)
	if err != nil && !errors.Is(err, ErrNotFound) {
		return err
	}
	if current.Protected {
		return fmt.Errorf("protected administrator access cannot be changed")
	}
	const q = `INSERT INTO rbac_assignments
(principal_type, principal_id, role, view_logs, manage_schedules, manage_notifications, view_stats, protected)
VALUES (?, ?, ?, ?, ?, ?, ?, ?)
ON CONFLICT(principal_type, principal_id) DO UPDATE SET
 role = excluded.role, view_logs = excluded.view_logs,
 manage_schedules = excluded.manage_schedules,
 manage_notifications = excluded.manage_notifications, view_stats = excluded.view_stats;`
	_, err = s.db.ExecContext(ctx, q, principalType, principalID, access.Role, access.ViewLogs,
		access.ManageSchedules, access.ManageNotifications, access.ViewStats, access.Protected)
	if err != nil {
		return fmt.Errorf("set %s %d access: %w", principalType, principalID, err)
	}
	return nil
}

func (s *Store) accessForExisting(ctx context.Context, principalType string, principalID int64) (AccessControl, error) {
	const q = `SELECT role, view_logs, manage_schedules, manage_notifications, view_stats, protected
FROM rbac_assignments WHERE principal_type = ? AND principal_id = ?;`
	var access AccessControl
	err := s.db.QueryRowContext(ctx, q, principalType, principalID).Scan(&access.Role, &access.ViewLogs,
		&access.ManageSchedules, &access.ManageNotifications, &access.ViewStats, &access.Protected)
	if errors.Is(err, sql.ErrNoRows) {
		return AccessControl{}, ErrNotFound
	}
	if err != nil {
		return AccessControl{}, fmt.Errorf("get %s %d access: %w", principalType, principalID, err)
	}
	return access, nil
}

// HasAnyUser reports whether at least one user exists, used to decide
// whether to bootstrap the admin account from DOUPRO_ADMIN_USER/PASSWORD
// on startup (see docs/settings.md).
func (s *Store) HasAnyUser(ctx context.Context) (bool, error) {
	var n int
	if err := s.db.QueryRowContext(ctx, "SELECT COUNT(*) FROM users;").Scan(&n); err != nil {
		return false, fmt.Errorf("count users: %w", err)
	}
	return n > 0, nil
}

// CreateUser inserts a new local account. passwordHash must already be
// bcrypt-hashed (see internal/auth) — this package never sees a plaintext
// password. mustChangePassword marks an account whose password must be
// changed before it can access anything else (see auth.Bootstrap's
// zero-config admin/admin default).
func (s *Store) CreateUser(ctx context.Context, username, passwordHash string, mustChangePassword bool) error {
	_, err := s.CreateUserWithAccess(ctx, username, passwordHash, mustChangePassword, AccessControl{Role: RoleAdmin})
	return err
}

// CreateUserWithAccess creates a local account and its RBAC assignment.
func (s *Store) CreateUserWithAccess(ctx context.Context, username, passwordHash string, mustChangePassword bool, access AccessControl) (User, error) {
	if !access.Valid() {
		return User{}, fmt.Errorf("create user: invalid role %q", access.Role)
	}
	var existingUsers int
	if err := s.db.QueryRowContext(ctx, "SELECT COUNT(*) FROM users;").Scan(&existingUsers); err != nil {
		return User{}, fmt.Errorf("count users before create: %w", err)
	}
	if existingUsers == 0 {
		access = AccessControl{Role: RoleAdmin, Protected: true}
	}
	const q = `INSERT INTO users (username, password_hash, must_change_password, created_at) VALUES (?, ?, ?, ?);`
	now := time.Now().UTC()
	res, err := s.db.ExecContext(ctx, q, username, passwordHash, mustChangePassword, now.Format(time.RFC3339Nano))
	if err != nil {
		return User{}, fmt.Errorf("create user %s: %w", username, err)
	}
	id, err := res.LastInsertId()
	if err != nil {
		return User{}, fmt.Errorf("create user %s: %w", username, err)
	}
	if err := s.SetAccess(ctx, "user", id, access); err != nil {
		_, _ = s.db.ExecContext(ctx, "DELETE FROM users WHERE id = ?;", id)
		return User{}, err
	}
	return User{ID: id, Username: username, PasswordHash: passwordHash, MustChangePassword: mustChangePassword,
		AuthProvider: "local", CreatedAt: now, Access: access}, nil
}

// ListUsers returns every local and OIDC identity with its RBAC assignment.
func (s *Store) ListUsers(ctx context.Context) ([]User, error) {
	const q = `SELECT id, username, password_hash, must_change_password, auth_provider, oidc_subject, created_at
FROM users ORDER BY username COLLATE NOCASE;`
	rows, err := s.db.QueryContext(ctx, q)
	if err != nil {
		return nil, fmt.Errorf("list users: %w", err)
	}
	var users []User
	for rows.Next() {
		var u User
		var subject sql.NullString
		var created string
		if err := rows.Scan(&u.ID, &u.Username, &u.PasswordHash, &u.MustChangePassword, &u.AuthProvider, &subject, &created); err != nil {
			rows.Close() //nolint:errcheck // returning early on scan error
			return nil, fmt.Errorf("scan user: %w", err)
		}
		u.OIDCSubject = subject.String
		u.CreatedAt, _ = time.Parse(time.RFC3339Nano, created)
		users = append(users, u)
	}
	if err := rows.Close(); err != nil {
		return nil, fmt.Errorf("list users: %w", err)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}

	// accessFor issues its own query against s.db, which the store's
	// single-connection pool (see store.Open's SetMaxOpenConns(1)) can
	// only serve once the users query's connection above has been
	// released — hence resolving access strictly after rows is closed,
	// never nested inside the loop, which would deadlock the pool
	// against itself.
	for i := range users {
		users[i].Access, err = s.accessFor(ctx, "user", users[i].ID)
		if err != nil {
			return nil, err
		}
	}
	return users, nil
}

// DeleteUser removes an identity and its RBAC assignment. Session rows are
// deleted through the users foreign key; the assignment is polymorphic and
// therefore removed explicitly.
func (s *Store) DeleteUser(ctx context.Context, id int64) error {
	access, err := s.accessFor(ctx, "user", id)
	if err != nil {
		return err
	}
	if access.Protected {
		return fmt.Errorf("protected administrator cannot be deleted")
	}
	res, err := s.db.ExecContext(ctx, "DELETE FROM users WHERE id = ?;", id)
	if err != nil {
		return fmt.Errorf("delete user %d: %w", id, err)
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return ErrNotFound
	}
	_, _ = s.db.ExecContext(ctx, "DELETE FROM rbac_assignments WHERE principal_type = 'user' AND principal_id = ?;", id)
	return nil
}

// AdminCount is used to prevent removing or demoting the last administrator.
func (s *Store) AdminCount(ctx context.Context) (int, error) {
	var count int
	err := s.db.QueryRowContext(ctx, `SELECT COUNT(*) FROM rbac_assignments WHERE principal_type = 'user' AND role = 'admin';`).Scan(&count)
	if err != nil {
		return 0, fmt.Errorf("count administrators: %w", err)
	}
	return count, nil
}

// GetUserByUsername looks up a user by username, or ErrNotFound.
func (s *Store) GetUserByUsername(ctx context.Context, username string) (User, error) {
	const q = `SELECT id, username, password_hash, must_change_password, auth_provider, oidc_subject FROM users WHERE username = ?;`
	var u User
	var oidcSubject sql.NullString
	err := s.db.QueryRowContext(ctx, q, username).Scan(&u.ID, &u.Username, &u.PasswordHash, &u.MustChangePassword, &u.AuthProvider, &oidcSubject)
	if errors.Is(err, sql.ErrNoRows) {
		return User{}, ErrNotFound
	}
	if err != nil {
		return User{}, fmt.Errorf("get user %s: %w", username, err)
	}
	u.OIDCSubject = oidcSubject.String
	u.Access, err = s.accessFor(ctx, "user", u.ID)
	if err != nil {
		return User{}, err
	}
	return u, nil
}

// GetUserByID looks up a user by its stable database identifier.
func (s *Store) GetUserByID(ctx context.Context, id int64) (User, error) {
	const q = `SELECT id, username, password_hash, must_change_password, auth_provider, oidc_subject, created_at FROM users WHERE id = ?;`
	var u User
	var subject sql.NullString
	var created string
	err := s.db.QueryRowContext(ctx, q, id).Scan(&u.ID, &u.Username, &u.PasswordHash, &u.MustChangePassword, &u.AuthProvider, &subject, &created)
	if errors.Is(err, sql.ErrNoRows) {
		return User{}, ErrNotFound
	}
	if err != nil {
		return User{}, fmt.Errorf("get user %d: %w", id, err)
	}
	u.OIDCSubject = subject.String
	u.CreatedAt, _ = time.Parse(time.RFC3339Nano, created)
	u.Access, err = s.accessFor(ctx, "user", u.ID)
	if err != nil {
		return User{}, err
	}
	return u, nil
}

// GetOrCreateOIDCUser resolves an OIDC identity (its stable `sub` claim) to
// a local user row, auto-provisioning one on first login. username seeds
// the display name (preferred_username or email claim — see
// internal/oidc.Claims) but is not the lookup key: subject is, since a
// username can collide with a local account or change at the IdP.
func (s *Store) GetOrCreateOIDCUser(ctx context.Context, subject, username string) (User, error) {
	const selectQ = `SELECT id, username, password_hash, must_change_password, auth_provider, oidc_subject FROM users WHERE oidc_subject = ?;`
	var u User
	var oidcSubject sql.NullString
	err := s.db.QueryRowContext(ctx, selectQ, subject).Scan(&u.ID, &u.Username, &u.PasswordHash, &u.MustChangePassword, &u.AuthProvider, &oidcSubject)
	if err == nil {
		u.OIDCSubject = oidcSubject.String
		u.Access, err = s.accessFor(ctx, "user", u.ID)
		if err != nil {
			return User{}, err
		}
		return u, nil
	}
	if !errors.Is(err, sql.ErrNoRows) {
		return User{}, fmt.Errorf("lookup oidc user %s: %w", subject, err)
	}

	// A username collision with an existing local account (or an earlier
	// OIDC identity that took the same preferred_username) is disambiguated
	// with a short subject suffix rather than failing the login outright.
	const insertQ = `INSERT INTO users (username, password_hash, must_change_password, auth_provider, oidc_subject, created_at) VALUES (?, '', 0, 'oidc', ?, ?);`
	res, err := s.db.ExecContext(ctx, insertQ, username, subject, time.Now().UTC().Format(time.RFC3339Nano))
	if err != nil && strings.Contains(err.Error(), "UNIQUE constraint failed") {
		suffix := subject
		if len(suffix) > 8 {
			suffix = suffix[:8]
		}
		username = username + "-" + suffix
		res, err = s.db.ExecContext(ctx, insertQ, username, subject, time.Now().UTC().Format(time.RFC3339Nano))
	}
	if err != nil {
		return User{}, fmt.Errorf("create oidc user %s: %w", username, err)
	}
	id, err := res.LastInsertId()
	if err != nil {
		return User{}, fmt.Errorf("create oidc user %s: %w", username, err)
	}
	access := AccessControl{Role: RoleRead}
	if err := s.SetAccess(ctx, "user", id, access); err != nil {
		return User{}, err
	}
	return User{ID: id, Username: username, AuthProvider: "oidc", OIDCSubject: subject, Access: access}, nil
}

// UpdatePassword sets a new password hash for userID and clears
// must_change_password — used by both the forced first-login change and a
// voluntary change from Settings.
func (s *Store) UpdatePassword(ctx context.Context, userID int64, passwordHash string) error {
	const q = `UPDATE users SET password_hash = ?, must_change_password = 0 WHERE id = ?;`
	res, err := s.db.ExecContext(ctx, q, passwordHash, userID)
	if err != nil {
		return fmt.Errorf("update password for user %d: %w", userID, err)
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return ErrNotFound
	}
	return nil
}

// CreateSession stores a new session identified by the SHA-256 hash of its
// token (see internal/auth.HashToken) — the raw token is never persisted.
func (s *Store) CreateSession(ctx context.Context, tokenHash string, userID int64, ttl time.Duration) error {
	now := time.Now().UTC()
	const q = `INSERT INTO sessions (token, user_id, created_at, expires_at) VALUES (?, ?, ?, ?);`
	_, err := s.db.ExecContext(ctx, q, tokenHash, userID,
		now.Format(time.RFC3339Nano), now.Add(ttl).Format(time.RFC3339Nano))
	if err != nil {
		return fmt.Errorf("create session: %w", err)
	}
	return nil
}

// SessionUser resolves a session token hash to its owning user, if the
// session exists and has not expired.
func (s *Store) SessionUser(ctx context.Context, tokenHash string) (User, error) {
	const q = `
SELECT u.id, u.username, u.password_hash, u.must_change_password, u.auth_provider, u.oidc_subject
FROM sessions s
JOIN users u ON u.id = s.user_id
WHERE s.token = ? AND s.expires_at > ?;`

	var u User
	var oidcSubject sql.NullString
	err := s.db.QueryRowContext(ctx, q, tokenHash, time.Now().UTC().Format(time.RFC3339Nano)).
		Scan(&u.ID, &u.Username, &u.PasswordHash, &u.MustChangePassword, &u.AuthProvider, &oidcSubject)
	if errors.Is(err, sql.ErrNoRows) {
		return User{}, ErrNotFound
	}
	if err != nil {
		return User{}, fmt.Errorf("resolve session: %w", err)
	}
	u.OIDCSubject = oidcSubject.String
	u.Access, err = s.accessFor(ctx, "user", u.ID)
	if err != nil {
		return User{}, err
	}
	return u, nil
}

// DeleteSession removes a session (logout). Deleting a token that doesn't
// exist is not an error — logout is idempotent.
func (s *Store) DeleteSession(ctx context.Context, tokenHash string) error {
	_, err := s.db.ExecContext(ctx, "DELETE FROM sessions WHERE token = ?;", tokenHash)
	if err != nil {
		return fmt.Errorf("delete session: %w", err)
	}
	return nil
}

// DeleteOtherSessions removes every session for userID except keepTokenHash
// (the caller's own current session, so changing your password doesn't log
// you out of the tab you're using). A stolen session cookie must not
// survive its victim changing their password — this is what makes that
// true.
func (s *Store) DeleteOtherSessions(ctx context.Context, userID int64, keepTokenHash string) error {
	_, err := s.db.ExecContext(ctx, "DELETE FROM sessions WHERE user_id = ? AND token != ?;", userID, keepTokenHash)
	if err != nil {
		return fmt.Errorf("delete other sessions for user %d: %w", userID, err)
	}
	return nil
}

// APIKey is an API key as listed in Settings → Security. KeyHash is never
// exposed outside this package; KeyPrefix is what the UI shows to help an
// operator tell keys apart without revealing the secret.
type APIKey struct {
	ID         int64
	Name       string
	KeyPrefix  string
	CreatedAt  time.Time
	LastUsedAt *time.Time
	Access     AccessControl
}

// CreateAPIKey stores a new API key by its hash (see
// internal/auth.HashToken) and a short prefix of the raw key for display.
func (s *Store) CreateAPIKey(ctx context.Context, name, keyHash, keyPrefix string) (int64, error) {
	return s.CreateAPIKeyWithAccess(ctx, name, keyHash, keyPrefix, AccessControl{Role: RoleAdmin})
}

// CreateAPIKeyWithAccess stores a key and its RBAC assignment together.
func (s *Store) CreateAPIKeyWithAccess(ctx context.Context, name, keyHash, keyPrefix string, access AccessControl) (int64, error) {
	if !access.Valid() {
		return 0, fmt.Errorf("create api key: invalid role %q", access.Role)
	}
	const q = `INSERT INTO api_keys (name, key_hash, key_prefix, created_at) VALUES (?, ?, ?, ?);`
	res, err := s.db.ExecContext(ctx, q, name, keyHash, keyPrefix, time.Now().UTC().Format(time.RFC3339Nano))
	if err != nil {
		return 0, fmt.Errorf("create api key: %w", err)
	}
	id, err := res.LastInsertId()
	if err != nil {
		return 0, fmt.Errorf("create api key: %w", err)
	}
	if err := s.SetAccess(ctx, "api_key", id, access); err != nil {
		_, _ = s.db.ExecContext(ctx, "DELETE FROM api_keys WHERE id = ?;", id)
		return 0, err
	}
	return id, nil
}

// AuthenticateAPIKey resolves a key hash to its key metadata and RBAC
// assignment, and updates last_used_at on success.
func (s *Store) AuthenticateAPIKey(ctx context.Context, keyHash string) (APIKey, error) {
	var key APIKey
	err := s.db.QueryRowContext(ctx, `SELECT id, name, key_prefix FROM api_keys WHERE key_hash = ?;`, keyHash).
		Scan(&key.ID, &key.Name, &key.KeyPrefix)
	if errors.Is(err, sql.ErrNoRows) {
		return APIKey{}, ErrNotFound
	}
	if err != nil {
		return APIKey{}, fmt.Errorf("lookup api key: %w", err)
	}
	key.Access, err = s.accessFor(ctx, "api_key", key.ID)
	if err != nil {
		return APIKey{}, err
	}
	_, _ = s.db.ExecContext(ctx, "UPDATE api_keys SET last_used_at = ? WHERE id = ?;",
		time.Now().UTC().Format(time.RFC3339Nano), key.ID)
	return key, nil
}

// APIKeyOwnerExists reports whether a given key hash matches a stored API
// key, and bumps its last-used timestamp if so.
func (s *Store) APIKeyOwnerExists(ctx context.Context, keyHash string) (bool, error) {
	_, err := s.AuthenticateAPIKey(ctx, keyHash)
	if errors.Is(err, ErrNotFound) {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	return true, nil
}

// ListAPIKeys returns every API key, most recent first (never the key
// material itself, see APIKey.KeyPrefix).
func (s *Store) ListAPIKeys(ctx context.Context) ([]APIKey, error) {
	const q = `SELECT id, name, key_prefix, created_at, last_used_at FROM api_keys ORDER BY created_at DESC;`
	rows, err := s.db.QueryContext(ctx, q)
	if err != nil {
		return nil, fmt.Errorf("list api keys: %w", err)
	}

	var out []APIKey
	for rows.Next() {
		var k APIKey
		var created string
		var lastUsed sql.NullString
		if err := rows.Scan(&k.ID, &k.Name, &k.KeyPrefix, &created, &lastUsed); err != nil {
			rows.Close() //nolint:errcheck // returning early on scan error
			return nil, fmt.Errorf("scan api key: %w", err)
		}
		k.CreatedAt, _ = time.Parse(time.RFC3339Nano, created)
		if lastUsed.Valid {
			if t, err := time.Parse(time.RFC3339Nano, lastUsed.String); err == nil {
				k.LastUsedAt = &t
			}
		}
		out = append(out, k)
	}
	if err := rows.Close(); err != nil {
		return nil, fmt.Errorf("list api keys: %w", err)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}

	// See ListUsers: accessFor must run after rows is closed, never
	// nested inside the loop, or it deadlocks the single-connection pool
	// against itself.
	for i := range out {
		out[i].Access, err = s.accessFor(ctx, "api_key", out[i].ID)
		if err != nil {
			return nil, err
		}
	}
	return out, nil
}

// RevokeAPIKey deletes an API key by ID, or ErrNotFound if it doesn't
// exist (already revoked, or never did).
func (s *Store) RevokeAPIKey(ctx context.Context, id int64) error {
	res, err := s.db.ExecContext(ctx, "DELETE FROM api_keys WHERE id = ?;", id)
	if err != nil {
		return fmt.Errorf("revoke api key %d: %w", id, err)
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return ErrNotFound
	}
	return nil
}
