# Security Policy

DoUpRo is granted read/write access to the Docker socket, which is
effectively root-equivalent access to its host. It is also designed to hold
credentials for container registries and notification services. Security is
treated as a first-class requirement, not an afterthought — this document is
both the public security policy and the internal rule set that governs how
code is written in this repository, binding on every contributor.

## Supported versions

While DoUpRo is pre-1.0, only the latest published image tag on Docker Hub
(`sharlihe/doupro:latest` and the corresponding semver tag) receives security
fixes. Once 1.0 ships, this table will list supported minor versions.

| Version   | Supported |
|-----------|-----------|
| `latest`  | ✅        |
| `< 1.0.0` (older tags) | ❌ (upgrade) |

## Reporting a vulnerability

**Do not open a public GitHub issue for a security vulnerability.**

Use GitHub's [private vulnerability reporting](https://github.com/arnaudcharles/DoUpRo/security/advisories/new)
feature on this repository. If that is unavailable, contact the maintainer
directly and mark the subject `[SECURITY] DoUpRo — <short summary>`.

Please include: affected version/image digest, a reproduction (config,
request, or steps), and the impact you believe it has. You will get an
acknowledgement as soon as possible and a coordinated disclosure timeline
once the issue is confirmed. Please give us reasonable time to ship a fix
before any public disclosure.

## Threat model

DoUpRo's core function — recreating containers from a socket mount — means a
compromise of DoUpRo itself is roughly equivalent to a compromise of the
Docker host. The design goal is therefore to minimize what an attacker gains
by exploiting DoUpRo's web UI, API, or a malicious/compromised upstream
image, and to make every trust boundary explicit rather than implicit.

Primary boundaries and how DoUpRo is expected to treat each one:

1. **Operator ↔ Web UI / API / CLI** — authenticated, session- or
   API-key-scoped, CSRF-protected forms, no default-open access.
2. **DoUpRo ↔ Docker socket** — the single most sensitive boundary in this
   project. Treated as equivalent to a root shell on the host; every code
   path that touches it must be deliberate and logged.
3. **DoUpRo ↔ container registries** — TLS-only, credentials never logged,
   scoped to pull access only.
4. **DoUpRo ↔ notification services** — outbound only, URLs/tokens treated
   as secrets, payloads never include credentials.

## Non-negotiable rules for code in this repository

These apply to every contributor working in this repo.

### Secrets

- **Never** commit API tokens, registry credentials, notification webhook
  URLs/tokens, session signing keys, TLS private keys, or any `.env` file
  with real values. `.env.example` ships with placeholders only
  (`CHANGE_ME`, `<your-token-here>`) — never a real-looking value.
- **Never** hardcode a secret, default password, or signing key in source
  code, Dockerfile, docker-compose.yml, or docs. All secrets are supplied at
  runtime via environment variables or files referenced by
  `*_FILE` env vars (Docker secrets convention, e.g. `DOUPRO_ADMIN_PASSWORD_FILE`).
- **Never** log a secret. Registry credentials, API keys, and notification
  URLs must be redacted before they reach the structured event log or
  stdout (`internal/events` must implement redaction centrally, not rely on
  every call site remembering to do it).
- If a secret is accidentally committed, it must be treated as compromised
  and rotated — rewriting history is not sufficient on its own.
- Before writing a real-looking credential, token, or key into any file in
  this repo, stop and use a placeholder instead.

### Docker socket access

- Document, every time it's relevant, that mounting `/var/run/docker.sock`
  grants root-equivalent access to the host — this must never be minimized
  in docs or defaults.
- Prefer supporting a **socket proxy** (e.g.
  [Tecnativa/docker-socket-proxy](https://github.com/Tecnativa/docker-socket-proxy))
  as a documented hardening option that restricts DoUpRo to only the Docker
  API endpoints it actually needs (containers, images — not exec, not
  swarm, not full system access).
- The container itself must run as a **non-root user** wherever the socket
  group permissions allow it, and must not request more Linux capabilities
  than required (`cap_drop: ALL` plus only what's proven necessary).
- `DOUPRO_DOCKER_SOCKET` also accepts a `tcp://host:port` URL (for a
  socket-proxy or a remote Docker host), but the daemon **refuses to start**
  with one unless `DOUPRO_ALLOW_INSECURE_DOCKER_TCP=true` is explicitly
  set — a plain, unauthenticated `tcp://` Docker API is equivalent to
  giving remote root access to the target host to anyone who can reach
  that port. This flag is a conscious opt-in, never a default; only set it
  when the endpoint is genuinely trusted (loopback-only, a private/internal
  network with no other tenants, or itself TLS-terminated). See
  `.env.example` and `internal/config`/`cmd/doupro/cmd_serve.go`'s
  `validateDockerSocketConfig`.

### Web UI / API

- Authentication is **on by default**. There is no supported configuration
  that ships with an open, unauthenticated API bound to a non-loopback
  address.
- Passwords are hashed with a modern algorithm (bcrypt or argon2id), never
  stored or logged in plaintext.
- Session cookies are `HttpOnly`, `Secure` (when TLS/reverse-proxy TLS is in
  use), and `SameSite=Lax` at minimum. State-changing requests are
  CSRF-protected.
- API keys are random, high-entropy, stored hashed (not reversible),
  displayed in full exactly once at creation time, and revocable
  individually from Settings.
- All user-supplied input rendered in the web UI is escaped by
  `html/template` (never `text/template` for HTML output, never manual
  string concatenation into HTML).
- Rate-limit authentication endpoints to blunt brute-force attempts.
- **OIDC/SSO** (`internal/oidc`) is opt-in and additive, never a
  replacement for local login — a misconfigured or unreachable IdP must
  never lock an operator out, which is why local admin login always
  keeps working alongside it. The ID token's issuer, audience, signature,
  and nonce are all verified server-side before a session is created; the
  OAuth `state` and PKCE `code_verifier` round-trip through a short-lived
  `HttpOnly` cookie (same double-submit reasoning as CSRF protection
  above), not the query string, so neither can be forged by an attacker
  who can't read that cookie. `DOUPRO_OIDC_ALLOWED_GROUPS` is an optional
  allow/deny gate on the IdP's `groups` claim, not a role system — DoUpRo
  has no per-user permissions yet, so every authenticated identity
  (local or OIDC) has full access.

### Registries and notifications

- Registry credentials are used strictly for pulling manifests/images —
  never persisted in plaintext logs, never returned by any API response
  (write-only fields).
- Bearer credentials are sent only to an HTTPS realm whose host equals the
  registry or the operator's explicit trusted `auth_host`.
- Notification channel URLs (Telegram bot tokens, Slack/Discord webhooks,
  etc.) follow the same write-only, redacted-in-logs rule.
- Reversible SQLite secrets are encrypted with AES-256-GCM under a 32-byte
  master key. By default the key is generated once at `/data/doupro.key` with
  mode `0600`; production deployments may mount a Docker secret via
  `DOUPRO_ENCRYPTION_KEY_FILE`. A missing, wrong, or corrupted key fails
  startup closed instead of silently discarding credentials.
- All outbound HTTP calls (registries, notification services) use TLS and
  verify certificates. Registry-specific private CAs extend the trusted root
  set; there is no configuration that disables certificate verification.

### Dependencies and build

- Multi-stage Dockerfile; the final image contains only the compiled
  binary and static assets — no build toolchain, no shell if avoidable
  (distroless/scratch base).
- Go modules are kept up to date; `go.sum` is committed and verified.
- Dependabot (or equivalent) should be configured on this repository to
  flag vulnerable dependencies — see `.github/`.
- CI must run `go vet`, `golangci-lint`, and `govulncheck` (or equivalent)
  before a change is considered mergeable.

### OWASP alignment

New endpoints and forms should be reviewed against the current
[OWASP Top 10](https://owasp.org/www-project-top-ten/) and the
[OWASP Docker Top 10](https://owasp.org/www-project-docker-top-10/) at
minimum: broken access control, injection, insecure design,
security misconfiguration, vulnerable/outdated components, identification
and authentication failures, and software/data integrity failures
(verify image digests, not just tags, when pulling for updates).

## Out of scope

DoUpRo does not attempt to sandbox or scan the images it updates for
malicious content — it manages *which* image version a container runs, not
the trustworthiness of that image's contents. Scanning upstream images is
the operator's responsibility (e.g. via Trivy/Grype in their own pipeline).
