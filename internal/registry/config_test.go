package registry

import (
	"context"
	"encoding/pem"
	"net/http"
	"net/http/httptest"
	"net/url"
	"runtime"
	"strings"
	"testing"
)

func TestPrivateRegistryBasicAuthCustomCAAndTags(t *testing.T) {
	server := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		user, pass, ok := r.BasicAuth()
		if !ok || user != "reader" || pass != "token" {
			w.Header().Set("WWW-Authenticate", `Basic realm="registry"`)
			w.WriteHeader(401)
			return
		}
		switch {
		case strings.Contains(r.URL.Path, "/manifests/"):
			w.Header().Set("Docker-Content-Digest", "sha256:test")
			w.WriteHeader(200)
		case strings.HasSuffix(r.URL.Path, "/tags/list"):
			_, _ = w.Write([]byte(`{"name":"team/app","tags":["1.2.3","1.2.4"]}`))
		default:
			w.WriteHeader(404)
		}
	}))
	defer server.Close()
	host := strings.TrimPrefix(server.URL, "https://")
	ca := pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: server.Certificate().Raw})
	if err := Configure([]Config{{Host: host, Username: "reader", Password: "token", CACertPEM: string(ca)}}); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = Configure(nil) })
	ref := Ref{Registry: host, Repository: "team/app", Tag: "1.2.3"}
	digest, err := Digest(context.Background(), nil, ref)
	if err != nil || digest != "sha256:test" {
		t.Fatalf("digest=%q err=%v", digest, err)
	}
	tags, err := ListTags(context.Background(), nil, ref)
	if err != nil || len(tags) != 2 {
		t.Fatalf("tags=%v err=%v", tags, err)
	}
	resolved, err := ResolveVersionTags(context.Background(), nil, ref, map[string]bool{"sha256:test": true})
	if err != nil || len(resolved) != 1 {
		t.Fatalf("resolved=%v err=%v", resolved, err)
	}
}

func TestRegistryProxyIsApplied(t *testing.T) {
	cfg := Config{ProxyURL: "https://proxy.example:8443"}
	transport, err := transportFor(cfg)
	if err != nil {
		t.Fatal(err)
	}
	req := &http.Request{URL: &url.URL{Scheme: "https", Host: "registry.example"}}
	proxy, err := transport.Proxy(req)
	if err != nil || proxy.String() != cfg.ProxyURL {
		t.Fatalf("proxy=%v err=%v", proxy, err)
	}
}

func TestNormalizeVersionLabel(t *testing.T) {
	tests := map[string]string{
		"refs/tags/version/2025.2.4": "2025.2.4",
		"refs/tags/v1.13.2":          "v1.13.2",
		" 10.4.57-ls139 ":            "10.4.57-ls139",
		"nightly":                    "",
	}
	for input, want := range tests {
		if got := NormalizeVersionLabel(input); got != want {
			t.Errorf("NormalizeVersionLabel(%q)=%q want %q", input, got, want)
		}
	}
}

func TestVersionFromLegacyStandardLabel(t *testing.T) {
	if got := VersionFromLabels(map[string]string{"org.label-schema.version": "3.2.1"}); got != "3.2.1" {
		t.Fatalf("version=%q", got)
	}
}

func TestCompositeVersionTag(t *testing.T) {
	tests := map[string]bool{
		"php8.1-1.17.999": true,
		"php8.2-1.17.999": true,
		"php8.2":          false,
		"latest_php8.2":   false,
	}
	for tag, want := range tests {
		if got := IsVersionTag(tag); got != want {
			t.Errorf("IsVersionTag(%q)=%v want %v", tag, got, want)
		}
	}
}

func TestVersionFromRepositoryScopedPublisherLabel(t *testing.T) {
	labels := map[string]string{
		"tiredofit.tiredofit/alpine.git_changelog_version":        "7.10.30",
		"tiredofit.tiredofit/freescout.git_changelog_version":     "1.17.999",
		"tiredofit.tiredofit/nginx-php-fpm.git_changelog_version": "7.7.19",
	}
	if got := VersionFromLabels(labels, "tiredofit/freescout"); got != "1.17.999" {
		t.Fatalf("version=%q", got)
	}
	if got := VersionFromLabels(labels, "someone/unknown"); got != "" {
		t.Fatalf("unrelated repository version=%q", got)
	}
}

func TestResolveVersionPrefersPlainVersionOverCompositeVariant(t *testing.T) {
	tags := map[string]string{
		"php8.2-1.17.999": "sha256:same",
		"1.17.999":        "sha256:same",
	}
	if got, ok := ResolveVersion(tags, "sha256:same"); !ok || got != "1.17.999" {
		t.Fatalf("version=%q ok=%v", got, ok)
	}
}

func TestClassifyUpdate(t *testing.T) {
	tests := []struct {
		name      string
		current   string
		available string
		pending   bool
		want      string
	}{
		{name: "no update", current: "1.2.3", available: "1.2.3", want: ""},
		{name: "new version", current: "1.2.3", available: "1.2.4", pending: true, want: UpdateKindVersion},
		{name: "same resolved version rebuild", current: "7.0.2-apache", available: "7.0.2-apache", pending: true, want: UpdateKindRebuild},
		{name: "unknown versions remain update", current: "sha256:old", available: "sha256:new", pending: true, want: UpdateKindVersion},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := ClassifyUpdate(tt.current, tt.available, tt.pending); got != tt.want {
				t.Fatalf("ClassifyUpdate()=%q want %q", got, tt.want)
			}
		})
	}
}

func TestVersionLabelForDigestFollowsOCIIndexAndConfig(t *testing.T) {
	server := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case strings.HasSuffix(r.URL.Path, "/manifests/sha256:index"):
			_, _ = w.Write([]byte(`{"manifests":[{"digest":"sha256:platform","platform":{"os":"` + runtime.GOOS + `","architecture":"` + runtime.GOARCH + `"}}]}`))
		case strings.HasSuffix(r.URL.Path, "/manifests/sha256:platform"):
			_, _ = w.Write([]byte(`{"config":{"digest":"sha256:config"}}`))
		case strings.HasSuffix(r.URL.Path, "/blobs/sha256:config"):
			_, _ = w.Write([]byte(`{"config":{"Labels":{"org.opencontainers.image.version":"refs/tags/version/4.5.6"}}}`))
		default:
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	defer server.Close()
	host := strings.TrimPrefix(server.URL, "https://")
	ca := pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: server.Certificate().Raw})
	if err := Configure([]Config{{Host: host, CACertPEM: string(ca)}}); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = Configure(nil) })
	version, err := VersionLabelForDigest(context.Background(), nil, Ref{Registry: host, Repository: "team/app", Tag: "latest"}, "sha256:index")
	if err != nil || version != "4.5.6" {
		t.Fatalf("version=%q err=%v", version, err)
	}
}

func TestConnectionUsesBasicAuthAndCustomCA(t *testing.T) {
	server := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		user, pass, ok := r.BasicAuth()
		if !ok {
			w.Header().Set("WWW-Authenticate", `Basic realm="registry"`)
			w.WriteHeader(http.StatusUnauthorized)
			return
		}
		if user != "reader" || pass != "token" {
			w.WriteHeader(http.StatusForbidden)
			return
		}
		w.WriteHeader(http.StatusOK)
	}))
	defer server.Close()
	host := strings.TrimPrefix(server.URL, "https://")
	ca := pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: server.Certificate().Raw})
	if err := TestConnection(context.Background(), Config{Host: host, Username: "reader", Password: "token", CACertPEM: string(ca)}); err != nil {
		t.Fatal(err)
	}
}
