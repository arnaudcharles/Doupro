# web/

Frontend assets embedded into the `doupro` binary via `go:embed`
(`internal/web`).

- `templates/` — Go `html/template` files: the sidebar shell layout and
  one template per section (Containers, Schedule, Notifications, Logs,
  Settings, Stats, Manual).
- `static/` — compiled Tailwind CSS (`app.css`, built at development/CI
  time via `npx tailwindcss` from `input.css` — no Tailwind CLI runs
  inside the container), htmx and Alpine.js vendored as static files,
  favicon, and any images.

Nothing here is built at container runtime — everything is compiled/
embedded ahead of time into the binary.
