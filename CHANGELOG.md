# Changelog

All notable changes to this project are documented in this file.

The format is based on [Keep a Changelog](https://keepachangelog.com/en/1.1.0/),
and this project adheres to [Semantic Versioning](https://semver.org/) once
it reaches `1.0.0`.

## [Unreleased]

## [0.3.0] - 2026-08-21

### Fixed

- Container state (`running`/`exited`/`paused`, ...) could go stale on the
  Containers page for up to the full check interval (30 minutes by
  default) after a container was started, stopped, or killed outside
  DoUpRo — e.g. manually via `docker start` after a host reboot. The
  crash-loop monitor's already-open Docker event stream now updates a
  container's stored state immediately on `start`/`stop`/`kill`/`die`/
  `pause`/`unpause`/`oom`, independent of crash-loop detection; the
  periodic check remains the fallback for anything the event stream
  misses (reconnect gaps, daemon restarts).
- A missing `rows.Err()` check after the plaintext-secret migration's
  `notification_queue` scan loop meant an interrupted iteration (as
  opposed to a clean end-of-rows) was silently swallowed, letting the
  migration transaction commit having encrypted only part of the queued
  secrets.

## [0.2.0] - 2026-08-17

Everything below shipped after the `0.1.0` tag, verified end-to-end
against a real homelab, not just unit tests.

### Added

- **RBAC**: three roles (admin/write/read) plus four fine-grained
  capabilities (view Logs, manage schedules, manage notifications, view
  Stats), for both local/OIDC users and API keys. The bootstrap
  administrator is protected — no editable permissions, can't be deleted
  or demoted.
- **Per-container operation lock**: a second Update/Rollback against the
  same container while one is already running is rejected (`409
  operation_in_progress`) instead of racing it.
- **Automatic crash-loop rollback**: a container that crashes repeatedly
  within a configurable window after an update is reverted on its own —
  no operator action required. Manual Update/Rollback still always
  available.
- **Persistent notification queue**: at-least-once delivery with
  automatic retries and dead-lettering after exhausting attempts,
  survives a restart.
- **Encrypted secrets at rest**: registry credentials and notification
  channel URLs/tokens stored as AES-256-GCM ciphertext, not plaintext.
- **Semver-aware schedules**: bump-policy target selection (patch/minor/
  major/digest-only) for both one-off and recurring schedules; `relative`
  and `delayed` recurring policies are now actually executed by the
  scheduler, not just creatable.
- **Authenticated private registries**: credentials, proxy, and custom CA
  certificate per registry host, on top of the existing Docker Hub
  anonymous-rate-limit workaround.
- **Registry image version identity**: resolves the concrete published
  version behind a floating tag (OCI labels, repository-scoped version
  labels, or version-shaped registry tags) instead of only detecting that
  a tag moved — distinguishes a real version bump from an image rebuild
  at the same version.
- **Safe self-update**: DoUpRo can update its own running container
  without losing rollback safety — a global maintenance gate blocks
  concurrent operations/schedules, and a helper process retains the
  original container until the replacement is confirmed healthy.
- **Nonce-based Content-Security-Policy**: every inline `<script>` across
  the web UI carries a fresh per-request nonce; an injected script without
  it is blocked by the browser.
- **Configurable Logs page size** (default 300, 50–1000, Settings-
  editable) — was hardcoded at 100.
- **Local Unix-socket CLI mode**: `docker exec doupro doupro ...` needs no
  `--host`/`--api-key` — reaching the socket already requires being
  inside the container's mount namespace.
- **Dry-run/preview before Update**: `doupro update <name> --dry-run` and
  `GET /api/v1/containers/{id}/update` show the resolved target without
  touching Docker.
- **Multi-arch Docker image**: `linux/amd64` and `linux/arm64`, native
  cross-compilation (no QEMU-emulated Go compiler).
- **Notification channel Modify**: rename a channel or change its
  filters without re-entering its destination (bot token, webhook URL,
  ...) — the destination is write-only and never round-trips to the
  browser, so this only works if the name doesn't change *and* the
  destination fields are left blank, or vice versa.
- **Collapsible sidebar**: a small toggle hides/shows it, persisted per
  browser.
- **Prometheus `stack` label** on `doupro_updates_total`,
  `doupro_rollbacks_total`, and `doupro_update_duration_seconds` —
  matches what `doupro_containers_total`/`doupro_updates_available`
  already had.
- Audit-log coverage for Settings → Exclusions and Settings →
  Notifications saves (`settings.exclusions_changed`,
  `settings.notifications_changed`) — previously silent.
- Two missing documented endpoints implemented:
  `GET /api/v1/containers/{id}` (detail) and
  `POST /api/v1/containers/{id}/check` (force an immediate check).
- Bulk "Update all" — global and per-stack.
- **CI/pre-push validation**: `scripts/validate.sh` runs the full check
  set (format, vet, lint, tests, Docker image build, container smoke test)
  in one command; CI gained a Docker container smoke test (build, start,
  `/health`, `HEALTHCHECK`) and a `docker compose config` validation job,
  on top of the existing build/test/lint jobs.
- An automated test asserts every route registered in the API matches
  what `openapi.json` documents, in both directions — no undocumented
  endpoint, no documented endpoint that doesn't exist. Caught and fixed
  three undocumented routes (`/api/v1/openapi.json`, the two OIDC routes).
- CLI end-to-end tests (`cmd/doupro`) exercise the built binary against a
  real API server for `containers`, `logs`, `settings`, `schedule`,
  `stats`, and `users`.
- Swagger UI and the OpenAPI document now have smoke tests: `/swagger`
  requires auth, `/api/v1/openapi.json` is a valid, unauthenticated OpenAPI
  3.x document.

### Security

- `DOUPRO_DOCKER_SOCKET` accepting a `tcp://` URL (see Fixed, below) could
  previously start with an unencrypted, unauthenticated connection to a
  remote Docker API — equivalent to giving remote root access on that host
  to anyone who can reach the port. The daemon now refuses to start on a
  `tcp://` socket unless the new `DOUPRO_ALLOW_INSECURE_DOCKER_TCP=true`
  is explicitly set — a conscious opt-in for a genuinely trusted endpoint
  (loopback-only, private network, or itself TLS-terminated), never a
  default. See `.env.example` and `SECURITY.md`.
- That same `tcp://` check was case- and whitespace-sensitive
  (`TCP://host:port`, or a stray leading space from a copy-pasted `.env`
  line, both bypassed it silently). Detection is now case/whitespace
  -insensitive and centralized in `internal/docker.IsTCPSocket`, the one
  place both the socket client and the startup gate call, so they can't
  drift apart again.

### Fixed

- `DOUPRO_DOCKER_SOCKET` set to a `tcp://host:port` URL (e.g. pointing at
  a `docker-socket-proxy`) was silently mangled into the invalid host
  `unix://tcp://host:port`, failing with a misleading "permission denied"
  instead of connecting — raw Unix socket paths and `unix://` URLs were
  unaffected. `tcp://` and `unix://` URLs are now used as-is; only a
  schemeless path still gets the `unix://` prefix (issue #10).
- A real deadlock: `ListUsers`/`ListAPIKeys` ran a nested query while
  their own result cursor was still open, against a store capped to one
  SQLite connection — any visit to Settings hung indefinitely.
- `Settings → Exclusions` never actually persisted the excluded flag to
  the containers table — the setting was silently a no-op since the
  feature shipped.
- Rollback's failure path was asymmetric with Update's (no safety-net
  revert); a `recreate()` bug dropped the new container's ID on a failed
  health check, breaking that revert.
- Schedule pinning was fake for floating tags — a "once" schedule could
  silently resolve to whatever the tag pointed to at execution time
  instead of what it pointed to when the schedule was created.
- 27 errcheck + 3 staticcheck golangci-lint issues.

## [0.1.0] - 2026-08-04

First stable-enough-to-tag release. Every sidebar section works
end-to-end and has been verified against a real homelab (not just unit
tests) — not a scaffold anymore.

### Added

- **Containers**: every container on the host (running or stopped),
  real Docker state names, resolved version vs. defined image, one-click
  **Update** and **Rollback** with a safety-net revert on failure for
  both, immediate visual feedback (progress spinner) and immediate
  version resolution after either action.
- **Schedule**: one-off per-container schedules and recurring
  stack/container policies (cron or relative delay), a digest-pinned
  target so a "once" schedule can't silently drift onto whatever a
  floating tag becomes by firing time, enable/disable without recreating.
- **Notifications**: Telegram, Discord, Slack, ntfy, Gotify, and generic
  webhook delivery via `containrrr/shoutrrr`, per-provider setup forms, a
  full delivery log, and a Test button per channel.
- **Logs**: every state change as structured JSON (stdout, Loki/Promtail/
  ELK-ready) and in-app, with filtering and a JSON export.
- **Settings**: general (check interval, theme), exclusions (by
  container/stack, searchable multi-select), security (password change,
  API keys, OIDC/SSO), Docker Hub authentication to raise the anonymous
  registry rate limit.
- **Stats**: an in-app summary and a full Prometheus `/metrics` endpoint.
- **Manual**: this documentation, rendered in-app, offline.
- **Auth**: local username/password with forced password change on the
  zero-config `admin`/`admin` default, OIDC/SSO (Authentik, Keycloak, or
  any standard OIDC provider) as an additive login option, rate limiting,
  CSRF protection, timing-safe login, session invalidation on password
  change, and security headers — audited against OWASP and fixed, not
  just designed.
- **API-first**: every web UI action has a corresponding, documented REST
  endpoint (`/api/v1`, OpenAPI spec browsable at `/swagger`), and a CLI
  (`doupro <resource> <verb>`) that's a thin client of that same API, so
  none of the three surfaces can ever behave differently.
- Docker packaging: multi-stage `Dockerfile` (distroless, non-root),
  `docker-compose.yml`, `.env.example`.
