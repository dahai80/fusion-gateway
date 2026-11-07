---
doc_id: fusion-gateway/guide
product: fusion-gateway
class: core
entry: service
doc_type: guide
title: { zh: "Claude Code 工作指引", en: "Claude Code guidance" }
audience: engineer
status: draft
version: 0.9.20
updated: 2026-11-07
owner: @dahai
source_of_truth:
  - CLAUDE.md
---

# CLAUDE.md

This file provides guidance to Claude Code (claude.ai/code) when working with code in this repository.

## Project Overview

Fusion-Gateway is a unified hybrid inference gateway for Apple Silicon local inference + cloud LLMs. It's the core traffic entry point for Fusion-Agent Studio, Fusion-MLX, and Fusion-Coder. Written in **Go** (not Python — to avoid competing with fusion-mlx for UMA memory).

**Four design principles:**
- **Non-invasive**: Pure routing/forwarding, no inference computation, never modifies fusion-mlx or any engine internals
- **Local-first**: Short/low-overhead/privacy requests go local; long/complex requests go cloud
- **Hardware-aware**: Routes based on macOS memory/GPU/Swap/inference load — not just request-level rules
- **Protocol-unified**: Full OpenAI v1 compatibility (chat/completions, completions, embeddings)

## Build & Run

```bash
# Build the single binary (module path: github.com/fusion-gateway/fusion-gateway)
go build -o fusion-gateway ./cmd/gateway

# Run (auto-starts fusion-mlx on :11434 via server.auto_start, gateway on :11432)
./fusion-gateway --config config.yaml
# config flag defaults to "config.yaml" in CWD

# Test
go test ./... -v
go test ./internal/router/... -v          # single package
go test -run TestTokenBudget -v           # single test (any package)
go test -run TestDecide ./internal/router/ -v   # scoped to a package

# Lint — no golangci-lint config is committed; use go vet
go vet ./...
# NOTE: gofmt -l flags the whole repo because the codebase uses 4-space
# indentation (multiples of 4) which is non-standard gofmt. This is
# pre-existing and intentional — match the surrounding style, do not reformat.

# Race detector (CI runs this; catches RWMutex-across-I/O, slot double-release)
go test ./... -count=1 -race

# Single package / single test
go test ./internal/router/... -v
go test -run TestTokenBudget -v           # any package
go test -run TestDecide ./internal/router/ -v   # scoped to a package
```

The binary embeds the admin dashboard frontend via `go:embed` (`internal/admin/ui/embed.go` → `dist/*`). A built `dist/` is checked in, so a plain `go build` works without Node. To rebuild the UI: `cd web/admin && npm install && npm run build` (Vite + TS, outputs to `web/admin/dist`), then copy into `internal/admin/ui/dist`.

## CI (`.github/workflows/`)

CI is **macOS-only** (`runs-on: macos-latest`) because `internal/hardware` uses darwin-only IOKit (purego) and sysctl swap-page counters with no cross-platform stubs. Two jobs run in parallel:

- **`ci.yml` → build-test**: `go vet` → **bare-goroutine gate** (`scripts/check_bare_goroutines.sh`, runs *before* Build) → `go build ./...` → `go test ./... -count=1 -race`.
- **`ci.yml` → vuln-scan**: `govulncheck -mode=source ./...` — scans the call graph (not just `go.mod`), fails only on *reachable* known vulns.
- **`release.yml`**: builds `darwin/arm64` binary, generates a **CycloneDX SBOM** (`cyclonedx-gomod`), `sha256` checksums, and publishes a GitHub Release. Prerelease gating: tags containing `-rc` publish as prerelease (not `latest`).

**Bare-goroutine gate** (CI red on violation): no bare `go func()` / `go call()` outside `safeGo`. `lifecycle.Worker` is allowlisted. Adding a *new* panic-safe concurrency primitive requires adding it to the ALLOW list in `scripts/check_bare_goroutines.sh` or CI fails.

## Admin Dashboard E2E (Playwright)

```bash
cd tests/e2e
npx playwright test                      # run all (targets FG_BASE or http://127.0.0.1:11432)
npx playwright test 05-config.spec.js    # single spec
npx playwright show-report
```

Tests assume a **live gateway** is already running (start `./fusion-gateway` first). `fullyParallel: false` — the admin API mutates shared `config.yaml`, so specs serialize. Global setup backs up `config.yaml` and global teardown restores it; do not run against a config you can't afford to mutate.

## Architecture

```
Clients (VSCode/UI/CLI/Agent)
        ↓
Fusion-Gateway :11432
├─ Ingress Layer       — Auth, parsing, standardization, rate limiting
├─ Preprocessing       — Tokenizer counting, prompt validation, param defaults
├─ Routing Engine      — Rule engine + hardware load sensing (core differentiator)
├─ Adapter Pool        — Unified interface for all inference backends
├─ Stream Forwarding   — SSE, cancel, retry, KV cache release
└─ Observability       — Logs, metrics, hot config reload
        ↓
Heterogeneous Inference Pool
├─ Local: fusion-mlx (:11434) / llama.cpp
├─ Private: vLLM-ascend / vLLM-cuda
└─ Cloud: Volcengine / Qianfan / Claude / OpenAI
```

### Wiring flow (cmd/gateway/main.go)

`run()` is the composition root. Order matters:
1. `config.Load(path)` → `*ConfigSnapshot` (versioned, context-propagated).
2. `autoStartLocal(cfg.Server.AutoStart)` — shell-exec `fusion-mlx/start.sh start`, poll `wait_url` up to `wait_secs`; `autoStopLocal` runs on shutdown.
3. `hardware.NewCollector` started in a context-cancellable goroutine (gopsutil + IOKit + fusion-mlx `/metrics`).
4. `router.NewEngine(snap, hwCollector)` — installs `NoopClassifier` by default; `wireIntentClassifier` swaps in the fusion-router-light classifier only when `routing.intent_classifier.enabled=true` and a `base_model` is set (else stays noop → P0–P7 rule chain unchanged).
5. `adapter.NewPool().BuildProviders(snap)` — constructs one `Provider` per `backends:` entry.
6. Wire local provider callbacks into the router: `SetLocalInFlight`, `SetLocalModels` (refreshed every 60s via `safeGo`), `SetLocalReady(true)`.
7. `server.New(...)` builds the HTTP mux and starts serving.

All background goroutines launch through `safeGo(name, fn)` (top of main.go) which recovers panics + logs the stack — never use bare `go func()` for long-lived workers.

### Routing decision engine (internal/router/engine.go)

Entry: `Engine.Decide(ctx, req)` → `*RouteDecision{Backend, Reason, CloudTarget}`. The engine holds a `sync.RWMutex` and per-backend `gobreaker` circuit breakers. `Decide` collects trip reasons under `RLock` then calls `Trip` **after** releasing the lock (RLock-inside-Lock on the same goroutine deadlocks in Go's RWMutex).

Priority chain (`decideLocked`), high to low:
- **Mode fast-path** — `routing.mode` `local`/`cloud`/`hybrid` overrides everything (`hybrid` falls through to the chain).
- **P-1 Semantic intent** (D4) — runs first so semantic dispatch wins; no-op when classifier disabled.
- **P0 Circuit breaker open** → cloud (with cluster fallback via `tryClusterLocked`).
- **P0.3 Session affinity** — same `space_id` sticks to the same provider (cowork spaces).
- **P0.5 Hardware collection error** → cloud when `hardware.collection_error_protection` is on.
- **P1/P1.5** — system memory ratio and MLX/GPU memory ratio overload → cloud + trip.
- **P2 Swap page in/out rate** thrashing → cloud + trip.
- **P3 Token budget** — total tokens > `routing.token_threshold` → cloud.
- **P4+** — token/output-input ratio tiers, model availability, local concurrency, fallback.

`ConfigSnapshot` is read from context (`config.SnapshotFromContext`) inside `Decide`, so hot-reloaded config takes effect on the next request without re-creating the engine.

### Config namespace: `routing.*`, not `route.*`

All routing knobs live under `routing:` in `config.yaml` and map to `config.RoutingConfig` (field tag `mapstructure:"routing"`). The `route:` key does **not** exist at runtime — handlers that wrote `route.*` were never reached (fixed in 6aa6477). When adding config, use the `routing.*` namespace and the `/admin/api/config/routing` domain.

Key fields actually in `config.yaml`:
- `routing.token_threshold` (default-tuned to **8000** in this repo's config, not 50)
- `routing.local_priority.max_system_memory_ratio`, `max_mlx_memory_ratio`, `swap_page_rate_threshold`, `max_concurrent`
- `routing.circuit_breaker.{memory_overload,swap_triggered,model_offline,...}`
- `routing.intent_classifier.{enabled,endpoint,base_model,adapter,min_confidence}`
- `routing.negotiation.{route_header:"X-Fusion-Route", route_header_value:"gateway-decision", disable_fusion_mlx_routing}`
- `routing.rate_limit`, `routing.retry`, `routing.fallback`, `routing.ratio_tiers`, `routing.token_tiers`

`server.auto_start` holds the fusion-mlx launch/stop commands and health `wait_url`.

### HTTP layer (internal/server/server.go)

`server.New(...)` constructs the server; routes are registered in `server.go` around line 692 on a plain `net/http` ServeMux (no router framework). The `internal/server/` package is split across many files (`anthropic_messages.go`, `chat_handlers.go`, `stream_resume.go`, `managed_settings.go`, `agent_tasks.go`, `mcp_listener`, etc.) — `server.go` holds the mux wiring + `Server` struct; feature logic lives in sibling files. Endpoints:
- OpenAI v1: `/v1/chat/completions`, `/v1/completions`, `/v1/embeddings`, `/v1/rerank`, `/v1/realtime`, `/v1/models`, `/v1/messages` (Anthropic), `/v1/images/generations`, `/v1/audio/*`, `/v1/moderations`, `/v1/batches`, `/v1/cost`, `/v1/status`.
- Stream resume: `/v1/stream/{sid}/events` (neutral route, `X-Fusion-Stream-Resume-URL` header) — replays a buffered local-MLX SSE stream by session id + seq; default-off, local-only, cloud NOT resumable.
- Agent slot scheduler: `/v1/agent/tasks` + `/v1/agent/tasks/{id}/cancel` (per-node slot cap, opt-in `slotQueue`, `task_registry` TTL+cap).
- Managed settings: `GET /gateway/v1/managed-settings` + `/pubkey` (Ed25519-signed payload, `X-Fusion-Signature:ed25519:<b64>`, opt-in `managed_settings.{enabled,private_key,payload}`, verbatim payload — no re-serialize).
- Browser scheduling: `/gateway/v1/browser/*` (cross-node browser-session scheduling, full REST proxy, opt-in `browser.enabled`).
- Identity gRPC control plane: `identity.*` config (opt-in, default off) — `AuthorizeAndAcquire` hot path + lease lifecycle; proto at `internal/identity/pb/identity.proto` (regen `paths=source_relative`).
- Connector framework: `/gateway/v1/connector/*`, `/gateway/v1/connection*`, `/gateway/v1/oauth2/*`.
- Admin: `/admin/api/*` (registered by `admin.Handler.RegisterRoutes`), `/admin/` SPA catch-all (`adminui.Handler()`), `/admin/api/fine-tune/` MLX proxy.
- MCP: dedicated listener (not the mux) — `mcp.host`/`mcp.port`; AuthGate constant-time + master_key fallback, fail-closed; Shutdown drains the MCP listener first.
- Ops: `/health`, `/healthz`, `/readyz`, `/livez`, `/metrics`, `/stats`, `/debug/pprof/*` (master-key gated), `/admin/gc`, `/admin/config/reload`.

All `/v1/*` routes go through `withMiddleware`; admin config mutation routes additionally require `withAdminOnly`/`requireAdminRole`. Auth is key-based with an `X-Fusion-Route` header (`routing.negotiation.route_header`) that lets upstream clients influence routing — a request missing this header is **skipped** by the stale-binary detection path (empty `/v1/models` = stale binary, rebuild + restart, not a config bug).

### Adapter pool (internal/adapter)

Every backend implements the `Provider` interface (`Chat/StreamChat/Embedding` + metadata). `pool.BuildProviders(snap)` builds one provider per `backends:` config entry; `pool.GetFusionMLX()` returns the typed local provider whose `InFlight`/`ModelSet` callbacks feed the router. Provider files: `fusion_mlx.go`, `openai_compatible.go`, `anthropic.go`, plus per-vendor shims (deepseek, moonshot, baichuan, dashscope, hunyuan, minimax, `fusion_kb.go`, `fusion_model_hub.go`).

**SSE hardening**: all SSE line buffers cap at 1 MiB/line and API responses at 10 MiB to prevent OOM — applies to `anthropic.go`, `openai_compatible.go`, `builtins.go`, `cloud_signed.go`, `bedrock.go`. An oversize line (>1 MiB) must **return** an error, not silently nil the buffer. Keep this cap when adding new streaming adapters.

**Transport cap**: every provider HTTP client is built via `internal/httpx.TransportForBackend` → `MaxConnsPerHost` (default 16) + `ResponseHeaderTimeout` 30s + `DialContext`. Bare `&http.Client{}` is banned (audit gate) — it leaves `MaxConnsPerHost=0` → FD exhaustion under concurrency. Cluster clients and the intent classifier go through the same helper.

### Admin dashboard

Backend: `internal/admin/` (JWT HS256 + HttpOnly cookie; `auth.go`, `handler.go`, `config_handlers.go` for the `/admin/api/config/*` domain endpoints). Admin keys are looked up via `Store.GetKeyByHash(SHA-256)`, **not** `config.yaml` auth keys (fixed v0.8.6) — when debugging admin auth, inspect the store, not the config file.

Frontend: `web/admin/` React + Ant Design + Vite SPA, embedded into the binary via `internal/admin/ui/embed.go` (`//go:embed dist/*`). The checked-in `dist/` makes `go build` work offline; rebuild from `web/admin` when frontend changes need to ship.

## Key Integration Points

| Service | Address | Protocol |
|---------|---------|----------|
| fusion-mlx (local inference) | :11434 | HTTP/OpenAI-compatible |
| Fusion-Gateway (this service) | :11432 | HTTP/OpenAI-compatible |
| Fusion-KB | :11434 | HTTP |
| Fusion-Doc | :11449 | HTTP |

## Fusion Ecosystem Context

This gateway is part of the Fusion-MLX monorepo at `/Users/dahai/fusion/`. Sibling projects:
- **fusion-mlx** — local MLX inference engine (the primary local backend). Start/stop with `~/fusion/fusion-mlx/start.sh start|stop|status|log`. Model downloads via `https://hf-mirror.com`; cache at `~/.fusion-mlx/models`.
- **fusion-desk** — desktop automation platform (Python, MCP server)
- **fusion-plugins-ecosystem** — plugin registry with `ClaudeGateway` for Claude full-chain adaptation
- **fusion-studio** — macOS native SwiftUI desktop client
- **fusion-model-hub** — model repository and management
- **fusion-code** — code generation/editing tools

The fusion-plugins-ecosystem `ClaudeGateway` handles Claude Desktop/Code/Web integration at the plugin layer; this Fusion-Gateway handles the lower-level inference routing and load balancing.

## Conventions

- Go code, 4-space indentation (multiples of 4) — non-standard gofmt, pre-existing, **do not reformat**.
- Structured logging throughout (`slog`); every new code path should log for traceability.
- No external state stores required (in-memory store in `internal/store/memory/`; optional Redis in `internal/store/redis/`). Single binary, local files only.
- SSE streaming must be zero-buffer async forwarding; client cancel must propagate downstream to release KV cache.
- Background goroutines always go through `safeGo` (panic recovery) — never bare `go func()`. Three primitives, picked by lifetime:
  - `safeGo.Go(name, fn)` — **one-shot** short task; recovers panic, logs stack, goroutine dies.
  - `safego.GoRestart(name, fn)` — **long-lived worker** that must survive a panic; restarts after backoff, has a panic-breaker (too many restarts → stop + log).
  - `lifecycle.Worker` — **joinable long-lived worker** with its own cancel + `WaitGroup`; `Shutdown` calls cancel then `wg.Wait()` so a mid-write shutdown is safe. Use for reapers, pollers, evictors.
  CI bans bare `go func()` outside these (see Bare-goroutine gate above); a new panic-safe primitive must be added to the allowlist.
- Config hot-reload is via `ConfigSnapshot` read from request context inside `Decide`/handlers; `fsnotify` watches `config.yaml` and `/admin/config/reload` forces a reload. Reload handlers + commit are serialized by a separate `reloadMu` (decoupled from `configMu`) to close the unlocked window. Validate config in `config.Validate`.
- All metrics: local QPS, cloud QPS, local hit rate, avg latency, circuit breaker trips — surfaced via `/metrics` (Prometheus) and the admin analytics API.

## Containerized Deployment

`Dockerfile` (alpine builder + runtime) + `config.container.yaml`. purego forces musl dynamic linkage, so a `scratch` image is impossible. Non-darwin guards exist: `swap_linux.go` (parses `/proc/vmstat`) and the IOKit path is guarded behind `//go:build darwin`. `deploy/` ships Helm (`deploy/helm/`), Kubernetes manifests, and Terraform (`deploy/terraform/` aws/gcp). launchd uses `launchctl bootstrap` (not `load`) on the managed host.
