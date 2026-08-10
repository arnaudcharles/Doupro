// Package cliclient is the thin REST API client the doupro binary's CLI
// mode is built on (see docs/cli.md — the CLI has no business logic of
// its own, only argument parsing and HTTP calls). It talks to whatever
// DoUpRo instance --host/--api-key (or DOUPRO_HOST/DOUPRO_API_KEY) point
// at, or, when neither is set and the daemon's local Unix socket
// (config.Config.LocalSocketPath, default /data/doupro.sock) is reachable
// — the case `docker exec doupro doupro ...` runs in — that socket
// instead, with no API key needed at all (see
// auth.WithTrustedLocalAccess: reaching the socket already requires being
// inside the container's mount namespace, the same trust level as the
// Docker socket DoUpRo itself holds).
package cliclient

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"os"
	"strings"
	"time"
)

// Client is a small wrapper around the DoUpRo REST API (docs/api.md).
type Client struct {
	baseURL string
	apiKey  string
	http    *http.Client
}

// New builds a Client. host must include a scheme (e.g. "https://doupro.home.arpa").
func New(host, apiKey string) *Client {
	return &Client{
		baseURL: strings.TrimRight(host, "/"),
		apiKey:  apiKey,
		http:    &http.Client{Timeout: 30 * time.Second},
	}
}

// NewLocal builds a Client that talks to the daemon's local Unix socket
// instead of a TCP host — no API key needed (the socket connection itself
// is the credential; see auth.WithTrustedLocalAccess). The base URL's host
// part is never actually resolved (DialContext below ignores it and
// always dials socketPath), it just needs to be a syntactically valid URL.
func NewLocal(socketPath string) *Client {
	return &Client{
		baseURL: "http://local-socket",
		http: &http.Client{
			Timeout: 30 * time.Second,
			Transport: &http.Transport{
				DialContext: func(ctx context.Context, _, _ string) (net.Conn, error) {
					var d net.Dialer
					return d.DialContext(ctx, "unix", socketPath)
				},
			},
		},
	}
}

// LocalSocketReachable reports whether path looks like a live local-CLI
// socket (see NewLocal) — it exists and is actually a Unix socket, not
// e.g. a leftover regular file. It does not attempt to connect: a
// listening-but-momentarily-busy socket should still be tried, not
// silently skipped in favor of erroring out for lack of --host.
func LocalSocketReachable(path string) bool {
	if path == "" {
		return false
	}
	info, err := os.Stat(path)
	return err == nil && info.Mode()&os.ModeSocket != 0
}

type apiError struct {
	Error struct {
		Code    string `json:"code"`
		Message string `json:"message"`
	} `json:"error"`
}

func (c *Client) do(ctx context.Context, method, path string, query url.Values, body, out any) error {
	if c.baseURL == "" {
		return fmt.Errorf("no host configured — pass --host or set DOUPRO_HOST")
	}

	u := c.baseURL + path
	if len(query) > 0 {
		u += "?" + query.Encode()
	}

	var bodyReader io.Reader
	if body != nil {
		b, err := json.Marshal(body)
		if err != nil {
			return fmt.Errorf("encode request body: %w", err)
		}
		bodyReader = bytes.NewReader(b)
	}

	req, err := http.NewRequestWithContext(ctx, method, u, bodyReader)
	if err != nil {
		return fmt.Errorf("build request: %w", err)
	}
	req.Header.Set("Accept", "application/json")
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	if c.apiKey != "" {
		req.Header.Set("Authorization", "Bearer "+c.apiKey)
	}

	resp, err := c.http.Do(req)
	if err != nil {
		return fmt.Errorf("request %s %s: %w", method, path, err)
	}
	defer resp.Body.Close() //nolint:errcheck // response already fully read below

	respBody, err := io.ReadAll(resp.Body)
	if err != nil {
		return fmt.Errorf("read response: %w", err)
	}

	if resp.StatusCode >= 400 {
		var apiErr apiError
		if json.Unmarshal(respBody, &apiErr) == nil && apiErr.Error.Message != "" {
			return fmt.Errorf("%s", apiErr.Error.Message)
		}
		return fmt.Errorf("%s %s: unexpected status %s", method, path, resp.Status)
	}

	if out != nil && len(respBody) > 0 {
		if err := json.Unmarshal(respBody, out); err != nil {
			return fmt.Errorf("decode response: %w", err)
		}
	}
	return nil
}
