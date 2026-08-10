// Package registry resolves whether a newer image is available by talking
// to a container registry's HTTP API v2 directly (manifest digest
// lookups), without a full image pull. See docs/containers.md.
//
// Digest comparison catches moved tags. Strict stable semver selection is
// implemented in semver.go; OCI tag discovery and registry-specific
// connectivity live in tags.go/config.go.
//
// ListDockerHubTags + ResolveVersion cover a related but distinct need:
// turning a digest DoUpRo already has into a human-readable version
// (e.g. resolving "latest"'s current digest to "1.25.3") for display —
// Docker Hub uses its digest-bearing tags API; other OCI registries resolve
// selected version tags through their manifest digests.
package registry

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/url"
	"regexp"
	"strings"

	digest "github.com/opencontainers/go-digest"
)

// Ref is a parsed image reference: registry host, repository path, and
// tag. ParseRef fills in Docker Hub defaults for unqualified references
// the way `docker pull` does (bare "redis" -> "library/redis" on
// registry-1.docker.io).
type Ref struct {
	Registry   string
	Repository string
	Tag        string
}

// UpdateKind describes whether a pending digest change also carries a new
// resolved application version. A registry tag may be rebuilt for a base-image
// or security refresh while the publisher version remains unchanged.
const (
	UpdateKindVersion = "version"
	UpdateKindRebuild = "rebuild"
)

// ClassifyUpdate returns an API/UI-safe description of a pending update.
// An empty result means no digest update is pending. A rebuild is only claimed
// when both immutable images resolve to the same concrete version; unknown
// versions deliberately remain ordinary updates rather than guesses.
func ClassifyUpdate(currentVersion, availableVersion string, updateAvailable bool) string {
	if !updateAvailable {
		return ""
	}
	if currentVersion != "" && availableVersion != "" &&
		!strings.HasPrefix(currentVersion, "sha256:") &&
		!strings.HasPrefix(availableVersion, "sha256:") &&
		currentVersion == availableVersion {
		return UpdateKindRebuild
	}
	return UpdateKindVersion
}

// IsDigestReference reports whether image identifies immutable content
// instead of a registry tag. Docker exposes both raw image IDs
// ("sha256:...") and canonical repository digests ("repo@sha256:...")
// after a container has been recreated for rollback. Neither form can be
// used for the moving-tag comparison performed by Digest.
func IsDigestReference(image string) bool {
	image = strings.TrimSpace(image)
	if at := strings.LastIndex(image, "@"); at >= 0 {
		image = image[at+1:]
	}
	_, err := digest.Parse(image)
	return err == nil
}

// dockerHubUsername/dockerHubPassword optionally authenticate the Docker
// Hub token requests Digest/ListDockerHubTags make, in place of an
// anonymous pull — Docker Hub's authenticated rate limit ceiling is
// substantially higher than the anonymous one, which a host tracking
// many containers can hit repeatedly.
// Package-level rather than threaded through every call site: this is a
// single, process-lifetime setting sourced from one place (config) at
// startup, not a value that legitimately varies per call. Zero value
// (both empty, the default) means anonymous requests only, identical to
// before this existed.
var dockerHubUsername, dockerHubPassword string

// SetDockerHubCredentials configures the credentials authenticate() sends
// on every subsequent Docker Hub token request. Call once at daemon
// startup (see cmd/doupro/cmd_serve.go) before any check/pull runs; safe
// to never call at all (requests stay anonymous). Never logged or
// persisted by this package.
func SetDockerHubCredentials(username, password string) {
	dockerHubUsername, dockerHubPassword = username, password
}

// ParseRef parses an image reference as found in `docker inspect` output,
// e.g. "redis:7", "prestashop/prestashop:latest", or
// "ghcr.io/karakeep-app/karakeep:release".
func ParseRef(image string) Ref {
	remainder := image
	registry := "registry-1.docker.io"

	if idx := strings.Index(remainder, "/"); idx != -1 {
		first := remainder[:idx]
		if strings.ContainsAny(first, ".:") || first == "localhost" {
			registry = first
			remainder = remainder[idx+1:]
		}
	}

	repo, tag := remainder, "latest"
	if idx := strings.LastIndex(remainder, ":"); idx != -1 {
		repo, tag = remainder[:idx], remainder[idx+1:]
	}

	// Docker itself sometimes canonicalizes Hub image references with an
	// explicit "docker.io/" (or "index.docker.io/") prefix — e.g. in
	// ContainerList's Image field — but the actual registry API is served
	// from registry-1.docker.io, not docker.io itself.
	if registry == "docker.io" || registry == "index.docker.io" {
		registry = "registry-1.docker.io"
	}
	if registry == "registry-1.docker.io" && !strings.Contains(repo, "/") {
		repo = "library/" + repo
	}

	return Ref{Registry: registry, Repository: repo, Tag: tag}
}

var manifestAccept = strings.Join([]string{
	"application/vnd.docker.distribution.manifest.v2+json",
	"application/vnd.docker.distribution.manifest.list.v2+json",
	"application/vnd.oci.image.manifest.v1+json",
	"application/vnd.oci.image.index.v1+json",
}, ", ")

// Digest fetches the current manifest digest for ref's tag from its
// registry. Handles the standard OCI/Docker distribution bearer-token
// challenge (WWW-Authenticate) that Docker Hub, GHCR, Quay, and most v2
// registries require even for anonymous/public pulls — no registry is
// special-cased.
func Digest(ctx context.Context, httpClient *http.Client, ref Ref) (string, error) {
	var err error
	httpClient, err = clientFor(ref, httpClient)
	if err != nil {
		return "", err
	}

	manifestURL := fmt.Sprintf("https://%s/v2/%s/manifests/%s", ref.Registry, ref.Repository, ref.Tag)

	resp, err := doManifestRequest(ctx, httpClient, manifestURL, "")
	if err != nil {
		return "", err
	}
	defer resp.Body.Close() //nolint:errcheck // response not read further, nothing to recover

	if resp.StatusCode == http.StatusUnauthorized {
		challenge := resp.Header.Get("Www-Authenticate")
		resp.Body.Close() //nolint:errcheck // discarding this response in favor of a re-authenticated retry below
		if strings.HasPrefix(strings.ToLower(challenge), "basic") {
			cfg, ok := ConfigForHost(ref.Registry)
			if !ok || cfg.Username == "" {
				return "", fmt.Errorf("registry %s requires basic authentication", ref.Registry)
			}
			resp, err = doManifestRequestBasic(ctx, httpClient, manifestURL, cfg.Username, cfg.Password)
			if err != nil {
				return "", err
			}
			defer resp.Body.Close() //nolint:errcheck // response fully consumed below
		} else {
			token, authErr := authenticate(ctx, httpClient, challenge, ref, nil)
			if authErr != nil {
				return "", fmt.Errorf("authenticate against %s: %w", ref.Registry, authErr)
			}
			resp, err = doManifestRequest(ctx, httpClient, manifestURL, token)
			if err != nil {
				return "", err
			}
			defer resp.Body.Close() //nolint:errcheck // response fully consumed below
		}
	}

	if resp.StatusCode != http.StatusOK {
		return "", fmt.Errorf("manifest request for %s/%s:%s: unexpected status %s",
			ref.Registry, ref.Repository, ref.Tag, resp.Status)
	}

	if digest := resp.Header.Get("Docker-Content-Digest"); digest != "" {
		return digest, nil
	}
	return "", fmt.Errorf("registry response for %s/%s:%s had no Docker-Content-Digest header",
		ref.Registry, ref.Repository, ref.Tag)
}

func doManifestRequestBasic(ctx context.Context, client *http.Client, manifestURL, username, password string) (*http.Response, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, manifestURL, nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("Accept", manifestAccept)
	req.SetBasicAuth(username, password)
	return client.Do(req)
}

func doManifestRequest(ctx context.Context, client *http.Client, manifestURL, bearerToken string) (*http.Response, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, manifestURL, nil)
	if err != nil {
		return nil, fmt.Errorf("build manifest request: %w", err)
	}
	req.Header.Set("Accept", manifestAccept)
	if bearerToken != "" {
		req.Header.Set("Authorization", "Bearer "+bearerToken)
	}

	resp, err := client.Do(req)
	if err != nil {
		return nil, fmt.Errorf("request manifest: %w", err)
	}
	return resp, nil
}

// authenticate implements the bearer-token challenge described in
// https://distribution.github.io/distribution/spec/auth/token/ — parse
// the WWW-Authenticate header's realm/service/scope, then request a token
// from the realm. Every major public registry (Docker Hub, GHCR, Quay)
// implements this for anonymous pulls of public images.
func authenticate(ctx context.Context, client *http.Client, challenge string, ref Ref, explicit *Config) (string, error) {
	params := parseBearerChallenge(challenge)
	realm := params["realm"]
	if realm == "" {
		return "", fmt.Errorf("no bearer challenge in WWW-Authenticate: %q", challenge)
	}

	u, err := url.Parse(realm)
	if err != nil {
		return "", fmt.Errorf("parse auth realm %q: %w", realm, err)
	}
	q := u.Query()
	if service := params["service"]; service != "" {
		q.Set("service", service)
	}
	if scope := params["scope"]; scope != "" {
		q.Set("scope", scope)
	}
	u.RawQuery = q.Encode()

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, u.String(), nil)
	if err != nil {
		return "", fmt.Errorf("build auth request: %w", err)
	}
	// Basic-auth the token request as a real account when credentials are
	// configured and the realm is actually Docker Hub's own auth server —
	// never send them to some other registry's realm (GHCR, Quay, a
	// private registry) just because a request happened to need one.
	cfg, configured := ConfigForHost(ref.Registry)
	if explicit != nil {
		cfg, configured = *explicit, true
	}
	if configured && cfg.Username != "" {
		allowed := strings.EqualFold(u.Host, ref.Registry) || (cfg.AuthHost != "" && strings.EqualFold(u.Host, cfg.AuthHost))
		if !allowed {
			return "", fmt.Errorf("token realm host %q is not trusted for registry %q", u.Host, ref.Registry)
		}
		if u.Scheme != "https" {
			return "", fmt.Errorf("refusing credentials to non-HTTPS token realm %q", u.String())
		}
		req.SetBasicAuth(cfg.Username, cfg.Password)
	} else if dockerHubUsername != "" && strings.Contains(u.Host, "docker.io") {
		req.SetBasicAuth(dockerHubUsername, dockerHubPassword)
	}
	resp, err := client.Do(req)
	if err != nil {
		return "", fmt.Errorf("request auth token: %w", err)
	}
	defer resp.Body.Close() //nolint:errcheck // response fully consumed below
	if resp.StatusCode != http.StatusOK {
		return "", fmt.Errorf("auth token request: unexpected status %s", resp.Status)
	}

	var body struct {
		Token       string `json:"token"`
		AccessToken string `json:"access_token"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&body); err != nil {
		return "", fmt.Errorf("decode auth response: %w", err)
	}
	if body.Token != "" {
		return body.Token, nil
	}
	return body.AccessToken, nil
}

// hubTagsPageSize/hubTagsMaxPages bound how much of a repository's tag
// history ListDockerHubTags will walk in the worst case. Verified against
// the real Hub API (library/nginx, ~1300 tags): an October-2023 release
// still shows up by page 13 of the most-recently-updated ordering, so 20
// pages (2000 tags) comfortably covers even a long-lived, actively
// maintained image's full version history — not just its latest builds.
const (
	hubTagsPageSize = 100
	hubTagsMaxPages = 20
)

type hubTagsPage struct {
	Next    string `json:"next"`
	Results []struct {
		Name   string `json:"name"`
		Digest string `json:"digest"`
	} `json:"results"`
}

// ListDockerHubTags returns {tag name: digest} pairs for repo (e.g.
// "library/nginx") from Docker Hub's public web API
// (hub.docker.com/v2/repositories/...), ordered most-recently-updated
// first. This is deliberately not the registry v2 API Digest uses —
// that API has no tag-listing endpoint, and reconstructing one would
// mean a manifest fetch per tag, hitting the same anonymous pull rate
// limit already documented as a recurring problem in docs/containers.md.
// The web API returns each tag's manifest digest inline, under its own,
// more generous rate limit, so resolving a digest back to a human tag
// costs a handful of ordinary GETs, not one pull per candidate tag.
// Docker Hub only — callers must check the ref is registry-1.docker.io
// before calling this.
//
// wanted is the set of digests the caller actually needs resolved (e.g.
// the container's current and, if any, available digest) — pagination
// stops as soon as every one of them has been seen, instead of always
// walking to hubTagsMaxPages. This keeps the common case (a recent
// digest, found on page 1) cheap while still allowing a deep scan for
// an old pinned release that's fallen out of the recent-activity window
// (see IsVersionTag's doc comment for a concrete case this fixed). Pass
// a nil/empty set to always scan the full page cap.
func ListDockerHubTags(ctx context.Context, httpClient *http.Client, repo string, wanted map[string]bool) (map[string]string, error) {
	var err error
	httpClient, err = clientFor(Ref{Registry: "registry-1.docker.io", Repository: repo}, httpClient)
	if err != nil {
		return nil, err
	}

	tags := make(map[string]string)
	remaining := len(wanted)
	next := fmt.Sprintf("https://hub.docker.com/v2/repositories/%s/tags?page_size=%d&ordering=last_updated", repo, hubTagsPageSize)

	for page := 0; next != "" && page < hubTagsMaxPages; page++ {
		req, err := http.NewRequestWithContext(ctx, http.MethodGet, next, nil)
		if err != nil {
			return nil, fmt.Errorf("build tags request: %w", err)
		}
		resp, err := httpClient.Do(req)
		if err != nil {
			return nil, fmt.Errorf("request tags for %s: %w", repo, err)
		}

		if resp.StatusCode != http.StatusOK {
			resp.Body.Close() //nolint:errcheck // response not read further, nothing to recover
			return nil, fmt.Errorf("tags request for %s: unexpected status %s", repo, resp.Status)
		}

		var body hubTagsPage
		err = json.NewDecoder(resp.Body).Decode(&body)
		resp.Body.Close() //nolint:errcheck // response already fully read above
		if err != nil {
			return nil, fmt.Errorf("decode tags response for %s: %w", repo, err)
		}

		for _, t := range body.Results {
			if t.Digest == "" {
				continue
			}
			tags[t.Name] = t.Digest
			// Only a version-shaped tag name counts as "found" — a
			// wanted digest can also carry non-version aliases (e.g.
			// "1.25.3-bookworm" alongside plain "1.25.3"), and those can
			// appear on an earlier page than the plain tag ResolveVersion
			// actually wants. Stopping on the alias would risk recording
			// only that one and missing the better name still to come.
			if wanted[t.Digest] && IsVersionTag(t.Name) {
				wanted[t.Digest] = false
				remaining--
			}
		}
		if len(wanted) > 0 && remaining <= 0 {
			break
		}
		next = body.Next
	}

	return tags, nil
}

// floatingTags are mutable aliases that never count as "the" resolved
// version of an image, even when their digest matches — they're the
// thing being resolved away from, not a target.
var floatingTags = map[string]bool{
	"latest": true, "stable": true, "edge": true, "nightly": true,
	"master": true, "main": true, "dev": true, "beta": true,
	"alpha": true, "rc": true, "testing": true, "unstable": true,
}

// versionTagPattern matches tags that look like a real version number:
// an optional "v" prefix, digit-separated components (e.g. "1.25.3",
// "v3.7.10", "16"), and — critically — any number of trailing hyphenated
// qualifiers (e.g. "-alpine", "-apache", "-bookworm"). Many real images
// only ever publish a version *with* such a qualifier and no bare
// numeric equivalent (postgres:16-alpine, mautic:5-apache never get a
// plain "16"/"5" tag pointing at the same build) — requiring a bare
// number would call every one of those "unresolved" even though the tag
// is already exactly as pinned/informative as a plain version would be.
// It intentionally does not try to parse full semver with pre-release/
// build metadata — a rough shape check is enough to separate "16-alpine"
// from "latest" or "nightly" (neither starts with a digit).
var versionTagPattern = regexp.MustCompile(`^v?[0-9]+(\.[0-9]+){0,3}(-[A-Za-z0-9]+)*$`)

// compositeVersionTagPattern covers repositories that prefix the application
// release with a build/runtime variant, for example
// "php8.2-1.17.999". Requiring a three-component release avoids treating a
// runtime-only alias such as "php8.2" as the application version.
var compositeVersionTagPattern = regexp.MustCompile(`(^|[-_])v?[0-9]+\.[0-9]+\.[0-9]+([._-][A-Za-z0-9]+)*$`)

// IsVersionTag reports whether tag itself already looks like a real,
// pinned version (e.g. "11.5.3", "v3.7.10", "16-alpine") rather than a
// floating alias (e.g. "latest", "nightly", "stable"). When true, the
// tag itself is the resolved version — no digest/registry lookup needed
// or more accurate.
func IsVersionTag(tag string) bool {
	return !floatingTags[strings.ToLower(tag)] && (versionTagPattern.MatchString(tag) || compositeVersionTagPattern.MatchString(tag))
}

// NormalizeVersionLabel turns the standard OCI image version label into a
// displayable publisher version. Some build systems write a Git ref such as
// refs/tags/version/2025.2.4 instead of the tag value itself.
func NormalizeVersionLabel(value string) string {
	value = strings.TrimSpace(value)
	for _, prefix := range []string{"refs/tags/", "refs/heads/", "version/"} {
		value = strings.TrimPrefix(value, prefix)
	}
	if !IsVersionTag(value) {
		return ""
	}
	return value
}

// VersionFromLabels checks the OCI standard first, then its widely deployed
// pre-OCI label-schema predecessor. When repository is supplied, it also
// supports publisher-defined version labels in a repository-neutral way: a
// version-shaped value whose label key identifies the repository itself wins.
// This handles images that publish several component versions without ever
// guessing from an unrelated dependency label.
func VersionFromLabels(labels map[string]string, repository ...string) string {
	for _, key := range []string{"org.opencontainers.image.version", "org.label-schema.version"} {
		if version := NormalizeVersionLabel(labels[key]); version != "" {
			return version
		}
	}

	if len(repository) == 0 || strings.TrimSpace(repository[0]) == "" {
		return ""
	}
	repoName := repository[0]
	if slash := strings.LastIndex(repoName, "/"); slash >= 0 {
		repoName = repoName[slash+1:]
	}
	normalizeKey := func(value string) string {
		return strings.Map(func(r rune) rune {
			if r >= 'a' && r <= 'z' || r >= '0' && r <= '9' {
				return r
			}
			if r >= 'A' && r <= 'Z' {
				return r + ('a' - 'A')
			}
			return -1
		}, value)
	}
	repoKey := normalizeKey(repoName)
	if repoKey == "" {
		return ""
	}

	match := ""
	for key, value := range labels {
		keyLower := strings.ToLower(key)
		if !strings.Contains(keyLower, "version") || !strings.Contains(normalizeKey(key), repoKey) {
			continue
		}
		version := NormalizeVersionLabel(value)
		if version == "" {
			continue
		}
		if match != "" && match != version {
			return ""
		}
		match = version
	}
	return match
}

// ResolveVersion finds the most specific version-looking tag in tags
// whose digest equals target. Docker Hub commonly publishes the same
// image under several tags (e.g. "1.25.3", "1.25", "1", "latest" all
// pointing at one build) — among matches, the tag with the most
// dot-separated components wins (prefers "1.25.3" over "1.25" over
// "1"), since that's the most informative label for what's running.
// Returns ok=false if nothing in tags matches target, which callers
// should treat as "unresolved" and fall back to the raw tag, not an
// error — resolution is best-effort by nature (private registries,
// non-Hub registries, and Hub API hiccups all legitimately produce no
// match).
func ResolveVersion(tags map[string]string, target string) (string, bool) {
	if target == "" {
		return "", false
	}

	best := ""
	bestDirect, bestParts := false, -1
	for name, digest := range tags {
		if digest != target || floatingTags[strings.ToLower(name)] {
			continue
		}
		if !versionTagPattern.MatchString(name) {
			continue
		}
		direct := versionTagPattern.MatchString(name)
		parts := strings.Count(name, ".") + 1
		if best == "" || direct && !bestDirect || direct == bestDirect && (parts > bestParts || parts == bestParts && len(name) < len(best)) {
			best, bestDirect, bestParts = name, direct, parts
		}
	}
	return best, best != ""
}

// parseBearerChallenge parses a header like:
//
//	Bearer realm="https://auth.docker.io/token",service="registry.docker.io",scope="repository:library/redis:pull"
func parseBearerChallenge(header string) map[string]string {
	out := make(map[string]string)
	header = strings.TrimSpace(strings.TrimPrefix(header, "Bearer"))
	for _, part := range strings.Split(header, ",") {
		part = strings.TrimSpace(part)
		kv := strings.SplitN(part, "=", 2)
		if len(kv) != 2 {
			continue
		}
		out[kv[0]] = strings.Trim(kv[1], `"`)
	}
	return out
}
