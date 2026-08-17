#!/usr/bin/env bash
set -euo pipefail

ROOT_DIR=$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)
cd "$ROOT_DIR"

echo "[test-all] Starting full local test suite"

echo "[step 1/6] Unit tests"
if ! go test ./...; then
  echo "[test-all] Unit tests failed" >&2
  exit 1
fi

echo "[step 2/6] Lint (go vet + golangci-lint if available)"
go vet ./...
if command -v golangci-lint >/dev/null 2>&1; then
  golangci-lint run || { echo "golangci-lint found issues" >&2; exit 1; }
else
  echo "[test-all] golangci-lint not found, skipping (install with 'brew install golangci-lint' or see docs)"
fi

echo "[step 3/6] Build"
mkdir -p bin
CGO_ENABLED=0 go build -trimpath -ldflags="-s -w -X main.version=local-test" -o bin/doupro ./cmd/doupro

if [ "${SKIP_INTEGRATION:-0}" = "1" ]; then
  echo "[test-all] SKIP_INTEGRATION=1 set, skipping integration tests and docker-compose steps"
  exit 0
fi

echo "[step 4/6] Start integration environment (docker-socket-proxy + doupro)"
COMPOSE_FILES=(docker-compose.yml dev/docker-compose.test.yml)
COMPOSE_ARGS=()
for f in "${COMPOSE_FILES[@]}"; do
  if [ -f "$f" ]; then
    COMPOSE_ARGS+=( -f "$f" )
  fi
done
if [ ${#COMPOSE_ARGS[@]} -eq 0 ]; then
  echo "[test-all] no docker-compose files found for integration, skipping" && exit 0
fi

docker-compose "${COMPOSE_ARGS[@]}" up -d --build

echo "[step 5/6] Wait for doupro to be healthy (timeout 60s)"
healthy=0
for i in $(seq 1 60); do
  logs=$(docker-compose "${COMPOSE_ARGS[@]}" logs --no-color --tail=50 doupro || true)
  echo "$logs" | grep -q "daemon.local_socket_listening" && healthy=1 && break || true
  sleep 1
done
if [ "$healthy" -ne 1 ]; then
  echo "[test-all] doupro did not report healthy within timeout. Showing last logs:" >&2
  docker-compose "${COMPOSE_ARGS[@]}" logs --no-color --tail=200 doupro || true
  docker-compose "${COMPOSE_ARGS[@]}" down || true
  exit 2
fi

echo "[step 6/6] Integration tests"
# run Go integration tests (tagged), limit to updater integration if needed
DOUPRO_DOCKER_INTEGRATION=1 go test -tags=integration -count=1 ./...

echo "[test-all] Integration passed. Tearing down environment."
docker-compose "${COMPOSE_ARGS[@]}" down

echo "[test-all] All steps succeeded"
