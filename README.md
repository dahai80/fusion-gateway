# Fusion-Gateway

**English | [中文](README_CN.md)**

Unified hybrid inference gateway for Apple Silicon local inference + cloud LLMs.

Core traffic entry point for Fusion-Agent Studio, Fusion-MLX, and Fusion-Coder. Written in **Go** to avoid competing with fusion-mlx for UMA memory.

## Architecture

```
Clients (VSCode/UI/CLI/Agent)
        |
Fusion-Gateway :11432
|- Ingress Layer       -- Auth, parsing, standardization, rate limiting
|- Preprocessing       -- Tokenizer counting, prompt validation, param defaults
|- Routing Engine      -- Rule engine + hardware load sensing (core differentiator)
|- Adapter Pool        -- Unified interface for all inference backends
|- MCP Gateway         -- MCP cluster tool registry, routing, token budget
|- Stream Forwarding   -- SSE, cancel, retry, KV cache release
|- Observability       -- Logs, metrics, hot config reload
        |
Heterogeneous Inference Pool
|- Local: fusion-mlx (:11434) / llama.cpp
|- Private: vLLM-ascend / vLLM-cuda
|- Cloud: Volcengine / Qianfan / Claude / OpenAI / DeepSeek / OpenRouter / AWS Bedrock / GCP Vertex / Azure Foundry
|- Cloud (China): DashScope / Moonshot / Zhipu / Minimax / Baichuan / Hunyuan / StepFun / Yi
```

## Quick Start

```bash
# Build
go build -o fusion-gateway ./cmd/gateway

# Copy and edit config
cp config.example.yaml config.yaml

# Run (auto-starts fusion-mlx on :11434, gateway on :11432)
./fusion-gateway --config config.yaml
```

The gateway listens on **port 11432** by default. With `auto_start.enabled: true`, it automatically launches fusion-mlx on port 11434 and waits for it to become healthy before serving requests.

## Configuration

See `config.example.yaml` for full reference. Key settings:

| Key | Default | Description |
|-----|---------|-------------|
| `server.port` | 11432 | Gateway listen port |
| `server.auto_start.enabled` | true | Auto-start fusion-mlx on gateway startup |
| `server.auto_start.command` | `~/claude-home/fusion-mlx/start.sh start` | Shell command to start local backend |
| `server.auto_start.stop_cmd` | `~/claude-home/fusion-mlx/start.sh stop` | Shell command to stop local backend on shutdown |
| `server.auto_start.wait_url` | `http://127.0.0.1:11434/health` | Health URL to poll after start |
| `server.auto_start.wait_secs` | 120 | Max seconds to wait for health check |
| `auth.enabled` | true | Enable API key authentication |
| `auth.master_key` | "" | Master key bypasses rate limits and model allowlists |
| `route.token_threshold` | 8000 | Token count threshold: below = local, above = cloud |
| `route.output_input_ratio_threshold` | 0.6 | Max predicted-output/input-token ratio before routing to cloud (skipped below `output_input_ratio_min_input_tokens`) |
| `route.output_input_ratio_min_input_tokens` | 32 | Minimum input tokens for the output/input ratio check to apply; below this the ratio is skipped (avoids tiny-request misroute, #48) |
| `route.mode` | hybrid | Routing mode: `local` (all local), `cloud` (all cloud), `hybrid` (smart routing by token/ratio/hardware) |
| `route.enable_hardware_judge` | true | Enable hardware-aware routing |
| `route.local_max_memory_ratio` | 0.9 | Max system memory ratio before forcing cloud |
| `route.local_max_mlx_memory_ratio` | 0.7 | Max MLX/GPU memory ratio before forcing cloud |
| `route.circuit_breaker.failure_threshold` | 5 | Consecutive failures before circuit opens |
| `route.rate_limit.enabled` | true | Enable per-key RPM/TPM rate limiting |
| `route.retry.max_retries` | 2 | Max retry attempts for non-streaming requests |
| `route.fallback.context_window_fallback` | {} | Model → larger model mapping for context overflow |
| `route.fallback.enabled` | false | Enable `model_mapping` below (alias → cloud model id) |
| `route.fallback.model_mapping` | {} | Map client/SDK model aliases (e.g. `claude-opus-4-7`) to the cloud backend's real model id (e.g. `glm5.2`); applied before forwarding on `/v1/messages` and `/v1/chat/completions` (avoids upstream 400 → 502 "response stopped arriving", #52) |
| `cache.enabled` | true | Enable LRU response cache for non-streaming |
| `cache.backend` | local | Cache backend: local (LRU) or redis |
| `cache.redis.addr` | localhost:6379 | Redis address (when backend=redis) |
| `cache.warmup_file` | "" | JSON file to preload cache at startup |
| `cache.ttl` | 5m | Cache entry TTL |
| `cost.enabled` | true | Enable cost tracking with built-in pricing |
| `pii.enabled` | false | Enable PII detection on request content |
| `pii.action` | log | PII action: log, mask, or deny |
| `cloud_routing.strategy` | round-robin | Cloud routing: latency, cost, weight, least-busy, round-robin |
| `hardware.collect_interval` | 2s | Hardware metrics collection interval |
| `hardware.mlx_metrics.enabled` | true | Collect fusion-mlx /metrics |
| `hot_reload.enabled` | true | Enable config file hot reload |
| `hot_reload.breaker_drain_timeout` | 10s | Wait time for in-flight drain before applying config |
| `hot_reload.breaker_warmup_success` | 3 | Success count to close breaker after warmup |
| `admin.enabled` | true | Enable admin dashboard and API |
| `admin.log_max_len` | 10000 | Max request log entries (ring buffer) |
| `admin.jwt_secret` | "" | JWT signing secret for admin auth |
| `cost.pricing_file` | "" | Custom pricing YAML with hot reload |
| `observability.otel_enabled` | false | Enable OpenTelemetry tracing |
| `observability.otel_endpoint` | localhost:4317 | OTel collector endpoint |
| `observability.otel_protocol` | grpc | OTel export protocol: grpc or http |
| `observability.otel_service_name` | fusion-gateway | Service name in OTel traces |
| `rbac.enabled` | false | Enable RBAC role-based access control |
| `rbac.default_role` | viewer | Default role when no OIDC claims: admin, editor, viewer |
| `team.enabled` | false | Enable team/org management |
| `team.default_team` | default | Default team for unassigned keys |
| `semantic_cache.enabled` | false | Enable semantic cache (similarity-based hit) |
| `semantic_cache.similarity_threshold` | 0.92 | Cosine similarity threshold for cache hit |
| `semantic_cache.max_entries` | 5000 | Max entries in semantic cache |
| `prompt_injection.enabled` | false | Enable prompt injection detection |
| `prompt_injection.action` | log | Action on detection: log or block |
| `cost_markup.enabled` | false | Enable cost markup (billing margin) |
| `cost_markup.global_markup` | 0 | Global markup ratio (0.2 = 20% surcharge) |
| `batch.enabled` | false | Enable /v1/batches API |
| `batch.max_batch_size` | 100 | Max requests per batch |
| `server.tls.cert_file` | "" | TLS certificate file (enables HTTPS) |
| `server.tls.key_file` | "" | TLS private key file |
| `encryption.master_key` | "" | AES-256 master key for at-rest encryption (≥32 chars) |
| `connector.persistence_path` | data/connections.json | Connection credentials file path |
| `mcp.enabled` | false | Enable MCP cluster gateway |
| `mcp.host` | 127.0.0.1 | MCP gateway host |
| `mcp.port` | 11446 | MCP gateway port |
| `mcp.token_budget` | 10000000 | Token budget for MCP tool calls |
| `mcp.max_requests` | 10000 | Max tracked MCP requests |
| `mcp.node_port` | 11445 | Remote node port for forwarding |
| `mcp.local_port` | 9000 | Local plugin server port |

## API Endpoints

| Endpoint | Method | Description |
|----------|--------|-------------|
| `/v1/chat/completions` | POST | OpenAI-compatible chat completions (stream + non-stream) |
| `/v1/completions` | POST | Legacy completions (auto-converted to chat format) |
| `/v1/embeddings` | POST | Text embeddings (local-first, cluster sharding for batch) |
| `/v1/rerank` | POST | Rerank documents (cloud-default, local when model available) |
| `/v1/cost` | GET | Cost tracking summary (optional `?key=<name>` filter) |
| `/v1/realtime` | WebSocket | Realtime API proxy (bidirectional WebSocket relay) |
| `/v1/models` | GET | List available models (concurrent per-provider fetch, 3s timeout each, failures skipped; `route.mode: local` lists only local providers) |
| `/v1/models/{id}/load` | POST | Load model (intercepted → model-hub `POST /api/v1/models/{id}/serve`) |
| `/v1/models/{id}/unload` | POST | Unload model (intercepted → model-hub `POST /api/v1/models/{id}/serve`) |
| `/health` | GET | Full health check with backend status |
| `/healthz` | GET | Liveness probe |
| `/readyz` | GET | Readiness probe (circuit breaker + health + GPU memory + queue depth + success rate) |
| `/livez` | GET | Liveness probe |
| `/v1/status` | GET | Detailed status (hardware, circuit breakers, stats) |
| `/metrics` | GET | Prometheus metrics (requires `master_key` auth) |
| `/v1/images/generations` | POST | Image generation (cloud-only, OpenAI-compatible) |
| `/v1/messages` | POST | Anthropic Messages API (native format + auto-convert to OpenAI) |
| `/v1/audio/transcriptions` | POST | Audio transcription (Whisper-compatible, cloud-only) |
| `/v1/audio/speech` | POST | Text-to-speech synthesis (cloud-only) |
| `/v1/moderations` | POST | Content moderation (cloud-only) |
| `/admin/gc` | POST | Trigger safe GC on fusion-mlx (only when in-flight = 0) |
| `/admin/config/reload` | POST | Config reload notification |
| `/admin/teams` | GET/POST | List/create teams (admin-only) |
| `/admin/teams/{id}` | GET/PUT/DELETE | Team CRUD (admin-only) |
| `/admin/orgs` | GET/POST | List/create organizations (admin-only) |
| `/admin/orgs/{id}` | GET/DELETE | Organization CRUD (admin-only) |
| `/v1/batches` | POST/GET | Create/list batches |
| `/v1/batches/{id}` | GET | Get batch status |
| `/v1/batches/{id}/cancel` | POST | Cancel a running batch |
| `/gateway/v1/connector/list` | GET | List registered connectors and their actions |
| `/gateway/v1/connector/test` | POST | Test action execution (no real side effects) |
| `/gateway/v1/connector/{key}/action/{action}` | POST | Execute connector action |
| `/gateway/v1/connection` | GET/POST | List/create connections |
| `/gateway/v1/connection/{id}` | GET/DELETE | Get/delete connection |
| `/gateway/v1/connection/{id}/refresh` | POST | Refresh connection authorization |
| `/gateway/v1/oauth2/authorize` | POST | Generate OAuth2 authorization URL |
| `/gateway/v1/oauth2/callback` | GET | OAuth2 callback — exchange code for tokens |
| `/mcp/v1/tools` | GET | List registered MCP tools |
| `/mcp/v1/tools/register` | POST | Register an MCP tool |
| `/mcp/v1/tools/unregister` | POST | Unregister an MCP tool |
| `/mcp/v1/call` | POST | Call an MCP tool (forwarded to node) |
| `/mcp/v1/stats` | GET | MCP gateway statistics |
| `/mcp/v1/health` | GET | MCP gateway health check |
| `/api/v1/` | ANY | Model-hub reverse proxy (with module permission enforcement) |
| `/admin/api/fine-tune/*` | ANY | fusion-mlx admin fine-tune API reverse proxy (#30) — jobs CRUD, SSE progress stream, adapters; same fg-key auth chain as `/v1/*`, gateway injects `X-Fusion-Route` internally |

## Routing Logic

Dual-dimension decision: **request dimension** + **hardware load dimension**

### Header Injection

| Header | Target | Value | Behavior |
|--------|--------|-------|----------|
| `X-Fusion-Route` | fusion-mlx | `gateway-decision` | Injected on all forwarded requests; inbound `X-Fusion-Route` passed through |
| `X-Fusion-Source` | model-hub | `gateway` | Injected on all proxied requests |

Priority chain (high to low), three-tier fallback: **local → cluster → cloud**

| Priority | Rule | Condition | Target |
|----------|------|-----------|--------|
| P-1 | Semantic intent | Classifier returns heavy/diffusion with confidence ≥ threshold | Platform cluster node → cloud |
| P0 | Circuit breaker | Local breaker is Open | Try cluster → cloud |
| P0.3 | Session affinity | Same `X-Space-Id` seen before | Route to same provider (KV cache reuse) |
| P0.5 | Metrics collection error | Hardware metrics unavailable | Try cluster → cloud |
| P1 | System memory | Used ratio > threshold | Try cluster → cloud + trip breaker |
| P1.5 | MLX memory | MLX/GPU ratio > threshold | Try cluster → cloud |
| P2 | Swap thrashing | Page rate > threshold | Try cluster → cloud + trip breaker |
| P2.5 | GPU memory low | Available < 20% of allocated | Try cluster → cloud |
| P3 | Local not ready | Backend health check failed | Try cluster → cloud |
| P4 | Token budget | Input tokens > threshold | Cloud |
| P5 | Concurrent limit | In-flight > max concurrent | Try cluster → cloud |
| P6 | Model availability | Model not found locally | Context window fallback → cluster → cloud |
| P6.5 | Context window fallback | Model not local but larger variant is | Route to larger local model |
| P7 | Local priority | All checks passed | Route to local |

### Semantic Intent Routing (D4)

Gateway evolves from static forwarding toward a semantic scheduling center. A lightweight intent classifier (`fusion-router-light` 1B LoRA adapter, base `mlx-community/Llama-3.2-1B-Instruct-4bit`, trained upstream in fusion-trainer#11) inspects each chat request and dispatches by intent **before** the P0–P7 rule chain. **Disabled by default** — when off, a `NoopClassifier` makes P-1 a no-op and existing behavior is unchanged.

The classifier calls the served LoRA adapter through the local fusion-mlx `/v1/chat/completions` endpoint (using the OpenAI `adapters` field to hot-load the derived engine) with the trained classification prompt. It emits one of five task-type labels — `code` / `chat` / `math` / `translate` / `summary` — all of which are lightweight/local-capable, so each maps to `IntentLightweight`. Heavy-model and diffusion intents stay with the rule chain + platform routing. The classifier **fails open**: on any transport error, timeout, or unrecognized label, the semantic layer defers to the rule chain so routing never breaks.

| Intent | Target |
|--------|--------|
| `lightweight` | Defer to rule chain (rule chain already routes healthy short requests to Mac local) |
| `heavy_model` | Cluster node on `windows-cuda` (or configured platform) → cloud fallback |
| `diffusion` | Cluster node on `windows-cuda` (or configured platform) → cloud fallback |
| `unknown` / low-confidence / classifier error | Defer to rule chain |

> **Prerequisite:** fusion-mlx must be started with
> `FUSION_LORA_ALLOWED_DIRS=~/.fusion-mlx/adapters` preset (see fusion-mlx#394
> — the adapters-dir auto-add races the EnginePool init). Without this the
> classifier calls fail open and the rule chain handles routing as usual.

```yaml
routing:
  intent_classifier:
    enabled: false              # default off; set true once fusion-router-light is deployed
    endpoint: "http://127.0.0.1:11434"
    base_model: "mlx-community/Llama-3.2-1B-Instruct-4bit"
    adapter: "/Users/dahai/.fusion-mlx/adapters/mlx-community--Llama-3.2-1B-Instruct-4bit/router-light-1b-intent-v3"
    api_key: ""                 # fusion-mlx auth.api_key when auth enabled
    timeout: 2s
    min_confidence: 0.7

cluster:
  platform_routing:
    enabled: false              # default off
    heavy_model_platform: "windows-cuda"
    diffusion_platform: "windows-cuda"
  nodes:
    - id: "mac-1"
      address: "http://127.0.0.1:11434"
      platform: "mac"
    - id: "win-cuda-1"
      address: "http://192.168.1.50:11434"
      platform: "windows-cuda"
```

The semantic layer is hot-reload aware: toggling `intent_classifier.enabled` in config takes effect without a restart.

### Heuristic Code-Intent Fast Path (<20ms)

The LLM classifier above is a full model call (2s timeout, no cache, every request) — acceptable for a disabled-by-default semantic layer, but it dominates gateway end-to-end overhead once enabled. For the **coding** intent specifically (the hottest path in vibe-coding / refactor / debug workflows), a deterministic in-process heuristic classifier replaces the LLM call and keeps gateway overhead well under 20ms.

`HeuristicClassifier` runs **before** the LLM classifier on every chat request. It scores a request from signals that need no model inference — model name contains `code`, fenced code blocks, language keywords, file extensions, code-action verbs, error terms, and a non-nil `tools` array — and, when the score meets `min_confidence`, returns `IntentCode`. The engine then routes to `LocalBackend` (fusion-mlx) with the LoRA code adapter (e.g. `lora-code`) hot-mounted via the per-request `adapters` field (ms-scale `FUSION_LORA_INPLACE_SWAP=1` swap, no base reload). Non-code intents fall through to the LLM classifier (if enabled) then the rule chain unchanged.

Results are cached by a sha256 key (model + tools-flag + scanned text prefix) in a bounded LRU with TTL, so repeated prompts skip even the regex scan. Benchmark on Apple M5 Max: **~0.8µs/op** steady-state (cached) — ~24000× headroom under the 20ms budget.

> **Heuristic vs LLM classifier:** the heuristic is a latency lever scoped to the code intent; it does not replace the LLM classifier for heavy/diffusion/translate dispatch. Both can be enabled together — the heuristic catches code first, the LLM classifier handles the rest.

```yaml
routing:
  heuristic_classifier:
    enabled: false            # default off; the rule chain routes as usual
    code_adapter: "lora-code" # LoRA adapter name passed in the per-request "adapters" field
    cache_size: 4096          # LRU entries (0 disables cache)
    cache_ttl: 5m             # entry expiry; 0 = never expire
    min_confidence: 0.6       # score threshold to classify as code intent
    text_scan_bytes: 4096     # cap regex scan to this prefix of the request text
```

The fast path is hot-reload aware: toggling `heuristic_classifier.enabled` takes effect without a restart. The `adapters` and `response_format` fields are passed through `ChatRequest` to fusion-mlx (opaque to cloud providers), so OpenAI-style constrained decoding also reaches the local engine without gateway interpretation.

> **fusion-mlx prerequisites:** `FUSION_LORA_INPLACE_SWAP=1` for true ms-scale in-place adapter swap (else falls back to a base reload); the base model must be pre-loaded (`POST /v1/models/{id}/load`) before an adapter request, since in-place swap requires a resident base.

### Outbound UDS to Local Backend

For the local hot path, the gateway can talk to fusion-mlx over a Unix Domain Socket instead of TCP — skipping the TCP stack shaves connection-setup latency from the gateway end-to-end overhead budget. Set `socket_path` on the fusion-mlx backend and launch fusion-mlx with `--host unix:/run/fusion-mlx.sock` (supported since fusion-mlx#351). The `base_url` becomes a dummy host (convention: `http://unix/`); the transport dials the socket and ignores the host.

The same transport factory tunes the connection pool for high-QPS local traffic — `MaxIdleConnsPerHost` 64 (vs the Go default of 2, which starves a busy local backend and forces redials). This pool tuning applies even when `socket_path` is empty (plain TCP), so every local backend benefits.

```yaml
backends:
  fusion-mlx:
    type: "fusion-mlx"
    base_url: "http://unix/"          # dummy host; transport dials socket_path
    socket_path: "/run/fusion-mlx.sock"  # empty (default) = plain TCP to base_url
    enabled: true
```

> fusion-mlx must be launched with the matching `--host unix:/run/fusion-mlx.sock` and the socket file readable/writable by the gateway process. On macOS the socket path must stay under the ~104-byte `SUN_LEN` cap.

### Inbound UDS Listener

Symmetric to the outbound path, the gateway can also **listen** on a Unix Domain Socket so local clients (fusion-code, the CLI, agent loops on the same machine) reach it without crossing the TCP stack. A stale socket file from a previous unclean shutdown is removed before binding; on shutdown the listener is closed and the socket file unlinked. The TCP listener stays up alongside it (admin dashboard, health probes, remote clients), so UDS is purely an additional low-latency entry point.

```yaml
server:
  unix_socket:
    enabled: true
    path: "/var/run/fusion-gateway.sock"  # required when enabled
    mode: 0660                             # 0 (default) = 0660
```

Connect with curl:

```sh
curl --unix-socket /var/run/fusion-gateway.sock http://unix/v1/chat/completions \
  -H "Content-Type: application/json" \
  -d '{"model":"<base>","messages":[{"role":"user","content":"hello"}]}'
```

> Orthogonal to `server.auto_start` (which launches fusion-mlx) and to `backends.*.socket_path` (outbound). The three are independent: you can run any combination of TCP/UDS inbound and TCP/UDS outbound.

### LoRA Adapter Index (Stream D)

The heuristic code path dispatches to LocalBackend with a configured `code_adapter` (e.g. `lora-code`) for per-request LoRA hot-swap on fusion-mlx. To validate that the adapter actually exists on the backend, the gateway maintains an in-memory `AdapterIndex` that polls fusion-mlx's `GET /admin/api/fine-tune/adapters` endpoint (the existing admin proxy target, now consumed directly). The index is built from the same `backends.fusion-mlx` config (`base_url`, `api_key`, `socket_path`), so the fetch rides the same transport (TCP or UDS) as inference traffic.

- **Refresh**: a background goroutine (`lora_index_refresh`) fetches on startup, then every 60s (mirrors the `refresh_model_set` cadence). On config hot-reload, the index is rebuilt if the fusion-mlx backend config changed and an immediate refresh is triggered, so newly published adapters appear without a restart.
- **Validation & path resolution**: in the routing engine, when a code intent resolves an adapter, the index is checked best-effort. A missing entry logs a warning but **does not suppress** the dispatch — the index may be stale, and fusion-mlx surfaces a hot-swap error if the adapter is truly absent. When no index is configured (no fusion-mlx backend) or it has never refreshed, validation is skipped. **Path resolution**: fusion-mlx's per-request `adapters` field requires the adapter's **full filesystem path** (a bare adapter name is rejected with `AdapterPathError`), so the engine resolves the configured `code_adapter` name to its `adapter_path` via the index before injecting it into the request. When the index has no path for the name (stale or pre-path-schema snapshot), the bare name is passed through and fusion-mlx surfaces the error (best-effort, mirroring the validation semantics). `X-Route-Decision` carries the bare adapter name (`intent:code:lora:<name>`) while the request body carries the resolved full path.
- **Response schema**: fusion-mlx returns a bare JSON array of `{adapter_name, model_id, adapter_path, has_weights, has_config, lora_rank}`; decode is capped at 10 MiB (OOM hardening, consistent with the SSE linebuf caps).
- **Route header**: the index fetch carries the negotiation header (`X-Fusion-Route: gateway-decision`) so fusion-mlx's `route_guard` middleware admits the `GET /admin/api/fine-tune/adapters` request. The header name/value are pulled from `routing.negotiation` and re-applied on config hot-reload.

No new config keys: the index derives everything from the existing `backends.fusion-mlx` entry and `routing.heuristic_classifier.code_adapter`. This is the first-version index source; the long-term path (fusion-trainer publishing adapters to fusion-model-hub, gateway consuming `GET /api/v1/models?model_type=lora` via a webhook) is tracked as upstream work that does not block this version.

### Inbound Model-Hub Webhook

To pick up newly published LoRA adapters without waiting for the 60s `AdapterIndex` poll, the gateway exposes an inbound webhook receiver for fusion-model-hub lifecycle events:

```
POST /webhooks/model-hub
```

- **Authentication**: HMAC-SHA256 over the raw request body, verified against `routing.webhooks.model_hub.secret`. The sender (fusion-model-hub's `_sign_payload`) sets `X-Webhook-Signature` (hex) and `X-Webhook-Event`; the gateway re-computes the MAC in constant time and rejects mismatches with 401. This is independent of the fg-key auth chain — webhooks are not behind `withMiddleware`.
- **Envelope**: `{"event": "<type>", "data": {...}}`. Body decode is capped at 1 MiB (OOM hardening, consistent with the SSE linebuf caps).
- **Refresh trigger**: on an `adapter.*` event (e.g. `adapter.published`, `adapter.merged`), the receiver triggers an immediate `AdapterIndex` refresh (the same refresh the 60s poll runs). Non-adapter events (`model.created`, `version.published`, ...) are acknowledged (200) and logged but do not trigger a refresh. A refresh failure is logged but still returns 200 so the sender does not retry-storm.
- **Config** (under `routing.webhooks.model_hub`):

  | Key | Default | Description |
  |-----|---------|-------------|
  | `enabled` | `false` | Register the `POST /webhooks/model-hub` route. Disabled by default for backward compatibility. |
  | `secret` | `""` | Shared HMAC secret. Required when `enabled=true` (validated at load). |

- **No refresher wired**: when no fusion-mlx backend is configured, `adapter.*` events are acknowledged but the refresh is skipped (nothing to refresh); the route is still registered when enabled.

The first-version index source remains fusion-mlx `GET /admin/api/fine-tune/adapters`; the webhook is the event-driven refresh path for the long-term model-hub source. Upstream dependencies (fusion-trainer#49 publishes adapters; fusion-models-hub#22 adds `base_model_id` FK + `adapter.*` webhook events + real LoRA merge) are tracked as issues that do not block this version.

### Cluster Load Balancing

When local can't serve, gateway tries cluster nodes before cloud fallback.

| Strategy | Scoring |
|----------|---------|
| `least-connections` | Select node with fewest in-flight requests |
| `hardware-aware` | Weighted score: 60% memory availability + 30% queue factor + 10% in-flight |
| `round-robin` | Atomic counter cycling across healthy nodes |

Node health is checked periodically. After N consecutive failures (configurable), node is marked dead and excluded from selection.

Two discovery modes:

| Mode | Description |
|------|-------------|
| `standalone` | Static node list from config + local health checks |
| `master` | Sync nodes from fusion-multi-node Master (`:9753`) — gateway calls `/api/nodes` periodically |

### Batch Sharding

For large embedding requests (input count > 32), gateway automatically splits the batch into shards and dispatches them to cluster nodes in parallel. Results are merged with correct index ordering before returning. Falls back to single-provider when cluster is unavailable.

### Embedding & Rerank Routing

The router engine uses **request type** (`chat`/`embedding`/`rerank`) for fast-path routing decisions:

| Request Type | Strategy | Fallback |
|-------------|----------|----------|
| `embedding` | Local-first (skip token budget checks) | Cluster → Cloud |
| `rerank` | Cloud-default (unless local rerank model detected) | Cluster → Cloud |

- **Embedding**: Routes locally when breaker closed + local ready. For large batches (>32 inputs), uses cluster sharding automatically. Accepts `input` as either a string or an array of strings (per the OpenAI API spec).
- **Rerank**: Routes to cloud by default since local MLX typically doesn't host rerank models. If a model with "rerank"/"reranker" in the name is available locally, routes there instead.

### Realtime API

The gateway proxies OpenAI Realtime API (`/v1/realtime`) via bidirectional WebSocket relay:

1. Client connects to `ws://gateway:11432/v1/realtime`
2. Gateway upgrades to WebSocket, dials configured backend (`wss://api.openai.com/v1/realtime`)
3. Messages are relayed in both directions (client↔backend) with zero-buffer forwarding
4. Either side closing triggers cleanup of both connections

Configuration: `realtime.enabled`, `realtime.backend_url`, `realtime.api_key`.

## Hardware Metrics (Three Sources)

| Source | Metrics | Method |
|--------|---------|--------|
| gopsutil | CPU, RAM, Swap total/used | Process-level |
| IOKit (ioreg) | GPU Device/Renderer/Tiler utilization, GPU memory | `ioreg -r -d 1 -w 0 -c AGXAccelerator` |
| fusion-mlx /metrics | MLX active memory, models loaded, inference queue depth | Prometheus parser |

## SSE Backpressure

When the downstream SSE channel is full (slow client), the gateway:
1. Emits a `warning` SSE event: `event: warning\ndata: {"type":"warning","message":"stream degraded, falling back to non-streaming"}`
2. Falls back to a non-streaming `Chat()` call, collecting the full response
3. Sends the complete response as a single SSE chunk
4. Sets `X-Fusion-Degraded: true` response header

If the non-streaming fallback also fails, an `error` SSE event is emitted.

**Memory safety**: SSE lines are capped at 1 MiB per line; oversized lines are discarded. External API responses are capped at 10 MiB. This prevents OOM from malformed or adversarial streams.

## Key-Level Security & Rate Limiting

### API Key Management

Each API key supports fine-grained access control:

| Field | Description |
|-------|-------------|
| `key` | API key string |
| `name` | Human-readable key identifier |
| `rpm` | Requests per minute (0 = unlimited) |
| `tpm` | Tokens per minute (0 = unlimited) |
| `allowed_models` | Model allowlist with wildcard support (e.g., `gpt-4o*`, `*`) |
| `allowed_backends` | Backend allowlist — requests routed to non-whitelisted backends get 403. Wildcard `*` allows all |
| `expires_at` | RFC3339 expiry timestamp |
| `budget_limit` | Monthly spend limit in USD (0 = unlimited) |

Keys come from **two sources**, both honored at the auth layer:

- **Static (config.yaml)**: `auth.api_keys` entries, matched by exact key string.
- **Admin-managed (dashboard)**: Keys created via `POST /admin/api/keys`. The full `sk-<raw>` key is returned once at creation time; the gateway stores only an 8-char prefix + a SHA-256 hash (`key_hash`). Auth hashes the presented key and looks it up in the Store, so admin-generated keys authenticate identically to static ones (quotas, allowlists, and budget all apply). Non-`active` keys are rejected.

### MasterKey

The `master_key` bypasses all rate limits and model allowlists. Use for internal services only.

### Sliding Window Rate Limiting

Per-key RPM/TPM enforcement using a sliding window algorithm (no Redis dependency):
- **RPM**: Tracks request timestamps within a 1-minute window
- **TPM**: Tracks token counts within a 1-minute window
- Returns `429` with `Retry-After` and `X-RateLimit-Remaining` headers
- Master key requests bypass rate limits entirely

## Response Caching

LRU in-memory cache for non-streaming chat completions:
- Cache key: SHA256(model + messages + temperature + max_tokens + top_p)
- Configurable TTL, max entries, and max memory
- `X-Cache: HIT` / `X-Cache: MISS` response headers
- Background eviction of expired entries every 30s

## Cost Tracking

Built-in cost tracking with per-model pricing:
- **15 models** pre-configured (GPT-4/4o/3.5, Claude-3/3.5, DeepSeek, embeddings)
- Automatic cost calculation per request (prompt + completion tokens × model rate)
- `/v1/cost` endpoint for aggregated summaries (by key, backend, model)
- Per-key cost breakdown with `?key=<name>` filter
- JSON export via `Tracker.ExportJSON()`
- **Custom pricing file**: YAML-based model pricing overrides with hot reload (`cost.pricing_file`)
- **Budget blocking**: Per-key monthly spend limits (`budget_limit`) enforced before request execution

## Stream Options

Full OpenAI `stream_options` support for chat completions:
- `stream_include_usage`: Accumulates output token count during SSE streaming
- Final SSE chunk includes complete `usage` object with prompt + completion tokens
- Ensures accurate token counting and cost tracking for streaming requests

## Anthropic Messages API

`/v1/messages` supports the Anthropic Messages API natively:

- **Native path**: Requests routed to an Anthropic backend are forwarded in native format (no conversion overhead)
- **Auto-convert path**: Requests routed to non-Anthropic backends are automatically converted: AnthropicRequest → ChatRequest → ChatResponse → AnthropicResponse
- **Bidirectional conversion**: System message extraction, tool format translation (OpenAI functions ↔ Anthropic tools), content block mapping
- **Content forms**: Accepts `content` as either a plain string or an array of content blocks (per the Anthropic API spec; string is normalized to `[{type:"text",text:s}]`)
- **Streaming**: Native Anthropic SSE events (`message_start`, `content_block_delta`, `message_delta`, `message_stop`)
- **Thinking**: Supports `thinking` parameter with `budget_tokens` for extended thinking
- **Non-stream internal stream+aggregate**: A non-stream `/v1/messages` routed to a `MessagesProvider` is internally streamed upstream (`stream=true`) and aggregated into a single non-stream Anthropic response via `adapter.AggregateAnthropicStreamEvents`. Reasoning upstreams (e.g. glm5.2 behind a LiteLLM proxy) withhold non-stream response headers until full generation completes, which trips `Client.Timeout exceeded while awaiting headers` / client-cancel 502s. The stream path has a ~2s TTFB, so the gateway no longer blocks on the upstream header. Text, `thinking` (+`signature_delta`), and `tool_use` (`input_json_delta`) blocks are all reconstructed.

### Cloud-Signed Providers (AWS Bedrock / GCP Vertex / Azure Foundry)

Beyond the standard `anthropic` backend, the gateway forwards `/v1/messages` to cloud-hosted Claude endpoints that require request signing rather than a static API key. All three implement the same `MessagesProvider` path — native Anthropic format in, native Anthropic SSE out — and are selected by setting a backend's `type:` in `config.yaml`. Credentials are read **only** from gateway-side environment variables and are never echoed back to clients.

| Backend `type` | Cloud | Auth mechanism | Required env vars |
|----------------|-------|-----------------|-------------------|
| `bedrock` | AWS Bedrock | AWS SigV4 (stdlib `crypto/hmac`+`sha256`; no AWS SDK dep) | `AWS_ACCESS_KEY_ID`, `AWS_SECRET_ACCESS_KEY`, `AWS_REGION` (or `AWS_DEFAULT_REGION`), optional `AWS_SESSION_TOKEN` |
| `vertex` | Google Vertex AI | OAuth2 service-account: self-signed RS256 JWT exchanged at `token_uri` → access token (cached, 5-min pre-expiry refresh; reuses `golang-jwt/jwt/v5`) | `VERTEX_SERVICE_ACCOUNT_JSON` (inline) or `GOOGLE_APPLICATION_CREDENTIALS` (path); `VERTEX_PROJECT_ID` (or `GOOGLE_CLOUD_PROJECT`); `VERTEX_REGION` (or `GOOGLE_CLOUD_REGION`) |
| `foundry` | Azure AI Foundry | `api-key` header OR `Authorization: Bearer` (Entra token) | `AZURE_API_KEY` (or `AZURE_OPENAI_API_KEY`) or `AZURE_ACCESS_TOKEN` |

- **URLs**: Bedrock `{base_url}/model/{model-encoded}/invoke` (`/invoke-with-response-stream` for streaming; `:` in model ids is `%3A`-encoded); Vertex `{base_url}/v1/projects/{project}/locations/{region}/publishers/anthropic/models/{model}:rawPredict`; Foundry `{base_url}/v1/messages` with `anthropic-version: 2023-06-01`.
- **Error transparency**: Non-2xx upstream responses surface to the client with the original status code, body, and `x-request-id`/`request-id` (via `*MessagesHTTPError`), never a generic 502.
- **SSE hardening**: 1 MiB/line and 10 MiB response caps apply, consistent with the rest of the adapter pool.

## Audio & Moderation

- `/v1/audio/transcriptions`: Multipart form upload, delegates to provider's `Transcription()` method (cloud-only)
- `/v1/audio/speech`: JSON body, returns audio binary stream from provider's `Speech()` method (cloud-only)
- `/v1/moderations`: JSON body, delegates to provider's `Moderation()` method (cloud-only)

These endpoints use interface assertion — providers that don't implement the method return a clear error.

## OpenTelemetry Tracing

Optional distributed tracing via OTel (disabled by default):

```yaml
observability:
    otel_enabled: true
    otel_endpoint: "localhost:4317"
    otel_protocol: "grpc"       # grpc | http
    otel_service_name: "fusion-gateway"
```

- Automatic HTTP span creation on every request via `HTTPMiddleware`
- 10% sampling rate by default (`TraceIDRatioBased(0.1)`)
- Graceful shutdown flushes pending spans
- Propagates trace context via W3C TraceContext headers

## RBAC & Team Management

Role-based access control with three roles: **admin** (full access), **editor** (read + write), **viewer** (read-only). Mutations (POST/PUT/PATCH/DELETE) are blocked for viewers.

```yaml
rbac:
    enabled: true
    default_role: "viewer"    # fallback when no OIDC claims
team:
    enabled: true
    default_team: "default"   # team assigned when no OIDC team claim
```

- OIDC claims `role`, `groups`, `team` automatically mapped to RBAC roles
- Master API key always gets admin role
- Team cost aggregation via `/admin/teams` — quota tracking per team
- Key-to-team binding: `BindKeyToTeam(apiKey, teamID)` in TeamsStore

## Semantic Cache

Similarity-based caching using cosine similarity on prompt embeddings. Avoids re-computing identical or near-identical requests.

```yaml
semantic_cache:
    enabled: true
    similarity_threshold: 0.92   # cosine similarity for cache hit
    max_entries: 5000
```

- Requires `EmbedFunc` to be set — disabled when no embedding function provided (no pseudo-embedding fallback)
- Pluggable `EmbedFunc` — swap to any embedding API for similarity
- 30-minute TTL, automatic expiry eviction

## Prompt Injection Detection

Regex-based detection of common injection patterns with configurable action.

```yaml
prompt_injection:
    enabled: true
    action: "log"    # log | block
```

- 14 built-in patterns (ignore previous, jailbreak, system prompt leak, etc.)
- Severity scoring: medium (1-2 matches), high (3+ matches)
- Block mode returns HTTP 400 with `content_filter` error type

## Cost Markup

Billing margin layer — apply markup on top of base cost per-key or globally.

```yaml
cost_markup:
    enabled: true
    global_markup: 0.2    # 20% surcharge on all requests
```

- `SetKeyMarkup(keyName, markup)` for per-key overrides
- `applyMarkup()` in Tracker automatically applies before recording
- `base_cost` vs `billed_cost` separation in logs

## Batch API

OpenAI-compatible `/v1/batches` endpoint for asynchronous bulk processing.

```yaml
batch:
    enabled: true
    max_batch_size: 100
```

## Connector Plugin Framework

Unified SaaS connector framework for third-party API integration (QuickBooks, Google Workspace, HubSpot, etc.).

### Architecture

- **Registry**: In-memory connector registry with plugin-style registration
- **Connection Manager**: OAuth2 / Static API Key / Basic Auth credential storage
- **Action Execution**: Unified `POST /gateway/v1/connector/{key}/action/{action}` interface
- **Audit Logging**: Every external API call logged with timestamp, permission level, input summary (for write actions)
- **Test Mode**: `POST /gateway/v1/connector/test` executes actions without real side effects
- **Persistence**: JSON file-based credential storage with atomic writes (tmp + rename)
- **Encryption**: AES-256-GCM at-rest encryption for OAuth2 tokens (configurable master key)

### Built-in Connectors (V1.0)

| Connector | Auth Type | Actions |
|-----------|-----------|---------|
| QuickBooks | OAuth2 | query_overdue_invoice, list_customers, create_invoice, get_company_info |
| Google Workspace | OAuth2 | list_users, get_user, list_calendar_events, send_email, read_drive_file |
| HubSpot | OAuth2 | list_contacts, get_contact, create_contact, list_deals, update_deal |

All connectors make real HTTP API calls to their respective SaaS endpoints. Google Workspace supports automatic token refresh on 401 responses.

### HTTPS / TLS

Optional HTTPS termination for single-binary deployment:

```yaml
server:
    tls:
        cert_file: "certs/server.crt"
        key_file: "certs/server.key"
```

When configured, the gateway uses `http.ListenAndServeTLS` instead of plain HTTP.

### AES-256-GCM Encryption

At-rest encryption for OAuth2 tokens and connection credentials:

```yaml
encryption:
    master_key: "your-32-character-minimum-secret-key"
```

- AES-256-GCM with per-entry random nonce
- Base64-encoded storage (nonce + ciphertext)
- Master key must be ≥32 characters
- When empty/disabled, tokens stored as plaintext
- Applied automatically to OAuth2 access/refresh tokens via `tokenCipher` interface
- Encryption failures are treated as hard errors — tokens are never silently stored as plaintext

### OAuth2 Authorization Flow

Full OAuth2 Authorization Code Flow support:

```
POST /gateway/v1/oauth2/authorize   →  Generate authorization URL
GET  /gateway/v1/oauth2/callback    →  Exchange code for tokens
```

Flow:
1. Client calls `/oauth2/authorize` with `connectorKey` and optional `state`
2. Gateway generates a cryptographic random `state` if not provided, stores it server-side with 10-minute TTL
3. Gateway returns the SaaS provider's authorization URL with the state parameter
4. User authorizes in browser, provider redirects to `/oauth2/callback`
5. Gateway validates the `state` parameter matches a previously-issued one (CSRF protection)
6. Gateway exchanges authorization code for access/refresh tokens
7. Tokens are encrypted (if master_key configured) and stored as a Connection

### Credential Persistence

Connections are persisted to a JSON file with atomic writes:

```yaml
connector:
    persistence_path: "data/connections.json"
```

- Atomic write: write to `.tmp` file then `os.Rename` for crash safety
- Auto-loaded on startup, auto-saved on Create/Delete/Refresh mutations
- Encrypted tokens are stored as-is; decryption happens at read time
- Directory permissions set to 0700 (owner-only access)

### Standard Error Codes

| Code | Meaning |
|------|---------|
| 1001 | Auth expired — refresh required |
| 1002 | Third-party rate limited |
| 1003 | Permission denied |
| 1004 | Resource not found |
| 1005 | Request timeout |
| 1006 | Auth failed — invalid or missing credentials |
| 1007 | External API error — upstream request failed |
| 2001 | Parameter validation failed |

### Session Affinity (Cowork Spaces)

When `X-Space-Id` header is present, the gateway maintains session affinity — routing requests from the same collaboration space to the same inference backend. This enables KV cache reuse for shared context scenarios.

- TTL-based affinity map (default 30 min, auto-eviction)
- Affinity breaks gracefully: if the target backend is unavailable, re-routes and updates mapping

- `POST /v1/batches` — create batch, returns immediately, processes in background
- `GET /v1/batches/{id}` — check status (pending/running/completed/failed/cancelled)
- `POST /v1/batches/{id}/cancel` — cancel a running batch
- Pluggable `ProcessFn` for custom batch processing logic

## Kubernetes & Helm Deployment

### Infrastructure Resources

| Resource | Manifest | Helm | Description |
|----------|----------|------|-------------|
| Deployment | ✅ | ✅ | 2 replicas, topology spread, probes |
| Service | ✅ | ✅ | ClusterIP :11432 |
| ConfigMap | ✅ | ✅ | Non-sensitive config |
| Secret | ✅ | ✅ | master_key, api_keys |
| ServiceAccount + RBAC | ✅ | ✅ | Config reader role |
| HPA | ✅ | ✅ | CPU 70% / Memory 80%, 2–10 replicas |
| PDB | ✅ | ✅ | minAvailable: 1 |
| Ingress | ✅ | ✅ | Nginx + TLS (cert-manager optional) |
| NetworkPolicy | ✅ | ✅ | Deny-all + allow intra-namespace |
| Namespace | ✅ | — | Dedicated namespace |

### Directory Structure

```
deploy/
├── Dockerfile                         # Multi-stage build (Go builder + Alpine runtime)
├── kubernetes/
│   ├── namespace.yaml                 # Namespace: fusion-gateway
│   ├── serviceaccount.yaml            # SA + Role + RoleBinding
│   ├── secret.yaml                    # Sensitive keys (master_key, api_keys)
│   ├── deployment.yaml                # 2 replicas, topology spread, SA, secretRef
│   ├── service.yaml                   # ClusterIP :11432
│   ├── configmap.yaml                 # Non-sensitive config
│   ├── hpa.yaml                       # HPA: CPU 70% / Memory 80%
│   ├── pdb.yaml                       # PDB: minAvailable 1
│   ├── ingress.yaml                   # Nginx Ingress + TLS
│   └── networkpolicy.yaml             # Deny-all + allow intra-namespace
├── helm/fusion-gateway/
│   ├── Chart.yaml                     # Helm chart v0.6.2
│   ├── values.yaml                    # Full values with HPA/PDB/Ingress/SA/Secret
│   └── templates/                     # All resource templates
└── terraform/
    ├── versions.tf                    # Terraform >= 1.5, providers
    ├── variables.tf                   # Common variables
    ├── outputs.tf                     # Endpoint outputs
    ├── aws/                           # AWS EKS (VPC + EKS + IRSA)
    │   ├── main.tf                    # VPC module
    │   ├── eks.tf                     # EKS module + K8s/Helm providers
    │   ├── irsa.tf                    # IAM Role for ServiceAccount
    │   ├── variables.tf
    │   └── outputs.tf
    ├── gcp/                           # GCP GKE Autopilot
    │   ├── main.tf                    # GKE Autopilot module + providers
    │   ├── variables.tf
    │   └── outputs.tf
    └── modules/helm-release/          # Reusable Helm release module
        ├── main.tf
        ├── variables.tf
        └── outputs.tf
```

### Quick Deploy

```bash
# Raw K8s manifests
kubectl apply -f deploy/kubernetes/

# Helm (minimal)
helm install fusion-gateway deploy/helm/fusion-gateway/

# Helm (with HPA, Ingress, PDB enabled)
helm install fusion-gateway deploy/helm/fusion-gateway/ \
  --set hpa.enabled=true \
  --set pdb.enabled=true \
  --set ingress.enabled=true \
  --set ingress.hosts[0].host=gw.example.com \
  --set secrets.master_key=your-secure-key
```

### Terraform Deployment

```bash
# AWS EKS
cd deploy/terraform/aws
terraform init
terraform apply -var="master_key=your-secure-key"

# GCP GKE Autopilot
cd deploy/terraform/gcp
terraform init
terraform apply -var="project_id=your-project" -var="master_key=your-secure-key"
```

### HPA Configuration

| Parameter | Default | Description |
|-----------|---------|-------------|
| `hpa.enabled` | false | Enable HorizontalPodAutoscaler |
| `hpa.minReplicas` | 2 | Minimum replica count |
| `hpa.maxReplicas` | 10 | Maximum replica count |
| `hpa.targetCPUUtilizationPercentage` | 70 | CPU threshold for scale-up |
| `hpa.targetMemoryUtilizationPercentage` | 80 | Memory threshold for scale-up |

For custom metrics (QPS, latency), add Prometheus Adapter and extend HPA with `metrics` blocks.

### PDB Configuration

| Parameter | Default | Description |
|-----------|---------|-------------|
| `pdb.enabled` | false | Enable PodDisruptionBudget |
| `pdb.minAvailable` | 1 | Minimum available pods during disruption |

### Ingress Configuration

| Parameter | Default | Description |
|-----------|---------|-------------|
| `ingress.enabled` | false | Enable Ingress |
| `ingress.className` | nginx | Ingress class |
| `ingress.tls` | [] | TLS configuration |

Add cert-manager annotation for automatic TLS: `cert-manager.io/cluster-issuer: letsencrypt-prod`

## Admin Dashboard

Built-in web admin dashboard at `/admin`, served from the single binary via Go `embed`.

**Authentication**: JWT (HS256) with HttpOnly cookie session. Login via `POST /admin/api/login`.

**Admin API** (`/admin/api/*`):

| Endpoint | Method | Description |
|----------|--------|-------------|
| `/admin/api/login` | POST | Login with admin credentials, get JWT cookie |
| `/admin/api/keys` | GET/POST | List / Create API keys |
| `/admin/api/keys/:name` | GET/PUT/DELETE | Read / Update / Delete API key |
| `/admin/api/channels` | GET/POST | List / Create channels |
| `/admin/api/channels/:name` | GET/PUT/DELETE | Read / Update / Delete channel |
| `/admin/api/logs` | GET | Query request logs with filters |
| `/admin/api/logs/:id` | GET | Get single request log |
| `/admin/api/analytics/tokens` | GET | Token usage statistics |
| `/admin/api/analytics/cost` | GET | Cost statistics |
| `/admin/api/analytics/models` | GET | Model distribution statistics |
| `/admin/api/analytics/latency` | GET | Latency statistics |
| `/admin/api/analytics/errors` | GET | Error statistics |
| `/admin/api/dashboard/overview` | GET | Dashboard overview (QPS, tokens, cost, local hit rate) |
| `/admin/api/quota/:key` | GET/PUT | Get / Set key quota usage |
| `/admin/api/config/:domain` | GET/PUT | Read / Update config domain (server, auth, rate-limit, retry, negotiation, cache, cost, cost-markup, pii, cloud-routing, hardware, tokenizer, observability, cors, hot-reload, cluster, realtime, admin, oidc, rbac, semantic-cache, prompt-injection, batch, store, validation) |

**Request Log Pipeline**: Every request is automatically logged with full metadata:
- Request ID, model, channel, route reason, token counts, cost, latency, TTFT
- Ring buffer storage with configurable max length
- Filterable by time range, key, model, channel, status, token/cost thresholds
- Exportable to JSON

**Frontend**: React + Ant Design + Vite SPA embedded in Go binary.

### GUI Configuration

All gateway configuration can be managed through the admin dashboard — no manual YAML editing required. Each config domain has a dedicated page with GET/PUT API endpoints for reading and updating settings live.

**Config API** (`/admin/api/config/*`):

| Endpoint | Method | Description |
|----------|--------|-------------|
| `/admin/api/config/server` | GET/PUT | Server settings (port, read/write timeout) |
| `/admin/api/config/auth` | GET/PUT | Auth settings (enabled, master_key, api_keys) |
| `/admin/api/config/rate-limit` | GET/PUT | Rate limiting (RPM/TPM per key) |
| `/admin/api/config/retry` | GET/PUT | Retry & backoff settings |
| `/admin/api/config/negotiation` | GET/PUT | Routing negotiation rules |
| `/admin/api/config/cache` | GET/PUT | Cache settings (backend, TTL, Redis) |
| `/admin/api/config/cost` | GET/PUT | Cost tracking & pricing |
| `/admin/api/config/cost-markup` | GET/PUT | Cost markup / billing margin |
| `/admin/api/config/pii` | GET/PUT | PII detection patterns & action |
| `/admin/api/config/cloud-routing` | GET/PUT | Cloud routing strategy & weights |
| `/admin/api/config/hardware` | GET/PUT | Hardware metrics collection |
| `/admin/api/config/tokenizer` | GET/PUT | Tokenizer engine settings |
| `/admin/api/config/observability` | GET/PUT | Logging, OTel, audit |
| `/admin/api/config/cors` | GET/PUT | CORS allowed origins/methods |
| `/admin/api/config/hot-reload` | GET/PUT | Hot-reload & drain settings |
| `/admin/api/config/cluster` | GET/PUT | Cluster discovery & load balancing |
| `/admin/api/config/realtime` | GET/PUT | Realtime API proxy |
| `/admin/api/config/admin` | GET/PUT | Admin panel settings (JWT, users) |
| `/admin/api/config/oidc` | GET/PUT | OIDC identity provider |
| `/admin/api/config/rbac` | GET/PUT | RBAC role mappings |
| `/admin/api/config/semantic-cache` | GET/PUT | Semantic cache settings |
| `/admin/api/config/prompt-injection` | GET/PUT | Prompt injection detection |
| `/admin/api/config/batch` | GET/PUT | Batch API settings |
| `/admin/api/config/store` | GET/PUT | Store backend (memory/Redis) |
| `/admin/api/config/validation` | GET/PUT | Request validation rules |

**Behavior**:
- GET returns current in-memory config with sensitive fields masked (`****`)
- PUT accepts partial updates — only provided fields are changed
- Empty string on sensitive fields (e.g. `api_key: ""`) means "keep current value"
- Updates are written to the YAML config file on disk, then picked up by hot-reload

## Retry & Backoff

Exponential backoff retry for non-streaming requests:
- Configurable `max_retries`, `initial_backoff`, `max_backoff`
- Default retryable status codes: 429, 500, 502, 503
- Connection refused / timeout errors are also retryable
- Respects context cancellation between retry attempts

## PII Detection

Regex-based PII scanning on request text content:

| Built-in Pattern | Description |
|-----------------|-------------|
| `email` | Email addresses |
| `phone_cn` | Chinese mobile numbers |
| `phone_us` | US phone numbers |
| `credit_card` | Credit card numbers |
| `ssn` | US Social Security numbers |
| `ip_v4` | IPv4 addresses |

Three actions:
- `log` (default): Log detection, allow request
- `mask`: Log detection with masking intent, allow request
- `deny`: Block request with 400 error listing detected PII types

Custom patterns supported via `pii.patterns` config.

## Cloud Routing Strategies

When multiple cloud backends are available, select a strategy via `cloud_routing.strategy`:

| Strategy | Description |
|----------|-------------|
| `round-robin` | Cycle through backends sequentially (default) |
| `latency` | Route to backend with lowest P95 latency |
| `cost` | Route to cheapest backend (DeepSeek < Qianfan/Volcengine < OpenAI/Anthropic) |
| `weight` | Weighted random selection via `cloud_weights` config |
| `least-busy` | Route to backend with fewest tracked samples |

Latency tracking uses quickselect algorithm for efficient P95 calculation (configurable window size, default 1000 samples).

## Config Hot-Update (Drain/Apply/Warmup)

On config file change, the gateway performs a three-phase reload:

1. **Drain**: Wait up to `breaker_drain_timeout` for in-flight requests to complete
2. **Apply**: Rebuild circuit breakers with new config, update routing rules, rebuild provider pool
3. **Warmup**: Set local circuit breaker to `half_open` — allows limited test requests before fully closing

Config changes are also audited when `observability.config_audit_log: true` — field-level diffs (old/new values) are logged and appended to `observability.config_audit_file` in JSONL format.

## Admin Endpoints

| Endpoint | Method | Description |
|----------|--------|-------------|
| `/admin/gc` | POST | Trigger GC on fusion-mlx. Queues if in-flight > 0 (returns 202). Executes immediately if idle (returns 200). |
| `/admin/config/reload` | POST | Trigger config reload |
| `/debug/pprof/` | GET | Go pprof profiling index (requires `enable_pprof` + `master_key`) |
| `/debug/pprof/profile` | GET | CPU profile (requires `enable_pprof` + `master_key`) |
| `/debug/pprof/trace` | GET | Execution trace (requires `enable_pprof` + `master_key`) |

## Safe GC

The gateway manages fusion-mlx KV cache GC safely:

- **Manual**: `POST /admin/gc` — triggers immediately if idle, queues for execution when idle if in-flight > 0
- **Idle timer**: Automatically triggers GC when `inFlightCounter == 0` for `min_idle_since_last_gc` duration (default 5min)
- **Cancel-driven**: After request cancellation, GC may trigger if idle long enough since last GC

## Benchmark

```bash
go test -bench=. -benchmem ./internal/router/
```

Typical results on Apple M5 Max:
- Local short path: ~148 ns/op
- Cloud long path: ~34 ns/op
- Parallel: ~386 ns/op

## Development

```bash
# Run all tests
go test ./... -v

# Run specific package
go test ./internal/router/... -v

# Run with coverage
go test ./... -cover -timeout 180s

# Lint
golangci-lint run

# Build
go build -o fusion-gateway ./cmd/gateway
```

### End-to-End Tests (Admin Dashboard)

The admin dashboard SPA has a Playwright E2E suite under `tests/e2e/`. It
covers every admin page — login, API keys, channels, dashboard/analytics/logs,
and all 26 config sections — exercising both the UI (buttons, inputs, forms)
and the runtime coupling (admin-side config changes must take effect in the
live gateway via hot-reload).

**49 tests** across 5 spec files:

| Spec | Covers |
|------|--------|
| `01-login.spec.js` | login form, wrong/right creds, `admin_token` cookie, route guard redirect |
| `02-keys.spec.js` | keys table, create via UI, runtime lifecycle (key works on `/v1/models` → delete → 401/403), empty-name 400, PUT edit persistence, id==name |
| `03-channels.spec.js` | channels table, create via UI form (name/type/base_url/priority/weight), UI delete, empty-name 400, PUT edit persistence |
| `04-dashboard-analytics.spec.js` | dashboard, analytics overview + profit, logs + export endpoints |
| `05-config.spec.js` | all 26 config sections GET (renders) + PUT (persists) + hot-reload round-trip; CORS preflight echo; per-key rate-limit 429; unauthenticated PUT rejected |

```bash
# Prerequisites: gateway running on :11432 with admin enabled
./fusion-gateway --config config.yaml

# Install Playwright (one-time)
cd tests/e2e && npm install

# Run all E2E tests (chromium, serial)
npx playwright test

# Run a single spec
npx playwright test tests/02-keys.spec.js

# Headed mode (watch the browser)
npx playwright test --headed

# View last run report
npx playwright show-report
```

The suite backs up `config.yaml` once before the run (via `global-setup.js`)
and restores it once after (`global-teardown.js`), so the mutating config-PUT
tests never leave the live gateway in a changed state. The user's running
keys and config are untouched.

### Test Coverage

All packages maintain ≥90% test coverage:

| Package | Coverage |
|---------|----------|
| `cmd/gateway` | 90.7% |
| `internal/adapter` | 90.5% |
| `internal/admin` | 94.6% |
| `internal/admin/ui` | 90.0% |
| `internal/batch` | 100% |
| `internal/cache` | 99.0% |
| `internal/cluster` | 97.1% |
| `internal/config` | 91.7% |
| `internal/connector` | 100% |
| `internal/cost` | 94.3% |
| `internal/hardware` | 90.0% |
| `internal/middleware` | 97.4% |
| `internal/observability` | 98.3% |
| `internal/realtime` | 96.1% |
| `internal/router` | 93.2% |
| `internal/safego` | 100% |
| `internal/server` | 90.7% |
| `internal/store/memory` | 92.4% |
| `internal/store/redis` | 92.3% |
| `internal/tokenizer` | 100% |

## Project Structure

```
cmd/gateway/          Entry point
internal/
  config/             Config loading, versioned snapshots, hot reload, audit log
  hardware/           Hardware metrics collector (gopsutil + IOKit + MLX)
  tokenizer/          Token counting + budget estimation + calibration
  router/             Routing decision engine + per-backend circuit breaker + cloud strategy + latency tracker
  adapter/            Provider interface + fusion-mlx + openai-compatible adapters + pool
  cluster/            Cluster node discovery, health check, load balancing, node adapter
  middleware/         Auth (MasterKey + key expiry + model allowlist + budget blocking), Rate limiting (RPM/TPM), PII detection, Retry, Request logging
  cache/              LRU in-memory cache with TTL for non-streaming responses
  cost/               Cost tracking with built-in model pricing table + custom pricing hot reload
  store/              Store interface (logs, keys, channels, analytics, dashboard, quota)
  store/memory/       In-memory store implementation (ring buffer logs, CRUD, analytics, teams/orgs)
batch/              Batch API store + async processing
  admin/              Admin API handlers + JWT auth + login
  admin/ui/           go:embed frontend assets (React SPA)
  observability/      Prometheus metrics + OpenTelemetry tracing
  server/             HTTP server + route registration + SSE forwarding + stream options
web/admin/            Admin dashboard frontend (React + Ant Design + Vite)
config.example.yaml   Example configuration
```

## Admin Dashboard Pages

| Page | Description |
|------|-------------|
| Dashboard | Real-time overview: QPS, token usage, cost, local hit rate, route distribution |
| API Keys | CRUD + quota management + budget limits + per-key usage analytics |
| Channels | Backend provider management + health check + connectivity test |
| Request Logs | Full request logs with routing reason, token counts, cost, latency |
| Analytics | Token usage trends, cost tracking, model distribution, latency/error stats |
| Configuration | 24 config pages for all gateway settings (Server, Auth, Rate Limit, Retry, Cache, Cost, PII, Cloud Routing, Hardware, Tokenizer, Observability, CORS, Hot Reload, Cluster, Realtime, Admin, OIDC, RBAC, Semantic Cache, Prompt Injection, Batch, Store, Validation, Cost Markup, Negotiation) |

**Differentiator**: The only AI gateway with **hardware-aware routing visualization** and **local inference savings tracking**.

## Audit Fixes

### v0.8.16 — Anthropic /v1/messages path now applies model alias mapping (#52)

| # | Fix | Details |
|---|-----|---------|
| 1 | **`/v1/messages` forwards mapped model, not raw alias** (#52) | claude code (Anthropic SDK) sends model aliases like `claude-opus-4-7` to the gateway. The `/v1/chat/completions` path already applied `routing.fallback.model_mapping`, but `/v1/messages` resolved its cloud provider via `resolveCloudProvider(decision, nil, w)` — the `nil` request meant the model-mapping branch was skipped, so the alias was forwarded raw to the cloud backend (glm52 / LiteLLM), which rejected it: `400: Invalid model name passed in model=claude-opus-4-7` → gateway 502 → claude code surfaced `API Error: The response stopped arriving`. |
| 2 | **Extracted `applyCloudModelMapping` helper** | The mapping logic lived inline inside `resolveCloudProvider` and mutated `*ChatRequest`, so it could not serve the anthropic path (`*AnthropicRequest`). Extracted into `Server.applyCloudModelMapping(model, cloudBackend) string` (returns the mapped id or the input unchanged when disabled/missed) and reused from both paths — no behavior change on chat, new behavior on messages. |
| 3 | **Config: `routing.fallback.enabled` + `model_mapping`** | Set `routing.fallback.enabled: true` and `model_mapping: { claude-opus-4-7: glm5.2 }` (added to `config.yaml` and documented in `config.example.yaml`). This gates the alias→backend-id translation. The map is generic — add any SDK alias → real model id pairs. |
| 4 | **Tests** | 2 new regression tests: `TestAnthropicMessages_ModelMappingApplied` (alias `claude-opus-4-7` → upstream sees `glm5.2`), `TestAnthropicMessages_ModelMappingDisabled` (`enabled:false` → raw alias passes through). Live-verified: `/v1/messages` with `claude-opus-4-7` now streams `200` from glm52 with `model:glm5.2`; log shows `model mapped for cloud routing local_model=claude-opus-4-7 cloud_model=glm5.2`. 2548 tests green; `go vet` clean. |

### v0.8.15 — Tiny requests no longer misrouted to cloud by output/input ratio (#48)

| # | Fix | Details |
|---|-----|---------|
| 1 | **Output/input ratio skipped for tiny requests** (#48) | A 4-token prompt ("say pong") with `max_tokens:5` produced `predict_output/input = 5/4 = 1.25 > 0.6`, so the `output_input_ratio_exceeded` rule (P4.5) misrouted the local-eligible request to a cloud backend (glm52) that did not recognize the `Qwen3.5-9B-4bit` model name → 400 → gateway 502. The ratio is statistically meaningless at very low input counts. P4.5 now skips the ratio check when `input_tokens < output_input_ratio_min_input_tokens` (default 32, config `routing.output_input_ratio_min_input_tokens`) and falls through to P6 model-availability → P7 local. |
| 2 | **Configurable input-token floor** | New `routing.output_input_ratio_min_input_tokens` (int, default 32). A zero/negative unset value falls back to the built-in default so existing `config.yaml` files gain the fix with no change. Validated non-negative in `config.Validate`. |
| 3 | **Skip log for traceability** | When the ratio check is skipped due to the input floor, the engine emits `output/input ratio skipped: input tokens below floor` with `input_tokens`, `min_input_tokens`, `predict_output_tokens`, so tiny-request routing is diagnosable. |
| 4 | **Tests** | 3 new regression tests: `TestDecide_OutputInputRatioSkippedForTinyInput` (4 input → local, not `output_input_ratio_exceeded`), `TestDecide_OutputInputRatioSkippedForTinyInput_ExplicitFloor` (explicit floor 64 → 50 input skips ratio), `TestValidate_OutputInputRatioMinInputTokensNegative` (negative floor rejected). Existing `TestDecide_OutputInputRatioThreshold` (100 input, above floor) still routes to cloud unchanged. 585 router/config/admin tests green; `go vet` clean. |

### v0.8.14 — Fix "Content block not found": content_block index=0 + duplicate/malformed message_stop (#46)

| # | Fix | Details |
|---|-----|---------|
| 1 | **content_block index=0 no longer dropped** (#46) | `AnthropicStreamEvent.Index` tag `json:"index,omitempty"` dropped `index:0` on marshal — the first content block (always the `thinking` block on reasoning models like glm5.2) was emitted with no `index` field, so the Anthropic SDK could not match `content_block_delta`/`_stop` events to an open block and threw `Content block not found`. Added a custom `MarshalJSON` (alias-marshal to avoid recursion) that forces an explicit `"index"` (even 0) only on block-scoped events (`content_block_start`/`_delta`/`_stop`); message-scoped events (`message_start`/`message_delta`/`message_stop`) still carry no index per the Anthropic SSE spec. |
| 2 | **No more duplicate / malformed message_stop** (#46) | `handleStreamAnthropicMessages` unconditionally appended `event: message_stop\ndata: {}` after the upstream channel closed, so when the upstream already sent a real `message_stop` the client received a **second** one (and it was malformed — `data:{}` with no `type`). Now tracks `sawMessageStop` and only synthesizes a well-formed `{"type":"message_stop"}` when the upstream omitted one. |
| 3 | **Client cancel no longer synthesizes a closing event** (#46) | On `ctx.Err() != nil` (client canceled mid-stream, e.g. long 4m+ thinking), the upstream goroutine closes the channel early with content blocks possibly still OPEN. The old synthetic `message_stop` then handed the SDK an unmatched block (`Content block not found`). Now suppresses any synthetic terminal event on cancellation — the client already gave up. |
| 4 | **Tests** | 4 new regression tests: `TestAnthropicStreamEvent_MarshalIndexZeroNotOmitted` (index 0 present on block events, absent on message events), `TestHandleStreamAnthropicMessages_NoDuplicateMessageStop`, `_SynthesizesMissingMessageStop`, `_ClientCancelSuppressesMessageStop`. Live-verified gateway→LiteLLM stream: 1 `message_stop`, 0 malformed `data:{}`, 0 SDK block-pairing errors. `go vet` clean. |

### v0.8.13 — Stream body no longer truncated by backend timeout (#44)

| # | Fix | Details |
|---|-----|---------|
| 1 | **Stream timeout-less client** (#44) | `AnthropicProvider` added a `streamHTTPClient` with `Timeout: 0` (body streams unbounded until the upstream closes it) and `Transport.ResponseHeaderTimeout = backend timeout` (a dead upstream still fails fast on headers/connect). `StreamChat` and `StreamMessages` now use it. Previously the single `httpClient` had `http.Client.Timeout = 120s`, which caps the **full request including body read**, force-closing long reasoning streams at 120s with `context deadline exceeded (... while reading body)` and truncating the non-stream aggregate path (#42) too. |
| 2 | **Non-stream unchanged** | Non-stream `Messages`/`Chat` keep the bounded `httpClient` so a hung non-stream upstream is still capped by the backend timeout. |
| 3 | **Tests** | New `TestAnthropicProvider_StreamMessagesNotTruncatedByClientTimeout`: a slow SSE upstream waits 600 ms (≫ the 300 ms test backend timeout) before its final event and asserts `message_stop` is reached. 2539 tests pass across 23 packages; `go vet` clean. |

### v0.8.12 — Non-stream /v1/messages internal stream+aggregate (#42)

| # | Fix | Details |
|---|-----|---------|
| 1 | **Non-stream internal stream+aggregate** (#42) | A non-stream `/v1/messages` routed to a `MessagesProvider` is now internally streamed upstream (`stream=true`) and aggregated into a single non-stream `AnthropicResponse` via new `adapter.AggregateAnthropicStreamEvents`. Reasoning upstreams (e.g. glm5.2 behind a LiteLLM proxy) withhold non-stream response headers until full generation completes (6-14s+), tripping `Client.Timeout exceeded while awaiting headers` / client-cancel 502s and a Claude Code retry storm. The stream path TTFB is ~2s, so the gateway no longer blocks on the upstream header. |
| 2 | **Full block reconstruction** | `AggregateAnthropicStreamEvents` reconstructs `text` (`text_delta`), `thinking` (`thinking_delta` + `signature_delta`), and `tool_use` (`input_json_delta` partial-json accumulation) content blocks; defaults `stop_reason` to `end_turn` when the stream ends without one; surfaces upstream `error` events. |
| 3 | **Tests** | 5 new `TestAggregateAnthropicStreamEvents_*` (text, thinking+signature, tool_use, error event, empty-default end_turn); 2538 tests green across 23 packages; `go vet` clean. |

### v0.8.11 — Cloud-Signed Providers: AWS Bedrock / GCP Vertex / Azure Foundry (#40)

| # | Fix | Details |
|---|-----|---------|
| 1 | **AWS Bedrock provider** (#40) | New `bedrock` backend type forwards `/v1/messages` to AWS Bedrock with AWS SigV4 request signing built on stdlib `crypto/hmac`+`crypto/sha256` (no AWS SDK dependency). Reads `AWS_ACCESS_KEY_ID`/`AWS_SECRET_ACCESS_KEY`/`AWS_REGION`/`AWS_SESSION_TOKEN` from gateway env. Streams via the Bedrock `invoke-with-response-stream` event-stream (unwraps `{"payload":{...}}` to native Anthropic events). Model id `:` is `%3A`-encoded on the path. |
| 2 | **Google Vertex AI provider** (#40) | New `vertex` backend type forwards `/v1/messages` to Vertex AI `:rawPredict` with an OAuth2 service-account flow: self-signed RS256 JWT (via the existing `golang-jwt/jwt/v5` dep) exchanged at the SA `token_uri` for an access token, cached with 5-min pre-expiry refresh. Reads `VERTEX_SERVICE_ACCOUNT_JSON`/`GOOGLE_APPLICATION_CREDENTIALS`/`VERTEX_PROJECT_ID`/`VERTEX_REGION` from gateway env. |
| 3 | **Azure AI Foundry provider** (#40) | New `foundry` backend type forwards `/v1/messages` to Azure AI Foundry with either an `api-key` header (`AZURE_API_KEY`/`AZURE_OPENAI_API_KEY`) or an Entra `Authorization: Bearer` token (`AZURE_ACCESS_TOKEN`). Sets `anthropic-version: 2023-06-01`. |
| 4 | **MessagesProvider dispatch** | `/v1/messages` handler now dispatches on the `MessagesProvider` interface (implemented by Anthropic + Bedrock + Vertex + Foundry) instead of the concrete `*AnthropicProvider`, so all four backends share one native-format path. OpenAI conversion fallback for non-Anthropic backends is unchanged. |
| 5 | **Error transparency** | Non-2xx upstream responses from the three new providers surface to the client with the original status code, body, and `x-request-id`/`request-id` (via `*MessagesHTTPError`) — no generic 502. |
| 6 | **Tests** | 16 new provider tests (SigV4 header structure, OAuth2 token exchange + cache, api-key/bearer auth, Bedrock event-stream unwrapping, native SSE passthrough, error passthrough, missing-creds) on top of shared-helper coverage; 2533 tests green across 23 packages; `go vet` clean. |

### v0.8.10 — Demo Key MLX VL Model Coverage (#37)

| # | Fix | Details |
|---|-----|---------|
| 1 | **Demo key covers MLX VL models** (#37) | The shipped demo key (`config.example.yaml`) gained `mlx-community--*` in `allowed_models`, so MLX community models with the `mlx-community--` prefix — including VL models like `mlx-community--Qwen2.5-VL-7B-Instruct-4bit` used by Computer Use — no longer 403 under the default demo key. Pure config fix; works with the existing case-insensitive suffix-wildcard matcher. |

### v0.8.9 — Case-Insensitive Model Allowlist, Collector Panic Guard (#32, #33)

| # | Fix | Details |
|---|-----|---------|
| 1 | **Case-insensitive model allowlist** (#32) | `CheckModelAllowlist` now matches both exact names and prefix wildcards (`qwen*`) case-insensitively. The shipped demo key's `allowed_models: ["qwen*"]` now matches actual fusion-mlx model names like `Qwen3.5-9B-4bit` instead of 403-ing. (Note: `qwen*` does not cover `mlx-community--` prefixed models — see v0.8.10 / #37.) Backward compatible — lowercase config globs still match lowercase model names. |
| 2 | **Hardware collector panic guard** (#33) | `Collector.Start` defaults `collect_interval` to 5s when it is missing or non-positive, instead of panicking in `time.NewTicker(0)`. Prevents a startup crash when `hardware.collect_interval` is absent from `config.yaml`. |

### v0.8.1 — Empty-Model Default Resolution (#28)

| # | Fix | Details |
|---|-----|---------|
| 1 | **Empty-model backfill** | `/v1/chat/completions` with an empty/missing `model` no longer 404s against fusion-mlx. The model is backfilled from `routing.default_model`, or auto-discovered from the first loaded local model (`/v1/models` of local providers) when no default is configured. Auto-discovery queries local providers only, so a slow/unreachable cloud backend never blocks (mirrors the #21 fix). |

### v0.7.4 — Header Injection, AllowedBackends, Model Interception

| # | Fix | Details |
|---|-----|---------|
| 1 | **X-Fusion-Route header** | Gateway injects `X-Fusion-Route: gateway-decision` on all requests forwarded to fusion-mlx. Inbound `X-Fusion-Route` headers pass through unchanged. |
| 2 | **X-Fusion-Source header** | Gateway injects `X-Fusion-Source: gateway` on all requests proxied to model-hub. |
| 3 | **AllowedBackends enforcement** | API keys with `allowed_backends` configured are restricted to those backends only. Requests routed to a non-whitelisted backend receive 403. Wildcard `"*"` allows all. |
| 4 | **Model load/unload interception** | `/v1/models/{id}/load` and `/v1/models/{id}/unload` are intercepted and redirected to model-hub `POST /api/v1/models/{id}/serve`. |

### v0.6.2 — Architecture & Robustness

v0.6.2 addresses architecture, concurrency, and maintainability findings from the full audit:

| # | Fix | Details |
|---|-----|---------|
| 1 | **Unified Principal auth model** (硬伤1) | Three separate auth context systems (APIKey, OIDC, RBAC) unified into a single `Principal` struct with one context key. `EnsurePrincipal` pattern — lazy-create on first middleware access, subsequent middlewares populate fields. All accessor functions maintain backward-compatible signatures. |
| 2 | **Cache runtime config update** (硬伤2) | Cache `UpdateConfig()` method for hot-reload of TTL, maxEntries, maxBytes without restart. |
| 3 | **Middleware chain built once** (M1) | Middleware chain constructed once at startup as `func(http.Handler) http.Handler` composition, rebuilt on reload — eliminates per-request re-composition overhead and closure variable capture bugs. |
| 4 | **safeGo panic recovery** (M2) | All background goroutines (cache eviction, hardware collection, rate limiter cleanup, model refresh) use `safego.Go()` with `recover()` + structured logging — prevents silent goroutine crashes. |
| 5 | **Unknown backend fail-fast** (M3) | `BuildProviders` returns error on unknown backend type instead of silent `slog.Warn + continue`. Prevents misconfigured backends from being silently skipped. |
| 6 | **Multimodal prompt injection** (L7) | Prompt injection detection extracts text from `image_url`, `input_audio`, `image` multimodal content objects and handles `[]interface{}` prompt fields. |
| 7 | **Per-key mutex for rate limiter** (L2) | `sync.Map` with per-key mutex eliminates global lock contention under high-concurrency rate limiting. |
| 8 | **P95 result caching** (P3) | LatencyTracker caches P95 result with TTL — avoids re-computing quickselect on every request when window hasn't changed. |
| 9 | **Runtime backend switch fallback** (A4) | Local backend failure triggers cloud fallback at runtime — resilient degradation without manual intervention. |

#### Remaining (P2 — Architectural)

| # | Finding | Scope |
|---|---------|-------|
| A1 | Global singleton → dependency injection | oidcProvider, jwtSecret — requires DI framework or wire-up refactor |
| A2 | Server god object splitting | Break server.go into per-domain files |
| A3 | Persistent storage (Redis/Postgres) | Replace in-memory stores for multi-instance deployments |

### v0.6.1 — Security & Correctness

v0.6.1 addresses security and correctness issues identified in a full audit:

| # | Fix | Details |
|---|-----|---------|
| 1 | Admin auth requires JWT secret + users map | `admin.jwt_secret` (min 32 chars) and `admin.users` map are now required when `admin.enabled=true`. Hard-coded credentials removed. |
| 2 | Cluster shared_token required | Cluster nodes must authenticate via `cluster.shared_token`. Unauthenticated node requests are rejected. |
| 3 | Semantic cache disabled without embedding function | No more deterministic hash-based pseudo-embedding fallback. Semantic cache is skipped when no `EmbedFunc` is configured, preventing false similarity matches. |
| 4 | Config reload has rollback semantics | Hot-reload handlers run before the config snapshot is committed. If any handler fails, the old config is retained — no partial application. |
| 5 | /metrics requires master_key | The `/metrics` endpoint now enforces `master_key` authentication. Unauthenticated access returns 401. |
| 6 | pprof disabled by default | `/debug/pprof/*` endpoints are off unless `enable_pprof: true` is set in config. Even when enabled, `master_key` auth is required. |
| 7 | Cache.Get uses RLock | Fast-path cache reads use `sync.RWMutex` RLock instead of full Lock, eliminating read contention under high QPS. |
| 8 | Admin password validation | Admin passwords must be at least 8 characters. Shorter passwords are rejected at login and in config validation. |
| 9 | Unknown backend types skip registration | Backends with unrecognized `type` are logged as warnings and skipped during pool initialization, rather than causing a crash. |
| 10 | Empty prompts route to local | Requests with empty prompt content route to local backend instead of consuming cloud quota. |
| 11 | Batch.Get returns deep copy | `Batch.Get()` returns a deep copy of the batch record to prevent data races when concurrent goroutines access the same entry. |
| 12 | Multimodal content in injection detection | Prompt injection detection now properly extracts text from multimodal content arrays (image+text), not just plain string prompts. |

### Breaking Changes (v0.6.1)

- `admin.jwt_secret` is now **required** (min 32 chars) when admin is enabled
- `admin.users` map is now **required** when admin is enabled — the hard-coded `admin/admin` credential is removed
- `/metrics` endpoint requires `master_key` query parameter or header
- `/debug/pprof/*` requires explicit `enable_pprof: true` in config
- Semantic cache will not activate unless an `EmbedFunc` is provided

## Troubleshooting

### `/v1/models` returns `{"data":[]}` (#29)

The gateway fans out `ListModels` to every local provider concurrently and skips any provider that errors or times out (3s per provider). If the list comes back empty, a local backend rejected the call. The most common cause is fusion-mlx's `route_guard` rejecting the gateway's internal `/v1/models` request with HTTP 403.

Diagnostics:
- The skip is logged at **Warn** (`list models failed for provider, skipping`) with the provider name and error; the 403 error now includes the response body so the route_guard reason is visible.
- Confirm the running **binary** includes the route-header fix (#26, commit `42951e8a`) — a stale binary omits `X-Fusion-Route` and gets 403. Rebuild: `go build -o fusion-gateway ./cmd/gateway`.
- Confirm `config.yaml` sets `backends.fusion-mlx.api_key` (the fusion-mlx auth key) and `negotiation.route_header: X-Fusion-Route` / `route_header_value: gateway-decision`.
- Direct check the backend itself: `curl -H "X-Fusion-Route: gateway-decision" -H "Authorization: Bearer <mlx_key>" http://127.0.0.1:11434/v1/models`.

Clients do **not** send `X-Fusion-Route`; the gateway injects it on all forwarded requests, so chat and list-models share one auth chain.

### `/admin/api/fine-tune/*` returns SPA HTML (#30)

If a fine-tune request returns the admin dashboard HTML instead of JSON, the route is falling through to the `/admin/` SPA catch-all — meaning the `/admin/api/fine-tune/` proxy route is not registered. This happens on a **stale binary** predating the #30 fix (commit `<this release>`). Rebuild: `go build -o fusion-gateway ./cmd/gateway`.

The proxy forwards `/admin/api/fine-tune/*` to fusion-mlx `:11434` 1:1 (method/path/query/body/SSE preserved) and injects `Authorization` + `X-Fusion-Route` internally; clients authenticate to the gateway with their fg-key (same chain as `/v1/*`) and send nothing extra.

Known upstream limitation: `GET /admin/api/fine-tune/jobs/models` returns 404 (`Job not found: models`) because fusion-mlx's `/jobs/{id}` route shadows the static `/jobs/models` path — see fusion-mlx#397. This is a fusion-mlx routing bug, not a gateway issue; the gateway forwards the request correctly (confirmed: the same 404 occurs on a direct `:11434` call).

## Fusion Ecosystem

| Project | Role |
|---------|------|
| fusion-mlx | Local MLX inference engine (primary local backend) |
| fusion-gateway | **This project** - Inference routing gateway |
| fusion-desk | Desktop automation platform |
| fusion-studio | macOS native SwiftUI client |
| fusion-model-hub | Model repository and management |
