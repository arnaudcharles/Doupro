# Notifications

Set up at least one channel in **Settings → Notifications** so you don't
have to keep the DoUpRo tab open. Supported channels include Telegram,
Discord, Slack, ntfy, Gotify, and generic webhooks — add the connection
details for a channel and send yourself a test message before relying on
it.

Saved credentials are write-only: reopening the page shows that a channel is
configured but never sends its token or webhook URL back to the browser. You
can still test the saved channel, edit its filters, and rename it — all
without re-entering the secret.

Click **Modify** next to a channel to reopen the form pre-filled with its
name, provider, and filters. The destination fields (bot token, chat ID,
webhook URL, ...) are left blank on purpose — leave them blank and its
current destination stays unchanged; fill them in to actually change it.
The one thing that doesn't work: renaming a channel *and* adding a brand
new one in the same save — DoUpRo can't tell which destination the
rename should keep in that case, and asks you to save them separately.

## What you'll be notified about

By default: an update becomes available, an update is applied, an update
fails, a rollback happens (manual or automatic), and a schedule fires.
Automatic rollbacks are **always** notified, even if you've turned other
notifications off — you should always know when DoUpRo had to revert
something on its own.

Each channel has its own **Filters** panel (click "Filters" next to a
saved channel, or fill it in while adding a new one) with two independent
controls:

- **Event types** — check only the ones you want that channel to receive.
  Leave everything unchecked to get every type (the default). Automatic
  rollback is always sent no matter what's checked here — that one always
  deserves your attention.
- **Only these stacks / Only these containers** — search boxes, same as
  Settings → Exclusions. Leave both empty to receive events for every
  container. A container matches if either its stack or its own name is
  in one of the two lists.

This is how you'd set up, say, ntfy to get every update while Telegram
only pings you for rollbacks, or scope a channel to just one homelab
stack you care about.

## Set up ntfy

1. Open your ntfy web app or mobile app and subscribe to a topic. Use a
   long, unpredictable topic name if the server allows anonymous access.
2. In DoUpRo, open **Notifications → + Add a channel**, choose **ntfy**, and
   enter the exact same topic.
3. For a self-hosted instance, enter only the hostname in **Server**
   (for example `ntfy.example.com`, without `https://` or a trailing slash).
   Leave **plain HTTP** unchecked when the server uses HTTPS.
4. Enter the ntfy username/password if access control is enabled, then click
   **Test**. Once the message arrives, click **Add & save**.

If the server uses a private or self-signed CA, mount its PEM into DoUpRo and
set `DOUPRO_EXTRA_CA_CERT_PATH` to that container path, then recreate DoUpRo.
Do not switch to plain HTTP merely to bypass a certificate error.

An ntfy server without authentication allows anonymous publishing and
subscription by default. Topic names are not passwords; for an
Internet-reachable private instance, enable ntfy access control and deny
anonymous access before using it for operational alerts.

## The Notifications page

The **Durable delivery queue** shows the lifecycle of every accepted message.
`pending` messages are waiting for their next attempt, `processing` is a short
delivery lease, `sent` is complete, and `dead_letter` means all six automatic
attempts failed. Use **Retry** on a dead-letter entry after fixing the provider.
DoUpRo resumes pending work after a restart; notification failures never block
the update or rollback that created them.

Every notification DoUpRo has ever tried to send is listed here — sent or
failed, with the reason if it failed — for example:

- `Update applied — AdGuard Home 1.0.2 → 1.0.3 (Telegram — Username, 23:34)`
- `Rollback (auto) — Prestashop 8.1.4 → 8.1.3 after 3 crashes in 10 min`
- `Rollback (manual) — Unifi Controller 8.1.113 → 8.1.109`

**Not built yet:** filtering this list by channel/event type/container,
and a quiet-hours window. If you need to be selective about *when* or
*where* notifications land, use each channel's Filters panel described
above instead — that part is real today.
