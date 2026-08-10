# Settings

## General

Check interval (how often DoUpRo looks for new versions), timezone, log
level, how long to keep logs, and default theme.

**Logs page size** controls how many recent events the Logs page shows at
once — 300 by default, adjustable from 50 up to 1000. Set it higher if
you're scrolling past the bottom a lot, lower if the page feels sluggish
on a busy host. The Export button always downloads everything up to the
1000-row ceiling, regardless of this setting.

## Registries

Add credentials for Docker Hub, GHCR, or a private registry if any of
your images need authentication to check for updates. Credentials are
write-only — once saved, DoUpRo never displays them again, including in
logs.

Enter the registry host without `https://`. You may add pull-only
credentials, an HTTP(S) proxy, and a private CA certificate. For a registry
whose bearer-token service uses another hostname, set that exact trusted
hostname in **Token auth host**; DoUpRo refuses to send credentials to any
other token service or over plain HTTP. Saved passwords, proxy URLs and CA
bodies are encrypted and are never returned by the API or UI.

Changes take effect immediately. Environment-based Docker Hub credentials
remain a bootstrap fallback when no Docker Hub entry exists. Prefer a
read-only access token over an account password.
Use **Test connection** before saving; it verifies the registry v2 endpoint
without persisting the form or downloading an image.

DoUpRo uses the proxy and private CA for manifest, tag and token checks. The
Docker daemon downloads the image layers, and the Docker API cannot attach a
proxy or CA to one pull. If the layer transfer also needs either setting,
configure the Docker daemon's proxy and `certs.d/<registry-host>/ca.crt`.
DoUpRo will never disable certificate verification to work around an invalid
certificate.

## Exclusions

Keep specific containers or whole stacks out of DoUpRo's checks entirely,
without editing your compose files (equivalent to the `doupro.enable=false`
label — see [Containers](containers.md)).

## Update policy

The General section controls automatic rollback after an update. The default
is 3 unexpected crashes within 10 minutes. Both the crash count and window
apply to newly completed updates and are saved in SQLite; restarting DoUpRo
does not clear an active guard.

Decide how "new version" is judged by default: only patch releases
(`1.0.2` → `1.0.3`), minor releases too (`1.0.2` → `1.1.0`), any release
including major (`1.0.2` → `2.0.0`), or digest-only for images that use a
moving tag like `latest`. Also where you set the crash-loop threshold for
automatic rollback (default: 3 crashes in 10 minutes) and how long DoUpRo
waits for a container to report healthy after an update.

## Notifications

Configure channels and which events they receive — see
[Notifications](notifications.md).

## Security

Manage API keys for CLI/scripted access and session timeout.

### Single sign-on with Authentik (or any OIDC provider)

DoUpRo can offload login to an OIDC identity provider — Authentik,
Keycloak, or anything speaking standard OIDC — while keeping the local
username/password login as a fallback (it never disappears, even once SSO
is set up).

### 1. Create the Provider

**Applications → Providers → Create → OAuth2/OpenID Provider**:

- **Name**: `doupro`
- **Authentication flow** / **Authorization flow**: leave the defaults
- **Client type**: **Confidential** (not Public — DoUpRo keeps the secret
  server-side)
- **Redirect URIs** (mode **Strict**): the exact URL DoUpRo will be
  reachable at, e.g. `http://<your-doupro-host>:8099/api/v1/auth/oidc/callback`
  — this must match `DOUPRO_OIDC_REDIRECT_URL` below byte-for-byte
- **Signing Key**: any existing RSA key (e.g. `authentik Self-signed
  Certificate`) — required to sign ID tokens

Then **Applications → Applications → Create**, name it `DoUpRo`, and set
its **Provider** to the one you just created.

The provider's page shows an **OpenID Configuration Issuer** link — that's
your `DOUPRO_OIDC_ISSUER_URL` below, typically
`https://authentik.example.com/application/o/doupro/`.

### 2. (Optional) Restrict login to a specific group

To use `DOUPRO_OIDC_ALLOWED_GROUPS`, Authentik needs to actually include a
`groups` claim in the ID token — **it does not ship one by default**, you
have to create it:

1. **Customization → Property Mappings → Create → Scope Mapping**:
   - **Scope name**: `groups`
   - **Expression**:
     ```python
     return {"groups": [group.name for group in request.user.ak_groups.all()]}
     ```
2. Back on the `doupro` provider (**Applications → Providers → doupro →
   Edit**), in **Scopes**, find your new mapping in "Available Scopes"
   (search `groups`) and move it into "Selected Scopes", alongside the
   default `email`/`openid`/`profile` ones. Save.
3. Create the group you want to gate access to: **Directory → Groups →
   Create**, e.g. `doupro-admins`. Add the users who should be allowed;
   leave others out.

### 3. Configure DoUpRo

Set these environment variables on the DoUpRo container and restart it
(see `.env.example`):

- `DOUPRO_OIDC_ISSUER_URL` — from step 1.
- `DOUPRO_OIDC_CLIENT_ID` / `DOUPRO_OIDC_CLIENT_SECRET` — from the
  provider you created.
- `DOUPRO_OIDC_REDIRECT_URL` — the exact same URL you set as the Redirect
  URI in step 1.
- `DOUPRO_OIDC_ALLOWED_GROUPS` (optional) — comma-separated Authentik
  group names, e.g. `doupro-admins`. Only members of one of these groups
  can log in via SSO; leave unset to allow any authenticated Authentik
  user. Requires the `groups` scope mapping from step 2 — without it,
  every SSO login is denied (no groups claim to check against), which is
  worth confirming: try it once *without* being in the allowed group and
  check the Logs page for `auth.oidc_login_denied`, then add yourself to
  the group and confirm login succeeds.
- `DOUPRO_OIDC_EXTRA_CA_CERT_PATH` (optional) — see below.

Reload `/login` — a "Sign in with SSO" button now appears below the local
login form.

**Changing `DOUPRO_OIDC_*` values requires the container to be recreated,
not just restarted** — a plain restart (`docker restart`) keeps the
environment the container was originally created with; use `docker
compose up -d` (or equivalent) so the new values actually take effect.

If Authentik is only reachable over an internal domain with a
self-signed (or internal-CA) certificate — common if it isn't exposed
publicly — DoUpRo will refuse to talk to it by default (correctly: it
never skips certificate verification). Mount that certificate's PEM file
into the container and set `DOUPRO_OIDC_EXTRA_CA_CERT_PATH` to its path;
this adds it to the trusted set, on top of the normal public CA bundle.

The first time someone signs in via SSO, DoUpRo creates a local account for
them automatically with the **Read** role. An administrator can change that
account under **Settings → Security — users and roles**.

Roles are **Admin** (everything), **Write** (view containers and run Update or
Rollback), and **Read** (view containers only). Four additional checkboxes can
grant access to Logs, schedules, notifications, and Stats independently. The
Authentik group setting above still controls who may sign in; it does not map
groups to roles. API keys use the same roles and checkboxes, selected when the
key is created.

The initial administrator is shown separately as **Admin · Full access**. It
is the protected break-glass account and cannot be reconfigured or deleted.
Only ordinary users expose role and permission controls. When an ordinary user
or API key is assigned the Admin role, all permissions are included
automatically, so the individual checkboxes are disabled.

## Backup & retention

Where DoUpRo keeps its own periodic database backup, and how long logs and
notification history are kept.

## Advanced

Docker socket path (useful if you're running a
[socket proxy](https://github.com/Tecnativa/docker-socket-proxy) for
extra hardening), bind address/port, outbound proxy, and **read-only
mode** — turns off all update/rollback actions instance-wide while
keeping everything else (browsing, checks, the API) working, useful if you
want visibility into a host before trusting DoUpRo to change anything on
it.
