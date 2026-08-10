package store

import (
	"context"
	"strings"
	"testing"

	appsecrets "github.com/arnaudcharles/doupro/internal/secrets"
)

func TestRegistrySecretsEncryptedAndWriteOnlyMerge(t *testing.T) {
	ctx := context.Background()
	st, err := Open(ctx, ":memory:")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = st.Close() })
	cipher, _ := appsecrets.New(make([]byte, 32))
	if err := st.ConfigureSecrets(ctx, cipher); err != nil {
		t.Fatal(err)
	}
	if err := st.SetRegistryConfigs(ctx, []RegistryConfig{{Host: "registry.example", Username: "reader", Password: "secret-token", ProxyURL: "https://user:pass@proxy", CACertPEM: "pem-data"}}); err != nil {
		t.Fatal(err)
	}
	var raw string
	if err := st.db.QueryRow(`SELECT value FROM settings WHERE key='registries.configs'`).Scan(&raw); err != nil {
		t.Fatal(err)
	}
	if !strings.HasPrefix(raw, "enc:v1:") || strings.Contains(raw, "secret-token") {
		t.Fatalf("raw registry setting is not encrypted: %q", raw)
	}
	if err := st.SetRegistryConfigs(ctx, []RegistryConfig{{Host: "registry.example", Username: "reader"}}); err != nil {
		t.Fatal(err)
	}
	configs, err := st.GetRegistryConfigs(ctx)
	if err != nil || len(configs) != 1 || configs[0].Password != "secret-token" || configs[0].ProxyURL == "" || configs[0].CACertPEM == "" {
		t.Fatalf("configs=%+v err=%v", configs, err)
	}
}
