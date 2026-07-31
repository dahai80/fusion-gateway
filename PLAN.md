# Audit Fix Plan — P0 + P1 from atom-gateway-audit-report.md

## P0 (Must Fix Before Production) — 6 items

### C1: Hard-coded admin credentials + default JWT secret
- Files: internal/admin/login.go, internal/admin/auth.go, internal/config/config.go, internal/server/server.go
- Fix:
  1. Delete adminCredentials hard-coded map → config-driven AdminConfig.Users map[string]string
  2. Delete jwtSecret default value → SetJWTSecret("") disables admin, panics if admin enabled without secret
  3. Validate len(JWTSecret) >= 32 when admin enabled
  4. HandleLogin reads credentials from injected AdminConfig, not package-level var

### C2: ClusterNodeProvider missing sharedToken
- Files: internal/cluster/node_adapter.go, internal/server/server.go, internal/cluster/shard.go
- Fix:
  1. Add sharedToken param to NewClusterNodeProvider, wire to apiKey field
  2. Update 5 call sites to pass clusterCfg.Master.SharedToken

### C3: Semantic cache pseudo-embedding fallback
- Files: internal/cache/semantic.go, internal/server/server.go
- Fix:
  1. Remove defaultEmbedFn/simpleHashEmbedding — no fallback
  2. NewSemanticCache: embedFn==nil → log error + return nil (disabled)

### C4: BuildProviders doesn't remove stale providers on reload
- File: internal/adapter/pool.go
- Fix: Diff current keys vs new config, delete removed/disabled entries

### C5: Config reload commits snapshot before handlers succeed
- File: internal/config/config.go
- Fix: Run handlers first, commit snapshot only if all succeed, rollback on failure

### 硬伤3+4: /metrics unauthenticated + pprof relies on auth chain
- File: internal/server/server.go
- Fix: /metrics add master-key middleware; pprof default off, flag to enable + master-key guard

## P1 (First Iteration) — 8 items

### L4: Empty prompt routed to cloud → split !ok vs InputTokens==0
### L7: Prompt injection skips multimodal → recursive extractPromptText
### L2: Single-lock rate limiter → sync.Map + idle cleanup
### L3: Cache.Get write lock → RLock read path, lazy deletion
### R3: Batch.Get mutable pointer → deep copy
### V1: Admin key no validation → validate Name/QuotaLimit/Key
### V2: Config validate 4 fields → extend to all security fields
### M3: Unknown backend type only warns → continue (skip backends registration)

## Execution Order
1. C1 → C2 → C3 → C4 → C5 → 硬伤3+4
2. L4 → L7 → L2 → L3 → R3 → V1 → V2 → M3
3. Lint + test + commit
