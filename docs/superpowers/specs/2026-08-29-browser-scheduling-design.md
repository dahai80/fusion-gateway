# Cross-node Browser-Session Scheduling — Design Spec

**Issue:** [dahai80/fusion-gateway#130](https://github.com/dahai80/fusion-gateway/issues/130)
**Date:** 2026-08-29
**Status:** Draft (awaiting human review)

## 1. Goal

Implement the gateway-side of the fusion-browser multi-node capacity
contract (`fusion-browser/docs/multinode-contract.md`, audit R-10/H-9).
fusion-browser is a non-persistent single-node WKWebView engine; it
exposes a per-node `{type:"capacity"}` UDS plane. This gateway becomes
the scheduler + full proxy: it discovers browser nodes, polls capacity,
places `create_session` on the node with most headroom, forwards all
session lifecycle ops to the pinned node, re-routes new creates away
from dead nodes, and exposes an operator node map.

## 2. Scope

In scope (all five contract parts + full proxy):
- Node registry + capacity poll (static config seed + dial-in register).
- Placement on `create_session` (headroom ranking + global ceiling).
- Route: forward `create_session` to chosen node UDS, relay response.
- Dead-node re-route (dead node skipped; in-flight sessions on it lost).
- Admin node map + optional cross-node global session ceiling.
- Full browser proxy: gateway fronts create/execute/close/metrics.

Out of scope (contract-stated, gateway does NOT do):
- Live in-place session migration (session pinned to creating node).
- Sticky node identity across browser restarts (`node_id` is per-process).
- Persisting the session->node pin map (non-persistent engine).

## 3. Non-goals

- No change to fusion-browser (contract already merged).
- No reuse of `internal/cluster` (model-inference-shaped; wrong domain) or
  `internal/mcp` forward (HTTP, not length-prefixed UDS). Browser owns it.
- No gRPC/protobuf (Phase 1 wire = length-prefixed JSON; D12 deferred).

## 4. Wire contract (byte-exact Go mirror)

Source of truth: `fusion-browser/Sources/FusionBrowser/Framing.swift`,
`Protocol.swift`, `Config.swift`, `ErrorModel.swift`. The Go structs in
`internal/browser/wire.go` MUST round-trip the same bytes.

**Framing** (`framing.go`): every message = 4-byte big-endian uint32
length prefix + JSON body. Frame cap `frame_max_bytes` (default 8MiB,
matches `Framing.swift maxFrameBytes`). A frame claiming > cap is a
hard error (drop + close conn). Partial-frame (prefix read, body
incomplete) timeout 30s — close conn, clear buffer. Go codec:
`WriteFrame(w, obj)` = `binary.BigEndian.PutUint32` len + `json.Marshal`
body. `ReadFrame(r)` = read 4 bytes BE len, bound-check, `io.ReadFull`
body, `json.Unmarshal`. Reuse `internal/httpx` line-cap discipline; cap
at `frame_max_bytes` to prevent OOM (matches SSE hardening convention).

**Envelope** (`wire.go`): `{type, payload, sessionId}`. Request `type` ∈
`create_session`/`execute`/`close`/`metrics`/`capacity`. Response `type`
∈ `create_session`/`state`/`closed`/`metrics`/`capacity`/`error`. Go
uses a tagged union via `type` discriminator + separate structs (not
Swift's Codable enum) — `RequestFrame` / `ResponseFrame` each carry the
raw `type` string + a `json.RawMessage` payload decoded per-type by the
caller. This avoids a full re-encode round-trip when proxying verbatim.

**Payloads** (snake_case wire keys via struct tags):
- `CreateSessionRequest`: `mode`(headless|headed), `initial_url?`,
  `max_actions?`, `task_timeout_ms?`, `credential_domain?`.
- `CreateSessionResponse`: `session_id`, `credential_injected`.
- `BrowserActionRequest`: `session_id`, `action`(click|type_text|scroll|
  navigate|screenshot|evaluate|close), `target_node_id?`, `payload_text?`,
  `scroll_delta_y?`, `trace_id?`.
- `BrowserStateResponse`: `session_id`, `url`, `title`, `ax_tree_markdown`,
  `interactive_nodes`, `screenshot_jpeg?`, `screenshot_png?`,
  `has_security_injection_blocked`, `execution_time_ms`,
  `security_audit?`, `session_recovered?`, `error?`, `trace_id?`,
  `evaluate_result?`. (Proxy passes through opaquely — gateway does not
  interpret AXTree/screenshot; decode only what routing needs.)
- `MetricsResponse`: `counters`, `latency` — opaque passthrough.
- `FBNodeCapacity`: `node_id`, `max_sessions`, `live_sessions`,
  `max_total_memory_mb`, `free_memory_mb`, `ram_gb`. (The placement
  signal — decoded and stored in the registry.)
- `FBError`: `code`, `message`, `retryable`.

**Proxy verbatim rule:** for `execute`/`close`/`metrics`/`state`, the
gateway forwards the request frame body to the pinned node and relays
the response frame body to the caller WITHOUT re-encoding the payload
into Go structs (only the framing length-prefix is rewritten). This
keeps the proxy wire-faithful and avoids a schema-drift trap if
fusion-browser adds a response field the gateway doesn't model. Only
`create_session` (placement decision needs the response `session_id`)
and `capacity` (registry needs the decoded fields) are fully decoded.

## 5. Package layout

One new self-contained package: `internal/browser/`. It owns the whole
subsystem; `cmd/gateway/main.go` + `internal/server/server.go` only wire
it when `browser.enabled`. No edits to `internal/cluster` or
`internal/mcp` (domain mismatch — see §3).

| file | responsibility |
|---|---|
| `framing.go` | `WriteFrame`/`ReadFrame`: 4-byte BE length-prefix codec, frame cap, partial-frame timeout. Mirrors `Framing.swift`. |
| `wire.go` | `RequestFrame`/`ResponseFrame` tagged-union + payload structs with snake_case tags. Mirrors `Protocol.swift`/`Config.swift`/`ErrorModel.swift`. |
| `client.go` | `NodeClient`: dial a node UDS, send one frame, read one response. Per-call dial (no pooled conn — fusion-browser closes per request; matches its per-client read loop). Dial timeout, frame timeout from config. |
| `registry.go` | `Registry`: node set + capacity snapshots. Hybrid discovery (static config seed + dial-in register). 5s background poll via `lifecycle.Worker`. Dead-node detection (failure_threshold). |
| `scheduler.go` | `Scheduler.Pick()`: headroom ranking + global ceiling + dead-node skip. Pure function over registry snapshot (no I/O) — fully unit-testable. |
| `proxy.go` | `Proxy`: session->node pin map + the five op handlers (Create/Execute/Close/Metrics/Nodes). Holds `*Registry` + `*NodeClient`. Pin map in-memory, evict on close. |
| `handler.go` | HTTP `Handler` registering `/v1/browser/*` + `/admin/api/browser/nodes`. Translates REST <-> proxy calls. Auth via `withMiddleware` + `withAdminOnly`. |
| `config.go` (or in `internal/config`) | `BrowserConfig` struct + defaults + `Validate`. Namespace `browser.*`. |
| `*_test.go` | Offline fake-UDS tests (§11). |

**Ownership invariant:** `browser/` never imports `cluster` or `mcp`.
It imports only `config`, `lifecycle`, `safego`, `observability`, stdlib.
This keeps the subsystem coherent and testable in isolation.

## 6. Registry + capacity poll

`Registry` holds `map[nodeID]*BrowserNode` under a `sync.RWMutex` (one
owner per field — no lock nesting, per E8 convention). `BrowserNode`:
`NodeID string`, `SocketPath string`, `Capacity *FBNodeCapacity` (nil
until first poll), `State` (live|dead), `failures int`, `lastPoll
time.Time`, `source` (config|dialin).

**Hybrid discovery:**
- **Static seed:** on `New(cfg)` + on hot-reload `DrainAndApply(cfg)`,
  read `browser.nodes` (id + socket_path) into the map with
  `State=live`, `Capacity=nil`. Config-node ID is the operator label,
  distinct from the per-process `node_id` returned by capacity poll —
  the registry keys on the config `id` (stable label), and stores the
  polled `node_id` inside `Capacity` for the admin map. **Registry key =
  stable config `id`, NEVER the per-process `node_id`** — this is why a
  browser restart (new `node_id`) is absorbed without churning the
  registry: the poll just overwrites the stale `node_id` under the same
  config-id key. Reconnect re-queries; `node_id` drift is invisible to
  placement.
- **Dial-in register:** when an unregistered connection dials the
  gateway's browser ingress and sends `{type:"capacity}`, the handler
  records `SocketPath` from the peer UDS address + the decoded capacity,
  mints a config-style label `dialin-<short-hash(socketPath)>`, adds the
  node `State=live source=dialin`. (UDS peer address = the socket path
  the dialer bound; this is how the gateway learns a self-registering
  node's socket to forward to later.)

**Capacity poll** (`lifecycle.Worker` "browser-poll", goroutine via
`safeGo` — never bare): every `poll_interval` (default 5s, jittered via
`internal/jitter` to avoid herd, per H5), for each `live` node:
`NodeClient.Capacity(ctx)` → on success store snapshot, reset
`failures=0`, `lastPoll=now`. On error (dial fail / conn-refused /
framing error): `failures++`; if `failures >= failure_threshold` (3) →
`State=dead`, `log.Warn`. Dead nodes stay in the map (so the admin map
shows them) but are skipped by placement. A dead node that recovers
(poll succeeds after `recovery_interval` 30s) → `State=live`, reset.

**Lifecycle:** `Registry.Start(ctx)` launches the worker; `Stop()` =
`worker.Stop()` (cancel + wg.Wait, per EI10). No bare `go func()` (CI
bare-goroutine gate — `lifecycle.Worker` is allowlisted).

## 7. Placement algorithm

`Scheduler.Pick() (nodeID string, err error)` — pure over a registry
snapshot, no I/O (deterministic, fully unit-testable). Snapshot taken
under `RLock`: copy live nodes + their `Capacity`.

Steps:
1. **Global ceiling:** if `global_max_sessions > 0` and
   `sum(live_sessions across live nodes) >= global_max_sessions` →
   return `ErrGlobalQuotaExceeded` (caller surfaces 503
   `quota_exceeded`, not retryable — matches `FBError.quotaExceeded`).
2. **Headroom filter:** candidate = node with `live_sessions <
   max_sessions` AND (`free_memory_mb == 0` OR `free_memory_mb >=
   min_free_mb_per_session`). `free_memory_mb==0` = probe failure =
   unknown (never fabricate), admitted with memory check skipped.
3. **Rank:** sort candidates by most `free_memory_mb` desc; tie-break
   fewest `live_sessions` asc; final tie-break config `id` asc
   (deterministic). If ALL candidates have `free_memory_mb==0`, rank by
   fewest `live_sessions` asc (contract fallback).
4. **No candidate:** return `ErrNoNodeHeadroom` (503, retryable — a
   slot may free up; caller may retry, per fusion-cowork retry path).

Dead nodes never reach the snapshot (filtered at step 2's live gate).
The chosen `nodeID` is the config label; the proxy uses it to look up
`SocketPath` for the forward.

## 8. Proxy surface (HTTP ingress + pin map)

`Proxy` holds `*Registry`, `*NodeClient`, `*Scheduler`, and an in-memory
pin map `map[sessionID]nodeID` under its own mutex (one owner — E8).

**REST routes** (registered in `handler.go` only when `browser.enabled`):
| method | path | op | auth |
|---|---|---|---|
| POST | `/v1/browser/sessions` | create | withMiddleware |
| POST | `/v1/browser/sessions/{id}/actions` | execute | withMiddleware |
| DELETE | `/v1/browser/sessions/{id}` | close | withMiddleware |
| GET | `/v1/browser/nodes` | admin node map | withAdminOnly |
| GET | `/v1/browser/metrics` | forward metrics | withAdminOnly |

**Create flow:**
1. Decode `CreateSessionRequest` from JSON body (size-capped via
   existing middleware).
2. `Scheduler.Pick()` → nodeID or 503 quota/headroom error.
3. `NodeClient.Create(ctx, socketPath, req)` → forward
   `{type:create_session, payload:req}` frame, read response.
4. On `create_session` response: record pin `sessionID -> nodeID`,
   return `{session_id, node_id, credential_injected}` (201).
5. On `error` response from node: relay `FBError` as 503 with the node's
   `code`/`retryable` (honest — don't mask node_stale/quota as 502).

**Execute flow:**
1. Path param `sessionID` → pin lookup.
2. Pin miss → 404 `session_not_found` (session unknown to gateway —
   created on a stale gateway, or pre-existing).
3. Pin node dead (registry `State=dead`) → 503 `session_lost`
   retryable=true (in-flight session on dead node is LOST per contract;
   caller recreates on a fresh node — surface honestly, do not retry
   internally).
4. `NodeClient.Execute(ctx, socketPath, actionReq)` → forward
   `{type:execute, payload:actionReq}` (verbatim body), read `state`
   response, relay body verbatim (200). Decode only `session_id` for
   consistency, pass the rest through.

**Close flow:** pin lookup → forward `{type:close, session_id}` → on
`closed` response evict pin (204). Pin miss → 404. Dead pin → best-effort
forward (close is idempotent; node may already be gone) then evict pin.

**Metrics flow:** admin-only; forward `{type:metrics}` to a chosen node
(query param `node` selects, else round-robin a live node) → relay
`metrics` response verbatim.

**Nodes flow:** admin-only; return JSON array of
`{id, node_id, socket_path, state, live_sessions, max_sessions,
free_memory_mb, last_poll}` from the registry snapshot (the operator
node map from #130 acceptance).

**Pin map lifecycle:** in-memory only. No persistence (non-persistent
engine — a persisted pin would point at a dead session after any browser
restart, a false guarantee). On gateway restart all pins reset; clients
recreate sessions (honest for a non-persistent system).

## 9. Config

Namespace `browser.*` in `config.yaml` (mirrors `mcp.*`/`cluster.*`
convention), mapped to `config.BrowserConfig` (tag `mapstructure:"browser"`).
Added to `ConfigSnapshot` + `DefaultConfig` + `Validate` (EI8: range-check
numerics).

```yaml
browser:
  enabled: false               # default-off gate; off = no routes, no goroutines
  poll_interval: 5s            # capacity poll cadence
  failure_threshold: 3         # consecutive poll fails -> dead
  recovery_interval: 30s       # dead node re-probe backoff
  global_max_sessions: 0       # 0 = no cross-node ceiling
  min_free_mb_per_session: 200 # memory floor for placement (free_mem==0 skips)
  frame_max_bytes: 8388608     # 8MiB frame cap, matches Framing.swift
  dial_timeout: 2s             # NodeClient UDS dial timeout
  frame_timeout: 30s           # partial-frame / response read timeout
  nodes:                       # static seed (hybrid discovery)
    - id: fb-node-a
      socket_path: /tmp/fusion-browser-a.sock
```

Hot-reload: `DrainAndApply(cfg)` rebuilds the node set from `browser.nodes`
(preserve live capacity snapshots for unchanged socket_paths; drop removed
nodes; add new ones `live`/`nil`). Toggling `enabled` off stops the worker
+ unregisters routes (operator must restart for route unregister — routes
are registered once at `server.New`; hot-disable makes the handler return
503 instead. Acceptable; matches mcp toggle behavior).

## 10. Error handling

| failure | detection | response | retryable |
|---|---|---|---|
| node dial fail / conn-refused | `NodeClient` dial error | poll: `failures++`; request: 503 `node_unreachable` | yes |
| node dead (failure_threshold) | registry `State=dead` | placement skips; pin op → 503 `session_lost` | yes |
| global ceiling hit | `Scheduler.Pick` | 503 `quota_exceeded` | no |
| no headroom anywhere | `Scheduler.Pick` | 503 `no_headroom` | yes |
| frame oversize (> cap) | `ReadFrame` | close conn; request → 502 `frame_oversize` | no |
| partial-frame timeout | `ReadFrame` 30s | close conn; request → 504 `frame_timeout` | yes |
| node returns `FBError` | decoded response | relay `code`/`retryable` as 503 | per-node `retryable` |
| pin miss | proxy lookup | 404 `session_not_found` | no |

**No silent masking:** a node `FBError` is relayed with its own
`code`/`message`/`retryable` — never coerced to a generic 502 (RC1
lesson: masked errors hide root cause). Gateway errors use distinct
codes (`node_unreachable`/`session_lost`/`quota_exceeded`/`no_headroom`)
so fusion-cowork can branch its retry/recreate logic.

## 11. Testing

Offline only — fake UDS server via `net.Listen("unix", path)` speaking
the length-prefix frames. No live fusion-browser (CI macos-only, no
external deps; matches repo offline-test convention). Coverage matrix:

`framing_test.go`: WriteFrame/ReadFrame round-trip; oversize frame
reject; partial-frame timeout; empty body; BE length prefix correctness.
Cross-check wire bytes against the contract doc examples.

`wire_test.go`: every payload struct encodes/decodes to the exact
snake_case wire keys from the contract (golden JSON from
`multinode-contract.md` examples); `FBNodeCapacity` round-trip;
`CreateSessionRequest` optional-field omission; `FBError` fields.

`scheduler_test.go`: headroom rank (most free_mem wins); tie-break
fewest live; all-free_mem==0 fallback ranks by fewest live; dead node
skipped; full node (live==max) skipped; free_mem below floor skipped;
global ceiling reject; no candidate → ErrNoNodeHeadroom; deterministic
order (same snapshot → same pick).

`registry_test.go`: static seed populates map; dial-in register adds
node; 5s poll updates snapshot (fake clock or short interval); poll
fail → failures++ → dead at threshold; dead node recovery; hot-reload
adds/removes/preserves; Start/Stop lifecycle (no goroutine leak via
`lifecycle.Worker`).

`proxy_test.go`: create → placement → forward → pin recorded → 201;
create on dead registry → 503 quota; execute → pin lookup → forward
verbatim → 200; execute pin miss → 404; execute dead pin → 503
session_lost; close → forward → pin evicted → 204; close dead pin →
best-effort + evict; metrics forward; nodes admin map shape. Fake
server returns canned frames per op.

`handler_test.go`: routes registered only when enabled; auth gates
(withMiddleware on create/execute/close, withAdminOnly on nodes/metrics);
disabled → 503; malformed body → 400.

## 12. Wiring (server.go)

In `cmd/gateway/main.go` `run()` (composition root), after the adapter
pool + router wiring, before `server.New`:
1. If `cfg.Config.Browser.Enabled` → build `browser.NewRegistry(cfg)`,
   `browser.NewScheduler(reg, cfg)`, `browser.NewProxy(reg, sched, cfg)`.
2. `reg.Start(ctx)` (launches the poll worker via `lifecycle.Worker` +
   `safeGo`). `reg.Stop()` registered on shutdown (before the HTTP
   server stops, after MCP listener drains — mirror the existing
   shutdown order).
3. Pass `proxy` into `server.New(...)`; `server.go` calls
   `proxy.Handler().RegisterRoutes(mux)` only when non-nil.

`server.New` signature gains an optional `browserProxy *browser.Proxy`
(nil when disabled) — minimal change to the existing constructor. No
change to the routing engine (browser is a separate proxy plane, not a
routing rule). All new goroutines go through `safeGo` + `lifecycle.Worker`
(CI bare-goroutine gate allowlist already covers `lifecycle.Worker`).

## 13. Release

#130 is a feature (new subsystem, new endpoints) → minor bump, not a
patch. Version `v0.9.0` (current stable is `v0.8.51`; `v0.8.52-rc1` is a
prerelease at the same commit). After #130 lands + tests green + CI green:
1. Update `CHANGELOG.md` / README version references.
2. `git tag v0.9.0` + `git push origin v0.9.0` → triggers `release.yml`
   (build darwin/arm64 binary, create GitHub Release).
3. Close #130 with the PR link.

`v0.8.52-rc1` stays a prerelease (the rc-tag-prerelease gate marks it
non-latest). The stable line jumps to `v0.9.0` for the feature.
