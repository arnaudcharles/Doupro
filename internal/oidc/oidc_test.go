package oidc

import (
	"context"
	"crypto/rand"
	"crypto/rsa"
	"crypto/sha256"
	"crypto/x509"
	"encoding/base64"
	"encoding/json"
	"encoding/pem"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"testing"
	"time"

	josejwt "github.com/go-jose/go-jose/v4"
	"github.com/go-jose/go-jose/v4/jwt"

	"github.com/arnaudcharles/doupro/internal/config"
)

// fakeIdP is a minimal OIDC provider — discovery document, JWKS, and a
// token endpoint that always returns one pre-signed ID token — enough to
// exercise Provider.Exchange's verification logic (issuer/audience/
// signature/nonce) without a real Authentik instance.
type fakeIdP struct {
	srv     *httptest.Server
	key     *rsa.PrivateKey
	idToken string // set per-test before the token endpoint is hit

	// expectedChallenge, if set, makes /token enforce PKCE like a real
	// IdP: it recomputes S256(code_verifier) from the request body and
	// rejects the exchange unless it matches. This is what caught the
	// real double-hashing bug in Provider.AuthCodeURL against Authentik —
	// earlier test runs left this unset and so never validated PKCE
	// end-to-end at all.
	expectedChallenge string
}

func newFakeIdP(t *testing.T) *fakeIdP {
	t.Helper()
	return newFakeIdPWithServer(t, false)
}

// newFakeIdPWithServer builds the fake IdP's handler and, if tlsServer,
// serves it over a self-signed TLS listener (httptest.NewTLSServer)
// instead of plain HTTP — used to exercise buildHTTPClient's extra-CA
// trust path the same way a self-signed internal Authentik would.
func newFakeIdPWithServer(t *testing.T, tlsServer bool) *fakeIdP {
	t.Helper()
	key, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		t.Fatalf("generate key: %v", err)
	}
	f := &fakeIdP{key: key}

	mux := http.NewServeMux()
	mux.HandleFunc("/.well-known/openid-configuration", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]any{
			"issuer":                                f.srv.URL,
			"authorization_endpoint":                f.srv.URL + "/authorize",
			"token_endpoint":                        f.srv.URL + "/token",
			"jwks_uri":                              f.srv.URL + "/jwks",
			"id_token_signing_alg_values_supported": []string{"RS256"},
		})
	})
	mux.HandleFunc("/jwks", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(josejwt.JSONWebKeySet{
			Keys: []josejwt.JSONWebKey{{Key: &f.key.PublicKey, KeyID: "test-key", Algorithm: "RS256", Use: "sig"}},
		})
	})
	mux.HandleFunc("/token", func(w http.ResponseWriter, r *http.Request) {
		if f.expectedChallenge != "" {
			_ = r.ParseForm()
			sum := sha256.Sum256([]byte(r.Form.Get("code_verifier")))
			if base64.RawURLEncoding.EncodeToString(sum[:]) != f.expectedChallenge {
				w.WriteHeader(http.StatusBadRequest)
				_ = json.NewEncoder(w).Encode(map[string]any{
					"error": "invalid_grant", "error_description": "code challenge did not match",
				})
				return
			}
		}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]any{
			"access_token": "fake-access-token",
			"token_type":   "Bearer",
			"id_token":     f.idToken,
		})
	})
	if tlsServer {
		f.srv = httptest.NewTLSServer(mux)
	} else {
		f.srv = httptest.NewServer(mux)
	}
	t.Cleanup(f.srv.Close)
	return f
}

// sign builds and signs an ID token, overriding claims from the base
// case via opts so each test can corrupt exactly the field it's checking.
func (f *fakeIdP) sign(t *testing.T, opts ...func(*jwt.Claims, *map[string]any)) string {
	t.Helper()
	signer, err := josejwt.NewSigner(josejwt.SigningKey{Algorithm: josejwt.RS256, Key: f.key}, (&josejwt.SignerOptions{}).WithType("JWT").WithHeader("kid", "test-key"))
	if err != nil {
		t.Fatalf("new signer: %v", err)
	}

	claims := jwt.Claims{
		Issuer:   f.srv.URL,
		Subject:  "user-123",
		Audience: jwt.Audience{"test-client"},
		Expiry:   jwt.NewNumericDate(time.Now().Add(time.Hour)),
		IssuedAt: jwt.NewNumericDate(time.Now()),
	}
	extra := map[string]any{
		"nonce":              "test-nonce",
		"preferred_username": "alice",
		"groups":             []string{"admins"},
	}
	for _, opt := range opts {
		opt(&claims, &extra)
	}

	token, err := jwt.Signed(signer).Claims(claims).Claims(extra).Serialize()
	if err != nil {
		t.Fatalf("sign token: %v", err)
	}
	return token
}

func newTestProvider(t *testing.T, f *fakeIdP, allowedGroups []string) *Provider {
	t.Helper()
	p, err := New(context.Background(), config.Config{
		OIDCIssuerURL:     f.srv.URL,
		OIDCClientID:      "test-client",
		OIDCClientSecret:  "test-secret",
		OIDCRedirectURL:   "https://doupro.example.com/api/v1/auth/oidc/callback",
		OIDCAllowedGroups: allowedGroups,
	})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	if p == nil {
		t.Fatal("expected a provider, got nil")
	}
	return p
}

func TestNew_DisabledWhenIssuerUnset(t *testing.T) {
	p, err := New(context.Background(), config.Config{})
	if err != nil {
		t.Fatalf("expected no error, got %v", err)
	}
	if p != nil {
		t.Fatal("expected nil provider when issuer URL is unset")
	}
}

func TestNew_ErrorsOnPartialConfig(t *testing.T) {
	_, err := New(context.Background(), config.Config{OIDCIssuerURL: "https://example.com"})
	if err == nil {
		t.Fatal("expected an error when client id/secret/redirect are missing")
	}
}

func TestExchange_ValidToken(t *testing.T) {
	f := newFakeIdP(t)
	f.idToken = f.sign(t)
	p := newTestProvider(t, f, nil)

	claims, err := p.Exchange(context.Background(), "any-code", "verifier", "test-nonce")
	if err != nil {
		t.Fatalf("Exchange: %v", err)
	}
	if claims.Subject != "user-123" {
		t.Errorf("Subject = %q, want user-123", claims.Subject)
	}
	if claims.Username != "alice" {
		t.Errorf("Username = %q, want alice", claims.Username)
	}
	if len(claims.Groups) != 1 || claims.Groups[0] != "admins" {
		t.Errorf("Groups = %v, want [admins]", claims.Groups)
	}
}

func TestExchange_NonceMismatch(t *testing.T) {
	f := newFakeIdP(t)
	f.idToken = f.sign(t)
	p := newTestProvider(t, f, nil)

	if _, err := p.Exchange(context.Background(), "any-code", "verifier", "wrong-nonce"); err == nil {
		t.Fatal("expected an error on nonce mismatch")
	}
}

func TestExchange_WrongAudience(t *testing.T) {
	f := newFakeIdP(t)
	f.idToken = f.sign(t, func(c *jwt.Claims, extra *map[string]any) {
		c.Audience = jwt.Audience{"someone-else"}
	})
	p := newTestProvider(t, f, nil)

	if _, err := p.Exchange(context.Background(), "any-code", "verifier", "test-nonce"); err == nil {
		t.Fatal("expected an error on audience mismatch")
	}
}

func TestExchange_ExpiredToken(t *testing.T) {
	f := newFakeIdP(t)
	f.idToken = f.sign(t, func(c *jwt.Claims, extra *map[string]any) {
		c.Expiry = jwt.NewNumericDate(time.Now().Add(-time.Hour))
	})
	p := newTestProvider(t, f, nil)

	if _, err := p.Exchange(context.Background(), "any-code", "verifier", "test-nonce"); err == nil {
		t.Fatal("expected an error on expired token")
	}
}

func TestGroupAllowed(t *testing.T) {
	f := newFakeIdP(t)

	tests := []struct {
		name    string
		allowed []string
		groups  []string
		want    bool
	}{
		{"no allowlist configured", nil, []string{"anything"}, true},
		{"member of allowed group", []string{"admins", "ops"}, []string{"ops"}, true},
		{"not a member of any allowed group", []string{"admins"}, []string{"guests"}, false},
		{"no groups claim at all", []string{"admins"}, nil, false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			p := newTestProvider(t, f, tt.allowed)
			got := p.GroupAllowed(Claims{Subject: "user-123", Groups: tt.groups})
			if got != tt.want {
				t.Errorf("GroupAllowed() = %v, want %v", got, tt.want)
			}
		})
	}
}

// writePEM writes cert's PEM encoding to a temp file and returns its path
// — stands in for the file an operator would mount in for
// DOUPRO_OIDC_EXTRA_CA_CERT_PATH.
func writePEM(t *testing.T, cert *x509.Certificate) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "ca.pem")
	block := pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: cert.Raw})
	if err := os.WriteFile(path, block, 0o600); err != nil {
		t.Fatalf("write PEM: %v", err)
	}
	return path
}

// TestNew_RejectsUntrustedTLS confirms discovery against a self-signed IdP
// fails by default — the same behavior that surfaced against a real
// internal Authentik instance during manual testing (self-signed
// *.example wildcard cert, not in the system trust store).
func TestNew_RejectsUntrustedTLS(t *testing.T) {
	f := newFakeIdPWithServer(t, true)
	f.idToken = f.sign(t)

	_, err := New(context.Background(), config.Config{
		OIDCIssuerURL:    f.srv.URL,
		OIDCClientID:     "test-client",
		OIDCClientSecret: "test-secret",
		OIDCRedirectURL:  "https://doupro.example.com/api/v1/auth/oidc/callback",
	})
	if err == nil {
		t.Fatal("expected discovery to fail against an untrusted self-signed server")
	}
}

// TestNew_TrustsExtraCACert confirms DOUPRO_OIDC_EXTRA_CA_CERT_PATH makes
// the same self-signed server trusted, additively — this is the fix for
// the real-world case above (an internal Authentik behind a self-signed
// or internal-CA certificate).
func TestNew_TrustsExtraCACert(t *testing.T) {
	f := newFakeIdPWithServer(t, true)
	f.idToken = f.sign(t)
	caPath := writePEM(t, f.srv.Certificate())

	p, err := New(context.Background(), config.Config{
		OIDCIssuerURL:    f.srv.URL,
		OIDCClientID:     "test-client",
		OIDCClientSecret: "test-secret",
		OIDCRedirectURL:  "https://doupro.example.com/api/v1/auth/oidc/callback",
		OIDCExtraCACert:  caPath,
	})
	if err != nil {
		t.Fatalf("New: %v", err)
	}

	claims, err := p.Exchange(context.Background(), "any-code", "verifier", "test-nonce")
	if err != nil {
		t.Fatalf("Exchange: %v", err)
	}
	if claims.Subject != "user-123" {
		t.Errorf("Subject = %q, want user-123", claims.Subject)
	}
}

// TestAuthCodeURL_ChallengeMatchesVerifierAtExchange is the regression test
// for a real bug found testing against Authentik: AuthCodeURL used to take
// an already-S256-hashed challenge and hash it again internally (via
// S256ChallengeOption), so the code_challenge sent to the IdP didn't
// correspond to the verifier later sent at token exchange — Authentik
// rejected every login with "Code challenge not matching". This test
// round-trips through the real methods callers use (AuthCodeURL to get the
// redirect URL, then Exchange with the same verifier) against a fake IdP
// that actually enforces PKCE, so a reintroduced double-hash fails here
// instead of only against a real IdP.
func TestAuthCodeURL_ChallengeMatchesVerifierAtExchange(t *testing.T) {
	f := newFakeIdP(t)
	f.idToken = f.sign(t)
	p := newTestProvider(t, f, nil)

	verifier := "a-fixed-test-verifier-1234567890"
	redirectURL := p.AuthCodeURL("state-1", "test-nonce", verifier)

	parsed, err := url.Parse(redirectURL)
	if err != nil {
		t.Fatalf("parse AuthCodeURL result: %v", err)
	}
	f.expectedChallenge = parsed.Query().Get("code_challenge")
	if f.expectedChallenge == "" {
		t.Fatal("AuthCodeURL did not include a code_challenge param")
	}

	if _, err := p.Exchange(context.Background(), "any-code", verifier, "test-nonce"); err != nil {
		t.Fatalf("Exchange with the matching verifier failed: %v", err)
	}
}
