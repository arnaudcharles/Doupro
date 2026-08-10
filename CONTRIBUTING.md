# Contributing to DoUpRo

Thanks for considering a contribution — DoUpRo is a young project and every
issue report, doc fix, and PR genuinely helps. This guide keeps
contributions consistent and easy to review.

## Code of Conduct

This project follows the [Contributor Covenant](CODE_OF_CONDUCT.md). By
participating, you're expected to uphold it.

## Before you start

- **Language**: all code, comments, commit messages, and docs are written
  in English, regardless of the language you think in.
- **Read the "How it's built" section of [`README.md`](README.md) first.**
  It covers the architecture decisions and non-negotiable conventions
  (API-first, structured logging, security posture) that every change
  must respect.
- For anything beyond a small fix, **open an issue before a PR** to agree
  on the approach — it saves everyone a rewritten PR.

## Ways to contribute

- **Bug reports** — use the [bug report template](.github/ISSUE_TEMPLATE/bug_report.yml).
  Include your DoUpRo version/image digest, `docker logs` output (redact
  anything sensitive), and steps to reproduce.
- **Feature requests** — use the [feature request template](.github/ISSUE_TEMPLATE/feature_request.yml).
  Explain the use case, not just the desired implementation.
- **Documentation** — fixes to `manuals/` or the README are always
  welcome and don't require an issue first.
- **Code** — see the workflow below.
- **Security issues** — do **not** open a public issue; follow
  [`SECURITY.md`](SECURITY.md).

## Development setup

Requirements: Go 1.22+, Docker, `make`.

```bash
git clone https://github.com/arnaudcharles/DoUpRo.git
cd DoUpRo
cp .env.example .env      # edit as needed
make build                 # compiles ./bin/doupro
make run                   # runs the daemon against your local Docker socket
make test                  # unit tests
make lint                  # go vet + golangci-lint
```

To build and run the full container image locally:

```bash
make docker-build
docker compose up
```

See the package layout under `internal/` (one directory per subsystem —
containers/scheduling live in `internal/updater`/`internal/scheduler`,
notifications in `internal/notifier`, etc.) before adding new files —
most changes touch exactly one of these and should follow the
conventions already established in that package's existing code.

## Branching and commits

- Branch from `main`: `feat/<short-name>`, `fix/<short-name>`,
  `docs/<short-name>`.
- Commit messages follow [Conventional Commits](https://www.conventionalcommits.org/):

  ```
  feat(scheduler): support cron expressions with seconds field
  fix(updater): keep previous image reference on failed health check
  docs(api): add rollback endpoint example
  chore(deps): bump containrrr/shoutrrr to v0.8
  ```

- Keep commits focused; it's fine to have several small commits in a PR.

## Pull requests

1. Make sure `make lint` and `make test` pass locally.
2. Update `manuals/` if user-facing behavior changes — a PR that changes
   behavior without updating docs will be asked to add them.
3. Fill in the PR template, including a short rationale (the "why", not
   just the "what" — the diff already shows the what).
4. Link the issue it resolves (`Closes #123`) if applicable.
5. A maintainer will review; expect feedback on API shape and log/metric
   consistency in particular, since those are contracts other tools may
   depend on.

## Code style

- `gofmt`-formatted, `go vet` and `golangci-lint` clean.
- Errors wrapped with context: `fmt.Errorf("recreate container %s: %w", name, err)`.
- Table-driven tests for anything with more than a couple of cases.
- No new third-party dependency for something the standard library already
  does well; do reuse existing well-maintained libraries (e.g.
  `containrrr/shoutrrr` for notifications, `robfig/cron` for scheduling)
  instead of reinventing them — see the "How it's built" section of
  [`README.md`](README.md) for the already-chosen stack.
- New REST endpoints are added to the OpenAPI spec in the same PR
  (`internal/api/openapi.json`), browsable at `/swagger`.
- New CLI commands must map 1:1 to an existing or new API endpoint — the
  CLI (`cmd/doupro`, `internal/cliclient`) has no business logic of its
  own.

## Release process

Maintainer-driven for now: pushing a tag matching `vX.Y.Z` triggers
[`.github/workflows/release.yml`](.github/workflows/release.yml), which
builds a multi-arch (`linux/amd64`, `linux/arm64`) image and pushes it as
both `sharlihe/doupro:X.Y.Z` and `sharlihe/doupro:latest`. Needs the
`DOCKERHUB_USERNAME`/`DOCKERHUB_TOKEN` repository secrets set first (see
the workflow file's own header comment). `CHANGELOG.md` follows
[Keep a Changelog](https://keepachangelog.com/) and should be updated
before tagging, not after — the release workflow doesn't touch it.

## Questions

Open a [GitHub Discussion](https://github.com/arnaudcharles/DoUpRo/discussions)
or an issue tagged `question`.
