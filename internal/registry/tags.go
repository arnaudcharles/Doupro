package registry

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/url"
	"runtime"
	"sort"
	"strings"
)

const maxTagPages = 20

// VersionLabelForDigest resolves a manifest digest through the standard OCI
// manifest/config graph and returns org.opencontainers.image.version. For a
// multi-platform index it selects the platform matching the running DoUpRo
// binary (normally the Docker host platform). No image layers are downloaded.
func VersionLabelForDigest(ctx context.Context, fallback *http.Client, ref Ref, manifestDigest string) (string, error) {
	client, err := clientFor(ref, fallback)
	if err != nil {
		return "", err
	}
	return versionLabelForManifest(ctx, client, ref, manifestDigest, 0)
}

func versionLabelForManifest(ctx context.Context, client *http.Client, ref Ref, reference string, depth int) (string, error) {
	if depth > 1 {
		return "", nil
	}
	manifestURL := fmt.Sprintf("https://%s/v2/%s/manifests/%s", ref.Registry, ref.Repository, reference)
	resp, err := authenticatedRegistryGET(ctx, client, manifestURL, ref, manifestAccept)
	if err != nil {
		return "", err
	}
	defer resp.Body.Close() //nolint:errcheck // response not read further, nothing to recover
	if resp.StatusCode != http.StatusOK {
		return "", fmt.Errorf("manifest config request for %s/%s: unexpected status %s", ref.Registry, ref.Repository, resp.Status)
	}
	var manifest struct {
		Annotations map[string]string `json:"annotations"`
		Config      struct {
			Digest string `json:"digest"`
		} `json:"config"`
		Manifests []struct {
			Digest   string `json:"digest"`
			Platform struct {
				OS           string `json:"os"`
				Architecture string `json:"architecture"`
			} `json:"platform"`
		} `json:"manifests"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&manifest); err != nil {
		return "", fmt.Errorf("decode registry manifest config: %w", err)
	}
	if version := VersionFromLabels(manifest.Annotations, ref.Repository); version != "" {
		return version, nil
	}
	if manifest.Config.Digest == "" {
		selected := ""
		for _, item := range manifest.Manifests {
			if item.Platform.OS == runtime.GOOS && item.Platform.Architecture == runtime.GOARCH {
				selected = item.Digest
				break
			}
		}
		if selected == "" && len(manifest.Manifests) > 0 {
			selected = manifest.Manifests[0].Digest
		}
		if selected == "" {
			return "", nil
		}
		return versionLabelForManifest(ctx, client, ref, selected, depth+1)
	}
	blobURL := fmt.Sprintf("https://%s/v2/%s/blobs/%s", ref.Registry, ref.Repository, manifest.Config.Digest)
	blob, err := authenticatedRegistryGET(ctx, client, blobURL, ref, "application/vnd.oci.image.config.v1+json, application/vnd.docker.container.image.v1+json")
	if err != nil {
		return "", err
	}
	defer blob.Body.Close() //nolint:errcheck // response fully decoded below, nothing to recover
	if blob.StatusCode != http.StatusOK {
		return "", fmt.Errorf("image config request for %s/%s: unexpected status %s", ref.Registry, ref.Repository, blob.Status)
	}
	var config struct {
		Config struct {
			Labels map[string]string `json:"Labels"`
		} `json:"config"`
	}
	if err := json.NewDecoder(blob.Body).Decode(&config); err != nil {
		return "", fmt.Errorf("decode registry image config: %w", err)
	}
	return VersionFromLabels(config.Config.Labels, ref.Repository), nil
}

func authenticatedRegistryGET(ctx context.Context, client *http.Client, rawURL string, ref Ref, accept string) (*http.Response, error) {
	request := func(kind, value, username, password string) (*http.Response, error) {
		req, err := http.NewRequestWithContext(ctx, http.MethodGet, rawURL, nil)
		if err != nil {
			return nil, err
		}
		if accept != "" {
			req.Header.Set("Accept", accept)
		}
		switch kind {
		case "basic":
			req.SetBasicAuth(username, password)
		case "bearer":
			req.Header.Set("Authorization", "Bearer "+value)
		}
		return client.Do(req)
	}
	resp, err := request("", "", "", "")
	if err != nil || resp.StatusCode != http.StatusUnauthorized {
		return resp, err
	}
	challenge := resp.Header.Get("Www-Authenticate")
	resp.Body.Close() //nolint:errcheck // no actionable recovery from this error
	if strings.HasPrefix(strings.ToLower(challenge), "basic") {
		cfg, ok := ConfigForHost(ref.Registry)
		if !ok || cfg.Username == "" {
			return nil, fmt.Errorf("registry requires authentication")
		}
		return request("basic", "", cfg.Username, cfg.Password)
	}
	token, err := authenticate(ctx, client, challenge, ref, nil)
	if err != nil {
		return nil, err
	}
	return request("bearer", token, "", "")
}

// ListTags uses the OCI Distribution tags/list endpoint for Docker Hub and
// private registries. The configured proxy, CA and credentials are applied.
func ListTags(ctx context.Context, fallback *http.Client, ref Ref) (map[string]string, error) {
	client, err := clientFor(ref, fallback)
	if err != nil {
		return nil, err
	}
	next := fmt.Sprintf("https://%s/v2/%s/tags/list?n=10000", ref.Registry, ref.Repository)
	out := make(map[string]string)
	for page := 0; next != "" && page < maxTagPages; page++ {
		resp, requestErr := tagsRequest(ctx, client, next, ref)
		if requestErr != nil {
			return nil, requestErr
		}
		if resp.StatusCode != http.StatusOK {
			status := resp.Status
			resp.Body.Close() //nolint:errcheck // no actionable recovery from this error
			return nil, fmt.Errorf("tags request for %s/%s: unexpected status %s", ref.Registry, ref.Repository, status)
		}
		var body struct {
			Tags []string `json:"tags"`
		}
		decodeErr := json.NewDecoder(resp.Body).Decode(&body)
		link := resp.Header.Get("Link")
		resp.Body.Close() //nolint:errcheck // no actionable recovery from this error
		if decodeErr != nil {
			return nil, fmt.Errorf("decode tags response: %w", decodeErr)
		}
		for _, tag := range body.Tags {
			out[tag] = ""
		}
		next = nextTagPage(next, link)
	}
	// Resolve only the selected candidates later; the empty digest values are
	// sufficient for semantic ordering and avoid one manifest call per tag.
	return out, nil
}

func tagsRequest(ctx context.Context, client *http.Client, rawURL string, ref Ref) (*http.Response, error) {
	resp, err := registryGET(ctx, client, rawURL, ref, "")
	if err != nil || resp.StatusCode != http.StatusUnauthorized {
		return resp, err
	}
	challenge := resp.Header.Get("Www-Authenticate")
	resp.Body.Close() //nolint:errcheck // no actionable recovery from this error
	if strings.HasPrefix(strings.ToLower(challenge), "basic") {
		cfg, ok := ConfigForHost(ref.Registry)
		if !ok || cfg.Username == "" {
			return nil, fmt.Errorf("registry requires authentication")
		}
		return registryGETBasic(ctx, client, rawURL, cfg.Username, cfg.Password)
	}
	token, err := authenticate(ctx, client, challenge, ref, nil)
	if err != nil {
		return nil, err
	}
	return registryGET(ctx, client, rawURL, ref, token)
}

func nextTagPage(current, link string) string {
	start, end := strings.Index(link, "<"), strings.Index(link, ">")
	if start < 0 || end <= start || !strings.Contains(link[end:], `rel="next"`) {
		return ""
	}
	next, err := url.Parse(link[start+1 : end])
	if err != nil {
		return ""
	}
	base, err := url.Parse(current)
	if err != nil {
		return ""
	}
	return base.ResolveReference(next).String()
}

// ResolveVersionTags returns version-shaped tags whose manifest digest
// matches one of wanted. Docker Hub uses its digest-bearing tags API; other
// OCI registries expose only tag names, so their candidate manifests are
// checked in most-specific-first order and stop as soon as every digest has
// a concrete version.
func ResolveVersionTags(ctx context.Context, fallback *http.Client, ref Ref, wanted map[string]bool) (map[string]string, error) {
	if ref.Registry == "registry-1.docker.io" {
		copyWanted := make(map[string]bool, len(wanted))
		for digest, needed := range wanted {
			copyWanted[digest] = needed
		}
		return ListDockerHubTags(ctx, fallback, ref.Repository, copyWanted)
	}
	tags, err := ListTags(ctx, fallback, ref)
	if err != nil {
		return nil, err
	}
	candidates := make([]string, 0, len(tags))
	for tag := range tags {
		if IsVersionTag(tag) {
			candidates = append(candidates, tag)
		}
	}
	sort.Slice(candidates, func(i, j int) bool {
		iParts, jParts := strings.Count(candidates[i], "."), strings.Count(candidates[j], ".")
		if iParts != jParts {
			return iParts > jParts
		}
		if len(candidates[i]) != len(candidates[j]) {
			return len(candidates[i]) < len(candidates[j])
		}
		return candidates[i] > candidates[j]
	})
	remaining := make(map[string]bool, len(wanted))
	for digest, needed := range wanted {
		if needed {
			remaining[digest] = true
		}
	}
	resolved := make(map[string]string)
	client, err := clientFor(ref, fallback)
	if err != nil {
		return nil, err
	}
	session := &manifestSession{client: client, ref: ref}
	var lastErr error
	for _, tag := range candidates {
		digest, digestErr := session.digest(ctx, tag)
		if digestErr != nil {
			lastErr = digestErr
			continue
		}
		if remaining[digest] {
			resolved[tag] = digest
			delete(remaining, digest)
			if len(remaining) == 0 {
				break
			}
		}
	}
	if len(resolved) == 0 && lastErr != nil {
		return nil, lastErr
	}
	return resolved, nil
}

type manifestSession struct {
	client   *http.Client
	ref      Ref
	authKind string
	token    string
	username string
	password string
}

func (s *manifestSession) digest(ctx context.Context, tag string) (string, error) {
	manifestURL := fmt.Sprintf("https://%s/v2/%s/manifests/%s", s.ref.Registry, s.ref.Repository, tag)
	request := func() (*http.Response, error) {
		switch s.authKind {
		case "basic":
			return doManifestRequestBasic(ctx, s.client, manifestURL, s.username, s.password)
		case "bearer":
			return doManifestRequest(ctx, s.client, manifestURL, s.token)
		default:
			return doManifestRequest(ctx, s.client, manifestURL, "")
		}
	}
	resp, err := request()
	if err != nil {
		return "", err
	}
	if resp.StatusCode == http.StatusUnauthorized && s.authKind == "" {
		challenge := resp.Header.Get("Www-Authenticate")
		resp.Body.Close() //nolint:errcheck // no actionable recovery from this error
		if strings.HasPrefix(strings.ToLower(challenge), "basic") {
			cfg, ok := ConfigForHost(s.ref.Registry)
			if !ok || cfg.Username == "" {
				return "", fmt.Errorf("registry requires authentication")
			}
			s.authKind, s.username, s.password = "basic", cfg.Username, cfg.Password
		} else {
			token, authErr := authenticate(ctx, s.client, challenge, s.ref, nil)
			if authErr != nil {
				return "", authErr
			}
			s.authKind, s.token = "bearer", token
		}
		resp, err = request()
		if err != nil {
			return "", err
		}
	}
	defer resp.Body.Close() //nolint:errcheck // no actionable recovery from this error
	if resp.StatusCode != http.StatusOK {
		return "", fmt.Errorf("manifest request for %s/%s:%s: unexpected status %s", s.ref.Registry, s.ref.Repository, tag, resp.Status)
	}
	digest := resp.Header.Get("Docker-Content-Digest")
	if digest == "" {
		return "", fmt.Errorf("registry response for %s/%s:%s had no Docker-Content-Digest header", s.ref.Registry, s.ref.Repository, tag)
	}
	return digest, nil
}

func registryGET(ctx context.Context, client *http.Client, rawURL string, _ Ref, token string) (*http.Response, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, rawURL, nil)
	if err != nil {
		return nil, err
	}
	if token != "" {
		req.Header.Set("Authorization", "Bearer "+token)
	}
	return client.Do(req)
}

func registryGETBasic(ctx context.Context, client *http.Client, rawURL, username, password string) (*http.Response, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, rawURL, nil)
	if err != nil {
		return nil, err
	}
	req.SetBasicAuth(username, password)
	return client.Do(req)
}
