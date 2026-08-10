# FAQ

**Does DoUpRo update containers automatically by default?**
No. It detects and shows you what's available; you choose whether to
update now, schedule it, or set up a recurring policy for a stack. See
[Schedule](schedule.md).

**Why does DoUpRo need access to the Docker socket?**
To see every container and recreate them with a new image when you ask it
to. This is equivalent to root access on the host — mount it deliberately,
and consider a [socket proxy](https://github.com/Tecnativa/docker-socket-proxy)
for extra hardening. See the [README security note](../README.md#security-note).

**What happens if an update breaks a container?**
If it doesn't come up healthy right away, DoUpRo reverts it immediately.
If it comes up but crashes 3 times within a short window afterwards,
DoUpRo rolls it back automatically and notifies you. You can also roll
back manually at any time a previous version is on record.

**Does DoUpRo keep more than one previous version?**
No, one — the version that was running right before the last update. This
keeps behavior predictable: rollback always means "go back to what was
running before".

**Does it work with plain `docker run` containers, or only Compose?**
Both. DoUpRo reads container configuration straight from the Docker
Engine API, not from your compose files — anything visible to `docker ps`
is visible to DoUpRo.

**Can I run this alongside Watchtower or Diun?**
You can, but they'll likely compete for the same job. DoUpRo is meant to
replace pure auto-updaters like Watchtower for setups where you want
scheduling, rollback, and visibility rather than silent updates.

**Is Swarm or Kubernetes supported?**
No — DoUpRo targets plain Docker/Compose hosts. If you're on Swarm or
Kubernetes, their native rolling-update mechanisms already solve this
problem differently.
