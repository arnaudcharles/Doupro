package main

import (
	"context"
	"encoding/json"
	"fmt"
	"net"
	"net/http"
	"os"
	"os/signal"
	"strings"
	"syscall"
	"time"

	"github.com/spf13/cobra"

	doupro "github.com/arnaudcharles/doupro"
	"github.com/arnaudcharles/doupro/internal/api"
	"github.com/arnaudcharles/doupro/internal/auth"
	"github.com/arnaudcharles/doupro/internal/config"
	"github.com/arnaudcharles/doupro/internal/crashloop"
	"github.com/arnaudcharles/doupro/internal/docker"
	"github.com/arnaudcharles/doupro/internal/events"
	"github.com/arnaudcharles/doupro/internal/metrics"
	"github.com/arnaudcharles/doupro/internal/notifier"
	oidcauth "github.com/arnaudcharles/doupro/internal/oidc"
	"github.com/arnaudcharles/doupro/internal/registry"
	"github.com/arnaudcharles/doupro/internal/scheduler"
	"github.com/arnaudcharles/doupro/internal/secrets"
	"github.com/arnaudcharles/doupro/internal/store"
	"github.com/arnaudcharles/doupro/internal/tlsconfig"
	"github.com/arnaudcharles/doupro/internal/updater"
	"github.com/arnaudcharles/doupro/internal/web"
)

func serveCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "serve",
		Short: "Run the daemon (the container's default CMD)",
		RunE: func(cmd *cobra.Command, args []string) error {
			return serve()
		},
	}
}

func versionCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "version",
		Short: "Print the version",
		RunE: func(cmd *cobra.Command, args []string) error {
			fmt.Println(version)
			return nil
		},
	}
}

func healthcheckCmd() *cobra.Command {
	return &cobra.Command{
		Use:    "healthcheck",
		Short:  "Check the local daemon's /health endpoint (used by the Dockerfile HEALTHCHECK)",
		Hidden: true,
		RunE: func(cmd *cobra.Command, args []string) error {
			return healthcheck()
		},
	}
}

func selfUpdateHelperCmd() *cobra.Command {
	var parentID, targetImage, token, socket, dbPath string
	cmd := &cobra.Command{
		Use: "self-update-helper", Hidden: true,
		RunE: func(cmd *cobra.Command, args []string) error {
			ctx, cancel := context.WithTimeout(cmd.Context(), 15*time.Minute)
			defer cancel()
			return updater.RunSelfUpdateHelper(ctx, socket, dbPath, parentID, targetImage, token)
		},
	}
	cmd.Flags().StringVar(&parentID, "parent-id", "", "")
	cmd.Flags().StringVar(&targetImage, "target-image", "", "")
	cmd.Flags().StringVar(&token, "token", "", "")
	cmd.Flags().StringVar(&socket, "socket", "/var/run/docker.sock", "")
	cmd.Flags().StringVar(&dbPath, "db-path", "", "")
	_ = cmd.MarkFlagRequired("parent-id")
	_ = cmd.MarkFlagRequired("target-image")
	_ = cmd.MarkFlagRequired("token")
	return cmd
}

// serve runs the daemon: an HTTP server exposing the API and web UI, plus
// the scheduler's background loop (periodic registry checks, one-off and
// recurring schedule execution), until it receives SIGINT/SIGTERM, then
// shuts down gracefully.
func serve() error {
	cfg, err := config.Load()
	if err != nil {
		return fmt.Errorf("load config: %w", err)
	}
	if (cfg.DockerHubUsername == "") != (cfg.DockerHubPassword == "") {
		return fmt.Errorf("only one of DOUPRO_DOCKERHUB_USERNAME/DOUPRO_DOCKERHUB_PASSWORD is set — set both, or leave both unset to keep Docker Hub requests anonymous (see .env.example)")
	}
	if err := tlsconfig.ConfigureDefaultTransport(cfg.ExtraCACert); err != nil {
		return fmt.Errorf("configure outbound TLS trust: %w", err)
	}

	logger := events.New(cfg.LogLevel)
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	logger.Emit(ctx, events.Event{
		Level:   events.LevelInfo,
		Type:    "daemon.starting",
		Actor:   events.ActorSystem,
		Message: fmt.Sprintf("doupro %s starting on %s", version, cfg.BindAddr),
	})

	st, err := store.Open(ctx, cfg.DBPath)
	if err != nil {
		return fmt.Errorf("open store: %w", err)
	}
	defer st.Close() //nolint:errcheck // best-effort on daemon shutdown
	secretCipher, err := secrets.LoadOrCreate(cfg.EncryptionKey, cfg.EncryptionKeyFile)
	if err != nil {
		return fmt.Errorf("load secret encryption key: %w", err)
	}
	if err := st.ConfigureSecrets(ctx, secretCipher); err != nil {
		return fmt.Errorf("configure secret encryption: %w", err)
	}
	logger.SetSink(st)
	logger.Emit(ctx, events.Event{Level: events.LevelInfo, Type: "security.secret_encryption_ready",
		Actor: events.ActorSystem, Message: "reversible secrets are protected with AES-256-GCM",
		Metadata: map[string]any{"key_source": func() string {
			if cfg.EncryptionKey != "" {
				return "environment"
			}
			return "file"
		}()}})

	if err := api.Bootstrap(ctx, st, cfg.AdminUser, cfg.AdminPassword); err != nil {
		return fmt.Errorf("bootstrap admin account: %w", err)
	}

	// A bad issuer URL or an unreachable Authentik at startup disables SSO
	// for this run rather than crashing the daemon — local admin login must
	// keep working regardless (see internal/oidc.New).
	oidcProvider, err := oidcauth.New(ctx, cfg)
	if err != nil {
		logger.Emit(ctx, events.Event{
			Level: events.LevelError, Type: "auth.oidc_setup_failed", Actor: events.ActorSystem,
			Message: fmt.Sprintf("OIDC disabled for this run: %s", err),
		})
		oidcProvider = nil
	}

	// Applies to every subsequent registry.Digest call this process makes
	// (package-level — see registry.SetDockerHubCredentials for why).
	registry.SetDockerHubCredentials(cfg.DockerHubUsername, cfg.DockerHubPassword)
	storedRegistries, err := st.GetRegistryConfigs(ctx)
	if err != nil {
		return fmt.Errorf("load registry settings: %w", err)
	}
	runtimeRegistries := make([]registry.Config, 0, len(storedRegistries)+1)
	hasDockerHub := false
	for _, item := range storedRegistries {
		if item.Host == "registry-1.docker.io" || item.Host == "docker.io" {
			hasDockerHub = true
		}
		runtimeRegistries = append(runtimeRegistries, registry.Config{Host: item.Host, Username: item.Username, Password: item.Password,
			AuthHost: item.AuthHost, ProxyURL: item.ProxyURL, CACertPEM: item.CACertPEM})
	}
	if !hasDockerHub && cfg.DockerHubUsername != "" {
		runtimeRegistries = append(runtimeRegistries, registry.Config{Host: "registry-1.docker.io", Username: cfg.DockerHubUsername,
			Password: cfg.DockerHubPassword, AuthHost: "auth.docker.io"})
	}
	if err := registry.Configure(runtimeRegistries); err != nil {
		return fmt.Errorf("configure registries: %w", err)
	}

	dockerClient, err := docker.New(cfg.SocketPath, cfg.DockerHubUsername, cfg.DockerHubPassword)
	if err != nil {
		return fmt.Errorf("create docker client: %w", err)
	}
	defer dockerClient.Close() //nolint:errcheck // best-effort on daemon shutdown

	// If the configured Docker socket is a TCP URL, warn the operator at
	// startup: an exposed Docker TCP endpoint without TLS/mTLS is unsafe.
	if strings.HasPrefix(cfg.SocketPath, "tcp://") {
		logger.Emit(ctx, events.Event{Level: events.LevelWarn, Type: "security.docker_socket_tcp", Actor: events.ActorSystem,
			Message: "DOUPRO_DOCKER_SOCKET is configured as a tcp:// URL — ensure TLS/mTLS or use a socket-proxy (see SECURITY.md) to avoid exposing the Docker API without encryption/authentication",
		})
	}
	notif := notifier.New(st, logger)
	upd := updater.New(dockerClient, st, logger, notif)
	crashMonitor := crashloop.New(dockerClient, st, upd, logger)
	metrics.Register(st)

	// The initial discovery/registry-check pass runs in the background,
	// not inline here: with enough containers (or a slow/rate-limited
	// registry — Docker Hub's anonymous manifest rate limit bit exactly
	// this during testing) it can take a long time, and the API/web UI
	// must be reachable immediately regardless of how long that first
	// pass takes.
	sched := scheduler.New(dockerClient, st, upd, logger, notif, scheduler.DefaultCheckInterval)
	upd.ConfigureSelfUpdate(sched.SelfUpdateBlockers, stop)
	go func() {
		scheduler.Check(ctx, logger, dockerClient, st, notif)
		sched.Run(ctx)
	}()
	go crashMonitor.Run(ctx)
	go notif.Run(ctx)

	mux := http.NewServeMux()
	resolveVer := func(ctx context.Context, containerID, imageRef string) error {
		return scheduler.ResolveVersionAfterAction(ctx, logger, dockerClient, st, containerID, imageRef)
	}
	api.RegisterRoutes(mux, st, upd, notif, logger, cfg.TrustProxyHeaders, oidcProvider, resolveVer, dockerClient)
	metrics.RegisterRoute(mux)
	web.RegisterRoutes(mux, st, upd, notif, version, doupro.TemplatesFS, doupro.StaticFS, doupro.ManualsFS, oidcProvider != nil)

	srv := &http.Server{
		Addr:              cfg.BindAddr,
		Handler:           api.SecurityHeaders(mux),
		ReadHeaderTimeout: 5 * time.Second,
	}

	errCh := make(chan error, 1)
	go func() {
		if err := srv.ListenAndServe(); err != nil && err != http.ErrServerClosed {
			errCh <- err
		}
	}()

	localSrv, localListener, err := startLocalSocketServer(ctx, logger, cfg.LocalSocketPath, mux)
	if err != nil {
		return err
	}
	if localSrv != nil {
		go func() {
			if err := localSrv.Serve(localListener); err != nil && err != http.ErrServerClosed {
				errCh <- err
			}
		}()
	}

	select {
	case err := <-errCh:
		return fmt.Errorf("http server: %w", err)
	case <-ctx.Done():
	}

	logger.Emit(context.Background(), events.Event{
		Level:   events.LevelInfo,
		Type:    "daemon.stopping",
		Actor:   events.ActorSystem,
		Message: "shutting down",
	})

	shutdownCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if localSrv != nil {
		_ = localSrv.Shutdown(shutdownCtx)
		_ = os.Remove(cfg.LocalSocketPath)
	}
	return srv.Shutdown(shutdownCtx)
}

// startLocalSocketServer sets up the Unix-socket listener docs/cli.md
// describes for `docker exec doupro doupro ...` (see
// config.Config.LocalSocketPath and auth.WithTrustedLocalAccess). Returns
// nil, nil, nil when socketPath is empty (the operator opted out). A stale
// socket file from an unclean previous shutdown is removed first — Listen
// fails with "address already in use" otherwise, even though nothing is
// actually listening on it anymore.
func startLocalSocketServer(ctx context.Context, logger *events.Logger, socketPath string, mux *http.ServeMux) (*http.Server, net.Listener, error) {
	if socketPath == "" {
		return nil, nil, nil
	}
	if err := os.Remove(socketPath); err != nil && !os.IsNotExist(err) {
		return nil, nil, fmt.Errorf("remove stale local socket %s: %w", socketPath, err)
	}
	listener, err := net.Listen("unix", socketPath)
	if err != nil {
		return nil, nil, fmt.Errorf("listen on local socket %s: %w", socketPath, err)
	}
	// Defense in depth beyond the mount-namespace boundary that already
	// makes this socket unreachable from outside the container: only the
	// owning user/group (both "doupro" in the final image, see Dockerfile)
	// can connect, not "anyone able to see the file".
	if err := os.Chmod(socketPath, 0o660); err != nil {
		_ = listener.Close()
		return nil, nil, fmt.Errorf("set local socket permissions: %w", err)
	}

	trustedHandler := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mux.ServeHTTP(w, r.WithContext(auth.WithTrustedLocalAccess(r.Context())))
	})
	logger.Emit(ctx, events.Event{
		Level: events.LevelInfo, Type: "daemon.local_socket_listening", Actor: events.ActorSystem,
		Message: fmt.Sprintf("local CLI socket listening at %s (docker exec doupro doupro ... needs no --host/--api-key)", socketPath),
	})
	return &http.Server{Handler: api.SecurityHeaders(trustedHandler), ReadHeaderTimeout: 5 * time.Second}, listener, nil
}

// healthcheck backs the Dockerfile's HEALTHCHECK instruction: it calls the
// daemon's own /health endpoint over loopback and exits non-zero if it
// doesn't respond 200 OK.
func healthcheck() error {
	cfg, err := config.Load()
	if err != nil {
		return fmt.Errorf("load config: %w", err)
	}

	addr := cfg.BindAddr
	if strings.HasPrefix(addr, ":") {
		addr = "localhost" + addr
	}

	httpClient := http.Client{Timeout: 3 * time.Second}
	resp, err := httpClient.Get("http://" + addr + "/health")
	if err != nil {
		return err
	}
	defer resp.Body.Close() //nolint:errcheck // response already fully read below

	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("unexpected status %s", resp.Status)
	}

	var body struct {
		Status string `json:"status"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&body); err == nil && body.Status != "" && body.Status != "ok" {
		return fmt.Errorf("unhealthy status %q", body.Status)
	}
	return nil
}
