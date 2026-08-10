# Schedule

For recurring schedules, choose the largest version change DoUpRo may apply:
**patch** stays on the current major/minor, **minor** stays on the current
major, and **major** permits any newer stable release. Delayed schedules start
their timer when that exact candidate is first detected and keep the timer
through DoUpRo restarts; a different newer candidate starts a new timer.

The Schedule page has two different things on it — one-off updates you
picked a time for, and recurring policies that apply to a whole stack
automatically.

## One-off schedules

These come from clicking **Schedule** on a container in the Containers
page (see [Containers](containers.md)). You'll see them listed here with
a countdown and a **Cancel** button. Nothing changes about how they run —
they're exactly like clicking Update, just delayed to a time you picked,
and locked to the version that was available when you scheduled it.

## Recurring policies

Created directly from this page, a recurring policy applies to a **stack**
(everything from one `docker-compose.yml`, or however you've grouped
containers) rather than one container at a time. Three kinds:

- **As soon as available** — updates the moment DoUpRo notices a new
  version, no waiting.
- **Available, but wait** — updates a set number of hours (or days) after
  a new version first appears. Useful so you're not the first to hit a
  bad release — a common default is 24 hours.
- **On a schedule (cron)** — updates at fixed times, e.g. every Monday at
  8am, to whatever the latest allowed version is at that moment. You can
  either use the visual picker (frequency, day, time) or type a cron
  expression directly if you already know the syntax — both stay in sync.

Every recurring policy logs what it does and, by default, sends a
notification each time it fires — you'll always know when something
updated itself, even if you weren't watching.

After an individual one-time schedule has run, it stays visible as a grayed
out `completed` row so you retain the immediate history. Its **Cancel** button
becomes **Delete**, which removes that historical row. A one-time schedule
that was only disabled before firing is not considered completed.

## Which one should I use?

If you want to review every update yourself: don't create a recurring
policy, just use **Update**/**Schedule** per container as needed. If
there's a stack you're comfortable not reviewing (e.g. something with good
release notes and low blast radius), a **delayed** recurring policy is a
reasonable middle ground — updates happen without you, but not on day
zero of a release.
