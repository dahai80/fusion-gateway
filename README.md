# Fusion-Gateway

Unified hybrid inference gateway for Apple Silicon local inference + cloud LLMs.

Core traffic entry point for Fusion-Agent Studio, Fusion-MLX, and Fusion-Coder. Written in **Go** to avoid competing with fusion-mlx for UMA memory.

## Architecture

```
Clients (VSCode/UI/CLI/Agent)
        |
Fusion-Gateway :8100
|- Ingress Layer       -- Auth, parsing, standardization, rate limiting
|- Preprocessing       -- Tokenizer counting, prompt validation, param defaults
|- Routing Engine      -- Rule engine + hardware load sensing (core differentiator)
|- Adapter Pool        -- Unified interface for all inference backends
|- Stream Forwarding   -- SSE, cancel, retry, KV cache release
|- Observability       -- Logs, metrics, hot config reload
        |
Heterogeneous Inference Pool
|- Local: fusion-mlx (:11434) / llama.cpp
|- Private: vLLM-ascend / vLLM-cuda
|- Cloud: Volcengine / Qianfan / Claude / OpenAI / DeepSeek / OpenRouter
```

## Quick Start

```bash
# Build
go build -o fusion-gateway ./cmd/gateway

# Copy and edit config
cp config.example.yaml config.yaml

# Run
./fusion-gateway --config config.yaml
```

## Configuration

See `config.example.yaml` for full reference. Key settings:

| Key | Default | Description |
|-----|---------|-------------|
| `server.port` | 8100 | Gateway listen port |
| `auth.enabled` | true | Enable API key authentication |
| `auth.master_key` | "" | Master key bypasses rate limits and model allowlists |
| `route.token_threshold` | 8000 | Token count threshold: below = local, above = cloud |
| `route.enable_hardware_judge` | true | Enable hardware-aware routing |
| `route.local_max_memory_ratio` | 0.9 | Max system memory ratio before forcing cloud |
| `route.local_max_mlx_memory_ratio` | 0.7 | Max MLX/GPU memory ratio before forcing cloud |
| `route.circuit_breaker.failure_threshold` | 5 | Consecutive failures before circuit opens |
| `route.rate_limit.enabled` | true | Enable per-key RPM/TPM rate limiting |
| `route.retry.max_retries` | 2 | Max retry attempts for non-streaming requests |
| `route.fallback.context_window_fallback` | {} | Model → larger model mapping for context overflow |
| `cache.enabled` | true | Enable LRU response cache for non-streaming |
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

## API Endpoints

| Endpoint | Method | Description |
|----------|--------|-------------|
| `/v1/chat/completions` | POST | OpenAI-compatible chat completions (stream + non-stream) |
| `/v1/completions` | POST | Legacy completions (auto-converted to chat format) |
| `/v1/embeddings` | POST | Text embeddings (local-first, cluster sharding for batch) |
| `/v1/rerank` | POST | Rerank documents (cloud-default, local when model available) |
| `/v1/cost` | GET | Cost tracking summary (optional `?key=<name>` filter) |
| `/v1/realtime` | WebSocket | Realtime API proxy (bidirectional WebSocket relay) |
| `/v1/models` | GET | List available models |
| `/health` | GET | Full health check with backend status |
| `/healthz` | GET | Liveness probe |
| `/readyz` | GET | Readiness probe (circuit breaker + health + GPU memory + queue depth + success rate) |
| `/livez` | GET | Liveness probe |
| `/v1/status` | GET | Detailed status (hardware, circuit breakers, stats) |
| `/metrics` | GET | Prometheus metrics |
| `/v1/images/generations` | POST | Image generation (cloud-only, OpenAI-compatible) |
| `/v1/messages` | POST | Anthropic Messages API (native format + auto-convert to OpenAI) |
| `/v1/audio/transcriptions` | POST | Audio transcription (Whisper-compatible, cloud-only) |
| `/v1/audio/speech` | POST | Text-to-speech synthesis (cloud-only) |
| `/v1/moderations` | POST | Content moderation (cloud-only) |
| `/admin/gc` | POST | Trigger safe GC on fusion-mlx (only when in-flight = 0) |
| `/admin/config/reload` | POST | Config reload notification |

## Routing Logic

Dual-dimension decision: **request dimension** + **hardware load dimension**

Priority chain (high to low), three-tier fallback: **local → cluster → cloud**

| Priority | Rule | Condition | Target |
|----------|------|-----------|--------|
| P0 | Circuit breaker | Local breaker is Open | Try cluster → cloud |
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

- **Embedding**: Routes locally when breaker closed + local ready. For large batches (>32 inputs), uses cluster sharding automatically.
- **Rerank**: Routes to cloud by default since local MLX typically doesn't host rerank models. If a model with "rerank"/"reranker" in the name is available locally, routes there instead.

### Realtime API

The gateway proxies OpenAI Realtime API (`/v1/realtime`) via bidirectional WebSocket relay:

1. Client connects to `ws://gateway:8100/v1/realtime`
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
| `expires_at` | RFC3339 expiry timestamp |
| `budget_limit` | Monthly spend limit in USD (0 = unlimited) |

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
- **Streaming**: Native Anthropic SSE events (`message_start`, `content_block_delta`, `message_delta`, `message_stop`)
- **Thinking**: Supports `thinking` parameter with `budget_tokens` for extended thinking

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

**Request Log Pipeline**: Every request is automatically logged with full metadata:
- Request ID, model, channel, route reason, token counts, cost, latency, TTFT
- Ring buffer storage with configurable max length
- Filterable by time range, key, model, channel, status, token/cost thresholds
- Exportable to JSON

**Frontend**: React + Ant Design + Vite SPA embedded in Go binary.

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
| `/debug/pprof/` | GET | Go pprof profiling index |
| `/debug/pprof/profile` | GET | CPU profile |
| `/debug/pprof/trace` | GET | Execution trace |

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

# Lint
golangci-lint run

# Build
go build -o fusion-gateway ./cmd/gateway
```

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
  store/memory/       In-memory store implementation (ring buffer logs, CRUD, analytics aggregation)
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

**Differentiator**: The only AI gateway with **hardware-aware routing visualization** and **local inference savings tracking**.

## Fusion Ecosystem

| Project | Role |
|---------|------|
| fusion-mlx | Local MLX inference engine (primary local backend) |
| fusion-gateway | **This project** - Inference routing gateway |
| fusion-desk | Desktop automation platform |
| fusion-studio | macOS native SwiftUI client |
| fusion-model-hub | Model repository and management |
