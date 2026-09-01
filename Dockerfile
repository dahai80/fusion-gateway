# syntax=docker/dockerfile:1
#
# Containerized fusion-gateway for the Fusion HTTP business plane (#143).
# Multi-stage: Go binary built in golang:1.26-alpine, run on alpine:3.20.
# The gateway reaches bare-metal fusion-mlx over the Docker host network via
# host.docker.internal:11434 — overridden at runtime by FUSION_MLX_URL. The
# in-container listen port is FUSION_GATEWAY_PORT (default 11432, EXPOSE'd).
# Logs go to stdout; the Docker json-file driver handles rotation in compose.
#
# Build:  docker build -t fusion-gateway .
# Run:    docker run -p 11432:11432 fusion-gateway
# Health: curl http://127.0.0.1:11432/healthz
#
# Why alpine (not scratch): the Go binary is dynamically linked to musl
# (interpreter /lib/ld-musl-aarch64.so.1) even with CGO_ENABLED=0 — purego
# (used by internal/hardware/iokit.go for IOKit/CoreFoundation on darwin) pulls
# the dynamic loader on linux arm64. scratch has no musl, so the binary cannot
# exec there ("exec: no such file or directory"). alpine:3.20 ships musl, so
# the binary runs. Image stays under the 50MB cap: alpine base (~7MB) + stripped
# Go binary (~24MB) + ca-certs (~0.2MB). No tini (the gateway is a single Go
# process with no children; Go's signal.Notify catches SIGTERM directly).
#
# NOTE: this image is for the HTTP business plane only — fusion-mlx (the MLX
# inference engine) is bare-metal on the host (UMA memory, Apple Silicon). The
# container never runs inference; it forwards to the host's :11434.

# ---- builder ----
FROM golang:1.26-alpine AS builder

# GOPROXY mirror — the default proxy.golang.org is unreachable from some build
# environments (CN network). goproxy.cn is the host's configured mirror; direct
# fallback covers modules not mirrored. Operators behind a corporate proxy can
# override via --build-arg or docker build --build-arg GOPROXY=...
ARG GOPROXY=https://goproxy.cn,direct
ENV GOPROXY=${GOPROXY}

# git: go build may embed vcs info. ca-certificates: certs for outbound TLS.
RUN apk add --no-cache git ca-certificates

WORKDIR /src

# Cache deps before copying source: copy only module manifests first.
COPY go.mod go.sum ./
RUN go mod download

# Source + embedded admin UI dist (go:embed internal/admin/ui/dist/*).
COPY . .

# CGO disabled (no libc at build time). netgo/osusergo force pure-Go net + user
# resolution. Trimpath strips local paths; -s -w strips the symbol table + DWARF
# (no runtime effect, ~30% smaller binary — the issue caps image size <50MB).
# ENV (not inline) so every go subcommand in this layer inherits CGO_ENABLED=0.
ENV CGO_ENABLED=0
RUN go build -tags "netgo osusergo" -trimpath -ldflags="-s -w" -o /out/gateway ./cmd/gateway

# ---- runtime ----
# alpine:3.20: minimal musl-based base (~7MB) that provides the musl dynamic
# loader the Go binary needs. Smaller than debian (glibc), bigger than scratch
# (which cannot run this binary). No package installs at runtime — the binary
# only needs ca-certs (staged from the builder below) + musl (in the base).
FROM alpine:3.20

# ca-certificates for outbound TLS to cloud providers (glm5.2 / LiteLLM /
# Anthropic / OpenAI). Without these, https dials fail x509 verification.
# --no-cache leaves no apk index behind (keeps the layer lean).
RUN apk add --no-cache ca-certificates

# Non-root user (container best practice); the gateway writes only to /data.
RUN addgroup -S -g 10001 gateway && adduser -S -D -H -u 10001 -G gateway gateway

# /data persists keys/channels/teams (store.data_dir). Pre-create + chown while
# still root (before USER gateway) so a bind mount OR a fresh named volume
# inherits 10001:10001 ownership — without this the VOLUME mountpoint is
# root-owned and the non-root gateway gets EACCES on the debounced persist
# (store.data_dir) at shutdown. VOLUME on a pre-existing dir preserves the image
# ownership into the volume's init layer.
RUN mkdir -p /data && chown 10001:10001 /data

# Minimal default config: env overrides (FUSION_GATEWAY_PORT, FUSION_MLX_URL,
# FG_MASTER_KEY) take precedence over every value here (bindSecretEnv). Auth is
# off by default so a bare `docker run` passes /healthz; mount a real config.yaml
# (or set FG_MASTER_KEY + api keys) for production.
COPY --chown=10001:10001 config.container.yaml /etc/fusion-gateway/config.yaml

COPY --from=builder --chown=10001:10001 /out/gateway /out/gateway

WORKDIR /etc/fusion-gateway
USER gateway

VOLUME ["/data"]

# Default listen port inside the container; overridable via FUSION_GATEWAY_PORT.
# fusion-mlx is reached on the Docker host; overridable via FUSION_MLX_URL.
ENV FUSION_GATEWAY_PORT=11432 \
    FUSION_MLX_URL=http://host.docker.internal:11434

EXPOSE 11432

# The gateway is PID 1: Go's signal.Notify (cmd/gateway/main.go) catches
# SIGINT/SIGTERM directly and runs graceful Shutdown — no init wrapper needed
# (no child processes to reap). --config points at the baked default; mount a
# volume at /etc/fusion-gateway/config.yaml to override with a full config.
ENTRYPOINT ["/out/gateway", "--config", "/etc/fusion-gateway/config.yaml"]
