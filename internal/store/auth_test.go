package store

import (
	"context"
	"fmt"
	"testing"
	"time"
)

func newTestStore(t *testing.T) *Store {
	t.Helper()
	st, err := Open(context.Background(), ":memory:")
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	t.Cleanup(func() { _ = st.Close() })
	return st
}

func TestGetOrCreateOIDCUser_CreatesThenReuses(t *testing.T) {
	st := newTestStore(t)
	ctx := context.Background()

	created, err := st.GetOrCreateOIDCUser(ctx, "subject-1", "alice")
	if err != nil {
		t.Fatalf("GetOrCreateOIDCUser: %v", err)
	}
	if created.Username != "alice" || created.AuthProvider != "oidc" || created.OIDCSubject != "subject-1" {
		t.Fatalf("unexpected created user: %+v", created)
	}
	if created.Access.Role != RoleRead {
		t.Fatalf("new OIDC role = %q, want read", created.Access.Role)
	}

	again, err := st.GetOrCreateOIDCUser(ctx, "subject-1", "alice-renamed-at-idp")
	if err != nil {
		t.Fatalf("GetOrCreateOIDCUser (second call): %v", err)
	}
	if again.ID != created.ID {
		t.Fatalf("expected the same user row, got a new one: %+v vs %+v", again, created)
	}
	if again.Username != "alice" {
		t.Fatalf("existing user's username should not change on re-login, got %q", again.Username)
	}
}

func TestAccessControlCan(t *testing.T) {
	tests := []struct {
		name       string
		access     AccessControl
		permission Permission
		want       bool
	}{
		{"admin can manage users", AccessControl{Role: RoleAdmin}, PermissionManageUsers, true},
		{"write can update", AccessControl{Role: RoleWrite}, PermissionWriteContainers, true},
		{"read cannot update", AccessControl{Role: RoleRead}, PermissionWriteContainers, false},
		{"read can receive logs grant", AccessControl{Role: RoleRead, ViewLogs: true}, PermissionViewLogs, true},
		{"write needs schedules grant", AccessControl{Role: RoleWrite}, PermissionManageSchedules, false},
		{"write can receive schedules grant", AccessControl{Role: RoleWrite, ManageSchedules: true}, PermissionManageSchedules, true},
		{"unknown role is denied", AccessControl{}, PermissionReadContainers, false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := tt.access.Can(tt.permission); got != tt.want {
				t.Fatalf("Can(%q) = %v, want %v", tt.permission, got, tt.want)
			}
		})
	}
}

func TestSessionAndAPIKeyCarryAccess(t *testing.T) {
	st := newTestStore(t)
	ctx := context.Background()

	if err := st.CreateUser(ctx, "admin", "hash", false); err != nil {
		t.Fatal(err)
	}
	if err := st.CreateUser(ctx, "operator", "hash", false); err != nil {
		t.Fatal(err)
	}
	u, err := st.GetUserByUsername(ctx, "operator")
	if err != nil {
		t.Fatal(err)
	}
	access := AccessControl{Role: RoleRead, ViewLogs: true}
	if err := st.SetAccess(ctx, "user", u.ID, access); err != nil {
		t.Fatal(err)
	}
	if err := st.CreateSession(ctx, "session-hash", u.ID, time.Hour); err != nil {
		t.Fatal(err)
	}
	sessionUser, err := st.SessionUser(ctx, "session-hash")
	if err != nil {
		t.Fatal(err)
	}
	if sessionUser.Access != access {
		t.Fatalf("session access = %+v, want %+v", sessionUser.Access, access)
	}

	keyAccess := AccessControl{Role: RoleWrite, ManageSchedules: true}
	if _, err := st.CreateAPIKeyWithAccess(ctx, "automation", "key-hash", "prefix", keyAccess); err != nil {
		t.Fatal(err)
	}
	key, err := st.AuthenticateAPIKey(ctx, "key-hash")
	if err != nil {
		t.Fatal(err)
	}
	if key.Access != keyAccess {
		t.Fatalf("API key access = %+v, want %+v", key.Access, keyAccess)
	}
}

func TestBootstrapAdministratorIsProtected(t *testing.T) {
	st := newTestStore(t)
	ctx := context.Background()
	if err := st.CreateUser(ctx, "owner", "hash", false); err != nil {
		t.Fatal(err)
	}
	owner, err := st.GetUserByUsername(ctx, "owner")
	if err != nil {
		t.Fatal(err)
	}
	if owner.Access.Role != RoleAdmin || !owner.Access.Protected {
		t.Fatalf("bootstrap access = %+v, want protected admin", owner.Access)
	}
	if err := st.SetAccess(ctx, "user", owner.ID, AccessControl{Role: RoleRead}); err == nil {
		t.Fatal("expected protected administrator demotion to fail")
	}
	if err := st.DeleteUser(ctx, owner.ID); err == nil {
		t.Fatal("expected protected administrator deletion to fail")
	}
}

func TestGetOrCreateOIDCUser_DisambiguatesUsernameCollision(t *testing.T) {
	st := newTestStore(t)
	ctx := context.Background()

	if err := st.CreateUser(ctx, "bob", "hash", false); err != nil {
		t.Fatalf("CreateUser: %v", err)
	}

	oidcUser, err := st.GetOrCreateOIDCUser(ctx, "subject-2", "bob")
	if err != nil {
		t.Fatalf("GetOrCreateOIDCUser: %v", err)
	}
	if oidcUser.Username == "bob" {
		t.Fatalf("expected the OIDC user's username to be disambiguated from the existing local user, got %q", oidcUser.Username)
	}

	localUser, err := st.GetUserByUsername(ctx, "bob")
	if err != nil {
		t.Fatalf("GetUserByUsername: %v", err)
	}
	if localUser.AuthProvider != "local" || localUser.OIDCSubject != "" {
		t.Fatalf("local user should be untouched, got %+v", localUser)
	}
}

func TestGetUserByUsername_IncludesAuthProviderColumns(t *testing.T) {
	st := newTestStore(t)
	ctx := context.Background()

	if err := st.CreateUser(ctx, "carol", "hash", false); err != nil {
		t.Fatalf("CreateUser: %v", err)
	}
	u, err := st.GetUserByUsername(ctx, "carol")
	if err != nil {
		t.Fatalf("GetUserByUsername: %v", err)
	}
	if u.AuthProvider != "local" {
		t.Fatalf("AuthProvider = %q, want local", u.AuthProvider)
	}
	if u.OIDCSubject != "" {
		t.Fatalf("OIDCSubject = %q, want empty for a local account", u.OIDCSubject)
	}
}

// TestListUsersDoesNotDeadlockTheSingleConnectionPool guards against a
// regression where ListUsers resolved each row's RBAC access with a
// nested s.db query while the outer rows cursor from the users query was
// still open — store.Open caps the pool to exactly one SQLite connection
// (see store.go), so that nested query could never acquire a connection
// until the outer one released it, which never happened until the loop
// finished needing it. Runs the call on a goroutine with a hard deadline
// so a regression fails fast with a clear message instead of hanging the
// whole test suite.
func TestListUsersDoesNotDeadlockTheSingleConnectionPool(t *testing.T) {
	st := newTestStore(t)
	ctx := context.Background()

	for i := 0; i < 3; i++ {
		if _, err := st.CreateUserWithAccess(ctx, "user"+string(rune('a'+i)), "hash", false, AccessControl{Role: RoleRead}); err != nil {
			t.Fatalf("CreateUserWithAccess: %v", err)
		}
	}

	done := make(chan error, 1)
	go func() {
		users, err := st.ListUsers(ctx)
		if err == nil && len(users) != 3 {
			err = fmt.Errorf("expected 3 users, got %d", len(users))
		}
		done <- err
	}()

	select {
	case err := <-done:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("ListUsers deadlocked — a nested query is likely running while its own rows cursor is still open against the single-connection pool")
	}
}

// TestListAPIKeysDoesNotDeadlockTheSingleConnectionPool is
// TestListUsersDoesNotDeadlockTheSingleConnectionPool for ListAPIKeys.
func TestListAPIKeysDoesNotDeadlockTheSingleConnectionPool(t *testing.T) {
	st := newTestStore(t)
	ctx := context.Background()

	for i := 0; i < 3; i++ {
		if _, err := st.CreateAPIKeyWithAccess(ctx, "key"+string(rune('a'+i)), "hash"+string(rune('a'+i)), "prefix", AccessControl{Role: RoleRead}); err != nil {
			t.Fatalf("CreateAPIKeyWithAccess: %v", err)
		}
	}

	done := make(chan error, 1)
	go func() {
		keys, err := st.ListAPIKeys(ctx)
		if err == nil && len(keys) != 3 {
			err = fmt.Errorf("expected 3 api keys, got %d", len(keys))
		}
		done <- err
	}()

	select {
	case err := <-done:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("ListAPIKeys deadlocked — a nested query is likely running while its own rows cursor is still open against the single-connection pool")
	}
}
