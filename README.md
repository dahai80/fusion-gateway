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
|- Cloud: Volcengine / Qianfan / Claude / OpenAI
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
| `route.token_threshold` | 8000 | Token count threshold: below = local, above = cloud |
| `route.enable_hardware_judge` | true | Enable hardware-aware routing |
| `route.local_max_memory_ratio` | 0.9 | Max system memory ratio before forcing cloud |
| `route.local_max_mlx_memory_ratio` | 0.7 | Max MLX/GPU memory ratio before forcing cloud |
| `route.circuit_breaker.failure_threshold` | 5 | Consecutive failures before circuit opens |
| `hardware.collect_interval` | 2s | Hardware metrics collection interval |
| `hardware.mlx_metrics.enabled` | true | Collect fusion-mlx /metrics |
| `hot_reload.enabled` | true | Enable config file hot reload |
| `hot_reload.breaker_drain_timeout` | 10s | Wait time for in-flight drain before applying config |
| `hot_reload.breaker_warmup_success` | 3 | Success count to close breaker after warmup |

## API Endpoints

| Endpoint | Method | Description |
|----------|--------|-------------|
| `/v1/chat/completions` | POST | OpenAI-compatible chat completions (stream + non-stream) |
| `/v1/embeddings` | POST | Text embeddings (local-first, cluster sharding for batch) |
| `/v1/rerank` | POST | Rerank documents (cloud-default, local when model available) |
| `/v1/realtime` | WebSocket | Realtime API proxy (bidirectional WebSocket relay) |
| `/v1/models` | GET | List available models |
| `/health` | GET | Full health check with backend status |
| `/healthz` | GET | Liveness probe |
| `/readyz` | GET | Readiness probe (circuit breaker + health + GPU memory + queue depth + success rate) |
| `/livez` | GET | Liveness probe |
| `/v1/status` | GET | Detailed status (hardware, circuit breakers, stats) |
| `/metrics` | GET | Prometheus metrics |
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
| P6 | Model availability | Model not found locally | Try cluster → cloud |
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
  router/             Routing decision engine + per-backend circuit breaker
  adapter/            Provider interface + fusion-mlx + openai-compatible adapters + pool
  cluster/            Cluster node discovery, health check, load balancing, node adapter
  middleware/         Auth, RequestID, CORS, config snapshot injection
  observability/      Prometheus metrics
  server/             HTTP server + route registration + SSE forwarding
config.example.yaml   Example configuration
```

## Fusion Ecosystem

| Project | Role |
|---------|------|
| fusion-mlx | Local MLX inference engine (primary local backend) |
| fusion-gateway | **This project** - Inference routing gateway |
| fusion-desk | Desktop automation platform |
| fusion-studio | macOS native SwiftUI client |
| fusion-model-hub | Model repository and management |
