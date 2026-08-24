# ADR-001: Agent Scheduling Ownership (Issue #97)

**Status**: Accepted
**Date**: 2026-08-24
**Issue**: #97 — A2: Agent 调度（slots/queue/cancel）归属未定

## Decision

Agent scheduling (slot pool + queue + agent-task cancel) belongs to the **gateway layer**.

gateway already owns the inference concurrency surface:
- `MaxConcurrent` (engine.go P5, line 567) — reject-style local in-flight cap → cluster/cloud
- `MLXInferenceQueueDepth` readiness gate (server.go:1697) — hardware-aware queue check
- client-stream cancel propagation (server.go:860) — ctx.Done → provider.Cancel(lastChunkID)
- batch `Store.Cancel` (batch.go:178) — per-id status flip

These are the primitives a scheduler composes. Building the scheduler anywhere else duplicates what gateway must own to route correctly: gateway sees every inference request, every backend, every cancel. core sees only its own client calls; it cannot count cross-node slots without re-querying gateway.

## Why gateway, not core

- **gateway is the single inference chokepoint** — every /v1/* request transits it. Slot accounting must be exact or slots leak under stream-abnormal recovery (the #97 failure mode: concurrent starvation). Only the layer that sees all inflight can count them correctly.
- **core is Agent-semantic, gateway is inference-capacity** — DAG/priority/dependency are Agent concepts (fusion-agent-studio/fusion-cowork). Slots/queue/cancel at the inference-unit level are capacity concepts. Layering: Agent scheduling logic stays in core; **inference-slot enforcement** lives in gateway. The two do not double-count (see below).
- **cross-node slot aggregation** — gateway already aggregates `localInFlight` + cluster `HealthyNodes` + per-node in-flight (discovery `InFlight` metrics). core has no view of remote nodes' in-flight.

## Sub-decisions (for the implementation issue)

### 1. Slot granularity: per-node (local) + per-cluster-node, NOT global
- Local: one slot pool sized by `MaxConcurrent` (existing config, default 8), counts local in-flight. Reuses `localInFlight()` counter — no new counter.
- Cluster: each node's `InFlight` (discovery already polls `/v1/status` metrics) is that node's slot occupancy. `MaxConcurrent` becomes a per-node ceiling via the existing `HealthyNodesByModel` filter — a node at its cap is skipped by `SelectNodeByModel` (#95). **No global slot pool** — a global pool can't reflect per-node capacity and would misroute.
- Rationale: slots must map 1:1 to a concrete inference unit that can be released. Global slots are an abstraction over nothing; the release signal (stream done / cancel) is per-request-per-node, not global.

### 2. Queue policy: bounded FIFO with cloud-fallback overflow, NOT priority/fair
- When local pool full AND a serving cluster node free → route to cluster (#95 path, already exists).
- When local pool full AND no serving cluster node free → **cloud fallback** (existing P5 fallthrough, line 572). NOT a wait-queue — the request goes to cloud immediately. A wait-queue at the gateway re-introduces head-of-line blocking and doubles latency for cloud-capable requests.
- Optional bounded **local wait-queue** (future, default OFF) only when `routing.mode=local` (cloud disabled): short FIFO, `queue_timeout` (default 5s), reject with 429 on timeout. This is the ONLY case a queue is warranted — when there's no cloud escape valve. Off by default keeps hybrid behavior unchanged.
- Rationale: priority/fair queues require Agent semantic labels (priority field) that gateway does not own and should not parse. FIFO + cloud-overflow is the capacity-correct policy; Agent priority is core's job and expresses as request arrival order at the gateway.

### 3. Cancel semantics: immediate slot release, downstream propagation already exists
- Slot release: `defer` decrement of the in-flight counter on stream end / cancel / error (gateway already does this — verify the release path covers all exit branches; the #97 "slot leak under stream abnormal" is the bug this catches).
- Agent-task cancel API: new `POST /v1/agent/tasks/{id}/cancel` — cancels by task id, propagates to the in-flight stream's context (server.go cancel path). For non-stream (aggregate), cancels the upstream fetch via ctx. Slot released on the same `defer`.
- Release is **immediate** (not "wait for in-flight completion") — the slot represents an inference slot; once the downstream is told to stop, the slot is free. The KV-cache release is the provider's concern (cancel already propagates).
- Rationale: "wait for completion" defeats the purpose of cancel (slot stays occupied → starvation, the exact #97 symptom). Immediate release + downstream cancel = correct.

## What stays in core

- DAG execution, node dependency resolution, priority assignment — Agent semantics.
- core passes through to gateway with the request as-is. core's `with_retry(disable=True)` (already done) leaves concurrency to gateway — this ADR confirms that handoff and closes the loop.

## Double-counting guard

`MaxConcurrent` is gateway's single source of truth for local slots. core does NOT also count — it disabled retry-concurrency already. If core ever re-enables client-side concurrency, it must count a DIFFERENT resource (e.g. concurrent Agents, not inference slots), never the same inference slot. Documented in the implementation issue.

## Non-invasive boundary

- No new inference computation in gateway (design principle: non-invasive).
- Slot enforcement composes existing counters (`localInFlight`, node `InFlight`); queue is opt-in + default-off; cancel reuses existing ctx propagation. The scheduler is a thin coordinator over existing surfaces, not a new engine.

## Implementation issue

Split into a single tracking issue with the three sub-decisions above fixed (not re-opened). See GitHub issue created from this ADR.
