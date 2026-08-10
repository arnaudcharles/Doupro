# Containers

The Containers page lists every container DoUpRo can see on the host,
running or stopped, grouped by stack (your Compose project, or however
you've labeled them).

## Reading a container row

- **State badge**: green means running and healthy. Amber means running
  but something's off (an unhealthy healthcheck, or repeated restarts).
  Red means it stopped unexpectedly. Gray means it's stopped on purpose
  (you or Compose stopped it) — that's not an error.
- **Current version** (light green chip) and **previous version** (gray
  chip): the previous chip only appears once DoUpRo has updated this
  container at least once — it's what a rollback would go back to.
- **Update available** (orange badge): a newer image was found. This is
  when the **Update** and **Schedule** buttons appear. When the image digest
  changed but its resolved application version did not, the badge says
  **Image rebuild available**: it is typically a base-image or security
  refresh, and is still a real update.
- **Update/Rollback in progress**: the row temporarily replaces its action
  buttons with the active operation. A second update or rollback cannot run
  against the same container at the same time.
- **Rollback protection checking**: after a successful update, DoUpRo watches
  the container for the configured safety window (default: 3 crashes within
  10 minutes triggers an automatic rollback). The badge just means the watch
  is active — it disappears once the window ends without incident, or once
  an automatic rollback fires. Crash-by-crash progress (`detected crash 1/3
  for ...`) is recorded in the Logs page, not on this badge, and you'll get
  a notification if an automatic rollback actually happens.

For registry images using `latest`, `release`, or another floating tag,
DoUpRo links the immutable image digest to a concrete version using the
publisher's OCI version label, a repository-identifying publisher version
label, or the registry's version tags (including composite tags such as
`php8.2-1.17.999`). This works with Docker Hub, GHCR, LSCR and configured
private registries. If a publisher provides no exact version for the running
artifact, the row shows its source tag (`latest`, `nightly`, etc.) in a neutral
chip and keeps the immutable digest in the tooltip. This avoids both an
unhelpful `Unresolved` label and a fabricated semantic version.

## Updating a container

Not sure what an update would actually do? `doupro update <name> --dry-run`
shows the current and target version (and whether it's a real version
bump or just an image rebuild at the same version) from the CLI without
changing anything — there's no web UI equivalent.

Click **Update**. DoUpRo pulls the new image, recreates the container
with the exact same settings (volumes, environment, network, ports —
nothing else changes), and waits for it to come up healthy. If it
doesn't come up cleanly, DoUpRo reverts automatically — you'll never be
left on a broken update.

If the container was stopped before the update, DoUpRo keeps it stopped and
does not execute the workload merely to change its image. Named, bind, and
anonymous volumes are reused, and the recreated Docker configuration is
checked before a running workload is started.

Once updated, the previous version is kept, and a **Rollback** button
appears — it stays there even later, whether or not the container still
looks "running", because running isn't the same as working correctly.

When you update DoUpRo itself, a temporary recovery helper performs the
restart. DoUpRo refuses the operation while another update or rollback is
running, or when a schedule will execute in the next five minutes. The page
waits for the replacement to become healthy; if it fails, the previous DoUpRo
container is restored automatically. Renaming the container does not disable
this protection when the recommended `doupro.instance=true` label is present.

### Updating several containers at once

The **Update all** button at the top of the page updates every container
that currently shows "Update available", skipping anything excluded in
Settings — the same as clicking Update on each one individually, just
faster. Each stack also has its own **Update all**, scoped to just that
stack. Clicking either just queues the work and reloads the page shortly
after; watch each container's row (an "update in progress" badge appears)
to see it actually happening, the same as a single Update.

## Scheduling instead

Click **Schedule** instead of **Update** to apply the *same* update, but
later — tonight at a set time, in a set number of hours, or a specific
date. DoUpRo locks in the version that's available right now, so even if
a newer one shows up before your scheduled time, it applies the one you
chose. See [Schedule](schedule.md) for recurring, automatic policies.

## Rolling back

Click **Rollback** any time it's shown. DoUpRo recreates the container
using the previous version and the same settings it had before. This also
happens automatically, without you clicking anything, if a container
crashes 3 times shortly after an update — you'll get a notification
either way (see [Notifications](notifications.md)).

While that post-update window is active, the row shows **Crash guard 0/3**
(or the configured threshold). Unexpected non-zero exits increment it.
Intentional clean exits do not. The count survives a DoUpRo restart, and a
rollback already triggered before a restart resumes automatically.

## Excluding a container from checks

Add the label `doupro.enable=false` to a container (in your `docker-compose.yml`,
under `labels:`) to keep DoUpRo from ever checking or updating it. It'll
still show up in the list, grayed out, so you know it's intentionally
excluded rather than missing. You can also exclude by name or stack from
**Settings → Exclusions** without touching your compose files.
