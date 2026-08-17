#!/usr/bin/env bash
# validate.sh — the one command to run before pushing to the public repo.
#
# Checked into the repo (unlike scripts/test-all.sh, which is a symlink to
# the maintainer's private local-only test runner — see .gitignore). Every
# check here runs from what's actually in the repo, so any contributor can
# run it, and CI runs the same checks split across ci.yml's jobs.
#
# What this proves before a push:
#   1. Code is formatted, vetted, and linted.
#   2. The full Go test suite passes (unit, API, store, CLI, contract, smoke).
#   3. Every CLI command reaches the API and works.
#   4. Swagger UI and the OpenAPI document are served correctly.
#   5. The API and its OpenAPI document are in sync — no undocumented
#      endpoint, no documented endpoint that doesn't exist.
#   6. The Dockerfile builds and the resulting image actually starts,
#      answers /health, and passes its own HEALTHCHECK command.
#   7. docker-compose.yml is valid.
set -euo pipefail

ROOT_DIR=$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)
cd "$ROOT_DIR"

step() { printf '\n\033[1;34m==>\033[0m %s\n' "$1"; }
fail() { printf '\033[1;31mFAILED:\033[0m %s\n' "$1"; exit 1; }

step "gofmt"
if [[ -n "$(gofmt -l .)" ]]; then
  gofmt -l .
  fail "gofmt needed — run: gofmt -w ."
fi

step "goimports"
if command -v goimports >/dev/null 2>&1; then
  if [[ -n "$(goimports -l .)" ]]; then
    goimports -l .
    fail "goimports needed — run: goimports -w ."
  fi
else
  echo "goimports not installed, skipping (CI always runs it — see .github/workflows/ci.yml)"
fi

step "go vet"
go vet ./... || fail "go vet found issues"

step "golangci-lint"
if command -v golangci-lint >/dev/null 2>&1; then
  golangci-lint run || fail "golangci-lint found issues"
else
  echo "golangci-lint not installed, skipping (CI always runs it)"
fi

step "go test ./... (unit, API↔OpenAPI contract, CLI end-to-end, Swagger smoke)"
go test ./... -race -cover || fail "go test found failures"

step "docker build"
docker build -t doupro:validate . || fail "docker build failed"

step "docker compose config"
docker compose -f docker-compose.yml config --quiet || fail "docker-compose.yml is invalid"

step "container smoke test (start, /health, /api/v1/openapi.json, HEALTHCHECK)"
VOLUME="doupro_validate_data_$$"
CONTAINER="doupro-validate-$$"
cleanup() {
  docker rm -f "$CONTAINER" >/dev/null 2>&1 || true
  docker volume rm "$VOLUME" >/dev/null 2>&1 || true
}
trap cleanup EXIT

docker volume create "$VOLUME" >/dev/null
# Fixed non-root uid:gid in the final image (see Dockerfile) — /data must
# already be writable by it before the daemon's first SQLite write.
docker run --rm -v "$VOLUME":/data alpine:3 chown -R 1000:1000 /data >/dev/null

PORT=18099
docker run -d --name "$CONTAINER" -p "${PORT}:8080" -v "$VOLUME":/data doupro:validate >/dev/null

healthy=false
for _ in $(seq 1 30); do
  status="$(docker inspect --format='{{.State.Health.Status}}' "$CONTAINER" 2>/dev/null || echo "")"
  if [[ "$status" == "healthy" ]]; then
    healthy=true
    break
  fi
  if [[ "$status" == "unhealthy" ]]; then
    break
  fi
  sleep 2
done
if [[ "$healthy" != "true" ]]; then
  docker logs "$CONTAINER" || true
  fail "container did not become healthy"
fi

code="$(curl -s -o /dev/null -w '%{http_code}' "http://127.0.0.1:${PORT}/health")"
[[ "$code" == "200" ]] || fail "GET /health = $code, want 200"

code="$(curl -s -o /dev/null -w '%{http_code}' "http://127.0.0.1:${PORT}/api/v1/openapi.json")"
[[ "$code" == "200" ]] || fail "GET /api/v1/openapi.json = $code, want 200"

docker exec "$CONTAINER" /usr/local/bin/doupro healthcheck || fail "in-container healthcheck command failed"

printf '\n\033[1;32mAll checks passed — safe to push.\033[0m\n'
