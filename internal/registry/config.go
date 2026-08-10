package registry

import (
	"context"
	"crypto/tls"
	"crypto/x509"
	"fmt"
	"net/http"
	"net/url"
	"strings"
	"sync"
	"time"
)

type Config struct {
	Host      string
	Username  string
	Password  string
	AuthHost  string
	ProxyURL  string
	CACertPEM string
}

var configured = struct {
	sync.RWMutex
	byHost map[string]Config
}{byHost: map[string]Config{}}

func Configure(configs []Config) error {
	next := make(map[string]Config, len(configs))
	for _, cfg := range configs {
		host := canonicalHost(cfg.Host)
		if host == "" {
			return fmt.Errorf("registry host is required")
		}
		cfg.Host = host
		if _, err := transportFor(cfg); err != nil {
			return fmt.Errorf("configure registry %s: %w", host, err)
		}
		next[host] = cfg
	}
	configured.Lock()
	configured.byHost = next
	configured.Unlock()
	return nil
}

func ValidateConfig(cfg Config) error { _, err := transportFor(cfg); return err }

func ConfigForHost(host string) (Config, bool) {
	configured.RLock()
	defer configured.RUnlock()
	cfg, ok := configured.byHost[canonicalHost(host)]
	return cfg, ok
}

func canonicalHost(host string) string {
	host = strings.TrimSpace(strings.ToLower(host))
	host = strings.TrimPrefix(host, "https://")
	host = strings.TrimSuffix(host, "/")
	if host == "docker.io" || host == "index.docker.io" {
		return "registry-1.docker.io"
	}
	return host
}

func clientFor(ref Ref, fallback *http.Client) (*http.Client, error) {
	cfg, ok := ConfigForHost(ref.Registry)
	if !ok {
		if fallback != nil {
			return fallback, nil
		}
		return &http.Client{Timeout: 10 * time.Second}, nil
	}
	return clientFromConfig(cfg, fallback)
}

func clientFromConfig(cfg Config, fallback *http.Client) (*http.Client, error) {
	transport, err := transportFor(cfg)
	if err != nil {
		return nil, err
	}
	timeout := 10 * time.Second
	if fallback != nil && fallback.Timeout > 0 {
		timeout = fallback.Timeout
	}
	return &http.Client{Transport: transport, Timeout: timeout}, nil
}

// TestConnection verifies the registry v2 endpoint with an isolated runtime
// configuration. It does not change the process-wide configuration or pull
// image layers, making it safe to use before a setting is persisted.
func TestConnection(ctx context.Context, cfg Config) error {
	cfg.Host = canonicalHost(cfg.Host)
	if cfg.Host == "" {
		return fmt.Errorf("registry host is required")
	}
	client, err := clientFromConfig(cfg, nil)
	if err != nil {
		return err
	}
	endpoint := "https://" + cfg.Host + "/v2/"
	request := func(authKind, value string) (*http.Response, error) {
		req, err := http.NewRequestWithContext(ctx, http.MethodGet, endpoint, nil)
		if err != nil {
			return nil, err
		}
		switch authKind {
		case "basic":
			req.SetBasicAuth(cfg.Username, cfg.Password)
		case "bearer":
			req.Header.Set("Authorization", "Bearer "+value)
		}
		return client.Do(req)
	}
	resp, err := request("", "")
	if err != nil {
		return fmt.Errorf("connect to registry %s: %w", cfg.Host, err)
	}
	if resp.StatusCode == http.StatusUnauthorized {
		challenge := resp.Header.Get("Www-Authenticate")
		resp.Body.Close() //nolint:errcheck // discarding this response in favor of a re-authenticated retry below
		if strings.HasPrefix(strings.ToLower(challenge), "basic") {
			if cfg.Username == "" {
				return fmt.Errorf("registry %s requires credentials", cfg.Host)
			}
			resp, err = request("basic", "")
		} else {
			token, authErr := authenticate(ctx, client, challenge, Ref{Registry: cfg.Host}, &cfg)
			if authErr != nil {
				return authErr
			}
			resp, err = request("bearer", token)
		}
		if err != nil {
			return fmt.Errorf("authenticate to registry %s: %w", cfg.Host, err)
		}
	}
	defer resp.Body.Close() //nolint:errcheck // response not read further, nothing to recover
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return fmt.Errorf("registry %s v2 check returned %s", cfg.Host, resp.Status)
	}
	return nil
}

func transportFor(cfg Config) (*http.Transport, error) {
	transport := http.DefaultTransport.(*http.Transport).Clone()
	if cfg.ProxyURL != "" {
		proxy, err := url.Parse(cfg.ProxyURL)
		if err != nil {
			return nil, fmt.Errorf("proxy URL is invalid")
		}
		if proxy.Scheme != "http" && proxy.Scheme != "https" {
			return nil, fmt.Errorf("proxy URL must use http or https")
		}
		transport.Proxy = http.ProxyURL(proxy)
	}
	if cfg.CACertPEM != "" {
		roots, err := x509.SystemCertPool()
		if err != nil {
			roots = x509.NewCertPool()
		}
		if !roots.AppendCertsFromPEM([]byte(cfg.CACertPEM)) {
			return nil, fmt.Errorf("custom CA contains no valid PEM certificate")
		}
		transport.TLSClientConfig = &tls.Config{MinVersion: tls.VersionTLS12, RootCAs: roots}
	}
	return transport, nil
}
