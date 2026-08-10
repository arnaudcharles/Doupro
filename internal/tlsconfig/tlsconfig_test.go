package tlsconfig

import (
	"encoding/pem"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"
)

func TestTransportWithExtraCATrustsCertificate(t *testing.T) {
	server := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusNoContent)
	}))
	defer server.Close()

	caPath := filepath.Join(t.TempDir(), "internal-ca.pem")
	certificate := pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: server.Certificate().Raw})
	if err := os.WriteFile(caPath, certificate, 0o600); err != nil {
		t.Fatal(err)
	}
	transport, err := transportWithExtraCA(http.DefaultTransport.(*http.Transport), caPath)
	if err != nil {
		t.Fatal(err)
	}
	client := &http.Client{Transport: transport}
	resp, err := client.Get(server.URL)
	if err != nil {
		t.Fatalf("request with extra CA failed: %v", err)
	}
	resp.Body.Close() //nolint:errcheck // test cleanup only
	if resp.StatusCode != http.StatusNoContent {
		t.Fatalf("status = %d, want %d", resp.StatusCode, http.StatusNoContent)
	}
}

func TestTransportWithExtraCARejectsInvalidPEM(t *testing.T) {
	caPath := filepath.Join(t.TempDir(), "invalid.pem")
	if err := os.WriteFile(caPath, []byte("not a certificate"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := transportWithExtraCA(http.DefaultTransport.(*http.Transport), caPath); err == nil {
		t.Fatal("expected invalid PEM to fail")
	}
}
