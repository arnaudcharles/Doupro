// Package config loads bootstrap configuration from DOUPRO_* environment
// variables at process start. Runtime-editable settings (see
// docs/settings.md) are stored and served through the store/api packages
// instead of here — this package only covers values that require a
// restart to change.
package config

import (
	"os"
	"strconv"
	"strings"
)

// Config holds bootstrap-only settings read once at process start.
type Config struct {
	BindAddr          string
	DataDir           string
	DBPath            string
	SocketPath        string
	AllowInsecureTCP  bool
	LogLevel          string
	AdminUser         string
	AdminPassword     string
	TrustProxyHeaders bool
	ExtraCACert       string
	EncryptionKey     string
	EncryptionKeyFile string

	// LocalSocketPath, when non-empty, makes the daemon also listen on a
	// Unix domain socket at this path, serving the exact same API/web
	// routes but with every request treated as trusted-local admin access
	// — no session/API key required (see auth.WithTrustedLocalAccess).
	// This is what makes `docker exec doupro doupro ...` work without
	// --host/--api-key (docs/cli.md): reaching the socket at all already
	// requires being inside the container's mount namespace, which is
	// root-equivalent access to the same Docker socket DoUpRo holds.
	// Empty disables it entirely (e.g. a read-only or non-writable /data
	// mount, or an operator who'd rather not have it). Default lives
	// alongside the database on the one volume already guaranteed
	// writable by the non-root image user, rather than a path like
	// /var/run that doesn't exist in the distroless final image.
	LocalSocketPath string

	OIDCIssuerURL     string
	OIDCClientID      string
	OIDCClientSecret  string
	OIDCRedirectURL   string
	OIDCAllowedGroups []string
	OIDCExtraCACert   string

	// DockerHubUsername/DockerHubPassword, when both set, authenticate
	// every Docker Hub registry check and pull as a real account instead
	// of an anonymous one — Docker Hub's authenticated rate limit ceiling
	// is substantially higher than the anonymous one (the recurring "429
	// toomanyrequests" gap, see docs/containers.md). Optional; both empty
	// (the default) keeps every request anonymous, identical to before
	// this existed. A Docker Hub Personal Access Token is recommended
	// over the account password itself (scoped to "public repo read",
	// revocable independently) — see docs/containers.md.
	DockerHubUsername string
	DockerHubPassword string
}

// Load reads configuration from the environment, applying the same
// defaults documented in .env.example. AdminUser/AdminPassword have no
// default here — both empty means "use the zero-config admin/admin
// account", which is internal/api's bootstrap logic to apply, not this
// package's; giving AdminUser a hardcoded fallback while AdminPassword
// has none would make every unconfigured install look like a
// half-configured one instead.
func Load() (Config, error) {
	trustProxy, _ := strconv.ParseBool(getEnv("DOUPRO_TRUST_PROXY_HEADERS", "false"))
	allowInsecureTCP, _ := strconv.ParseBool(getEnv("DOUPRO_ALLOW_INSECURE_DOCKER_TCP", "false"))
	return Config{
		BindAddr:          getEnv("DOUPRO_BIND_ADDR", ":8080"),
		DataDir:           getEnv("DOUPRO_DATA_DIR", "/data"),
		DBPath:            getEnv("DOUPRO_DB_PATH", "/data/doupro.db"),
		SocketPath:        getEnv("DOUPRO_DOCKER_SOCKET", "/var/run/docker.sock"),
		AllowInsecureTCP:  allowInsecureTCP,
		LogLevel:          getEnv("DOUPRO_LOG_LEVEL", "info"),
		AdminUser:         getEnv("DOUPRO_ADMIN_USER", ""),
		AdminPassword:     getEnv("DOUPRO_ADMIN_PASSWORD", ""),
		TrustProxyHeaders: trustProxy,
		ExtraCACert:       getEnv("DOUPRO_EXTRA_CA_CERT_PATH", ""),
		EncryptionKey:     getEnv("DOUPRO_ENCRYPTION_KEY", ""),
		EncryptionKeyFile: getEnv("DOUPRO_ENCRYPTION_KEY_FILE", getEnv("DOUPRO_DATA_DIR", "/data")+"/doupro.key"),
		LocalSocketPath:   getEnv("DOUPRO_LOCAL_SOCKET", getEnv("DOUPRO_DATA_DIR", "/data")+"/doupro.sock"),

		OIDCIssuerURL:     getEnv("DOUPRO_OIDC_ISSUER_URL", ""),
		OIDCClientID:      getEnv("DOUPRO_OIDC_CLIENT_ID", ""),
		OIDCClientSecret:  getEnv("DOUPRO_OIDC_CLIENT_SECRET", ""),
		OIDCRedirectURL:   getEnv("DOUPRO_OIDC_REDIRECT_URL", ""),
		OIDCAllowedGroups: splitCSV(getEnv("DOUPRO_OIDC_ALLOWED_GROUPS", "")),
		OIDCExtraCACert:   getEnv("DOUPRO_OIDC_EXTRA_CA_CERT_PATH", ""),

		DockerHubUsername: getEnv("DOUPRO_DOCKERHUB_USERNAME", ""),
		DockerHubPassword: getEnv("DOUPRO_DOCKERHUB_PASSWORD", ""),
	}, nil
}

func splitCSV(v string) []string {
	if v == "" {
		return nil
	}
	parts := strings.Split(v, ",")
	out := make([]string, 0, len(parts))
	for _, p := range parts {
		if p = strings.TrimSpace(p); p != "" {
			out = append(out, p)
		}
	}
	return out
}

func getEnv(key, fallback string) string {
	if v, ok := os.LookupEnv(key); ok && v != "" {
		return v
	}
	return fallback
}
