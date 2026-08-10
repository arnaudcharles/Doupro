# Logs

The Logs page shows everything DoUpRo has done or noticed: checks run,
updates found, updates applied or failed, rollbacks, schedule changes,
notifications sent, and settings changes — filterable by type, container,
level, and date range.

## Using logs outside the app

Everything shown here is also written as structured JSON to the
container's standard output, one line per event, so it works with
whatever log collector you already run (Loki, Elasticsearch/ELK, or
anything that reads Docker container logs). Point your log driver at the
`doupro` container the same way you would for any other service — there's
nothing DoUpRo-specific to configure on the collector side — every field
in a log line is self-explanatory (timestamp, level, event type,
container/stack, message) if you're building a dashboard against it.

## Retention

DoUpRo keeps events in its own database for the number of days set in
**Settings → Backup & retention** (30 days by default) so the in-app Logs
page doesn't grow forever. This has no effect on logs your own log
collector has already picked up.
