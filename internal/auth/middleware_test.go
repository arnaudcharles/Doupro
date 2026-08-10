package auth

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/arnaudcharles/doupro/internal/store"
)

func TestRequirePermission(t *testing.T) {
	tests := []struct {
		name   string
		access store.AccessControl
		want   int
	}{
		{"admin", store.AccessControl{Role: store.RoleAdmin}, http.StatusNoContent},
		{"write", store.AccessControl{Role: store.RoleWrite}, http.StatusNoContent},
		{"read", store.AccessControl{Role: store.RoleRead}, http.StatusForbidden},
		{"missing assignment", store.AccessControl{}, http.StatusForbidden},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			next := http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
				w.WriteHeader(http.StatusNoContent)
			})
			handler := RequirePermission(store.PermissionWriteContainers)(next)
			req := httptest.NewRequest(http.MethodPost, "/api/v1/containers/demo/update", nil)
			if tt.access.Valid() {
				req = req.WithContext(context.WithValue(req.Context(), accessContextKey, tt.access))
			}
			resp := httptest.NewRecorder()
			handler.ServeHTTP(resp, req)
			if resp.Code != tt.want {
				t.Fatalf("status = %d, want %d", resp.Code, tt.want)
			}
		})
	}
}

func TestRequireAuthGrantsFullAdminAccessOverTrustedLocalSocket(t *testing.T) {
	next := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		access, ok := AccessFromContext(r.Context())
		if !ok || !access.Can(store.PermissionManageUsers) {
			t.Fatal("trusted-local request did not get full admin access")
		}
		if ActorFromContext(r.Context()) == "" {
			t.Fatal("trusted-local request has no actor attached")
		}
		w.WriteHeader(http.StatusNoContent)
	})
	// st is nil on purpose: a trusted-local request must be recognized and
	// granted access before RequireAuth ever needs to touch the store —
	// passing nil makes any accidental fallthrough to the normal
	// API-key/session path panic loudly instead of silently "working".
	handler := RequireAuth(nil)(next)

	req := httptest.NewRequest(http.MethodGet, "/api/v1/containers", nil)
	req = req.WithContext(WithTrustedLocalAccess(req.Context()))
	resp := httptest.NewRecorder()
	handler.ServeHTTP(resp, req)

	if resp.Code != http.StatusNoContent {
		t.Fatalf("status = %d, want %d", resp.Code, http.StatusNoContent)
	}
}

func TestRequireAuthRejectsUnauthenticatedRequestWithoutTrustedLocalMarker(t *testing.T) {
	next := http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		t.Fatal("handler ran for an unauthenticated, non-trusted-local request")
	})
	st, err := store.Open(context.Background(), ":memory:")
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = st.Close() }()
	handler := RequireAuth(st)(next)

	req := httptest.NewRequest(http.MethodGet, "/api/v1/containers", nil)
	req.Header.Set("Accept", "application/json")
	resp := httptest.NewRecorder()
	handler.ServeHTTP(resp, req)

	if resp.Code != http.StatusUnauthorized {
		t.Fatalf("status = %d, want %d", resp.Code, http.StatusUnauthorized)
	}
}
