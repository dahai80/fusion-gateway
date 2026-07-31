# Audit Fix Plan — from atom-gateway-audit-report.md

## v0.6.1 (Batch 1) — DONE ✅

### P0 (Must Fix Before Production) — 6 items
- C1: Hard-coded admin credentials + default JWT secret ✅
- C2: ClusterNodeProvider missing sharedToken ✅
- C3: Semantic cache pseudo-embedding fallback ✅
- C4: BuildProviders doesn't remove stale providers on reload ✅
- C5: Config reload commits snapshot before handlers succeed ✅
- 硬伤3+4: /metrics unauthenticated + pprof relies on auth chain ✅

### P1 (First Iteration) — 8 items
- L4: Empty prompt routed to cloud → split !ok vs InputTokens==0 ✅
- L7: Prompt injection skips multimodal → recursive extractPromptText ✅
- L2: Single-lock rate limiter → sync.Map + idle cleanup ✅
- L3: Cache.Get write lock → RLock read path, lazy deletion ✅
- R3: Batch.Get mutable pointer → deep copy ✅
- V1: Admin key no validation → validate Name/QuotaLimit/Key ✅
- V2: Config validate 4 fields → extend to all security fields ✅
- M3: Unknown backend type only warns → continue (skip backends registration) ✅

## v0.6.2 (Batch 2) — DONE ✅

- 硬伤1: Unified Principal auth model (3 separate context systems → single Principal struct) ✅
- 硬伤2: Cache runtime config update (UpdateConfig for TTL/maxEntries/maxBytes) ✅
- M1: Middleware chain built once at startup (func(http.Handler)http.Handler composition) ✅
- M2: safeGo panic recovery for all background goroutines ✅
- M3: Unknown backend fail-fast (return error instead of warn+skip) ✅
- L7: Multimodal prompt injection (image_url/input_audio/image content extraction) ✅
- L2: Per-key mutex via sync.Map for rate limiter ✅
- P3: P95 result caching with TTL in LatencyTracker ✅
- A4: Runtime backend switch fallback (local fails → cloud) ✅

## Remaining (P2 — Architectural)

- A1: Global singleton → dependency injection (oidcProvider, jwtSecret)
- A2: Server god object splitting (break server.go into per-domain files)
- A3: Persistent storage (Redis/Postgres) for multi-instance deployments
