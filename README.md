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
| `/v1/models` | GET | List available models (concurrent per-provider fetch, 3s timeout each, failures skipped; `route.mode: local` lists only local providers). Each model carries a `loaded` flag — `true` only when the model id is in fusion-mlx's resident `loaded_models` set, so downstream consumers can distinguish "registered" from "servable" (#59). |
| `/v1/models/{id}/load` | POST | Load model (intercepted → model-hub `POST /api/v1/models/{id}/serve`) |
| `/v1/models/{id}/unload` | POST | Unload model (intercepted → model-hub `POST /api/v1/models/{id}/serve`) |
| `/health` | GET | Full health check with backend status. `backends.fusion-mlx` carries `{healthy, model_loaded, loaded_models}` from the authoritative fusion-mlx `/health` endpoint; a live process with no model loaded is reported as `degraded` (not a false green, #59). |
| `/healthz` | GET | Liveness probe |
| `/readyz` | GET | Readiness probe (circuit breaker + health + GPU memory + queue depth + success rate). Adds a fusion-mlx `model_loaded` check — when no model is resident the local path reports `not_ready`/`degraded` with `local_reasons:["model_not_loaded"]` even if the process responds 200 (#59). |
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

#### Store persistence (admin-generated keys/channels/teams survive restart)

By default the memory store is non-persistent — admin-generated keys, channels, and teams are lost on every process restart. Set `store.data_dir` to enable disk persistence (v0.8.21, #65):

```yaml
store:
    backend: memory
    data_dir: ~/.fusion-gateway/data   # empty = non-persistent (backward compatible)
```

- **Scope**: admin CRUD on API keys, channels, teams/orgs, and key↔team bindings are flushed to JSON files (`keys.json`, `channels.json`, `teams.json`) on each mutation, and reloaded on boot. **Excluded**: `AddCost` (per-request high-frequency usage/billing) and logs/analytics (ring-buffer, regenerable from logs) — persisting these would write disk on every inference request.
- **`~` expansion**: `~/x` resolves against the user home dir (`os.UserHomeDir`); absolute paths pass through. Insensitive to the process working directory, so launchd-managed deployments work.
- **Atomic writes**: each file is written via temp-file + `fsync` + `rename`, so a crash mid-write cannot corrupt `keys.json` (critical for credentials). Files are mode `0o600`, the data dir `0o700`.
- **Corruption tolerance**: a malformed JSON file is logged and skipped — that sub-store starts empty rather than crashing the gateway.
- **Redis backend**: not applicable (Redis provides its own persistence); `data_dir` is ignored when `backend: redis`.
- Static `auth.api_keys` are unaffected — they are read directly from the config snapshot, never from the Store.

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

### Stream Keepalive & Idle Watchdog (issue #69)

Distinct from the connection-phase retry above (which covers TTFB / pre-header failures), the **mid-stream stall** is a different failure: the stream connection is already established (headers flushed, partial deltas already sent to the client) but the upstream then stops pushing deltas without closing the TCP connection — the gateway blocks indefinitely and the client eventually times out with `The response stopped arriving. The response above may be incomplete.` Two mechanisms, configured under `routing.stream`, work together:

| Field | Default | Purpose |
|-------|---------|---------|
| `keepalive_interval` | `15s` | Emits an Anthropic-native `event: ping\ndata: {"type":"ping"}` SSE frame every interval. The Anthropic SDK ignores pings, but the client keeps seeing bytes — slow-but-live reasoning (e.g. glm5.2 extended thinking) is no longer mistaken for a dead stream. `0` disables keepalive and falls back to the original pure-blocking forward loop (backward compat). |
| `idle_timeout` | `180s` | Watchdog: if no upstream event arrives for this long, the gateway cancels the upstream read (via a child context on the provider's `body.Read`, unblocked through Go's transport context watcher) and synthesizes a clean terminal `message_stop`. The client receives a short-but-complete response it can retry, instead of hanging for 16+ minutes. `0` disables the watchdog. |

Both apply to the `/v1/messages` **stream** path (keepalive + watchdog) and the **non-stream internal-stream** path (watchdog only — a non-stream client waits for one JSON body, no SSE to keepalive). The watchdog trip vs a real client disconnect are distinguished by the parent request context: a watchdog trip (parent ctx still alive) synthesizes `message_stop`; a real client cancel (parent ctx canceled) suppresses the synthetic event, preserving the issue #46 fix and avoiding SDK `Content block not found` errors.

The keepalive timer doubles as the watchdog check granularity (at most one interval of latency). Connection-phase TTFB is **not** covered here — it remains governed by `ResponseHeaderTimeout` (120s) plus the v0.8.22 connection-phase retry.

On the non-stream aggregate path, a real client cancel is now treated as a silent INFO, not an ERROR + 502 (issue #94): the handler checks `ctx.Err() != nil` before writing any error and returns without logging ERROR or flushing a 502 body to the already-abandoned pipe — consistent with the stream-path `end_reason=client_canceled` semantics (#46/#90). A watchdog trip (parent ctx still alive) still errors normally.

#### Open-block finalization (issue #71)

When the stream ends without a terminal `message_stop` — because the upstream truncated (litellm/glm5.2 dropped the connection mid-block) or the idle watchdog cancelled a stalled stream — the gateway synthesizes a closing sequence. The Anthropic SDK requires every `content_block_start` to be closed by a matching `content_block_stop` **before** the terminal `message_stop`; an open block at stream end throws `API Error: Content block not found`. The gateway now tracks open content-block indices across both forward loops (`content_block_start` adds, `content_block_stop` removes) and, on the synthetic path, emits a `content_block_stop` for each still-open index in ascending order, then a `message_delta` carrying `stop_reason: end_turn`, then the terminal `message_stop`. This is defense-in-depth: the gateway closes whatever the upstream left open so the client never sees an unmatched block. The client-cancel suppression path (#46) is unchanged — a real disconnect suppresses all synthesis because the client already gave up.

#### Connection-phase transport-reset retry (issue #73)

The connection-phase retry (v0.8.22, #67) retries `StreamMessages` failures that occur **before** any response header is written to the client — TTFB timeouts, 502/503/429. The retryable-error matcher (`isRetryableError`) matched status-code substrings plus `connection refused` / `timeout` / `deadline exceeded`, but **not** transport-level TCP resets: when the upstream (litellm→glm5.2) drops the connection during the connection phase, Go surfaces it as `io.EOF` or `connection reset by peer`, and the error string reads `...Post "...": EOF`. This matched nothing → skipped retry → direct 502 to the client → claude code surfaced it and required a manual `continue`. Over 08/20–08/23 the gateway logged 25 such EOFs (predating the #71 fix; verified non-gateway by live cloud-path probing). `isRetryableError` now also matches `EOF` (covers `io.EOF` and `unexpected EOF`), `connection reset by peer` (Go net-package TCP-RST string), and `use of closed network connection` (idle keep-alive connection closed by the peer mid-pool — v0.8.31, #85), so a connection-phase upstream reset transparently retries up to `routing.retry.max_retries` (7) with backoff, instead of a hard 502. Mid-stream EOF is **not** affected: once a channel is returned the headers are committed and `RetryStreamMessages` only ever sees the pre-channel connection-phase error, so an in-flight `EOF` closes the SSE channel and runs the open-block finalization (#71) — it is never re-dispatched. No config change; the existing retry knobs take effect immediately.

#### Open-block finalization on upstream message_stop (issue #75)

The #71 finalization closed open content blocks only when the upstream ended the stream **without** a terminal `message_stop` (truncation, or the idle watchdog cancelling a stalled stream) — it was gated behind `if !sawMessageStop`. A second, intermittent upstream failure mode exists: litellm→glm5.2 emits `content_block_start` for one or more blocks and then sends `message_stop` **without** the matching `content_block_stop`(s) — a *malformed terminal*. The forward loop set `sawMessageStop=true` and forwarded the upstream `message_stop` verbatim; the open-block closing never ran (it was gated behind `!sawMessageStop`), so the Anthropic SDK saw an unmatched block at `message_stop` and threw `API Error: Content block not found` — the same #71 symptom, from a different upstream trigger. The forward loops now intercept the upstream `message_stop`: if any block is still open (`len(openBlocks) > 0`), the gateway synthesizes a `content_block_stop` per open index (ascending) **before** forwarding the upstream `message_stop`. A shared `closeOpenBlocks` helper serves both forward loops and the post-loop synth path, so open blocks are closed regardless of whether the upstream truncated, sent a malformed terminal, or never sent a terminal at all. The upstream `message_stop` is still forwarded as-is (no duplicate synth).

#### Client-cancel open-block closure (issue #90)

#71 (truncation) and #75 (malformed terminal) closed open content blocks on the two upstream-driven end paths, but a **third** path left blocks open: the client cancel. The original #46 suppression treated `ctx.Err() != nil` (client cancel) as equivalent to a dead socket and returned immediately, skipping all terminal events. That was correct only when the write pipe had already broken (`writeFailed==true`, the #79 case — the client is gone, synthesizing to a dead pipe is pointless). But a client cancel is usually a *request-level signal* — Claude Code timing out or retrying at a layer above — while the write pipe stays alive and the client keeps draining its buffer to finalize. On those streams any `content_block_start` left OPEN never got its `content_block_stop`, so the Anthropic SDK held an open block it could not finalize and threw `API Error: Content block not found` — the same symptom as #71/#75, from the cancel path. The #81 stream-summary field (working once #88 fixed its shadow) proved it: 12 recurring `client_canceled` streams (14:25–14:34) all had `last_event_idle` 8–139 ms (live streams, not stalled) and `last_event_type` `content_block_delta` (a block OPEN). The cancel branch now distinguishes the two cases: `ctx.Err() != nil && writeFailed==false` (cancel, pipe alive) calls `closeOpenBlocks()` to emit a `content_block_stop` per open index, then synthesizes `message_delta`(`stop_reason:max_tokens` per #77 truncation semantics) + `message_stop`, so the SDK finalizes cleanly; `ctx.Err() != nil && writeFailed==true` (pipe broke) stays the #79 behavior — no synth to a dead pipe. `end_reason` stays `client_canceled` (the loop already set it on the first cancel tick); the cancel is still a cancel, it just now closes its open blocks. This completes the open-block lifecycle: every `content_block_start` now gets a `content_block_stop` whether the stream ended by truncation (#71), malformed terminal (#75), client cancel (#90), or clean upstream `message_stop`.

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

OpenAI-compatible `/v1/batches` endpoint (submissions accepted and validated; background execution NOT implemented — POST returns 501 until a worker is wired).

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

- `POST /v1/batches` — validates + accepts submission, returns 501 Not Implemented (no background worker yet)
- `GET /v1/batches/{id}` — check status (pending/running/completed/failed/cancelled)
- `POST /v1/batches/{id}/cancel` — cancel a running batch

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

Exponential backoff retry for upstream connection-phase failures:
- Configurable `max_retries`, `initial_backoff`, `max_backoff` (under `routing.retry`)
- Default retryable status codes: 429, 500, 502, 503
- Connection refused / timeout / deadline-exceeded errors are also retryable
- Respects context cancellation between retry attempts

Applies to both `/v1/chat/completions` (non-stream) and `/v1/messages` (stream + non-stream). On `/v1/messages`, retry wraps the `StreamMessages` **connection phase** — when the upstream returns a TTFB timeout / 502 / 503 / 429 before any SSE header is written to the client. Once the stream opens (200 headers committed) and an event channel is returned, mid-stream disconnects are NOT retried (SSE is already flushing); the existing synthetic `message_stop` finalization handles that case instead.

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
| `/admin/config/reload` | POST | Trigger deterministic config reload. Returns `{"status":"reloaded","version":N}`. fsnotify file-watch is unreliable on macOS, so this endpoint is the reliable reload path (issue #57). |
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
batch/              Batch API store (submission+list+cancel; no execution worker)
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

### v0.8.50 — Non-blocking audit SHOULD/NICE (S2/S3/N1-N5)

Clears the 7 non-blocking SHOULD/NICE items from the product audit (`audit/fusion-gateway-audit-result-product-0828.md`). The verdict was upgraded Conditional-Go → Go after the v0.8.49 M1/M2/S1 blockers shipped; these are hardening/correctness sweeps that close the remaining SHOULD/NICE tier so the audit is fully remediated at the code layer.

| # | Fix | Details |
|---|-----|---------|
| 1 | **S2 (SHOULD) — MCP tool counter data race** | `MCPTool.callCount` (`int64`) was incremented in `HandleToolCall` outside `toolsMu` and read by `ToolCallCount` under `RLock` → data race under `-race`. Same race on `MCPClusterGateway.totalTokenCount`. Both moved to `atomic.Int64` (`Add`/`Load`/`Store`); the budget gate reads `Load()`. Test sites updated to `Store()`. |
| 2 | **S3 (SHOULD) — dead log-rotation config surface** | `log_rotation_max_size`/`log_rotation_max_backups` were declared, parsed, and admin-editable but had **no consumer** (`setupLogging` writes stderr only). Marked deprecated in `config.go` with a guard comment (fields retained for YAML backward-compat = graceful ignore, not surfaced as editable until a consumer exists). |
| 3 | **N1 (NICE) — `allowedNodes` unsynced write** | `SetAllowedNodes` wrote the map unlocked while `forwardToNode` reads it per-request → concurrent map read/write. Added `allowedNodesMu sync.RWMutex` guarding write (`SetAllowedNodes`) + read (`forwardToNode` RLock). |
| 4 | **N2 (NICE) — MCP `forwardToNode` bare `&http.Client{}`** | Constructed a bare `&http.Client{Timeout}` per request → no `MaxConnsPerHost` cap (RR11 FD-exhaustion gap) and no `ResponseHeaderTimeout`/`DialContext` (R5 wedged-slot gap). Routed through `httpx.TransportForBackend` (cloned transport, default cap 16, R5 timeouts); the per-request client reuses that transport. |
| 5 | **N3 (NICE) — MCP `requests` map eviction picked a random entry** | The cap-reached eviction comment said "oldest" but `for k := range g.requests; break` deletes a RANDOM entry (Go map iteration is randomized). Replaced with a min-`CreatedAt` search (skipping the just-inserted `requestID`), `delete` the oldest, and an `slog.Info` eviction log. Strengthened `TestHandleToolCall_MaxRequestsEviction` to assert the oldest (tool1) is evicted and the two newest (tool2, tool3) survive — the prior test only checked the count cap and would pass under the random-eviction bug. |
| 6 | **N4 (NICE) — PII scan scope gap** | Only `handleChatCompletions` scanned for PII; `/v1/completions` (legacy) and `/v1/messages` (Anthropic, the Claude Code path) were unscanned. Extracted a shared `scanPIIOrDeny(w, textContent, model, endpoint)` helper and wired it into all three handlers with an endpoint label in the log. Tests: `TestCompletions_PIIDeny` + `TestAnthropicMessages_PIIDeny` (both assert 400 on PII-bearing body). |
| 7 | **N5 (NICE) — PII `ScanText` first-match-only** | `ScanText` used `FindStringIndex` (first match) on validator patterns: a leading false positive the validator rejected (e.g. `999.1.1.1` for ipv4, octet >255) masked a real match later in the text → false negative. Switched to `FindAllStringIndex` iterating every candidate, appending the name on the first validator-accepting one. Test: `TestPIIMiddleware_ScanText_ValidatorSecondCandidate` scans `bad 999.1.1.1 then real 10.0.0.1` and expects ipv4 detected. |
| 8 | **helm lockstep 0.8.48→0.8.50** | `deploy/helm/fusion-gateway/Chart.yaml` `version` + `appVersion` bumped to 0.8.50 (had lagged at 0.8.48 past the v0.8.49 M1/M2/S1 release). R14 lockstep restored. |

**Verify**: `check_bare_goroutines.sh` OK; `go vet ./...` clean; `go build ./...` OK; `go test ./... -count=1 -race` all packages green (incl. mcp, middleware, server, config).

### v0.8.49 — Enterprise release-tag integrity (M1/M2/S1)

Closes the three MUST items blocking the commercial release tag (audit `audit/fusion-gateway-audit-result-product-0828.md`, verdict Conditional-Go). No new features — honesty, release hygiene, and a stream-truncation fix on advertised cloud vendors.

| # | Fix | Details |
|---|-----|---------|
| 1 | **M1 — `/v1/batches` honesty (501)** | The endpoint silently accepted submissions, returned 200, and **never executed** them: commit `32a1217` deleted the `internal/batch` worker (`go s.process(b)` + `ProcessFn`) and rewired to a store that persists `status=pending` forever. A commercial release must not advertise execution it cannot perform. `handleBatches` now returns **501 Not Implemented** after validating the body (malformed input still gets a precise 400); GET list + per-item CRUD stay (harmless reads on an empty store). README corrected: async-processing claim, `ProcessFn` bullet, `internal/batch 100%` table row, and the dir-layout line all updated to truth. Tests: `TestBatches_Create_RejectedNotImplemented` + `TestBatches_CreateError` (501) green. |
| 2 | **M2 — helm chart lockstep 0.8.46→0.8.48** | `deploy/helm/fusion-gateway/Chart.yaml` `version` + `appVersion` were stuck at 0.8.46 while HEAD = v0.8.48 (R14 lockstep violated by ~2 releases). Bumped to 0.8.48 so the chart matches the binary. |
| 3 | **S1 — R3 dual-client on 6 cloud providers** | 6 providers (base_openai + openrouter/bedrock/foundry/vertex/volcengine) used ONE `httpClient{Timeout:120s}` for BOTH stream + non-stream → vendor long-reasoning streams (>120s) were truncated, since the gateway advertises exactly these vendors this was commercial-scope MUST. Replicated the proven dual-client pattern (separate `streamHTTPClient{Timeout:0}` + cloned transport preserving `ResponseHeaderTimeout` + RR11 `MaxConnsPerHost` cap) via a shared helper `cloneStreamTransportForBackend` (`internal/adapter/transport.go`). base_openai covers the 10 Bearer vendor shims (deepseek/moonshot/baichuan/dashscope/hunyuan/minimax/zhipu/qianfan/stepfun/yi) in one edit; vertex threads a `stream bool` flag through the shared `doRawPredict`. The 3 already-fixed refs (openai_compatible/fusion_mlx/anthropic) stay inline (working + tested). Tests: `TestR3_StreamClientUnboundedTimeout` extended with 6 subtests (stream client Timeout==0, non-stream >0, stream transport `*http.Transport` with `ResponseHeaderTimeout==cfg.Timeout` + RR11 cap). |

**Verify**: `check_bare_goroutines.sh` OK; `go vet ./...` clean; `go build ./...` OK; `go test ./... -count=1 -race` all packages green.

### v0.8.48 — Open issues #128/#129: cluster coordination + guard/PII SSOT

Resolves the two open issues against v0.8.47. No open PRs. The only non-`main` branch (`fix/audit-hre-remediation-e8`) was a **stale duplicate** — all 23 of its files already on `main` (PR #127 + 5 follow-ups), content diff vs `main` = -2624/+368 (merging would **regress**). Deleted, not merged. `main` already holds every branch commit, so "merge all branches to main" is satisfied by deleting the stale duplicate.

**#129 — cluster coordination** (3 gaps, gateway-side code + docs):

| # | Fix | Details |
|---|-----|---------|
| 1 | **B1 — shared-port queue startup WARN (Gap 1)** | When `routing.mode==local` AND `routing.local_priority.queue_enabled==false`, `config.WarnSharedPortSafety()` (called from `cmd/gateway/main.go` after Load) emits an `slog.Warn` with the multi-client opt-in guidance (`queue_enabled: true`). Single-node default stays OFF (queue on a lone local node adds latency for no safety gain — Rule 2). Advisory not blocking; silent in `hybrid`/`cloud` mode or when the queue is already on. Tests: `internal/config/shared_port_safety_test.go` (5 cases — fires/silent across all mode×queue combos). |
| 2 | **B2 — OTel traceparent outbound inject (Gap 2)** | The request-ID correlation hop was already complete (`X-Request-ID` minted + echoed + stamped onto ctx by `middleware.RequestID`, re-stamped on every outbound request by `InjectFusionHeaders`). The real gap: `observability.HTTPMiddleware` Extracts inbound trace context + starts a span on the handler ctx, but **nothing Injected it outbound** — the distributed trace chain broke at every gateway→fusion-mlx / gateway→cloud hop. Fix: one `otel.GetTextMapPropagator().Inject(ctx, propagation.HeaderCarrier(req.Header))` inside `InjectFusionHeaders` (`internal/adapter/provider.go`), guarded behind `span.IsRecording()`. No-op when OTel disabled (no span on ctx). Lands the traceparent on all 30+ outbound sites for free. Tests: `internal/adapter/r12b_otel_inject_test.go` (3 cases — recording span emits traceparent, bare ctx no-op, empty ctx no panic). |
| 3 | **B3 — managed-MCP per-node tool allowlist (Gap 3)** | NET-NEW. Redis store has no pub/sub (no PUBLISH/SUBSCRIBE anywhere), so building a distributed-config-subsystem for a single consumer is over-engineering (Rule 2). Declared managed-MCP **per-node by design**: one config field `mcp.managed_tool_allowlist []string` (enterprise tool names, set identically across nodes via deployment), enforced at the MCP listener admission path (`internal/mcp/gateway.go` `admitTool` — rejects a tool call not in the allowlist when the list is non-empty, `slog.Warn` the rejection). Empty list = unrestricted (current behavior preserved). Hot-reloadable via `RebuildMiddlewareChain` → `Handler.SetManagedToolAllowlist`. Rolling-update convergence on next restart; no runtime fan-out (the allowlist changes rarely). `config.example.yaml` gets a commented example. Tests: `internal/mcp/gateway_b3_managed_allowlist_test.go` (5 cases — empty admits all, non-empty admits listed + rejects unlisted, hot-reload toggle, direct `admitTool`). |

**#128 — guard/PII SSOT alignment** (upstream-blocked, vendored):

| # | Fix | Details |
|---|-----|---------|
| 1 | **Vendored 15 guard redaction patterns as SSOT** | Guard's 15 canonical redaction patterns (`fg-redact/src/lib.rs:57-130` `Redactor::new` `defs`) are hardcoded-only and never serialized via any IPC dump (`guard.rules.dump` carries authorization rules, not redaction patterns) — there is no runtime-fetch surface to consume. Vendored as `internal/middleware/pii_patterns.go`: 15 `PatternDef` entries in guard's priority order, regex strings copied verbatim (Go `regexp` is RE2-compatible with the Rust `regex` crate, same syntax, no lookaround). Replaces the prior 6 hardcoded gateway builtins that had drifted (gateway-only `ssn`/`phone_us`; guard-only `jwt`/`oauth_bearer`/`api_key`/`conn_string`/`password`/`secret_kv`/`env_kv`/`netrc`/`aws_secret`/`private_key`/`id_number`; gateway `credit_card` was 16-fixed, weaker than guard's Luhn-validated `\d{13,19}`). |
| 2 | **4 Rust validators ported to Go** | `ipv4`/`aws_secret`/`credit_card`/`phone` carry a Rust validator (regex crate has no lookaround, so the check cannot be a regex). Ported as `ValidatorFn func(content string, start, end int) bool` — `validIPv4` (octet ≤255 + boundary non-digit/dot), `validAWSSecret` (40-char base64 + ≥6 distinct + boundary non-base64), `validLuhn` (boundary non-digit + Luhn checksum), `validPhone` (boundary non-digit). Applied on each candidate match (`ScanText`); a candidate whose validator returns false is skipped (matches guard `collect_spans` semantics — `999.1.1.1`/non-Luhn digit runs/swallowed digit runs rejected). |
| 3 | **Drift test** | `internal/middleware/pii_patterns_test.go`: pins COUNT == 15 + the canonical name set + priority order + all-compileable + the 4 validator-attached patterns. A guard-side add/remove/rename is caught at gateway build time before a divergent set ships. Hash-of-regex is brittle across syntax tweaks; name-set + count is the durable guard. |
| 4 | **`pii.guard_fetch` flag reserved** | Optional config flag (`PIIConfig.GuardFetch`, default OFF) reserves the integration point for a future `guard.redact.patterns.dump` IPC contract — when off (always, until guard lands it), gateway uses the vendored SSOT. No dead runtime call (guard-unreachable is a non-issue by construction — there is no runtime dependency). Upstream issue filed: **fusion-guard#7** requesting the `guard.redact.patterns.dump` method (regex + validator tag enum). Non-blocking — vendored SSOT + drift test is self-contained and ships now. |
| 5 | **Tests** | `pii_test.go` updated to the guard-aligned names (`phone_cn`→`phone`, `ip_v4`→`ipv4`); bare Luhn-valid PAN `4111111111111111` for `credit_card` (guard does not strip separators); new `RejectsNonLuhn` case (`1234567890123` non-Luhn rejected); `ssn`/`phone_us` now assert NOT detected (removed from SSOT — operators add via `pii.patterns` user set). **Verify**: `check_bare_goroutines.sh` OK; `go vet ./...` clean; `go build ./...` OK; `go test ./... -count=1 -race` all 25 packages green. |

### v0.8.47 — Audit C1/C2/C3 forced at the code layer (release-blocking P1s)

The v0.8.46 changelog listed C1/C2/C3 as "ops-only, not code-fixed". This release forces all three at the config/validate layer so a deployment cannot start with the insecure defaults the audit graded release-blocking ("不满足 C1 或 C2 不得对外发布").

| # | Fix | Details |
|---|-----|---------|
| 1 | **C1 — placeholder detection (R7 extension)** | `looksLikePlaceholder()` pattern layer on top of the exact-match blacklist: `fg-*` prefix, `your-*-key` / `sk-your-*` / `sk-ant-your-*` stubs, `*change-me` / `*do-not-ship` / `*replace-me` / `placeholder` / `set-me` / `example-key` / `todo` markers. The shipped `config.example.yaml` stubs (`fg-master-key-change-me`, `your-volcengine-key`, …) now fail `config.Load`. **New surface**: `validate()` rejects a known-placeholder `api_key` on an **enabled** backend — R7 only covered the gateway auth keys (`auth.APIKeys`); upstream provider credentials (`backends.<name>.api_key`) were never validated. Disabled backends keep their stub (reserved, not live); empty `api_key` allowed (local backends need none). `config.example.yaml`: `openai` backend `enabled:true`→`false` (forcing fires only when an operator enables a cloud backend); removed a leaked real fusion-mlx key → empty + comment; C1 comment block above `auth.master_key`. |
| 2 | **C2 — `encryption.master_key` fail-closed (was WARN)** | When OIDC is enabled OR a connector `persistence_path` is configured (OAuth2 token persistence active) WITHOUT a `master_key`, `validate()` now **errors** at Load instead of warning — without it, OAuth2/connector tokens persist PLAINTEXT to `data/connections.json`. Also refuses a placeholder or `<32`-char `master_key`. Local-only no-OIDC/no-connector deployments need no `master_key` (nothing to encrypt) and are unaffected. The admin PUT-reload path runs the same `validate()`, so enabling OIDC via the dashboard without a `master_key` is rejected with 400. |
| 3 | **C3 — deploy port unify 8100→11432** | Dockerfile `EXPOSE`, `kubernetes/{deployment,service,configmap,ingress,networkpolicy}.yaml`, helm `values.yaml`, `terraform/{outputs,gcp/outputs,aws/outputs}.tf` all unified to **11432** (matches `DefaultConfig.Server.Port`). The audit found manifests at 8100 while the binary listens on 11432, so probes/services mismatched the actual listener. |
| 4 | **Tests** | `internal/config/c123_audit_test.go` (13 cases): placeholder pattern + JWT pattern detection; `auth.master_key` / `api_key` / admin-password rejection; enabled-backend `api_key` rejected (disabled+empty pass); C2 fail-closed for OIDC/connector/placeholder/short `master_key`; local-only no-`master_key` passes; C3 deploy files contain no `8100` + `DefaultConfig` port matches. `internal/admin/config_handlers_test.go`: planted `encryption.master_key` in the shared fixture + `update_enabled_no_master_key_rejected` subtest pinning the admin-layer C2 reject. **Verify**: `check_bare_goroutines.sh` OK; `go vet ./...` clean; `go build ./...` OK; `go test ./... -count=1 -race` all 24 packages green. |

### v0.8.46 — Product-readiness audit: all P0–P3 code fixes (R1–R15 + N1–N8)

Product-readiness audit (`audit/fusion-gateway-audit-result-product-0827.md`, 2026-08-27) judged v0.8.45 **Conditional Go**: 0 P0 (the one claimed P0 — handler panic — disproven: Go `net/http` has built-in `defer recover`, a handler panic closes the connection not the process), 6 P1, 15 P2, 8 P3 = 29 items. This release fixes all **23 code-fixable** items (R1–R15 + N1–N8) + C4 docs. The remaining 3 P1 are **operational** (C1/C2/C3) — cannot be code-fixed, called out below.

| Phase | Items | Details |
|-------|-------|---------|
| **1 — P1 code** | R3 stream timeout, R4 version ldflags, R5 log_level | **R3**: dual HTTP client per provider — `httpClient{Timeout}` for non-stream, `streamHTTPClient{Timeout:0, Transport:ResponseHeaderTimeout}` for stream; the shared 120s `Client.Timeout` no longer truncates long generation (>120s) and the 180s idle watchdog is no longer dead code for header-stall. **R4**: `var (version, commit)` package vars in main.go, stamped via `-ldflags "-X main.version=… -X main.commit=…"`, surfaced on `/v1/status` + otel resource `ServiceVersion`. **R5**: `config.Server.log_level` now drives `slog.SetDefault` (debug/info/warn/error → `slog.Level*`); static at startup (hot-reload of log level is a future item, documented in runbook). |
| **2 — P2 surgical** | R1, R2, R6, R7, R8, R9, R10, R12, N3, N4, N5 (R11 deferred) | **R1**: `nodeBreakerOpenLocked` inline read under held `breakerMu.RLock` (no recursive RLock writer-starvation). **R2**: `withMiddleware` `defer recover()` — handler panic → 500 + stack log, process stays alive. **R6**: admin login rate-limit + lockout (5 fails → 15min lock) + `MaxBytesReader(4096)` body cap. **R7**: `config.Validate` refuses known placeholder secrets (change-me/fg-/DO-NOT-SHIP/default) in master_key/api_keys/admin password; empty `encryption.master_key` + connector/oauth2 enabled → error (forces C2). **R8**: stream closed without `finish_reason` → `endReason=incomplete` → `recordOutcome(false)` so the circuit breaker sees truncation. **R9**: per-request `context.WithTimeout` (`routing.stream.max_request_duration`, default 600s) backstop for stalled cloud backends. **R10**: global concurrent-stream semaphore (`routing.stream.max_concurrent_streams`, default 256) — excess → 429. **R12**: `X-Request-ID` added to `fusionPassthroughHeaders` for downstream log correlation. **N3**: Anthropic native stream raw passthrough (`StreamChunk.Raw`) — verbatim upstream bytes emitted directly, no per-frame re-marshal. **N4**: backend `api_key` AES-256-GCM encryption at rest (`enc:` prefix) keyed by `encryption.master_key`; plaintext keys still load (warn). **N5**: per-IP anonymous rate limit (`routing.rate_limit.anonymous_rpm`, default 60) — anonymous (no key) requests no longer unlimited. **R11** (sync.Pool for `StreamChunk`/JSON buffers) deferred — E1 raw passthrough mitigates the marshal burn; pooling needs a `Provider.StreamChat` interface change (high blast radius), tracked for v1.0. |
| **3 — infra/docs** | R13, R14, R15, N6, N7, N8 | **R13**: `.github/workflows/release.yml` — tag-triggered (`v*`), macOS-only, builds `darwin/arm64` binary with R4 ldflags, generates a git-log changelog, publishes a GitHub Release. **R14**: Helm `Chart.yaml` `version`+`appVersion` synced to 0.8.46 (was 0.6.0). **R15**: `docs/runbook-recovery.md` — RPO/RTO table, cold-tar + hot-`cp` backup/restore (atomic persist makes both safe), graceful shutdown order (EI10 lifecycle.Worker join), C1/C2/C3 ops steps, log-rotation note (external launchd/newsyslog; log_level static at startup). **N6**: otel version sourced from R4's injected var (3 hardcoded `0.4.0` literals replaced); log rotation documented (no in-process lumberjack — keep lean). **N7**: semantic cache `Store` eviction O(1) via `container/list` (`Remove(Back())`+`PushFront`), replacing the slice-copy-on-full path. **N8**: fusion-model-hub HealthCheck client + proxy Transport routed through `TransportForBackend` for the `MaxConnsPerHost` FD cap. |
| **4 — architectural** | N1, N2 + cfg-swap race fix | **N1**: pool.go 21-way `switch backendCfg.Type` → provider factory registry (`register.go`); `BuildProviders` resolves type→ctor via `LookupProviderFactory`, adding a backend is one registration line; behavior identical (M3 fail-fast, EI6 cloud-wrap preserved). **N2**: shared transport/reader helpers extracted to `internal/httpx` leaf package (imports only `internal/config`); `adapter/transport.go` is now a thin re-export shim (111 in-adapter call sites unchanged); cluster/middleware/hardware/router import `httpx` for leaf helpers — removes the cluster→adapter reverse-edge coupling smell. **cfg-swap race fix** (pre-existing, surfaced by `-race` on phase 4 verify): `RebuildMiddlewareChain` wrote `s.cfg` unlocked while background reaper goroutines read it — added `cfgMu sync.RWMutex` + `snapshot()` RLock helper; writer locks the swap, both reapers read via `snapshot()`. |
| **Ops-only (C1/C2/C3)** | not code-fixed — must be done by ops before public release | **C1** rotate ALL default credentials (jwt_secret ≥32 random, admin password, master_key, api_keys, each backend api_key) — R7 makes `config.Validate` refuse known placeholders so a shipped config.yaml fails Load until rotated, forcing the issue. **C2** set `encryption.master_key` (≥32 random) in config.yaml, else OAuth2 connector tokens persist plaintext — R7 errors when connector/oauth2 enabled and master_key empty. **C3** unify deploy port: Dockerfile/k8s/helm use 8100 while `DefaultConfig`=11432 — set `server.port` in the configmap or change the manifest to 11432 so probes/services match. |

**Verify**: `go vet ./...` + `go build ./...` + `go test ./... -count=1 -race` all green (2700+ tests, incl. 3 previously-racing `cmd/gateway` reload tests now fixed). Bare-goroutine CI gate clean. Release workflow (R13) will build + publish on `git tag v0.8.46`.

### v0.8.31 — Connection-phase retry covers idle keep-alive conn closed by peer (#85)

| # | Fix | Details |
|---|-----|---------|
| 1 | **`isRetryableError` matches `use of closed network connection`** (#85) | Prod logs on 08/24 showed a 5-minute cluster of 5× `ERROR anthropic messages upstream error ... status=502` with `error="anthropic stream messages failed: Post \"http://113.57.198.109:4000/litellm/v1/messages\": dial tcp 10.0.0.1:420->113.57.198.109:4000: use of closed network connection"` (11:49–11:54). The gateway's `streamHTTPClient` pools keep-alive connections; when litellm→glm5.2 closes an idle connection on its side (idle timeout) and the pool hands that stale connection to a new request, Go's `net` package surfaces `use of closed network connection`. This is a connection-phase error (the `Do` call fails before any header is written — `anthropic.go` `StreamMessages` returns `nil, err`), so it is idempotent and safe to retry — exactly the class #73 covered. But the #73 substring set (`EOF` / `connection reset by peer` / `connection refused` / `timeout` / `deadline exceeded`) did **not** include this string, so `isRetryableError` returned false → `RetryStreamMessages` logged `non-retryable stream messages error, skipping retry` (0 `retrying` logs in the 11:49–11:54 window, verified) → direct 502 to claude code. Added `use of closed network connection` (the Go net-package canonical string for a pooled-connection closed under us) to the matcher, so the existing retry loop (up to `max_retries` 7 with backoff) transparently recovers instead of a hard 502. |
| 2 | **Same class as #73, same safety** | Transport-reset, connection-phase only. The fix touches only the error-string matcher — `StreamMessages`' two-phase contract is unchanged: a `Do` error (pre-header) returns `nil, err` → retried; once a 200 arrives the channel + parse goroutine path takes over → a mid-stream error only closes the channel and runs the open-block finalization (#71), never re-dispatched. A closed pooled connection surfaces only at `Do` time (the pool check happens before the request is sent), so mid-stream behavior is byte-for-byte unchanged. No config change; `routing.retry.max_retries: 7` already configured. |
| 3 | **Tests** | `TestIsRetryableError_ClosedNetworkConnection` matches the exact prod error string (`dial tcp ...->113.57.198.109:4000: use of closed network connection`) → true. End-to-end `TestRetryStreamMessages_RetriesOnClosedNetworkConnection`: first call returns the prod closed-conn error, second returns a successful channel → asserts 2 calls + non-nil channel (mirrors `TestRetryStreamMessages_RetriesOnEOF`). The non-retryable guard (`TestRetryStreamMessages_DoesNotRetryOnNonRetryable`, `invalid api key`) still passes — only transport-reset strings are retryable, business errors are not. Full suite green; `go vet` clean. |

### v0.8.30 — Local-exclusive model guard: stop cloud-diverting models the cloud can't serve (#83)

| # | Fix | Details |
|---|-----|---------|
| 1 | **P3.5 guard short-circuits local-exclusive models to local** (#83) | Prod logs showed 152× `ERROR chat failed provider=glm52 status 400 "Invalid model name ... model=Qwen3.5-9B-4bit"` (2026/08/19–08/24, 50× on 08/24 alone). Root cause in the routing priority chain: P4 (token-budget, `engine.go:425`) and P4.5 (output/input ratio, `engine.go:475`) both `return CloudBackend` **before** P6 model-availability (`engine.go:495`) runs. A model that is loaded locally (`Qwen3.5-9B-4bit` — `model_loaded=true`, gateway local model set=46) but has no `routing.fallback.model_mapping` entry (config only maps the 4 Claude aliases → `glm5.2`) was being cloud-diverted by the ratio rule (predict 2048 / input 132 = 15.52 > 0.6). `applyCloudModelMapping` then returned the raw local name unchanged → the cloud backend (glm52/LiteLLM) rejected it with 400 → not retried (`isRetryableError` excludes 400) → client churn. The new P3.5 guard, inserted after P3 (local-not-ready) and before P4: if `req.Model` is in the local model set **and** absent from `model_mapping` (or mapping disabled), return `LocalBackend` with reason `local_exclusive_model` — a cloud that can't serve the model is never asked to. P0–P2 hardware/breaker cloud-diverts stay upstream (overload still wins); a model present in the local set **and** in `model_mapping` falls through (cloud can serve it via the mapping), so the existing ratio/token cloud-divert behavior for mapped models is preserved. |
| 2 | **Tests** | `TestDecide_LocalExclusiveModel_RatioNotCloudDiverted` and `TestDecide_LocalExclusiveModel_TokenBudgetNotCloudDiverted` pin the guard against both P4.5 ratio and P4 token-budget cloud-diverts — a `Qwen3.5-9B-4bit` in the local set with no mapping routes `local_exclusive_model` even at ratio 15.52 and input 500 > threshold 100. `TestDecide_MappedModel_RatioStillCloudDiverts` confirms a model in the local set **and** in `model_mapping` (`claude-opus-4-7`) still cloud-diverts under high ratio (guard no-ops). Existing `TestDecide_TokenBudgetExceeded` / `TestDecide_ModelAvailableLocally` / `TestDecide_ModelNotAvailableLocally` unchanged. Full suite green; `go vet` clean. |
| 3 | **Scope** | Routing-layer fix only — `applyCloudModelMapping`, `anthropic.go`, and config are untouched. Non-invasive: no silent model substitution (rejected direction B), no rejection error (rejected direction C). A locally-served model with no cloud equivalent is now served locally regardless of token/ratio pressure, matching the local-first design principle. The config omission (no Qwen mapping) is no longer a 400 — and intentionally not "fixed" by adding a mapping, since the correct routing for a local-only model is local, not a mapping to a different cloud model. |

### v0.8.43 — multimodal /v1/messages forced to local vision model (stop cloud 502) (#112)

| # | Fix | Details |
|---|-----|---------|
| 1 | **Multimodal guard before `Decide`** (#112) | A multimodal `/v1/messages` request (image/audio content block) returned **502**: the handler built `RouteRequest{Model, Text, Stream}` for the router, but image blocks are invisible to this text-only signal (`extractAnthropicTextContent` concatenates only `text` blocks). The rule chain then routed by text token budget and `applyCloudModelMapping` rewrote the `claude-*` alias to **`glm5.2`** — a text-only cloud model — which rejected the image with `400 multimodal_not_supported`, wrapped to 502. The `Loaded` flag (#59) and local-exclusive guard (#83) cannot help: the alias maps to cloud and there is no router signal for multimodal content. New `anthropicRequestHasImage` helper + a guard in `handleAnthropicMessages` (runs **before** `Decide` so the rule chain cannot divert the payload): when an image block is present and `routing.multimodal.local_model` is set, force `LocalBackend` and rewrite the model to the configured local vision model; when the knob is empty, reject with a clear `400 invalid_request` naming the missing knob instead of a masked cloud-400-as-502. Non-invasive — pure routing decision, no inference computation. |
| 2 | **Config: `routing.multimodal.local_model`** | New `MultimodalConfig{LocalModel}` under `routing:`. Default in this repo's config: `mlx-community--Qwen2.5-VL-7B-Instruct-4bit` (registered locally, verified serving multimodal via `/v1/chat/completions` with `image_url`). MLX auto-loads it on first request. `FusionMLXProvider` is not a `MessagesProvider`, so the local path takes the existing `AnthropicToOpenAIChatRequest` conversion which already preserves `image` blocks → `image_url` (anthropic.go `anthropicBlocksToContent`). Empty value = clear 400. |
| 3 | **Tests** | `TestHandleAnthropicMessages_MultimodalRoutesLocalWithVisionModel`: image-bearing request with `routing.multimodal.local_model` set → routes local with model rewritten to the VL model (asserted via a `modelRecordingProvider` that atomically records the forwarded model id; uses `mockProvider` which is non-MessagesProvider, matching real `FusionMLXProvider` shape so it exercises the `AnthropicToOpenAIChatRequest` branch). `TestHandleAnthropicMessages_MultimodalRejectsWhenNoLocalModel`: image-bearing request with no knob → 400 with body containing "multimodal" (not 502). `go test ./...` → 2709 passed in 23 packages, `go vet ./...` clean. Live-verified: `claude-fable-5` + image → 200, `model=mlx-community--Qwen2.5-VL-7B-Instruct-4bit`, "The pixel in the image is black." (was 502). |

### v0.8.42 — /v1/models local-first ordering: stop cloud models masking locally-served models (#108)

| # | Fix | Details |
|---|-----|---------|
| 1 | **`handleModels` sorts local models ahead of cloud** (#108 observation 2) | `/v1/models` returned cloud provider models (`claude-3-5-sonnet-...`, owned_by `anthropic`) at the head of the list because cloud backends sit at the top of config `backends:` and `listModelsConcurrent` preserves provider-arrival order. A downstream consumer picking the first listed model got a cloud-routed id, not a locally-served one — masking which models are actually resident in the local engine. The `Loaded` flag (#59) already distinguishes servable from catalogued, but consumers that select by position still hit cloud. New `modelListLess` comparator + `sort.SliceStable` in `handleModels` orders: local (`owned_by=="local"`) before cloud, loaded local before unloaded local, alphabetical by ID within each tier. SliceStable keeps equal-tier entries in provider-arrival order. Verified live: head of `/v1/models` is now `Qwen3.5-9B-4bit` (local, loaded) instead of `claude-3-5-sonnet-20241022`; 47 local models precede the 5 cloud models. |
| 2 | **#108 primary (stream 502 `connection refused`) — not reproducible on current binary** | The reported `dial tcp 127.0.0.1:11434: connect: connection refused` on `stream:true` while non-stream succeeded could not be reproduced against the live v0.8.41 binary: `Qwen3.8-27B-4bit` stream and non-stream both return 200 with correct SSE deltas, and `FusionMLXProvider.StreamChat`/`Chat` share the same `httpClient`+`baseURL`+Transport (no separate dial target). Logs show the `connection refused` entries are all background `RefreshModelSet`/`HealthDetail` polls (to `/v1/models`, `/health`) during real MLX restarts, not user stream requests. The stream forward path (`Stream` bool forwarded verbatim via `json:"stream"`, `parseSSEStream` in a `safego` goroutine) is correct. Closing the primary as not-a-gateway-bug; the model-ordering fix addresses the reproducible gateway defect in the same issue. |
| 3 | **Tests** | `TestHandleModels_LocalModelsListedFirst`: registers a cloud provider (anthropic) whose model IDs (`a-cloud-1/2`) sort alphabetically ahead of local IDs (`z-local-*`) — only a local-first rule can put `z-local-loaded` at index 0, so a plain alphabetical sort fails it. Asserts full order `[z-local-loaded, z-local-unloaded, a-cloud-1, a-cloud-2]` (local-first, loaded-first, alpha within tier). Existing `TestHandleModels_*` (concurrent-skip, mode-local-only, per-provider-timeout, loaded-flag) all green. `go vet ./...` clean; `go test ./...` → 2707 passed in 23 packages. |

### v0.8.41 — agent slot scheduler: per-node cap, opt-in wait-queue, task cancel (#102 ADR-001)

| # | Fix | Details |
|---|-----|---------|
| 1 | **Per-node cap in `SelectNodeByModel`** (#102 sub-task 2) | `SelectNodeByModel(strategy, model string, maxConcurrent int)` now skips a healthy cluster node whose `InFlight` slots are full (`>= maxConcurrent`). The engine threads `routing.local_priority.max_concurrent` through `tryClusterLocked`, so a capped-out node is no longer selected — the cluster falls back to cloud (existing fallback behavior). Signature rippled through the `ClusterSelector` interface + 2 impls + 3 mocks + 3 test call sites. `TestDiscovery_SelectNodeByModel_SkipsCappedNodes` pins: node-a at cap → node-b selected; both at cap → error → cloud; cap disabled (`maxConcurrent=0`) → no skip. |
| 2 | **Opt-in local wait-queue (mode=local only, default OFF)** (#102 sub-task 3) | New `slotQueue` (`internal/router/queue.go`): bounded FIFO counting semaphore of size `max_concurrent`; `Acquire(ctx, queue_timeout)` blocks until a slot frees or returns `ErrQueueTimeout` (→ **429** `rate_limit_error`), honoring ctx cancel. Engaged **only** when `routing.mode=local` AND `routing.local_priority.queue_enabled=true` — `LocalQueue()` returns nil in hybrid/cloud, so the default path is **unchanged** (zero regression). The gate sits at the handler (`handleChatCompletions` + `handleAnthropicMessages`) before forwarding, so the engine stays pure (no blocking). New config: `LocalPriority.QueueEnabled` (default `false`) + `QueueTimeout` (default `5s`) + validate non-negative. Tests: `queue_test.go` (acquire/release, timeout-429, no-wait fail-fast, concurrent-up-to-cap, ctx-cancel-while-waiting) + server e2e `TestServer_QueueModeLocal_429OnCap`. |
| 3 | **`POST /v1/agent/tasks/{id}/cancel` endpoint** (#102 sub-task 4) | New `TaskRegistry` (`internal/server/task_registry.go`): `Register`/`Cancel`/`Release`/`Len`, thread-safe. The chat + Anthropic messages stream paths register the task-id (= `X-Request-ID`, middleware-injected — no new header) with a `context.WithCancel` of the forward ctx, and `defer Release` on exit. `POST /v1/agent/tasks/{id}/cancel` invokes the registered cancel func → the stream ctx is canceled → the stream goroutine observes `ctx.Err()`, exits, and releases the slot via its existing `defer` (the v0.8.40 sole-release path). The registry **never** touches the in-flight counter, so cancel cannot double-release. Manual path parse (no Go 1.22 path-pattern dep). Returns `200 {"status":"canceled","task_id":...}` on hit, `404` on unknown/completed. Tests: `task_registry_test.go` (register/cancel/release/not-found/idempotent/concurrent) + server e2e (200, 404, malformed-404, 405, full integration cancel-terminates-stream + slot-returns-to-0). |
| 4 | **ADR-001 conformance + non-invasive** | Per-node slots (no global pool) ✓; FIFO + cloud-overflow queue default OFF, mode=local only ✓; immediate-release cancel ✓; non-invasive — composes existing `localInFlight`/`Node.InFlight` counters, opt-in default-off queue, cancel reuses existing ctx propagation. No engine rewrite, no new inference computation. `go vet ./...` clean; `go test ./...` → 2706 passed in 23 packages; CI green (macos-latest). |

### v0.8.40 — fusion-mlx slot-leak: stop double-decrement on stream cancel (#97/#102)

| # | Fix | Details |
|---|-----|---------|
| 1 | **`FusionMLXProvider.Cancel` no longer touches the in-flight counter** (#97/#102) | The local in-flight slot counter (`inFlightCounter`, atomic.Int64) was double-released on every stream cancel. `StreamChat` hands the single release to its forward goroutine via `defer goroutineRelease()`; a ctx-cancel propagates to `resp.Body` (the request is built with `http.NewRequestWithContext`), so the goroutine exits and releases the slot on its own. But `Cancel` *also* manually decremented the counter → the counter underflowed to **negative**, silently bypassing the P5 `max_concurrent` gate (`e.localInFlight() >= int64(maxConcurrent)` → negative < max → never limited) on every subsequent cancel. Symptom: under concurrent cancels the local concurrency limit stopped applying, letting unbounded local requests through. Fix: `Cancel` is now a no-op signal (logs `request_id` + current `in_flight` for traceability); the goroutine's `defer` is the sole release. Idle GC after cancel was already covered by `StartIdleGCTimer`, so the cancel-time GC trigger that motivated the manual decrement was redundant — removing it loses nothing. |
| 2 | **Tests** | `fusion_mlx_test.go`: `TestFusionMLXProvider_Cancel` rewritten to assert the no-op contract (counter stays 2 across three `Cancel` calls, not decremented to 1/0). New `TestFusionMLXProvider_StreamChat_CancelNoDoubleRelease` regression: an `httptest` SSE server flushes the first chunk then blocks on `<-r.Context().Done()`; the test reads one chunk, cancels mid-stream (mirrors `handleStreamChat:864`'s `canceler.Cancel`), drains the channel, and polls up to 3s asserting `InFlight()` returns to **exactly 0** (never negative — the underflow signature). Full adapter + 2687-test suite green; `go vet` clean. |
| 3 | **Scope** | `FusionMLXProvider` only — the only provider with an `inFlightCounter`/`Cancel`. `OpenAICompatibleProvider`/`AnthropicProvider` have no in-flight counter and no `Cancel`, so they are unaffected. `Cancel` is only ever called at `server.go:864` (interface-guarded `provider.(interface{ Cancel(string) })`), so the no-op cannot reach any other provider. Routing/engine/adapter-pool unchanged; the P5 `max_concurrent` gate behavior is **restored**, not altered. This addresses the **slot-release audit** sub-task of #102; the remaining #102 sub-tasks (per-node cap in `SelectNodeByModel`, opt-in local wait-queue, `POST /v1/agent/tasks/{id}/cancel`) remain open and are not part of this patch. |

### v0.8.39 — Surface upstream error detail on /v1/chat/completions failure (#104)

| # | Fix | Details |
|---|-----|---------|
| 1 | **OpenAI chat path now surfaces upstream cause instead of a bare "Chat failed"/"Stream chat failed" 502** (#104) | `/v1/chat/completions` failures returned a generic 502 `{"error":{"message":"Chat failed","type":"server_error"}}` that hid the upstream cause — e.g. a cloud 400 `"Invalid model name passed in model=qwen3.5-9b"` left the client unable to self-diagnose a wrong model name (the `/v1/messages` Anthropic path already surfaced detail via `writeMessagesError` [#40]; the OpenAI chat path was the gap). New `writeChatFailedError(w, prefix, err)` helper: 502 JSON `{error:{message, type:server_error}}` with `err.Error()` appended after the prefix, capped at 512 chars so a large upstream body does not flood the client response (`json.Marshal` escapes safely — no response injection). `handleStreamChat` stream-chat error → `writeChatFailedError("Stream chat failed", err)`; `handleNonStreamChat` tracks `failErr` through the A4 cloud-fallback so a failed fallback surfaces its **own** detail (not the original local error), terminal call `writeChatFailedError("Chat failed", failErr)`. Verified live: a bad-model request now returns `502 {"error":{"message":"Chat failed: anthropic returned status 400: ...Invalid model name passed in model=qwen3.5-9b. Call /v1/models...","type":"server_error"}}` (both stream + non-stream). No API-key material appears in upstream error strings, so surfacing is safe. |
| 2 | **Tests** | `server_test.go`: `TestWriteChatFailedError_SurfacesUpstreamDetail` (err with status 400 + "Invalid model name" → 502, type `server_error`, message carries the prefix + detail); `TestWriteChatFailedError_CapsLongDetail` (2000-char detail → message ≤ `len(prefix)+512`); `TestWriteChatFailedError_NilErr` (nil err → 502, message = prefix + ": "). Full server package (483 tests) green; `go vet` clean; live smoke on prod confirmed both paths. |
| 3 | **Scope** | Failure-response body only on the OpenAI chat path — routing, adapter pool, engine, and the `/v1/messages` Anthropic path are untouched. Non-invasive: a successful chat response is byte-identical to before; only the error body changes. Root cause note: the triggering `qwen3.5-9b` 400 was a **client typo** (a lowercase alias absent from the local model set), NOT a routing bug — the P3.5 local-exclusive guard (#83) correctly did not fire because the alias is not the canonical `Qwen3.5-9B-4bit` the local set holds. Route-time rejection of unknown models was rejected as unsafe (the cloud model set is unknowable to the gateway; `glm5.2` is a legitimate cloud-direct target). The fix is purely error transparency so clients can self-correct. |

### v0.8.38 — daily-reset cost cap per API key: completes cloud_fallback migration (#87)

| # | Fix | Details |
|---|-----|---------|
| 1 | **`daily_budget_limit` — a per-key daily-reset USD cap, distinct from the cumulative `budget_limit`** (#87) | fusion-multi-node's `cloud_fallback.py` enforced `max_cost_per_day` (default $10, rolling 86400s window) — a daily spend cap the gateway lacked (its `budget_limit` is cumulative lifetime per key). Issue #87 ("两项都接收，全量迁移") required gateway to fully cover cloud_fallback + mcp_gateway. Exploration showed mcp_gateway was **already fully ported** to `internal/mcp/` (1:1 Go port, off by default — no change needed) and cloud_fallback was 90% pre-existing (adapters in `internal/adapter/*`, per-model pricing richer than Python in `internal/cost/pricing.go` + `custom_pricing.go`, usage in `cost/tracker.go`); the one real gap was the daily-reset cap. Fix: a `daily_budget_limit` field on `AuthKeyConfig` (`config.go:72`) + `APIKeyEntry` (`store.go:55`). `QuotaStore` (`quota.go`) gains a per-key `dailyUsage` map + `dailyDate` (local `YYYY-MM-DD`); `Check` returns `exceeded=true` when **either** the cumulative `budget_limit` OR the daily `daily_budget_limit` is reached (whichever trips first blocks), logging which cap tripped; `Deduct` accumulates into both; a date rollover (local midnight) zeros the daily bucket. `BudgetBlock` middleware is **unchanged** — it already reads `exceeded` from `CheckQuota`, so the daily cap folds in with zero middleware change. The live `*APIKeyEntry` is synced with `DailyUsed`/`DailyDate` on every `Check`/`Deduct` (mirrors the existing `QuotaUsed` sync) so `/admin/api/keys` GET/list surfaces `daily_budget`/`daily_used`/`daily_date` for ops. Admin key create/update accept `daily_budget`; config.yaml `auth.api_keys` entries support `daily_budget_limit` (validated non-negative alongside `budget_limit`). Default `0` = disabled → only the cumulative cap applies → zero regression for existing keys. Both caps coexist: a key with `budget_limit=100, daily_budget_limit=10` blocks at $10/day but can spend up to $100 lifetime. |
| 2 | **Tests** | `quota_test.go` (new): `TestQuota_DailyLimit_TripsAndResets` (deduct 0.6 ok, 0.5 → exceeded; inject +26h via overridable `nowFn` → rollover → not exceeded); `TestQuota_DailyLimit_Disabled` (`daily_budget_limit=0` → only cumulative, no regression); `TestQuota_BothCaps_DailyTripsFirst` (daily=2.0 trips before cumulative=100.0); `TestQuota_DailyLimit_SyncsEntry` (`Check`/`Deduct` sync `live.DailyUsed`/`live.DailyDate`). `handler_test.go`: `create_key_negative_daily_budget` (400), `create_key_with_daily_budget` (201 + store has `DailyBudgetLimit=10`), `list_keys_reflects_daily_fields` (list response carries `daily_budget`/`daily_used`/`daily_date`). `nowFn` indirection (default `time.Now`, test-overridable) avoids touching real time. Full suite (2683 tests) green; `go vet` clean. |
| 3 | **Scope** | Cost-control enforcement only — no middleware/adapter/engine/routing change. `BudgetBlock`, `cost/tracker.go`, `cost/pricing.go`, `internal/mcp/*` all unchanged. Daily reset boundary = **local calendar midnight** (`time.Now().Format("2006-01-02")`), chosen over cloud_fallback's rolling-86400s window because it matches the ops mental model "budget per calendar day" and is simpler (a rolling 24h window needs timestamps per deduction); if rolling-24h is ever wanted, separate change. `dailyUsage`/`dailyDate` are in-memory (QuotaStore) — reset on restart; acceptable (cumulative `usage` is also in-memory — pre-existing; a restart mid-day loses the daily counter, key could overspend that day; disk persistence covers keys/channels, not quota usage — [[gateway-store-disk-persistence]]). mcp_gateway needs **no code** this PR (already ported in `internal/mcp/`); the #87 close comment + memory confirm it. fusion-multi-node can now remove both `cloud_fallback.py` and `mcp_gateway/` modules (their PR, not ours). |

### v0.8.37 — Cluster model-aware routing: prefer a node that serves the requested model (#95)

| # | Fix | Details |
|---|-----|---------|
| 1 | **`tryClusterLocked` is now model-aware — no more upstream 404 when a cluster node doesn't serve the model** (#95) | In a multi-node cluster where nodes serve different models, cluster routing was model-agnostic: `tryClusterLocked` called `SelectNode(strategy)` (picks any healthy node), then `ClusterNodeProvider` forwarded `req.Model` verbatim to that node's `/v1/chat/completions` (node_adapter.go) — and the upstream returned **404** when the selected node didn't serve the model. The `Node` struct had no model registry, `ClusterNodeConfig`/`MasterNodeInfo` had no model field, and `ClusterNodeProvider.ListModels` had zero non-test callers. Fix: a per-node model registry polled from `GET /v1/models`. `Discovery.fetchModels(node)` runs on every health-check tick (piggyback on `fetchRemoteMetrics`, default 10s), decodes `[]adapter.ModelInfo`, and stores the served-model IDs into `Node.models` under `node.mu`. New `SelectNodeByModel(strategy, model)` / `HealthyNodesByModel(model)` (mirroring the existing `SelectNodeByPlatform` D4 pair) filter healthy nodes that serve the model before applying the load-balancer strategy. `tryClusterLocked` now takes `model string` and calls `SelectNodeByModel`; when no healthy node serves the model it returns `nil` → the caller falls through to **cloud** (the existing uniform contract at all 13 call sites — no new 4xx). Empty `model` falls back to legacy `SelectNode` (no regression). `decideEmbeddingLocked`/`decideRerankLocked` signatures thread `model` so embeddings/rerank get the same model-aware selection. |
| 2 | **Tests** | `TestDiscovery_SelectNodeByModel_PrefersServingNode` (3 nodes, only node-2 serves qwen3 → SelectNodeByModel returns node-2; absent-model → error); `TestDiscovery_SelectNodeByModel_EmptyModel_Legacy` (empty model → legacy least-connections); `TestDiscovery_FetchModels_PopulatesNodeModels` (httptest `/v1/models` → `node.models` populated, `servesModel` checks); `TestDecide_ClusterModelAware` (trip local + modelNode map → routes to the serving node); `TestDecide_ClusterNoModelMatch_CloudFallback` (no node serves model → CloudBackend, not 4xx — the core #95 guarantee). 3 mocks (`mockClusterSelector`, `mockClusterDiscovery`, `mockClusterDiscoveryWithNode`) gain `SelectNodeByModel`/`HealthyNodesByModel`. 5 new tests; full suite (2676 tests) green; `go vet` clean. |
| 3 | **Scope** | Selection refinement only — no new config field (poll rides the existing `health_check_interval`), no engine config change, no new 4xx, cloud fallback unchanged. Registry source = poll `/v1/models` (self-contained, works both standalone + master modes, no upstream PR needed). Known limitations: (a) master-mode nodes are not health-checked node-to-node (they sync via master), so they keep empty model sets → never model-selected → cloud — safe but master-mode loses model-awareness (separate change); (b) `ShardEmbedding` (shard.go) remains model-blind — same 404 class for batch embeddings >32 inputs across ≥2 nodes, rare trigger, out of scope (separate issue if hit). A node that doesn't implement `/v1/models` keeps an empty registry → never model-selected → cloud (correct — an unreporting node can't be trusted to serve a specific model). |

### v0.8.36 — Bridge dead Prometheus metrics to live state (#96)

| # | Fix | Details |
|---|-----|---------|
| 1 | **circuit_breaker_state, in_flight_requests, hw*, hw_collection_errors_total now report live values** (#96) | `/metrics` reported stale zeros for five gauges that were declared but never set: `fusion_gateway_circuit_breaker_state` (gauge, no setter), `circuit_breaker_trips_total` (RecordCircuitBreakerTrip only test-called), `in_flight_requests` (UpdateInFlight had no production caller), all `hw_*` gauges (UpdateHardwareMetrics had no caller), and `hw_collection_errors_total` (RecordCollectionError had no caller). The breakers still tripped and the hardware collector still ran — the gauges just never reflected it, so `/metrics` monitoring and the admin analytics diverged from `/stats` JSON. Fix: bridge each to its live source. `Engine.Trip` now calls `RecordCircuitBreakerTrip` + `UpdateCircuitBreakerState(backend, StateOpen)`; `RecordSuccess`/`RecordFailure` publish the breaker's resulting `State()` (RecordFailure also emits a `failure_threshold` trip when it opens); a new `PublishBreakerStates()` walks all breakers and publishes current state on a 5s cadence — this catches the **lazy half_open transition** (Go's `gobreaker` flips to half_open on the first `State()` read after `Timeout`, not at the trip call site, so a per-transition publish misses it). The hardware collector's `collect()` now calls `UpdateHardwareMetrics` with the freshly-sampled `HardwareMetrics` + `RecordCollectionError("hardware_collect")` when a source fails. `Node.IncrInFlight`/`DecrInFlight` now call `UpdateInFlight("cluster-<ID>", …)`, and a `metrics_sync` safeGo loop publishes `in_flight_requests{backend="local"}` from `Engine.LocalInFlight()` on the same 5s cadence. router→observability import is acyclic (observability already imported engine-free). |
| 2 | **Tests** | `TestTrip_PublishesCircuitBreakerMetrics` (Trip → /metrics shows `circuit_breaker_trips_total` + `circuit_breaker_state{backend="local"} 1`); `TestRecordFailure_OpensBreaker_PublishesStateAndTrip` (FailureThreshold=2, 1 failure → state 0, 2 failures → state 1 + `failure_threshold` trip); `TestPublishBreakerStates_ReflectsCurrentState` (Trip cloud + PublishBreakerStates → cloud state 1); `TestLocalInFlight_WithoutWiring` (0 before SetLocalInFlight, no panic); `TestNode_InFlight_PublishesMetrics` (IncrInFlight×2 → `in_flight_requests{backend="cluster-metric-node"} 2`, then Decr → 1); `TestCollect_PublishesHardwareMetrics` (collect with mocked gopsutil → /metrics shows `hw_memory_used_ratio 0.42`, `hw_swap_used_bytes`, `hw_mlx_active_memory_bytes`, `hw_mlx_models_loaded 2`, `hw_mlx_inference_queue_depth 3`); `TestCollect_PublishesCollectionError` (gopsutil fails → `hw_collection_errors_total{source="hardware_collect"}` present). 7 new tests; full suite (2671 tests) green; `go vet` clean. |
| 3 | **Scope** | Observability-only wiring — no routing decision, no config, no engine behavior change. New setters (`UpdateCircuitBreakerState`, already-existing `UpdateHardwareMetrics`/`RecordCollectionError`/`UpdateInFlight` gain callers), one new engine method (`PublishBreakerStates`/`LocalInFlight`), one new safeGo loop in `main.go`, and publish hooks in `collector.collect()` + `Node.Incr/DecrInFlight`. The gauges were always meant to be set; this closes the gap between declared and live metrics so `/metrics`, `/stats`, and the admin analytics tell the same story. |

### v0.8.35 — Silent client-cancel on non-stream /v1/messages aggregate: stop ERROR + 502 to a dead pipe (#94)

| # | Fix | Details |
|---|-----|---------|
| 1 | **non-stream client cancel (both phases) no longer logs ERROR + 502** (#94) | Recurring `ERROR anthropic messages upstream error status=502` with body `{"error":{"message":"anthropic aggregate stream canceled: context canceled","type":"api_error"}}` (~6/h under CC non-stream cancel load, 9 occurrences 08/24 13:44–14:57). Root cause: `handleNonStreamAnthropicMessages` (`server.go`) forces an internal stream + `AggregateAnthropicStreamEvents` to avoid the reasoning-upstream (glm5.2 via LiteLLM) header-withholding 502. A client cancel surfaces at **either** of two error returns — the connection phase (`msgFn`/`StreamMessages`, slow TTFB → `ctx.Err()`) and the aggregate (`AggregateAnthropicStreamEvents` `case <-ctx.Done()`; the idle watchdog has its own `case <-idleC` branch, so `ctx.Done` is purely client cancel). Both were routed to `writeMessagesError` → unconditional `slog.Error` + `http.Error(502)` to a pipe the client already abandoned. Fix: a shared `nonStreamClientCanceled(ctx, w, err, phase)` helper checks `ctx.Err() != nil` (parent gone = client cancel, deterministic) before each `writeMessagesError` → logs `INFO anthropic messages non-stream client canceled` with the `phase` label (`connection phase` / `aggregate`) + silent `return`. Why the parent-ctx check and not `errors.Is(err, context.Canceled)`: the idle watchdog branch and the retry wrapper's `<-ctx.Done()` branch both wrap `context.Canceled`, so the typed check is ambiguous; the parent-ctx check disambiguates (watchdog trips while parent stays alive → `ctx.Err()==nil` → still 502, correct — a watchdog stall IS a fault). Non-stream twin of #46/#90 (stream-path cancel). |
| 2 | **Tests** | `TestHandleNonStreamAnthropicMessages_ClientCancelSilent` (a `stallStreamProvider` mock emits `message_start` then holds its channel open, forcing Aggregate to select solely on `ctx.Done()`; cancel the parent ctx at 150 ms — well under the 180 s default IdleTimeout so the watchdog cannot race; assert **no body written** and no 502) — fails pre-fix (97-byte 502 body `...aggregate stream canceled: context canceled...`), passes post-fix. `TestHandleNonStreamAnthropicMessages_ClientCancelConnectionPhase` (a `slowConnectProvider` mock blocks `StreamMessages` on `ctx.Done()`, simulating slow TTFB; cancel at 150 ms → same no-body/no-502 assertion) — fails pre-fix (60-byte 502 body `context canceled`), passes post-fix. `TestHandleNonStreamAnthropicMessages_Error` no-regression (upstream 500 via `context.Background`, never cancels → `ctx.Err()==nil` → still 502). `TestAggregateAnthropicStreamEvents_CtxCancel` (aggregate returns cancel err, unchanged). Full suite (2664 tests) green; `go vet` clean. |
| 3 | **Scope** | One helper (`nonStreamClientCanceled`) guarding the two error returns in `handleNonStreamAnthropicMessages` — the client-cancel carve-out before `writeMessagesError`. `AggregateAnthropicStreamEvents` (returns the cancel err as-is, callers may need it), `writeMessagesError` (real upstream errors still 502), `config.yaml`, and the idle-watchdog path unchanged. Non-invasive — consistent with the stream-path cancel semantics (#46/#90/#77): a client cancel is a request-level signal, not an upstream fault, so it is logged at INFO (silent) rather than ERROR. |

### v0.8.34 — Strip orphan tool_choice on /v1/messages: stop glm5.2 400 "tool_choice requires tools" (#92)

| # | Fix | Details |
|---|-----|---------|
| 1 | **strip orphan `tool_choice` (present, no `tools`)** (#92) | Recurring ERROR `anthropic messages upstream error status=502` from vLLM `Hosted_vllmException: When using tool_choice, tools must be set` (22 occurrences 08/24 14:11–14:14; recurs 08/19/21/22). Root cause: Claude Code web-search tool-use requests carry `tool_choice:"auto"` + `web_search_options:{}` but **no `tools` array** — Anthropic's server-side-tool protocol (the search tool lives server-side, declared via `web_search_options`, not in the client `tools` list). `handleAnthropicMessages` parses into `AnthropicRequest`, which has a `ToolChoice` field but **no `web_search_options` field** — so `web_search_options` is silently dropped while `tool_choice:"auto"` is forwarded verbatim. The request reaches glm5.2 (vLLM) with `tool_choice` present and `tools` absent → vLLM validation error → 400 → gateway wraps as 502. Fix: after parsing, before routing, if `ToolChoice != nil && len(Tools) == 0` set `ToolChoice = nil` + log INFO `stripping orphan tool_choice (no tools)`. The request degrades to plain text generation (200) instead of a hard 502. |
| 2 | **Tests** | `TestAnthropicMessages_StripsOrphanToolChoice` (request `tool_choice:"auto"`, no `tools` → captured upstream body must NOT contain `tool_choice`, response 200) — fails pre-fix (body contained `tool_choice:"auto"`), passes post-fix. `TestAnthropicMessages_PreservesToolChoiceWithTools` (request `tool_choice:"auto"` + a real tool definition → `tool_choice` must be preserved) — no-regression guard for legitimate client tool-use. Full suite green; `go vet` clean. |
| 3 | **Scope** | One sanitization block in `handleAnthropicMessages`, before routing — covers both the `MessagesProvider` (glm5.2) and OpenAI-conversion forward paths. No engine change, no config change. Non-invasive. Note: glm5.2 does not support Anthropic server-side `web_search_options` (upstream limitation, out of scope) — the fix removes the 502 noise so the request degrades gracefully to plain generation; it does not add web-search capability. |

### v0.8.33 — Close open content blocks on client-cancel: stop CC "Content block not found" (#90)

| # | Fix | Details |
|---|-----|---------|
| 1 | **client-cancel now closes open blocks + synthesizes a terminal** (#90) | Claude Code reported `API Error: Content block not found` (12 recurring `client_canceled` streams 14:25-14:34, all `last_event_idle` 8-139 ms = live streams with a block OPEN — proven by the now-working #88 field). Root cause: `handleStreamAnthropicMessages` (`server.go`) #46 suppression path treated a client cancel as a dead pipe and returned immediately, skipping ALL terminal events — any `content_block_start` left OPEN never got its matching `content_block_stop`, so the Anthropic SDK held an open block it could not finalize → "Content block not found". #71/#75 closed open blocks on the truncation and malformed-terminal paths but explicitly excluded the client-cancel path. Fix: a cancel with the write pipe still alive (`writeFailed==false`) now calls `closeOpenBlocks()` (emits `content_block_stop` per open index, ascending) then synthesizes `message_delta`(`stop_reason:max_tokens` per #77 truncation semantics) + `message_stop`, so the SDK finalizes cleanly. A cancel with the pipe already broken (`writeFailed==true`) stays the #79 behavior — no synth to a dead pipe. |
| 2 | **Tests** | `TestHandleStreamAnthropicMessages_ClosesOpenBlocksOnClientCancel` (hardened loop, backend stalls with block 0 OPEN, cancel after 150 ms → body must contain `content_block_stop` idx 0 before exactly 1 `message_stop`, `stop_reason:max_tokens`) — fails pre-fix (body had no `content_block_stop`), passes post-fix. `TestHandleStreamAnthropicMessages_ClientCancelWriteFailedSkipsSynth` guards the #79 no-regression: a failing writer + cancel must NOT synthesize. `TestHandleStreamMessages_ClientCancelSuppressesSynth` (mid-stream cancel via `newStallingBackend`) and `TestHandleStreamAnthropicMessages_ClientCancelSuppressesMessageStop` (pre-loop cancel) updated to the new behavior. Full server suite (476 tests) green; `go vet` clean. |
| 3 | **Scope** | One path in `handleStreamAnthropicMessages` — the client-cancel branch. Completes the open-block lifecycle trilogy: #71 (truncation), #75 (malformed terminal), #90 (client cancel). `anthropic.go`, `config.yaml`, the `writeFailed` branch, and the post-loop `!sawMessageStop` synth path unchanged. Non-invasive — only the gateway SSE closing logic, no engine change. |

### v0.8.32 — Fix `last_event_idle` field: shadowed var made the #81 stall discriminator useless (#88)

| # | Fix | Details |
|---|-----|---------|
| 1 | **`last_event_idle` now reports the real gap** (#88) | v0.8.29 (#81) added a per-stream `last_event_idle` field to discriminate "response stopped arriving" recurrences: a large idle + `pings>0` + `client_canceled` means the upstream stalled under the watchdog (H-B), while a small idle means CC cancelled a live stream (H-A). But the hardened forward loop (the prod path, `KeepaliveInterval > 0`) declared `lastEventAt := time.Now()` with `:=`, shadowing the outer `lastEventAt` (`server.go`) that `streamSummary` reads. The loop updated only the inner var; the outer stayed zero; the `IsZero()` guard in `streamSummary` forced `last_event_idle=0s` on every stream. Prod confirmed: 4585 summary lines on 08/24, all `last_event_idle=0s` — the field that was supposed to localize upstream stalls never worked. Changed `:=` to `=` so the loop assigns the outer var; `streamSummary` now reads the real last-event time and the gap is reported truthfully. The watchdog's own `time.Since(lastEventAt)` check reads the same var and was already correct (it read the inner var, which the loop updated — now it reads the same now-shared var, behavior unchanged). |
| 2 | **Test** | `TestHandleStreamAnthropicMessages_LastEventIdleNonZeroAfterGap` sends one delta, holds the connection ~150 ms (well past `KeepaliveInterval`, so the hardened loop ticks several pings), then cancels like CC. Asserts the summary's `last_event_idle` is NOT `0s` — the shadow bug returned `0s` here, hiding a 150 ms upstream gap as "upstream was live". The existing `TestHandleStreamAnthropicMessages_StreamSummaryLogged` (clean + cancel paths) still passes. Full server suite (474 tests) green; `go vet` clean. |
| 3 | **Scope** | One-character correctness fix (`:=` → `=`) in `handleStreamAnthropicMessages`, plus the comment. Observability-only — no change to ping frequency, timeouts, retry, synth, or watchdog behavior. `anthropic.go` untouched. The fix restores the diagnostic v0.8.29 promised: the next "response stopped arriving" recurrence will show a real `last_event_idle`, finally separating H-A (CC cancel of a live stream) from H-B (upstream stalled, gateway pinging, CC gave up). |

### v0.8.29 — Per-stream timing observability for "response stopped arriving" (#81)

| # | Fix | Details |
|---|-----|---------|
| 1 | **One INFO summary line per `/v1/messages` stream** (#81) | Claude Code surfaces `API Error: The response stopped arriving` when its internal stall-detection judges an upstream stream dead and cancels it. The gateway then logs only the consequence (`client canceled`) — nothing about the stream timing that led CC there. So a recurrence cannot be localized: was the upstream truly stalled (under the 180 s watchdog, so no trip) while the gateway kept pinging, or did CC cancel a still-live stream? Added per-stream counters to `handleStreamAnthropicMessages` that emit one INFO line on every exit path: `anthropic stream summary model=… duration=… events=… deltas=… pings=… first_event_ttfb=… last_event_idle=… last_event_type=… end_reason=…`. `end_reason` is the key discriminator: `clean` (upstream sent message_stop), `client_canceled` (CC gave up), `write_failed` (#79), `watchdog_tripped` (#69), `ch_closed_no_stop` (synth path). Next recurrence with `end_reason=client_canceled` + large `last_event_idle` + `pings>0` → upstream stalled under the watchdog (H-B); small `last_event_idle` → CC cancelled a live stream (H-A). |
| 2 | **Tests** | `TestHandleStreamAnthropicMessages_StreamSummaryLogged` pins the two most diagnostic paths — a clean stream (`end_reason=clean`, `deltas=1`) and a client-cancelled stall (`end_reason=client_canceled`). Full suite (2652 tests) green; `go vet` clean. |
| 3 | **Scope** | Observability-only — no change to ping frequency, timeouts, retry, or synth behavior; `anthropic.go` untouched. Pure instrumentation in `handleStreamAnthropicMessages`. Follow-up behavior fix deferred until a recurrence's summary line confirms upstream-stall vs CC-side. **Note:** the `last_event_idle` field added here did not actually work until v0.8.32 (#88) — a `:=` shadow in the hardened loop left the outer var zero, so every summary read `last_event_idle=0s`. v0.8.32 fixes the field; only post-v0.8.32 logs carry a trustworthy idle gap. |

### v0.8.28 — Stream forward loop captures client write failures (#79)

| # | Fix | Details |
|---|-----|---------|
| 1 | **Client write errors no longer silently dropped** (#79) | `handleStreamAnthropicMessages` forward loops (backward-compat + hardened keepalive) called `fmt.Fprintf(w, ...)` and `flusher.Flush()` without checking the return error. When the client socket broke mid-response (Claude Code gone, broken pipe), the write error was discarded and the loop kept spinning until the cancelled request ctx fired the `client canceled` branch — so a gateway-side write failure was logged identically to a CC-side disconnect. Both are consistent with the recurring `API Error: Connection lost mid-response`, making the fault impossible to locate. Added a `writeSSE` helper that captures the `fmt.Fprintf` error and flushes; on failure it logs `anthropic stream client write failed` (distinct from `client canceled`), stops the loop, and skips the post-loop synth + cancel-log (the client is already gone). The next recurrence is now diagnosable: `client write failed` → gateway side; only `client canceled` → CC side. |
| 2 | **Tests** | `TestHandleStreamAnthropicMessages_WriteFailureLogged` drives the handler with a `failingResponseWriter` whose `Write` returns a broken-pipe error; asserts the `client write failed` log is emitted, the `client canceled` log is NOT (conflation was the blind spot), and no synthetic `message_stop` is produced. Full suite (2651 tests) green; `go vet` clean. |
| 3 | **Scope** | Observability-only — no behavior change for healthy writes; the failing-write path now fails visibly instead of silently. The same path in `closeOpenBlocks` is covered. No config change. Follow-up behavior fix deferred until logs confirm whether the recurring "Connection lost mid-response" is gateway-side (H2) or CC-side (H1). |

### v0.8.27 — Synth terminal stop_reason: end_turn → max_tokens for truncation (#77)

| # | Fix | Details |
|---|-----|---------|
| 1 | **Synthesized terminal signals truncation, not false completion** (#77) | When litellm→glm5.2 dropped a stream mid-generation without a `message_stop` (EOF/truncation), `handleStreamAnthropicMessages` synthesized a terminal `message_delta` carrying a hardcoded `stop_reason:"end_turn"`. `end_turn` means the model finished its turn naturally (a complete response), but this path is reached only on a **truncated** stream. Claude Code had already received partial text; pairing it with a false-complete `end_turn` made it detect the mismatch (content cut off, yet claimed complete) and surface `API Error: The response stopped arriving. The response above may be incomplete.` Changed the shared post-loop synth path (covers both backward-compat and hardened keepalive loops) to emit `stop_reason:"max_tokens"` — the correct Anthropic truncation signal — so the client retries or continues rather than trusting a false completion. Logged the value for traceability. |
| 2 | **Tests** | `TestHandleStreamAnthropicMessages_SynthesizesMissingMessageStop` now asserts the synthesized `message_delta` carries `"stop_reason":"max_tokens"` and does NOT carry `"stop_reason":"end_turn"`. Full suite green; `go vet` clean. |
| 3 | **Scope** | One-line behavioral change + regression test. No config change. The upstream drop itself is intermittent/transient (direct probes small + large + long up to 119s all healthy — no repro); this is a defensive correctness fix for when a drop does occur. |

### v0.8.26 — Open-block finalization on upstream message_stop (#75)

| # | Fix | Details |
|---|-----|---------|
| 1 | **Forward loops close open blocks before an upstream `message_stop`** (#75) | The #71 open-block finalization was gated behind `if !sawMessageStop`, so it only ran when the upstream ended the stream *without* a terminal `message_stop`. A second intermittent upstream failure mode — litellm→glm5.2 emits `content_block_start` then `message_stop` with no matching `content_block_stop` (a malformed terminal) — was forwarded verbatim; the open-block closing never ran and the SDK threw `API Error: Content block not found` (the #71 symptom, different trigger). Both forward loops now intercept the upstream `message_stop`: if `len(openBlocks) > 0`, a `content_block_stop` is synthesized per open index (ascending) **before** the upstream `message_stop` is forwarded. The upstream `message_stop` is forwarded as-is (no duplicate synth); client-cancel suppression (#46) unchanged. |
| 2 | **Shared `closeOpenBlocks` helper** | Extracted the ascending-sort + per-index `content_block_stop` emit into a closure reused by both forward loops and the post-loop synth path, so open blocks are closed on every stream-end variant (truncation, malformed terminal, watchdog cancel) without duplicated logic. |
| 3 | **Tests** | Added `TestHandleStreamAnthropicMessages_ClosesOpenBlocksOnUpstreamMessageStop` (backward-compat loop: upstream sends idx0+idx1 starts, no stops, then `message_stop` → asserts 2 synthesized stops ascending before `message_stop`, exactly 1 `message_stop`, exactly 1 `message_delta`) and `..._Hardened` (keepalive-enabled loop, idx0 open + upstream `message_stop`). Full suite green; `go vet` clean. |

### v0.8.25 — Connection-phase transport-reset retry covers upstream EOF (#73)

| # | Fix | Details |
|---|-----|---------|
| 1 | **`isRetryableError` matches transport-reset substrings** (#73) | When litellm→glm5.2 dropped the connection during the connection phase (pre-header), Go surfaced it as `io.EOF` / `connection reset by peer`, and the error string read `...Post "...": EOF`. The matcher only knew status codes + `connection refused` / `timeout` / `deadline exceeded`, so the reset matched nothing → retry skipped → direct 502 → claude code required a manual `continue`. Added `EOF` (covers `io.EOF` and `unexpected EOF`) and `connection reset by peer` (Go net-package TCP-RST string) to `isRetryableError`. A connection-phase reset now transparently retries up to `max_retries` (7) with backoff instead of a hard 502. 25 EOFs logged 08/20–08/23 (predates #71; verified non-gateway by live cloud-path probing). |
| 2 | **Mid-stream EOF unchanged** | The fix touches only the connection phase. `StreamMessages` is two-phase: a `Do` error (pre-header) returns `nil, err` → retried; once a 200 arrives a channel + parse goroutine is returned → a mid-stream `EOF` only closes the channel (no error return) and runs the open-block finalization (#71), never re-dispatched. The retry never sees an in-flight stream, so mid-stream behavior is byte-for-byte unchanged. |
| 3 | **No config change** | `routing.retry.max_retries: 7` / `initial_backoff: 10s` / `max_backoff: 1m` / `retryable_status_codes: [429,500,502,503]` already configured; the new substrings take effect immediately. |
| 4 | **Tests** | Added `TestIsRetryableError_EOF` (matches the exact prod error string `...Post "...": EOF`), `TestIsRetryableError_UnexpectedEOF`, `TestIsRetryableError_ConnectionResetByPeer`, and the end-to-end `TestRetryStreamMessages_RetriesOnEOF` (first call returns the prod EOF error, second returns a successful channel → asserts 2 calls + non-nil channel). Full suite green; `go vet` clean. |

### v0.8.24 — Synth message_stop now closes open content blocks (#71)

| # | Fix | Details |
|---|-----|---------|
| 1 | **Synthetic terminal sequence closes open content blocks** (#71) | `handleStreamAnthropicMessages` synthesized a bare `message_stop` when the upstream ended without one (upstream truncation or the #69 idle watchdog tripping mid-block). The Anthropic SDK requires every `content_block_start` to be closed by a matching `content_block_stop` **before** `message_stop`; an open block at stream end threw `API Error: Content block not found` after long (28m+) sessions. The forward loops now track open block indices (`content_block_start` adds, `content_block_stop` removes) and the synth path emits a `content_block_stop` per open index (ascending), then a `message_delta` (`stop_reason: end_turn`), then `message_stop`. Defense-in-depth: closes whatever the upstream left open. The client-cancel suppression (#46) is unchanged. |
| 2 | **Upstream malformed-SSE note** (#71) | Probed litellm→glm5.2 directly: simple + truncating tool_use streams are balanced (2 start / 2 stop), but the defect is intermittent under long-session pressure. Gateway forwards the SSE sequence faithfully (parser + forward loop pure pass-through, `MarshalJSON` preserves `index` even 0, `Delta` is `json.RawMessage` verbatim). Filed as an upstream concern; the gateway-side finalization above covers it regardless. |
| 3 | **Tests** | Updated `TestHandleStreamAnthropicMessages_SynthesizesMissingMessageStop` to assert the open block is closed before `message_stop`. Added `TestHandleStreamAnthropicMessages_SynthClosesMultipleOpenBlocks` (thinking index 0 + text index 1, both closed ascending) and `TestHandleStreamAnthropicMessages_WatchdogClosesOpenBlocks` (watchdog trips with block open → closed). Full suite passes; `go vet` clean. |

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
