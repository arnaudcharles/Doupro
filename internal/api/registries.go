package api

import (
	"crypto/sha256"
	"crypto/x509"
	"encoding/hex"
	"encoding/json"
	"encoding/pem"
	"net/http"
	"strings"

	"github.com/arnaudcharles/doupro/internal/events"
	"github.com/arnaudcharles/doupro/internal/metrics"
	"github.com/arnaudcharles/doupro/internal/registry"
	"github.com/arnaudcharles/doupro/internal/store"
)

type registryConfigResponse struct {
	Host                  string `json:"host"`
	Username              string `json:"username,omitempty"`
	AuthHost              string `json:"auth_host,omitempty"`
	CredentialsConfigured bool   `json:"credentials_configured"`
	ProxyConfigured       bool   `json:"proxy_configured"`
	CAConfigured          bool   `json:"ca_configured"`
	CAFingerprint         string `json:"ca_sha256,omitempty"`
}

func handleTestRegistry(st *store.Store, logger *events.Logger) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		var requested store.RegistryConfig
		if err := json.NewDecoder(r.Body).Decode(&requested); err != nil {
			writeError(w, 400, "invalid_request", "invalid JSON body")
			return
		}
		requested.Host = strings.ToLower(strings.TrimSpace(requested.Host))
		if requested.Host == "" || strings.Contains(requested.Host, "://") {
			writeError(w, 400, "invalid_request", "registry host must be a host[:port] value without a scheme")
			return
		}
		existing, _ := st.GetRegistryConfigs(r.Context())
		for _, cfg := range existing {
			if strings.EqualFold(cfg.Host, requested.Host) {
				if requested.Username == cfg.Username && requested.Password == "" {
					requested.Password = cfg.Password
				}
				if requested.ProxyURL == "" {
					requested.ProxyURL = cfg.ProxyURL
				}
				if requested.CACertPEM == "" {
					requested.CACertPEM = cfg.CACertPEM
				}
				break
			}
		}
		cfg := registry.Config{Host: requested.Host, Username: requested.Username, Password: requested.Password,
			AuthHost: requested.AuthHost, ProxyURL: requested.ProxyURL, CACertPEM: requested.CACertPEM}
		if err := registry.TestConnection(r.Context(), cfg); err != nil {
			metrics.RegistryChecksTotal.WithLabelValues(requested.Host, "failed").Inc()
			logger.Emit(r.Context(), events.Event{Level: events.LevelWarn, Type: "registry.connection_test_failed", Actor: events.ActorUser,
				Message: "registry connection test failed", Metadata: map[string]any{"registry": requested.Host}})
			writeError(w, 502, "registry_connection_failed", err.Error())
			return
		}
		metrics.RegistryChecksTotal.WithLabelValues(requested.Host, "success").Inc()
		logger.Emit(r.Context(), events.Event{Level: events.LevelInfo, Type: "registry.connection_tested", Actor: events.ActorUser,
			Message: "registry connection test succeeded", Metadata: map[string]any{"registry": requested.Host}})
		writeJSON(w, 200, map[string]bool{"ok": true})
	}
}

func publicRegistryConfigs(configs []store.RegistryConfig) []registryConfigResponse {
	out := make([]registryConfigResponse, 0, len(configs))
	for _, cfg := range configs {
		item := registryConfigResponse{Host: cfg.Host, Username: cfg.Username, AuthHost: cfg.AuthHost,
			CredentialsConfigured: cfg.Username != "" && cfg.Password != "", ProxyConfigured: cfg.ProxyURL != "", CAConfigured: cfg.CACertPEM != ""}
		if cfg.CACertPEM != "" {
			item.CAFingerprint = caFingerprint(cfg.CACertPEM)
		}
		out = append(out, item)
	}
	return out
}

func caFingerprint(caPEM string) string {
	block, _ := pem.Decode([]byte(caPEM))
	if block == nil {
		return ""
	}
	cert, err := x509.ParseCertificate(block.Bytes)
	if err != nil {
		return ""
	}
	sum := sha256.Sum256(cert.Raw)
	return hex.EncodeToString(sum[:])
}

func handleGetRegistries(st *store.Store) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		configs, err := st.GetRegistryConfigs(r.Context())
		if err != nil {
			writeError(w, 500, "internal_error", "failed to load registries")
			return
		}
		writeJSON(w, 200, publicRegistryConfigs(configs))
	}
}

func handleSetRegistries(st *store.Store, logger *events.Logger) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		var configs []store.RegistryConfig
		if err := json.NewDecoder(r.Body).Decode(&configs); err != nil {
			writeError(w, 400, "invalid_request", "invalid JSON body")
			return
		}
		existing, _ := st.GetRegistryConfigs(r.Context())
		existingByHost := map[string]store.RegistryConfig{}
		for _, cfg := range existing {
			existingByHost[strings.ToLower(cfg.Host)] = cfg
		}
		seen := map[string]bool{}
		for _, cfg := range configs {
			host := strings.ToLower(strings.TrimSpace(cfg.Host))
			if host == "" || strings.Contains(host, "://") || seen[host] {
				writeError(w, 400, "invalid_request", "registry hosts must be unique host[:port] values without a scheme")
				return
			}
			seen[host] = true
			authHost := strings.ToLower(strings.TrimSpace(cfg.AuthHost))
			if strings.Contains(authHost, "://") || strings.ContainsAny(authHost, "/?#") {
				writeError(w, 400, "invalid_request", "token auth host must be a host[:port] value without a scheme or path")
				return
			}
			if cfg.Username == "" && cfg.Password != "" {
				writeError(w, 400, "invalid_request", "a username is required with a password/token")
				return
			}
			if cfg.Username != "" && cfg.Password == "" && existingByHost[host].Password == "" {
				writeError(w, 400, "invalid_request", "a password/token is required for a new credential")
				return
			}
			if err := registry.ValidateConfig(registry.Config{Host: host, ProxyURL: cfg.ProxyURL, CACertPEM: cfg.CACertPEM}); err != nil {
				writeError(w, 400, "invalid_request", err.Error())
				return
			}
		}
		if err := st.SetRegistryConfigs(r.Context(), configs); err != nil {
			writeError(w, 500, "internal_error", "failed to save registries")
			return
		}
		stored, err := st.GetRegistryConfigs(r.Context())
		if err != nil {
			writeError(w, 500, "internal_error", "failed to reload registries")
			return
		}
		runtime := make([]registry.Config, 0, len(stored))
		for _, cfg := range stored {
			runtime = append(runtime, registry.Config{Host: cfg.Host, Username: cfg.Username, Password: cfg.Password, AuthHost: cfg.AuthHost, ProxyURL: cfg.ProxyURL, CACertPEM: cfg.CACertPEM})
		}
		if err := registry.Configure(runtime); err != nil {
			writeError(w, 400, "invalid_request", err.Error())
			return
		}
		logger.Emit(r.Context(), events.Event{Level: events.LevelInfo, Type: "settings.registries_changed", Actor: events.ActorUser, Message: "registry configuration updated", Metadata: map[string]any{"count": len(stored)}})
		writeJSON(w, 200, publicRegistryConfigs(stored))
	}
}
