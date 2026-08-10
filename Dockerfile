# syntax=docker/dockerfile:1

## ---- Build stage -----------------------------------------------------------
# --platform=$BUILDPLATFORM pins this stage to the builder's own
# architecture regardless of which TARGETPLATFORM buildx is producing an
# image for, so a multi-arch build (linux/amd64,linux/arm64) compiles
# natively on whichever single host runs `docker buildx build` instead of
# running the Go toolchain itself under QEMU emulation for the non-native
# target — Go already cross-compiles natively via GOOS/GOARCH, and the only
# CGO dependency this module would otherwise need (SQLite) is deliberately
# the pure-Go modernc.org/sqlite (see CLAUDE.md) specifically so this stays
# possible with CGO_ENABLED=0, no cross C toolchain required either way.
FROM --platform=$BUILDPLATFORM golang:1.25-alpine AS build

# Fixed, non-root UID/GID so the final distroless image can carry the same
# user without a shell to create one there. Kept in sync with the group_add
# guidance in docker-compose.yml / README.md for docker.sock access.
RUN addgroup -g 1000 doupro && adduser -D -u 1000 -G doupro doupro

WORKDIR /src
COPY go.mod go.sum ./
RUN go mod download
COPY . .

# TARGETOS/TARGETARCH are set automatically by buildx per platform in
# --platform; defaulted here so a plain `docker build` (no buildx, no
# --platform) still works exactly as before, building for the host it runs
# on, same as when this was a hardcoded GOOS=linux with no GOARCH override.
ARG TARGETOS=linux
ARG TARGETARCH
ARG VERSION=dev
RUN CGO_ENABLED=0 GOOS=${TARGETOS} GOARCH=${TARGETARCH} go build \
      -trimpath \
      -ldflags="-s -w -X main.version=${VERSION}" \
      -o /out/doupro \
      ./cmd/doupro

## ---- Final stage -------------------------------------------------------------
FROM gcr.io/distroless/static-debian12:nonroot AS final

LABEL org.opencontainers.image.title="DoUpRo" \
      org.opencontainers.image.description="Docker Update Rollback — update, schedule and roll back Docker containers with confidence." \
      org.opencontainers.image.source="https://github.com/arnaudcharles/DoUpRo" \
      org.opencontainers.image.licenses="MIT"

COPY --from=build /etc/passwd /etc/passwd
COPY --from=build /etc/group /etc/group
COPY --from=build /out/doupro /usr/local/bin/doupro
COPY --from=build /src/web/static /web/static
COPY --from=build /src/web/templates /web/templates

USER doupro:doupro

VOLUME ["/data"]
EXPOSE 8080

HEALTHCHECK --interval=30s --timeout=3s --start-period=10s --retries=3 \
  CMD ["/usr/local/bin/doupro", "healthcheck"]

ENTRYPOINT ["/usr/local/bin/doupro"]
CMD ["serve"]
