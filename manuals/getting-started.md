# Getting started

## What DoUpRo does

DoUpRo watches the Docker containers on your host (via the Docker socket)
and tells you when a newer image is available. You decide what happens
next: update immediately, schedule it for later, or let a recurring policy
handle a whole stack automatically — and you can always roll back to the
version that was running before.

## Run it

```bash
docker run -d \
  --name doupro \
  --restart unless-stopped \
  -p 8080:8080 \
  -v /var/run/docker.sock:/var/run/docker.sock \
  -v doupro_data:/data \
  --group-add "$(getent group docker | cut -d: -f3)" \
  sharlihe/doupro:latest
```

Or with Docker Compose — see the example in the repository
[`docker-compose.yml`](../docker-compose.yml). Then open
`http://<your-host>:8080` and log in with `admin` / `admin` — DoUpRo will
ask you to set a real password before letting you do anything else. To
skip that and start with a real password right away, set
`DOUPRO_ADMIN_USER`/`DOUPRO_ADMIN_PASSWORD` (both) before the first
start.

> DoUpRo needs read/write access to the Docker socket to recreate
> containers when it updates them. That's equivalent to root access on
> your host — mount it deliberately. See the note in the main
> [README](../README.md#security-note) and, for hardening options,
> [`SECURITY.md`](../SECURITY.md).

## First steps

1. **Containers** — you'll immediately see every container on the host,
   running or stopped, with its current state. DoUpRo doesn't touch
   anything on its own by default.
2. Wait for the first check cycle (or click **Check now**) — containers
   with a newer image available get an **Update available** badge.
3. Try an update on something low-stakes first: click **Update**, watch
   the row change, and notice the **Rollback** button that appears.
4. Set up a notification channel in **Settings → Notifications** so you
   don't have to keep the tab open (see [Notifications](notifications.md)).
5. Once you trust it, set up a **recurring policy** in **Schedule** for a
   stack you don't want to babysit — see [Schedule](schedule.md).

## Is it safe to just let it auto-update everything?

By default, no — DoUpRo only *detects* updates and shows them to you. If
a container crashes 3 times shortly after an update, DoUpRo rolls it back
automatically on its own, but you still choose whether updates happen
automatically in the first place, per stack, in **Schedule**. Start with
manual updates on the things you care about, and turn on recurring
policies for the things you don't need to review every time.
