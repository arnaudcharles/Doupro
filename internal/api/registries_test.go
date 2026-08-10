package api

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/arnaudcharles/doupro/internal/events"
	appsecrets "github.com/arnaudcharles/doupro/internal/secrets"
	"github.com/arnaudcharles/doupro/internal/store"
)

func TestRegistrySettingsAreWriteOnlyAndPreserved(t *testing.T) {
	ctx := context.Background()
	st, err := store.Open(ctx, ":memory:")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = st.Close() })
	cipher, err := appsecrets.New(make([]byte, 32))
	if err != nil {
		t.Fatal(err)
	}
	if err := st.ConfigureSecrets(ctx, cipher); err != nil {
		t.Fatal(err)
	}
	logger := events.New("error")

	set := httptest.NewRequest(http.MethodPatch, "/api/v1/settings/registries", strings.NewReader(`[{"host":"registry.example","username":"reader","password":"secret-token","proxy_url":"https://user:pass@proxy.example"}]`))
	setResp := httptest.NewRecorder()
	handleSetRegistries(st, logger).ServeHTTP(setResp, set)
	if setResp.Code != http.StatusOK || strings.Contains(setResp.Body.String(), "secret-token") || strings.Contains(setResp.Body.String(), "user:pass") {
		t.Fatalf("status=%d body=%s", setResp.Code, setResp.Body.String())
	}

	patch := httptest.NewRequest(http.MethodPatch, "/api/v1/settings/registries", strings.NewReader(`[{"host":"registry.example","username":"reader"}]`))
	patchResp := httptest.NewRecorder()
	handleSetRegistries(st, logger).ServeHTTP(patchResp, patch)
	if patchResp.Code != http.StatusOK {
		t.Fatalf("status=%d body=%s", patchResp.Code, patchResp.Body.String())
	}
	configs, err := st.GetRegistryConfigs(ctx)
	if err != nil || len(configs) != 1 || configs[0].Password != "secret-token" || configs[0].ProxyURL == "" {
		t.Fatalf("configs=%+v err=%v", configs, err)
	}
}

func TestRegistrySettingsRejectUntrustedAuthHostShape(t *testing.T) {
	ctx := context.Background()
	st, err := store.Open(ctx, ":memory:")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = st.Close() })
	cipher, _ := appsecrets.New(make([]byte, 32))
	if err := st.ConfigureSecrets(ctx, cipher); err != nil {
		t.Fatal(err)
	}
	req := httptest.NewRequest(http.MethodPatch, "/api/v1/settings/registries", strings.NewReader(`[{"host":"registry.example","auth_host":"https://evil.example/token"}]`))
	resp := httptest.NewRecorder()
	handleSetRegistries(st, events.New("error")).ServeHTTP(resp, req)
	if resp.Code != http.StatusBadRequest {
		t.Fatalf("status=%d body=%s", resp.Code, resp.Body.String())
	}
}
