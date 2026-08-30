# Fusion-Gateway Recovery Runbook

# R15 (audit): operational recovery runbook. The product-readiness audit found
# no RPO/RTO/backup documentation. This runbook covers data durability, restart
# recovery, and the ops-only P1 items (C1/C2/C3) that cannot be code-fixed.

## RPO / RTO

| Data | Durability | RPO | RTO |
|------|-----------|-----|-----|
| API keys / channels / teams | on-disk JSON (`data_dir/`), debounced persist | ~2s (debounced) | <30s (binary restart) |
| Quota counters (used/limit) | on-disk JSON, debounced persist | ~2s | <30s |
| Request logs (`logs/`) | on-disk ring buffer, bounded (`max_entries`) | restart-lost beyond `max_entries` | n/a (rebuilds from live traffic) |
| Cache (`cache/` + semantic) | in-memory only | restart-lost | n/a (cold-start, repopulates on traffic) |
| Circuit-breaker state | in-memory only | restart-lost (resets to `closed`) | n/a (re-trips on real failures) |
| `config.yaml` | operator-managed file | git/version-controlled | <30s (restart after fix) |

**Single-binary, local-first**: RTO is a process restart (<30s). No external
state store required — all durable state is JSON under `data_dir/` (default
`./data`). Quota + teams persist are coalesced via a shared debounced
persister (one 250ms flush window); keys/channels persist per-mutation. Logs
and cache are intentionally restart-lost (bounded + in-memory).

## Backup / Restore

The durable store is the `data_dir/` directory (`config.store.data_dir`,
default `./data`). It holds JSON files for API keys, channels, teams, quota
counters, and (when the OAuth2 connector is enabled) connection tokens.

**Backup (cold):**
```bash
# Stop the gateway so the debounced persister flushes its final state, then
# snapshot the whole data_dir. A tarball preserves file modes + relative paths.
~/claude-home/fusion-mlx/start.sh stop    # stop the LOCAL backend (optional)
tar -czf data-backup-$(date +%Y%m%d).tar.gz -C ./data .
```

**Backup (hot, online):** the JSON files are written atomically (temp +
rename), so a plain `cp` mid-run is safe — at worst you copy a half-written
temp file, which the next load skips (unparseable files are logged + ignored,
never crash the store).

**Restore:**
```bash
# Replace the data_dir with the backup, then start the gateway. The store
# loads every JSON file on startup (keys/channels/teams/quota). Quota
# counters from the backup are the source of truth until the next debounced
# persist overwrites them with live traffic.
mkdir -p ./data
tar -xzf data-backup-YYYYMMDD.tar.gz -C ./data
./fusion-gateway --config config.yaml
```

**Config restore:** `config.yaml` is operator-managed (and gitignored — it
holds secrets). Restore it from your secret-management source (git-crypt,
1Password, Vault, etc.) — the gateway reads it at startup; a missing or
unparseable config fails fast on `config.Load`. The E2E test suite uses a
backup/restore pattern on `config.yaml` (global setup backs up, global
teardown restores) — reuse that pattern in any scripted config mutation.

## Restart Recovery

A clean restart recovers everything except restart-lost state (logs beyond
`max_entries`, cache, circuit breakers). Circuit breakers reset to `closed`
on restart, so a previously-failing backend is re-tried immediately — if it
is still failing, the breaker re-trips within a few requests (default
threshold). This is the desired behavior for a restart after an upstream
outage (you WANT to retry, not stay open).

**Graceful shutdown order** (enforced in `cmd/gateway/main.go`):
1. Stop accepting new connections + drain in-flight requests (30s window).
2. Then stop the local inference backend (fusion-mlx) — after in-flight local
   requests have drained, so they don't 502 mid-drain.
3. Then stop cluster discovery.
4. Join all background workers (model-set refresh, LoRA index, metrics sync,
   semantic-cache eviction) via `lifecycle.Worker.Stop` (cancel + wait).

**If a restart hangs:** the 30s drain window is the only block. If in-flight
requests don't drain in 30s, the gateway forces shutdown and logs a
`server shutdown error` — check the logs for a stuck stream (an upstream that
never sends DONE with no client cancel). The stream idle watchdog (180s) +
per-request deadline (`routing.stream.max_request_duration`, default 600s,
R9) bound this in normal operation.

**Crash recovery:** the binary is stateless beyond `data_dir/`. If the process
panics, `safeGo`/`lifecycle.Worker.GoRestart` recover + restart the goroutine
(H3); only an unrecovered panic in the main goroutine exits. On a hard crash
(OOM, kill -9), restart the binary — the store reloads from `data_dir/`, and
the debounced persister means at most ~2s of quota/team mutations are lost
(the RPO ceiling). Keys/channels/teams persist per-mutation, so those are
not lost even mid-debounce.

## Ops-only P1 — credential rotation (C1)

The audit flagged default/placeholder credentials in shipped `config.yaml`.
Code assist: `config.Validate` (R7) refuses known placeholder literals
(`change-me`, `fg-`, `DO-NOT-SHIP`, `default`, etc.) in `auth.master_key`,
`api_keys[*].key`, and admin passwords — so a shipped config with a
placeholder FAILS `config.Load` until rotated. This forces the issue but
cannot rotate credentials for you.

**Rotate before public release:**
- `auth.jwt_secret` — ≥32 random bytes (generate: `openssl rand -hex 32`).
- `auth.master_key` — ≥32 random bytes. Gatekeeper for admin + MCP; never
  ship the default.
- Admin passwords — bcrypt-hashed (the admin PUT handler hashes before
  persist, H8). Set via the admin dashboard or by writing a bcrypt hash to
  `config.admin.users[*].password_hash`.
- `api_keys[*].key` — generated gateway API keys. Stored hashed (SHA-256) in
  the store, never plaintext. Generate via the admin dashboard `/admin/api/keys`.
- Each backend `api_key` — per-provider (`anthropic`, `openai`, `glm52`, etc.).
  These are the upstream-cloud credentials; rotate on the vendor side + update
  `config.yaml`. See C2 for at-rest encryption.

**Never hard-delete a generated API key** — disable/revoke it instead
(disabled keys stay in the store for audit; deletion breaks referential
integrity in logs/cost analytics). Confirm with the operator before any
destructive key removal.

## Ops-only P1 — encryption.master_key (C2)

`encryption.master_key` encrypts OAuth2 connector tokens at rest
(`data/connections.json`). If it is empty AND the connector or any OAuth2
provider is enabled, tokens persist in PLAINTEXT — a data leak if
`data/connections.json` is read off-disk.

Code assist: `config.Validate` (R7) warns loudly (and can refuse) when
`encryption.master_key` is empty but the connector/OAuth2 is enabled. Backend
`api_key` values with an `enc:` prefix are AES-256-GCM decrypted at
`config.Load` (N4); plaintext keys still load with a warn (backward compat).

**Set it:**
```yaml
encryption:
  master_key: "<32+ random bytes, base64 or hex>"
```
Generate: `openssl rand -base64 32`. Rotate by setting a new key + re-persisting
the connection tokens (the gateway re-encrypts on next persist with the new
key — there is no automatic re-encrypt, so rotate when no OAuth2 flows are
mid-handshake, or accept a transient plaintext window).

## Ops-only P1 — deploy port unify (C3)

The default config + `DefaultConfig` serve on **11432**. Some deploy manifests
(Dockerfile, k8s/helm) still reference **8100** — a probe/service/port mismatch
that makes health checks hit the wrong port. Code cannot decide the deploy
target port; this is an ops config fix.

**Fix the manifests to 11432** (or set `server.port: 8100` in the configmap to
match the manifest — pick ONE and make every layer agree):
- `deploy/helm/fusion-gateway/values.yaml` — service port + container port.
- `deploy/helm/fusion-gateway/templates/*` — Service + Deployment + probes.
- `Dockerfile` — `EXPOSE` + any `HEALTHCHECK` port.
- The `config.yaml` served in the container — `server.port: 11432`.

Verify end-to-end: the gateway health endpoints (`/health`, `/healthz`,
`/readyz`, `/livez`) must answer on whatever port the Service routes to. The
`/v1/models` empty-response signal (stale binary, rebuild + restart — NOT a
port bug) is a separate diagnostic; see the CLAUDE.md "stale binary" note.

## Log rotation (N6)

The gateway logs to `stderr` via `slog` (text handler, level from
`server.log_level`, R5). It does NOT manage its own log files — rotation is
external. For a single-binary local-first deployment on macOS, use launchd
`StandardErrorPath` + `newsyslog` rotation, or pipe stderr through a rotator:

```bash
# launchd: rotate via newsyslog (macOS native). Add to /etc/newsyslog.d/fusion-gateway:
# /var/log/fusion-gateway.log 644 7 2048 * J
# (7 rotated copies, rotate at 2MiB, bzip2-compress old logs)
```

Or, if running under systemd (Linux nodes), `journalctl` handles rotation
natively — just let stderr flow to the journal. Do NOT add an in-process
lumberjack dependency: it adds weight for marginal gain on a single binary,
and external rotation is the platform's job (Rule 2 — keep lean).

The log level is set at startup from `server.log_level` (debug/info/warn/
error; unknown → info + warn). Hot-reload does NOT re-apply the log level
(stays at the startup level) — restart to change it.

## Alert triage

Prometheus alerting rules live in `deploy/observability/alerts.yaml`; the
Grafana dashboard is `deploy/observability/grafana-dashboard.json`. Each alert
below maps a fired alert to a triage action. The metric source is `/metrics`
(gated by master-key — scrape config must carry the `X-Fusion-Key` /
master-key header, or run `server.enable_metrics_noauth` if your deployment
relies on network policy instead).

### High-error-rate

`FusionGatewayHighErrorRate` (critical): a backend 5xx rate > 2% over 5m.
1. Confirm the backend: `sum(rate(fusion_gateway_requests_total{status=~"5.."}[5m])) by (backend)`.
2. If `backend=local`: check fusion-mlx health (`~/claude-home/fusion-mlx/start.sh status` + `/health`); a model OOM or crash is the usual cause. Restart fusion-mlx if down.
3. If `backend=cloud`: check the cloud vendor status page + the gateway log for upstream error bodies (capped 512 bytes, surfaced via `writeChatFailedError`).
4. If the circuit breaker is also open (see below), the diversion is working as designed — the error rate alert is then expected; resolve the upstream first.

### Circuit-breaker-open

`FusionGatewayCircuitBreakerOpen` (critical): the local breaker is OPEN for
> 2m, diverting to cloud.
1. Inspect the trip reason: `fusion_gateway_circuit_breaker_trips_total` by `reason` (memory_overload / swap_triggered / model_offline / ...).
2. `memory_overload` → see Memory pressure. `swap_triggered` → see Swap thrashing. `model_offline` → the local backend stopped serving the requested model; restart fusion-mlx or preload the model.
3. The breaker auto-recovers (`half_open` → probe → `closed`) once the underlying fault clears; do not force-reset.

### Hardware-collection-errors

`FusionGatewayHwCollectionErrors` (warning): a hardware collector source is
erroring, so hardware-aware routing (P0.5/P1/P2) may be forcing cloud.
1. Identify the source label (`gopsutil` / `iokit` / `mlx`).
2. `mlx` source → fusion-mlx `/metrics` unreachable; check it is up and `hardware.mlx_metrics_url` is correct.
3. `iokit`/`gopsutil` → a node-level collector fault; restart the gateway process (the collector re-inits on startup). This is non-fatal — the gateway stays up and routes by request-level rules until collection recovers.

### Memory-pressure

`FusionGatewayMemoryPressure` (warning): system memory used ratio > 85% for
5m.
1. Reduce resident models in fusion-mlx (unload models not in active use) to free UMA.
2. If sustained, lower `routing.local_priority.max_system_memory_ratio` so the router diverts earlier, or scale the local set down.
3. Check for a non-gateway memory consumer on the node (`top -o mem`).

### Swap-thrashing

`FusionGatewaySwapThrashing` (critical): swap page-out rate > 1000/s for 3m.
The node is thrashing; local inference latency is collapsed.
1. Immediately reduce resident models in fusion-mlx.
2. If the gateway is the cause (a burst of large contexts), confirm `routing.token_threshold` is diverting long requests to cloud.
3. Restart fusion-mlx to release fragmented UMA if the thrash persists after unloading.

### MLX-queue-depth

`FusionGatewayMLXQueueDepth` (warning): fusion-mlx inference queue > 16 for
5m. The local backend is saturating.
1. Raise `routing.local_priority.max_concurrent` if the node has headroom, OR
2. Lower `routing.token_threshold` to divert more long requests to cloud, OR
3. Add a cluster node (the gateway routes across nodes by model availability + load).

### High-p99-latency

`FusionGatewayHighP99Latency` (warning): p99 request duration > 10s for 10m.
1. Confirm it is not a legitimately long streaming response (a long-thinking model can legitimately exceed this — check the model mix).
2. Check `fusion_gateway_in_flight_requests` for slot saturation: if pinned at `max_concurrent`, the bounded pool is the bottleneck — raise the cap or divert.
3. Check for stuck slots: a connected-but-silent upstream should trip `ResponseHeaderTimeout` (30s default); if latency is BELOW 30s but still high, the upstream is slow, not stuck — divert to cloud or a faster node.

