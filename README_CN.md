# Fusion-Gateway

**[English](README.md) | 中文**

面向 Apple Silicon 本地推理 + 云端大模型的统一混合推理网关。

Fusion-Agent Studio、Fusion-MLX、Fusion-Coder 的核心流量入口。使用 **Go** 编写,避免与 fusion-mlx 争夺 UMA 内存。

## 架构

```
Clients (VSCode/UI/CLI/Agent)
        |
Fusion-Gateway :11432
|- Ingress Layer       -- 鉴权、解析、标准化、限流
|- Preprocessing       -- Tokenizer 计数、prompt 校验、参数默认值
|- Routing Engine      -- 规则引擎 + 硬件负载感知 (核心差异点)
|- Adapter Pool        -- 所有推理后端的统一接口
|- MCP Gateway         -- MCP 集群工具注册、路由、token 预算
|- Stream Forwarding   -- SSE、取消、重试、KV cache 释放
|- Observability       -- 日志、指标、热配置重载
        |
异构推理池
|- 本地: fusion-mlx (:11434) / llama.cpp
|- 私有: vLLM-ascend / vLLM-cuda
|- 云端: Volcengine / Qianfan / Claude / OpenAI / DeepSeek / OpenRouter / AWS Bedrock / GCP Vertex / Azure Foundry
|- 云端 (国内): DashScope / Moonshot / Zhipu / Minimax / Baichuan / Hunyuan / StepFun / Yi
```

## 快速开始

```bash
# 构建
go build -o fusion-gateway ./cmd/gateway

# 复制并编辑配置
cp config.example.yaml config.yaml

# 运行 (自动在 :11434 启动 fusion-mlx,网关在 :11432)
./fusion-gateway --config config.yaml
```

网关默认监听 **11432 端口**。当 `auto_start.enabled: true` 时,会在启动时自动拉起 11434 端口的 fusion-mlx,并等待其健康就绪后再开始对外服务。

## 配置

完整参考见 `config.example.yaml`。关键配置项:

| 键 | 默认值 | 说明 |
|-----|---------|-------------|
| `server.port` | 11432 | 网关监听端口 |
| `server.auto_start.enabled` | true | 网关启动时自动拉起 fusion-mlx |
| `server.auto_start.command` | `~/claude-home/fusion-mlx/start.sh start` | 启动本地后端的 shell 命令 |
| `server.auto_start.stop_cmd` | `~/claude-home/fusion-mlx/start.sh stop` | 关闭时停止本地后端的 shell 命令 |
| `server.auto_start.wait_url` | `http://127.0.0.1:11434/health` | 启动后轮询的健康检查 URL |
| `server.auto_start.wait_secs` | 120 | 健康检查最长等待秒数 |
| `auth.enabled` | true | 启用 API key 鉴权 |
| `auth.master_key` | "" | master key 绕过限流和模型白名单 |
| `route.token_threshold` | 8000 | token 数阈值:低于走本地,高于走云端 |
| `route.output_input_ratio_threshold` | 0.6 | 预测输出/input token 比例上限,超过则路由云端 (低于 `output_input_ratio_min_input_tokens` 时跳过) |
| `route.output_input_ratio_min_input_tokens` | 32 | output/input ratio 判据的最小 input token 数;低于此值跳过 ratio (避免极小请求误判, #48) |
| `route.mode` | hybrid | 路由模式:`local` (全本地)、`cloud` (全云端)、`hybrid` (按 token/比例/硬件智能路由) |
| `route.enable_hardware_judge` | true | 启用硬件感知路由 |
| `route.local_max_memory_ratio` | 0.9 | 系统内存占比上限,超过则强制走云端 |
| `route.local_max_mlx_memory_ratio` | 0.7 | MLX/GPU 内存占比上限,超过则强制走云端 |
| `route.circuit_breaker.failure_threshold` | 5 | 连续失败次数,超过则熔断器打开 |
| `route.rate_limit.enabled` | true | 启用按 key 的 RPM/TPM 限流 |
| `route.retry.max_retries` | 2 | 非流式请求最大重试次数 |
| `route.fallback.context_window_fallback` | {} | 模型 → 更大模型的映射,用于上下文溢出回退 |
| `route.fallback.enabled` | false | 启用下方 `model_mapping`(别名 → 云端模型 id) |
| `route.fallback.model_mapping` | {} | 将客户端/SDK 模型别名(如 `claude-opus-4-7`)映射到云端后端真实模型 id(如 `glm5.2`);在转发前应用于 `/v1/messages` 和 `/v1/chat/completions`(避免上游 400 → 502 "response stopped arriving",#52) |
| `cache.enabled` | true | 启用非流式响应的 LRU 缓存 |
| `cache.backend` | local | 缓存后端:local (LRU) 或 redis |
| `cache.redis.addr` | localhost:6379 | Redis 地址 (backend=redis 时) |
| `cache.warmup_file` | "" | 启动时预加载缓存的 JSON 文件 |
| `cache.ttl` | 5m | 缓存条目 TTL |
| `cost.enabled` | true | 启用带内置定价的成本跟踪 |
| `pii.enabled` | false | 启用请求内容的 PII 检测 |
| `pii.action` | log | PII 处理动作:log、mask 或 deny |
| `cloud_routing.strategy` | round-robin | 云端路由策略:latency、cost、weight、least-busy、round-robin |
| `hardware.collect_interval` | 2s | 硬件指标采集间隔 |
| `hardware.mlx_metrics.enabled` | true | 采集 fusion-mlx /metrics |
| `hot_reload.enabled` | true | 启用配置文件热重载 |
| `hot_reload.breaker_drain_timeout` | 10s | 应用配置前等待在途请求排空的时间 |
| `hot_reload.breaker_warmup_success` | 3 | warmup 后关闭熔断器所需的成功次数 |
| `admin.enabled` | true | 启用管理后台与 API |
| `admin.log_max_len` | 10000 | 请求日志最大条数 (环形缓冲) |
| `admin.jwt_secret` | "" | 管理后台鉴权的 JWT 签名密钥 |
| `cost.pricing_file` | "" | 自定义定价 YAML,支持热重载 |
| `observability.otel_enabled` | false | 启用 OpenTelemetry 链路追踪 |
| `observability.otel_endpoint` | localhost:4317 | OTel collector 端点 |
| `observability.otel_protocol` | grpc | OTel 导出协议:grpc 或 http |
| `observability.otel_service_name` | fusion-gateway | OTel 链路中的服务名 |
| `rbac.enabled` | false | 启用 RBAC 基于角色的访问控制 |
| `rbac.default_role` | viewer | 无 OIDC claims 时的默认角色:admin、editor、viewer |
| `team.enabled` | false | 启用团队/组织管理 |
| `team.default_team` | default | 未分配 key 的默认团队 |
| `semantic_cache.enabled` | false | 启用语义缓存 (基于相似度命中) |
| `semantic_cache.similarity_threshold` | 0.92 | 缓存命中的余弦相似度阈值 |
| `semantic_cache.max_entries` | 5000 | 语义缓存最大条目数 |
| `prompt_injection.enabled` | false | 启用 prompt 注入检测 |
| `prompt_injection.action` | log | 检测到时的动作:log 或 block |
| `cost_markup.enabled` | false | 启用成本加价 (计费利润) |
| `cost_markup.global_markup` | 0 | 全局加价比率 (0.2 = 20% 附加费) |
| `batch.enabled` | false | 启用 /v1/batches API |
| `batch.max_batch_size` | 100 | 每批最大请求数 |
| `server.tls.cert_file` | "" | TLS 证书文件 (启用 HTTPS) |
| `server.tls.key_file` | "" | TLS 私钥文件 |
| `encryption.master_key` | "" | 静态加密的 AES-256 主密钥 (≥32 字符) |
| `connector.persistence_path` | data/connections.json | 连接凭证文件路径 |
| `mcp.enabled` | false | 启用 MCP 集群网关 |
| `mcp.host` | 127.0.0.1 | MCP 网关主机 |
| `mcp.port` | 11446 | MCP 网关端口 |
| `mcp.token_budget` | 10000000 | MCP 工具调用的 token 预算 |
| `mcp.max_requests` | 10000 | 最大跟踪 MCP 请求数 |
| `mcp.node_port` | 11445 | 转发用的远端节点端口 |
| `mcp.local_port` | 9000 | 本地插件服务端口 |

## API 端点

| 端点 | 方法 | 说明 |
|----------|--------|-------------|
| `/v1/chat/completions` | POST | OpenAI 兼容的 chat completions (流式 + 非流式) |
| `/v1/completions` | POST | 传统 completions (自动转换为 chat 格式) |
| `/v1/embeddings` | POST | 文本 embedding (本地优先,批量时分片到集群) |
| `/v1/rerank` | POST | 文档重排 (默认走云端,本地有模型时走本地) |
| `/v1/cost` | GET | 成本跟踪汇总 (可选 `?key=<name>` 过滤) |
| `/v1/realtime` | WebSocket | Realtime API 代理 (双向 WebSocket 中继) |
| `/v1/models` | GET | 列出可用模型 (并发按 provider 拉取,各 3s 超时,失败跳过;`route.mode: local` 时仅列本地 provider) |
| `/v1/models/{id}/load` | POST | 加载模型 (被拦截 → model-hub `POST /api/v1/models/{id}/serve`) |
| `/v1/models/{id}/unload` | POST | 卸载模型 (被拦截 → model-hub `POST /api/v1/models/{id}/serve`) |
| `/health` | GET | 含后端状态的完整健康检查 |
| `/healthz` | GET | 存活探针 |
| `/readyz` | GET | 就绪探针 (熔断器 + 健康 + GPU 内存 + 队列深度 + 成功率) |
| `/livez` | GET | 存活探针 |
| `/v1/status` | GET | 详细状态 (硬件、熔断器、统计) |
| `/metrics` | GET | Prometheus 指标 (需 `master_key` 鉴权) |
| `/v1/images/generations` | POST | 图像生成 (仅云端,OpenAI 兼容) |
| `/v1/messages` | POST | Anthropic Messages API (原生格式 + 自动转换为 OpenAI) |
| `/v1/audio/transcriptions` | POST | 音频转写 (Whisper 兼容,仅云端) |
| `/v1/audio/speech` | POST | 文本转语音合成 (仅云端) |
| `/v1/moderations` | POST | 内容审核 (仅云端) |
| `/admin/gc` | POST | 触发 fusion-mlx 安全 GC (仅在在途数 = 0 时) |
| `/admin/config/reload` | POST | 配置重载通知 |
| `/admin/teams` | GET/POST | 列出/创建团队 (仅 admin) |
| `/admin/teams/{id}` | GET/PUT/DELETE | 团队 CRUD (仅 admin) |
| `/admin/orgs` | GET/POST | 列出/创建组织 (仅 admin) |
| `/admin/orgs/{id}` | GET/DELETE | 组织 CRUD (仅 admin) |
| `/v1/batches` | POST/GET | 创建/列出批次 |
| `/v1/batches/{id}` | GET | 查询批次状态 |
| `/v1/batches/{id}/cancel` | POST | 取消运行中的批次 |
| `/gateway/v1/connector/list` | GET | 列出已注册 connector 及其 actions |
| `/gateway/v1/connector/test` | POST | 测试 action 执行 (无真实副作用) |
| `/gateway/v1/connector/{key}/action/{action}` | POST | 执行 connector action |
| `/gateway/v1/connection` | GET/POST | 列出/创建连接 |
| `/gateway/v1/connection/{id}` | GET/DELETE | 获取/删除连接 |
| `/gateway/v1/connection/{id}/refresh` | POST | 刷新连接授权 |
| `/gateway/v1/oauth2/authorize` | POST | 生成 OAuth2 授权 URL |
| `/gateway/v1/oauth2/callback` | GET | OAuth2 回调 — 用 code 换 token |
| `/mcp/v1/tools` | GET | 列出已注册 MCP 工具 |
| `/mcp/v1/tools/register` | POST | 注册 MCP 工具 |
| `/mcp/v1/tools/unregister` | POST | 注销 MCP 工具 |
| `/mcp/v1/call` | POST | 调用 MCP 工具 (转发到节点) |
| `/mcp/v1/stats` | GET | MCP 网关统计 |
| `/mcp/v1/health` | GET | MCP 网关健康检查 |
| `/api/v1/` | ANY | model-hub 反向代理 (带模块权限校验) |
| `/admin/api/fine-tune/*` | ANY | fusion-mlx admin 微调 API 反向代理 (#30) — 任务 CRUD、SSE 进度流、adapters;与 `/v1/*` 同一套 fg-key 鉴权链,网关内部注入 `X-Fusion-Route` |

## 路由逻辑

双维度决策:**请求维度** + **硬件负载维度**

### Header 注入

| Header | 目标 | 值 | 行为 |
|--------|--------|-------|----------|
| `X-Fusion-Route` | fusion-mlx | `gateway-decision` | 所有转发请求均注入;入站 `X-Fusion-Route` 透传 |
| `X-Fusion-Source` | model-hub | `gateway` | 所有代理请求均注入 |

优先级链 (由高到低),三级回退:**本地 → 集群 → 云端**

| 优先级 | 规则 | 条件 | 目标 |
|----------|------|-----------|--------|
| P-1 | 语义意图 | 分类器返回 heavy/diffusion 且置信度 ≥ 阈值 | 平台集群节点 → 云端 |
| P0 | 熔断器 | 本地熔断器 Open | 尝试集群 → 云端 |
| P0.3 | 会话亲和 | 同一 `X-Space-Id` 之前出现过 | 路由到同一 provider (复用 KV cache) |
| P0.5 | 指标采集错误 | 硬件指标不可用 | 尝试集群 → 云端 |
| P1 | 系统内存 | 已用占比 > 阈值 | 尝试集群 → 云端 + 触发熔断 |
| P1.5 | MLX 内存 | MLX/GPU 占比 > 阈值 | 尝试集群 → 云端 |
| P2 | Swap 抖动 | 换页速率 > 阈值 | 尝试集群 → 云端 + 触发熔断 |
| P2.5 | GPU 内存不足 | 可用 < 已分配的 20% | 尝试集群 → 云端 |
| P3 | 本地未就绪 | 后端健康检查失败 | 尝试集群 → 云端 |
| P4 | token 预算 | 输入 token > 阈值 | 云端 |
| P5 | 并发上限 | 在途 > 最大并发 | 尝试集群 → 云端 |
| P6 | 模型可用性 | 本地找不到模型 | 上下文窗口回退 → 集群 → 云端 |
| P6.5 | 上下文窗口回退 | 本地无该模型但有更大变体 | 路由到更大的本地模型 |
| P7 | 本地优先 | 所有检查通过 | 路由到本地 |

### 语义意图路由 (D4)

网关从静态转发演进为语义调度中心。一个轻量意图分类器 (`fusion-router-light` 1B LoRA adapter,基座 `mlx-community/Llama-3.2-1B-Instruct-4bit`,上游在 fusion-trainer#11 训练) 检查每个 chat 请求,在 P0–P7 规则链**之前**按意图分发。**默认关闭** — 关闭时 `NoopClassifier` 让 P-1 成为空操作,既有行为不变。

分类器通过本地 fusion-mlx 的 `/v1/chat/completions` 端点调用已加载的 LoRA adapter (用 OpenAI `adapters` 字段热加载派生引擎),配合训练好的分类 prompt。它输出五种任务类型标签之一 — `code` / `chat` / `math` / `translate` / `summary` — 全部轻量/本地可承担,因此各自映射为 `IntentLightweight`。重模型与扩散意图仍由规则链 + 平台路由处理。分类器**失败放行**:任何传输错误、超时或未识别标签时,语义层回退到规则链,路由永不中断。

| 意图 | 目标 |
|--------|--------|
| `lightweight` | 回退到规则链 (规则链已将健康短请求路由到 Mac 本地) |
| `heavy_model` | `windows-cuda` (或配置的平台) 上的集群节点 → 云端回退 |
| `diffusion` | `windows-cuda` (或配置的平台) 上的集群节点 → 云端回退 |
| `unknown` / 低置信度 / 分类器错误 | 回退到规则链 |

> **前提:** fusion-mlx 启动时需预设
> `FUSION_LORA_ALLOWED_DIRS=~/.fusion-mlx/adapters` (见 fusion-mlx#394
> — adapters-dir 自动添加会与 EnginePool 初始化竞态)。否则分类器调用失败放行,由规则链照常路由。

```yaml
routing:
  intent_classifier:
    enabled: false              # 默认关闭;fusion-router-light 部署后设为 true
    endpoint: "http://127.0.0.1:11434"
    base_model: "mlx-community/Llama-3.2-1B-Instruct-4bit"
    adapter: "/Users/dahai/.fusion-mlx/adapters/mlx-community--Llama-3.2-1B-Instruct-4bit/router-light-1b-intent-v3"
    api_key: ""                 # 启用鉴权时的 fusion-mlx auth.api_key
    timeout: 2s
    min_confidence: 0.7

cluster:
  platform_routing:
    enabled: false              # 默认关闭
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

语义层支持热重载:在配置中切换 `intent_classifier.enabled` 无需重启即生效。

### 启发式代码意图快路径 (<20ms)

上方的 LLM 分类器是一次完整模型调用(2s 超时、无缓存、每请求都跑)——对默认关闭的语义层尚可接受,但一旦启用就成为网关端到端开销的主导项。针对**代码**意图(vibe-coding / 重构 / 调试工作流中的最热路径),一个确定性的进程内启发式分类器替代了这次 LLM 调用,将网关开销控制在 20ms 以内。

`HeuristicClassifier` 在每个 chat 请求中先于 LLM 分类器执行。它用无需模型推理的信号打分——模型名含 `code`、围栏代码块、语言关键字、文件扩展名、代码动作动词、错误术语、以及非空 `tools` 数组——当分数达到 `min_confidence` 时返回 `IntentCode`。引擎随后路由到 `LocalBackend`(fusion-mlx),并通过 per-request `adapters` 字段热挂载 LoRA 代码 adapter(如 `lora-code`;`FUSION_LORA_INPLACE_SWAP=1` 下 ms 级热切,无 base 重载)。非代码意图则继续落到 LLM 分类器(若启用)再落规则链,行为不变。

结果按 sha256 key(模型 + tools 标志 + 文本前缀)缓存在带 TTL 的有界 LRU 中,重复请求连正则扫描都跳过。Apple M5 Max 基准:**~0.8µs/op** 稳态(命中缓存)——比 20ms 预算留有 ~24000× 余量。

> **启发式 vs LLM 分类器:**启发式是针对代码意图的延迟杠杆,不替代 LLM 分类器对 heavy/diffusion/translate 的派发。两者可同时启用——启发式先捕获代码,LLM 分类器处理其余。

```yaml
routing:
  heuristic_classifier:
    enabled: false            # 默认关闭;规则链照常路由
    code_adapter: "lora-code" # 传入 per-request "adapters" 字段的 LoRA adapter 名
    cache_size: 4096          # LRU 条目数(0 禁用缓存)
    cache_ttl: 5m             # 条目过期;0 = 永不过期
    min_confidence: 0.6       # 判定为代码意图的分数阈值
    text_scan_bytes: 4096     # 正则扫描的请求文本前缀上限
```

快路径支持热重载:切换 `heuristic_classifier.enabled` 无需重启即生效。`adapters` 与 `response_format` 字段经 `ChatRequest` 透传到 fusion-mlx(对云端 provider 透明),故 OpenAI 风格的约束解码也能直达本地引擎,无需网关解释。

> **fusion-mlx 前置条件:**需设 `FUSION_LORA_INPLACE_SWAP=1` 才能实现真正的 ms 级原地 adapter 切换(否则回退为 base 重载);adapter 请求前 base 模型须已预加载(`POST /v1/models/{id}/load`),因为原地切换要求 base 已驻留。

### 出站 UDS 到本地后端

对本地热路径,网关可经 Unix Domain Socket 而非 TCP 与 fusion-mlx 通信——绕过 TCP 栈可从网关端到端开销预算中省去连接建立延迟。在 fusion-mlx 后端设 `socket_path`,并以 `--host unix:/run/fusion-mlx.sock` 启动 fusion-mlx(fusion-mlx#351 起支持)。此时 `base_url` 变为哑主机(约定 `http://unix/`);transport 拨号到 socket,忽略主机。

同一 transport 工厂还为高 QPS 本地流量调优连接池——`MaxIdleConnsPerHost` 64(Go 默认为 2,会饿死繁忙的本地后端并强制重拨)。该池调优在 `socket_path` 为空(纯 TCP)时同样生效,故每个本地后端都受益。

```yaml
backends:
  fusion-mlx:
    type: "fusion-mlx"
    base_url: "http://unix/"          # 哑主机;transport 拨号到 socket_path
    socket_path: "/run/fusion-mlx.sock"  # 空(默认)= 纯 TCP 到 base_url
    enabled: true
```

> fusion-mlx 须以匹配的 `--host unix:/run/fusion-mlx.sock` 启动,且 socket 文件对网关进程可读写。macOS 上 socket 路径须保持在 ~104 字节 `SUN_LEN` 上限内。

### 入站 UDS 监听器

与出站路径对称,网关也可**监听** Unix Domain Socket,使本地客户端(fusion-code、CLI、同机 agent 回路)不经 TCP 栈即可访问。绑定前会移除上次非正常关闭残留的 socket 文件;关闭时关闭监听器并 unlink socket 文件。TCP 监听器与之并存(管理后台、健康探针、远程客户端),UDS 纯粹是额外的低延迟入口。

```yaml
server:
  unix_socket:
    enabled: true
    path: "/var/run/fusion-gateway.sock"  # 启用时必填
    mode: 0660                             # 0(默认)= 0660
```

用 curl 连接:

```sh
curl --unix-socket /var/run/fusion-gateway.sock http://unix/v1/chat/completions \
  -H "Content-Type: application/json" \
  -d '{"model":"<base>","messages":[{"role":"user","content":"hello"}]}'
```

> 与 `server.auto_start`(启动 fusion-mlx)和 `backends.*.socket_path`(出站)三者相互独立:可任意组合 TCP/UDS 入站与 TCP/UDS 出站。

### LoRA Adapter 索引(Stream D)

启发式代码路径将 code 意图派发到 LocalBackend,并挂载配置的 `code_adapter`(如 `lora-code`),对 fusion-mlx 做 per-request LoRA 热切。为校验该 adapter 在后端确实存在,网关维护内存中的 `AdapterIndex`,定时轮询 fusion-mlx 的 `GET /admin/api/fine-tune/adapters` 端点(已有的 admin 代理目标,现由网关直接消费)。索引从同一个 `backends.fusion-mlx` 配置(`base_url`、`api_key`、`socket_path`)构建,因此拉取与推理流量走同一传输层(TCP 或 UDS)。

- **刷新**:后台 goroutine(`lora_index_refresh`)启动时拉取一次,之后每 60s 刷新一次(与 `refresh_model_set` 节奏一致)。配置热重载时,若 fusion-mlx 后端配置有变,索引重建并立即刷新,新发布的 adapter 无需重启即可生效。
- **校验**:在路由引擎中,当 code 意图解析出 adapter 后,对索引做尽力校验。若条目缺失,记一条 warn 日志但**不阻断**派发——索引可能已过期,fusion-mlx 在 adapter 确实不存在时会自行报热切错误。未配置 fusion-mlx 后端或索引从未刷新成功时,跳过校验。
- **响应 schema**:fusion-mlx 返回裸 JSON 数组 `{adapter_name, model_id, has_weights, has_config, lora_rank}`;解码上限 10 MiB(防 OOM,与 SSE linebuf 上限一致)。

无新增配置项:索引全部派生自既有 `backends.fusion-mlx` 条目与 `routing.heuristic_classifier.code_adapter`。此为第一版索引源;长期路径(fusion-trainer 将 adapter 发布至 fusion-model-hub,网关经 webhook 消费 `GET /api/v1/models?model_type=lora`)列为上游工作,不阻塞本版。

### 入站 Model-Hub Webhook

为在 60s `AdapterIndex` 轮询之外即时感知新发布的 LoRA adapter,网关暴露 fusion-model-hub 生命周期事件的入站 webhook 接收端:

```
POST /webhooks/model-hub
```

- **鉴权**:对原始请求体做 HMAC-SHA256,用 `routing.webhooks.model_hub.secret` 校验。发送方(fusion-model-hub 的 `_sign_payload`)设置 `X-Webhook-Signature`(十六进制)与 `X-Webhook-Event` 头;网关用恒定时间比较重算 MAC,不匹配返回 401。该路径独立于 fg-key 鉴权链——webhook 不经 `withMiddleware`。
- **信封**:`{"event": "<类型>", "data": {...}}`。解码上限 1 MiB(防 OOM,与 SSE linebuf 上限一致)。
- **刷新触发**:收到 `adapter.*` 事件(如 `adapter.published`、`adapter.merged`)时,接收端立即触发一次 `AdapterIndex` 刷新(与 60s 轮询同一刷新路径)。非 adapter 事件(`model.created`、`version.published` 等)仅确认(200)并记日志,不触发刷新。刷新失败记日志但仍返回 200,避免发送方重试风暴。
- **配置**(位于 `routing.webhooks.model_hub`):

  | 键 | 默认 | 说明 |
  |-----|---------|-------------|
  | `enabled` | `false` | 注册 `POST /webhooks/model-hub` 路由。默认关闭以保持向后兼容。 |
  | `secret` | `""` | 共享 HMAC 密钥。`enabled=true` 时必填(加载时校验)。 |

- **未接 refresher**:未配置 fusion-mlx 后端时,`adapter.*` 事件被确认但刷新被跳过(无可刷新对象);启用时路由仍注册。

第一版索引源仍为 fusion-mlx `GET /admin/api/fine-tune/adapters`;webhook 是长期 model-hub 源的事件驱动刷新路径。上游依赖(fusion-trainer#49 发布 adapter;fusion-models-hub#22 加 `base_model_id` FK + `adapter.*` webhook 事件 + 真实现 LoRA merge)列为不阻塞本版的 issue。

### 集群负载均衡

当本地无法服务时,网关在回退到云端前先尝试集群节点。

| 策略 | 评分方式 |
|----------|---------|
| `least-connections` | 选择在途请求最少的节点 |
| `hardware-aware` | 加权评分:60% 内存可用 + 30% 队列因子 + 10% 在途 |
| `round-robin` | 原子计数器在健康节点间轮询 |

节点健康周期性检查。连续 N 次失败 (可配置) 后,节点标记为死亡并排除出选择。

两种发现模式:

| 模式 | 说明 |
|------|-------------|
| `standalone` | 配置中的静态节点列表 + 本地健康检查 |
| `master` | 从 fusion-multi-node Master (`:9753`) 同步节点 — 网关周期性调用 `/api/nodes` |

### 批次分片

对于大批量 embedding 请求 (输入数 > 32),网关自动将批次拆分为分片,并行分发到集群节点。返回前按正确索引顺序合并结果。集群不可用时回退到单 provider。

### Embedding 与 Rerank 路由

路由引擎用**请求类型** (`chat`/`embedding`/`rerank`) 做快速路径路由决策:

| 请求类型 | 策略 | 回退 |
|-------------|----------|----------|
| `embedding` | 本地优先 (跳过 token 预算检查) | 集群 → 云端 |
| `rerank` | 默认云端 (除非检测到本地 rerank 模型) | 集群 → 云端 |

- **Embedding**:熔断器关闭 + 本地就绪时走本地。大批量 (>32 输入) 自动集群分片。`input` 接受字符串或字符串数组 (遵循 OpenAI API 规范)。
- **Rerank**:默认路由到云端,因为本地 MLX 通常不承载 rerank 模型。若本地有名称含 "rerank"/"reranker" 的可用模型,则改走本地。

### Realtime API

网关通过双向 WebSocket 中继代理 OpenAI Realtime API (`/v1/realtime`):

1. 客户端连接 `ws://gateway:11432/v1/realtime`
2. 网关升级为 WebSocket,拨号配置的后端 (`wss://api.openai.com/v1/realtime`)
3. 双向零缓冲转发消息 (客户端↔后端)
4. 任一端关闭即触发两端连接清理

配置:`realtime.enabled`、`realtime.backend_url`、`realtime.api_key`。

## 硬件指标 (三个来源)

| 来源 | 指标 | 方式 |
|--------|---------|--------|
| gopsutil | CPU、RAM、Swap 总量/已用 | 进程级 |
| IOKit (ioreg) | GPU Device/Renderer/Tiler 利用率、GPU 显存 | `ioreg -r -d 1 -w 0 -c AGXAccelerator` |
| fusion-mlx /metrics | MLX 活跃内存、已加载模型、推理队列深度 | Prometheus 解析器 |

## SSE 背压

当下游 SSE 通道写满 (慢客户端) 时,网关:
1. 发出 `warning` SSE 事件:`event: warning\ndata: {"type":"warning","message":"stream degraded, falling back to non-streaming"}`
2. 回退到非流式 `Chat()` 调用,收集完整响应
3. 将完整响应作为单个 SSE chunk 发送
4. 设置 `X-Fusion-Degraded: true` 响应头

若非流式回退也失败,则发出 `error` SSE 事件。

**内存安全**:SSE 每行上限 1 MiB,超长行丢弃;外部 API 响应上限 10 MiB。防止畸形或恶意流导致 OOM。

## Key 级安全与限流

### API Key 管理

每个 API key 支持细粒度访问控制:

| 字段 | 说明 |
|-------|-------------|
| `key` | API key 字符串 |
| `name` | key 的人类可读标识 |
| `rpm` | 每分钟请求数 (0 = 无限) |
| `tpm` | 每分钟 token 数 (0 = 无限) |
| `allowed_models` | 模型白名单,支持通配符 (如 `gpt-4o*`、`*`) |
| `allowed_backends` | 后端白名单 — 路由到非白名单后端的请求返回 403。通配符 `*` 允许全部 |
| `expires_at` | RFC3339 过期时间戳 |
| `budget_limit` | 每月消费上限 (USD,0 = 无限) |

Key 来自**两个来源**,鉴权层均认可:

- **静态 (config.yaml)**:`auth.api_keys` 条目,按 key 字符串精确匹配。
- **后台管理 (dashboard)**:通过 `POST /admin/api/keys` 创建。创建时一次性返回完整 `sk-<raw>` key;网关仅存储 8 字符前缀 + SHA-256 哈希 (`key_hash`)。鉴权时对提交的 key 哈希后在 Store 中查找,因此后台生成的 key 与静态 key 鉴权方式完全一致 (配额、白名单、预算全部生效)。非 `active` 状态的 key 被拒绝。

### MasterKey

`master_key` 绕过所有限流和模型白名单。仅供内部服务使用。

### 滑动窗口限流

按 key 的 RPM/TPM 强制执行,采用滑动窗口算法 (不依赖 Redis):
- **RPM**:跟踪 1 分钟窗口内的请求时间戳
- **TPM**:跟踪 1 分钟窗口内的 token 数
- 返回 `429` 并带 `Retry-After` 和 `X-RateLimit-Remaining` 头
- master key 请求完全绕过限流

## 响应缓存

非流式 chat completions 的 LRU 内存缓存:
- 缓存键:SHA256(model + messages + temperature + max_tokens + top_p)
- 可配置 TTL、最大条目数、最大内存
- `X-Cache: HIT` / `X-Cache: MISS` 响应头
- 每 30s 后台淘汰过期条目

## 成本跟踪

内置按模型定价的成本跟踪:
- **15 个模型**预置定价 (GPT-4/4o/3.5、Claude-3/3.5、DeepSeek、embeddings)
- 每请求自动成本计算 (prompt + completion tokens × 模型费率)
- `/v1/cost` 端点提供聚合汇总 (按 key、后端、模型)
- 按 key 成本拆分,支持 `?key=<name>` 过滤
- 通过 `Tracker.ExportJSON()` 导出 JSON
- **自定义定价文件**:基于 YAML 的模型定价覆盖,支持热重载 (`cost.pricing_file`)
- **预算阻断**:按 key 的月度消费上限 (`budget_limit`) 在请求执行前强制

## Stream Options

chat completions 完整支持 OpenAI `stream_options`:
- `stream_include_usage`:SSE 流式过程中累计输出 token 数
- 最后一个 SSE chunk 含完整 `usage` 对象 (prompt + completion tokens)
- 确保流式请求的精确 token 计数与成本跟踪

## Anthropic Messages API

`/v1/messages` 原生支持 Anthropic Messages API:

- **原生路径**:路由到 Anthropic 后端的请求以原生格式转发 (无转换开销)
- **自动转换路径**:路由到非 Anthropic 后端的请求自动转换:AnthropicRequest → ChatRequest → ChatResponse → AnthropicResponse
- **双向转换**:system 消息提取、工具格式翻译 (OpenAI functions ↔ Anthropic tools)、content block 映射
- **内容形式**:`content` 接受纯字符串或 content block 数组 (遵循 Anthropic API 规范;字符串归一化为 `[{type:"text",text:s}]`)
- **流式**:原生 Anthropic SSE 事件 (`message_start`、`content_block_delta`、`message_delta`、`message_stop`)
- **Thinking**:支持 `thinking` 参数与 `budget_tokens` (扩展思考)
- **非流式内部流+聚合**:路由到 `MessagesProvider` 的非流式 `/v1/messages` 内部以流式 (`stream=true`) 上行,再通过 `adapter.AggregateAnthropicStreamEvents` 聚合为单个非流式 Anthropic 响应。推理类上游 (如 LiteLLM 代理后的 glm5.2) 会扣留非流式响应头直到生成完成,从而触发 `Client.Timeout exceeded while awaiting headers` / 客户端取消 502。流式路径 TTFB 约 2s,网关不再阻塞在上游响应头上。文本、`thinking` (+`signature_delta`)、`tool_use` (`input_json_delta`) block 均被重建。

### 云端签名 Provider (AWS Bedrock / GCP Vertex / Azure Foundry)

除标准 `anthropic` 后端外,网关还将 `/v1/messages` 转发到需要请求签名而非静态 API key 的云端 Claude 端点。三者实现同一 `MessagesProvider` 路径 — 原生 Anthropic 格式进、原生 Anthropic SSE 出 — 通过在 `config.yaml` 设置后端 `type:` 来选择。凭证**仅**从网关侧环境变量读取,绝不回传给客户端。

| 后端 `type` | 云 | 鉴权机制 | 所需环境变量 |
|----------------|-------|-----------------|-------------------|
| `bedrock` | AWS Bedrock | AWS SigV4 (标准库 `crypto/hmac`+`sha256`;无 AWS SDK 依赖) | `AWS_ACCESS_KEY_ID`、`AWS_SECRET_ACCESS_KEY`、`AWS_REGION` (或 `AWS_DEFAULT_REGION`),可选 `AWS_SESSION_TOKEN` |
| `vertex` | Google Vertex AI | OAuth2 service-account:自签 RS256 JWT 在 `token_uri` 换 access token (缓存,过期前 5 分钟刷新;复用 `golang-jwt/jwt/v5`) | `VERTEX_SERVICE_ACCOUNT_JSON` (内联) 或 `GOOGLE_APPLICATION_CREDENTIALS` (路径);`VERTEX_PROJECT_ID` (或 `GOOGLE_CLOUD_PROJECT`);`VERTEX_REGION` (或 `GOOGLE_CLOUD_REGION`) |
| `foundry` | Azure AI Foundry | `api-key` 头 或 `Authorization: Bearer` (Entra token) | `AZURE_API_KEY` (或 `AZURE_OPENAI_API_KEY`) 或 `AZURE_ACCESS_TOKEN` |

- **URL**:Bedrock `{base_url}/model/{model-encoded}/invoke` (流式用 `/invoke-with-response-stream`;model id 中的 `:` 编码为 `%3A`);Vertex `{base_url}/v1/projects/{project}/locations/{region}/publishers/anthropic/models/{model}:rawPredict`;Foundry `{base_url}/v1/messages`,带 `anthropic-version: 2023-06-01`。
- **错误透明**:三个新 provider 的非 2xx 上游响应以原始状态码、body、`x-request-id`/`request-id` (经 `*MessagesHTTPError`) 透传给客户端,绝不返回通用 502。
- **SSE 加固**:1 MiB/行 与 10 MiB 响应上限,与 adapter pool 其余部分一致。

## 音频与审核

- `/v1/audio/transcriptions`:multipart 表单上传,委托 provider 的 `Transcription()` 方法 (仅云端)
- `/v1/audio/speech`:JSON body,从 provider 的 `Speech()` 方法返回音频二进制流 (仅云端)
- `/v1/moderations`:JSON body,委托 provider 的 `Moderation()` 方法 (仅云端)

这些端点用接口断言 — 未实现该方法的 provider 返回明确错误。

## OpenTelemetry 链路追踪

可选的分布式追踪,基于 OTel (默认关闭):

```yaml
observability:
    otel_enabled: true
    otel_endpoint: "localhost:4317"
    otel_protocol: "grpc"       # grpc | http
    otel_service_name: "fusion-gateway"
```

- 经 `HTTPMiddleware` 为每个请求自动创建 HTTP span
- 默认 10% 采样率 (`TraceIDRatioBased(0.1)`)
- 优雅关闭时 flush 待发送 span
- 经 W3C TraceContext 头传播 trace 上下文

## RBAC 与团队管理

基于角色的访问控制,三个角色:**admin** (全部权限)、**editor** (读 + 写)、**viewer** (只读)。viewer 的变更操作 (POST/PUT/PATCH/DELETE) 被阻断。

```yaml
rbac:
    enabled: true
    default_role: "viewer"    # 无 OIDC claims 时的回退角色
team:
    enabled: true
    default_team: "default"   # 无 OIDC team claim 时分配的团队
```

- OIDC claims `role`、`groups`、`team` 自动映射到 RBAC 角色
- master API key 始终获得 admin 角色
- 通过 `/admin/teams` 团队成本聚合 — 按团队跟踪配额
- key 与团队绑定:TeamsStore 中的 `BindKeyToTeam(apiKey, teamID)`

## 语义缓存

基于 prompt embedding 余弦相似度的相似度缓存。避免重复计算相同或近似请求。

```yaml
semantic_cache:
    enabled: true
    similarity_threshold: 0.92   # 缓存命中的余弦相似度
    max_entries: 5000
```

- 需设置 `EmbedFunc` — 未提供 embedding 函数时禁用 (无伪 embedding 回退)
- 可插拔 `EmbedFunc` — 替换为任意 embedding API 做相似度
- 30 分钟 TTL,自动过期淘汰

## Prompt 注入检测

基于正则的常见注入模式检测,动作可配置。

```yaml
prompt_injection:
    enabled: true
    action: "log"    # log | block
```

- 14 个内置模式 (ignore previous、jailbreak、system prompt leak 等)
- 严重度评分:medium (1-2 个匹配),high (3+ 个匹配)
- block 模式返回 HTTP 400,错误类型为 `content_filter`

## 成本加价

计费利润层 — 在基础成本上按 key 或全局加价。

```yaml
cost_markup:
    enabled: true
    global_markup: 0.2    # 所有请求 20% 附加费
```

- `SetKeyMarkup(keyName, markup)` 用于按 key 覆盖
- Tracker 中的 `applyMarkup()` 在记录前自动应用
- 日志中 `base_cost` 与 `billed_cost` 分离

## Batch API

OpenAI 兼容的 `/v1/batches` 端点,用于异步批量处理。

```yaml
batch:
    enabled: true
    max_batch_size: 100
```

## Connector 插件框架

面向第三方 API 集成 (QuickBooks、Google Workspace、HubSpot 等) 的统一 SaaS connector 框架。

### 架构

- **Registry**:内存中 connector 注册表,插件式注册
- **Connection Manager**:OAuth2 / 静态 API Key / Basic Auth 凭证存储
- **Action 执行**:统一 `POST /gateway/v1/connector/{key}/action/{action}` 接口
- **审计日志**:每次外部 API 调用记录时间戳、权限级别、输入摘要 (写操作)
- **测试模式**:`POST /gateway/v1/connector/test` 执行 action 但无真实副作用
- **持久化**:基于 JSON 文件的凭证存储,原子写入 (tmp + rename)
- **加密**:OAuth2 token 的 AES-256-GCM 静态加密 (主密钥可配置)

### 内置 Connector (V1.0)

| Connector | 鉴权类型 | Actions |
|-----------|-----------|---------|
| QuickBooks | OAuth2 | query_overdue_invoice、list_customers、create_invoice、get_company_info |
| Google Workspace | OAuth2 | list_users、get_user、list_calendar_events、send_email、read_drive_file |
| HubSpot | OAuth2 | list_contacts、get_contact、create_contact、list_deals、update_deal |

所有 connector 都对其 SaaS 端点发起真实 HTTP API 调用。Google Workspace 在 401 响应时支持自动 token 刷新。

### HTTPS / TLS

单二进制部署的可选 HTTPS 终结:

```yaml
server:
    tls:
        cert_file: "certs/server.crt"
        key_file: "certs/server.key"
```

配置后,网关使用 `http.ListenAndServeTLS` 而非普通 HTTP。

### AES-256-GCM 加密

OAuth2 token 与连接凭证的静态加密:

```yaml
encryption:
    master_key: "your-32-character-minimum-secret-key"
```

- AES-256-GCM,每条目随机 nonce
- Base64 编码存储 (nonce + ciphertext)
- 主密钥需 ≥32 字符
- 空或禁用时,token 以明文存储
- 经 `tokenCipher` 接口自动应用于 OAuth2 access/refresh token
- 加密失败视为硬错误 — token 绝不静默以明文存储

### OAuth2 授权流程

完整支持 OAuth2 Authorization Code Flow:

```
POST /gateway/v1/oauth2/authorize   →  生成授权 URL
GET  /gateway/v1/oauth2/callback    →  用 code 换 token
```

流程:
1. 客户端带 `connectorKey` 和可选 `state` 调用 `/oauth2/authorize`
2. 网关在未提供 `state` 时生成密码学随机 `state`,服务端存储 10 分钟 TTL
3. 网关返回 SaaS provider 的授权 URL (带 state 参数)
4. 用户在浏览器授权,provider 重定向到 `/oauth2/callback`
5. 网关校验 `state` 参数与此前签发的一致 (CSRF 防护)
6. 网关用 authorization code 换 access/refresh token
7. token 加密 (若配置 master_key) 后作为 Connection 存储

### 凭证持久化

连接持久化到 JSON 文件,原子写入:

```yaml
connector:
    persistence_path: "data/connections.json"
```

- 原子写:先写 `.tmp` 文件再 `os.Rename`,保证崩溃安全
- 启动时自动加载,Create/Delete/Refresh 变更时自动保存
- 加密 token 原样存储;读取时解密
- 目录权限设为 0700 (仅属主访问)

### 标准错误码

| Code | 含义 |
|------|---------|
| 1001 | 鉴权过期 — 需刷新 |
| 1002 | 第三方限流 |
| 1003 | 权限拒绝 |
| 1004 | 资源未找到 |
| 1005 | 请求超时 |
| 1006 | 鉴权失败 — 凭证无效或缺失 |
| 1007 | 外部 API 错误 — 上游请求失败 |
| 2001 | 参数校验失败 |

### 会话亲和 (Cowork 空间)

当存在 `X-Space-Id` 头时,网关维持会话亲和 — 将同一协作空间的请求路由到同一推理后端。从而在共享上下文场景下复用 KV cache。

- 基于 TTL 的亲和映射 (默认 30 分钟,自动淘汰)
- 亲和优雅降级:目标后端不可用时,重新路由并更新映射

- `POST /v1/batches` — 创建批次,立即返回,后台处理
- `GET /v1/batches/{id}` — 查询状态 (pending/running/completed/failed/cancelled)
- `POST /v1/batches/{id}/cancel` — 取消运行中的批次
- 可插拔 `ProcessFn` 用于自定义批处理逻辑

## Kubernetes 与 Helm 部署

### 基础设施资源

| 资源 | Manifest | Helm | 说明 |
|----------|----------|------|-------------|
| Deployment | ✅ | ✅ | 2 副本,拓扑分布,探针 |
| Service | ✅ | ✅ | ClusterIP :11432 |
| ConfigMap | ✅ | ✅ | 非敏感配置 |
| Secret | ✅ | ✅ | master_key、api_keys |
| ServiceAccount + RBAC | ✅ | ✅ | Config reader 角色 |
| HPA | ✅ | ✅ | CPU 70% / 内存 80%,2–10 副本 |
| PDB | ✅ | ✅ | minAvailable: 1 |
| Ingress | ✅ | ✅ | Nginx + TLS (cert-manager 可选) |
| NetworkPolicy | ✅ | ✅ | Deny-all + 允许同命名空间 |
| Namespace | ✅ | — | 专用命名空间 |

### 目录结构

```
deploy/
├── Dockerfile                         # 多阶段构建 (Go builder + Alpine runtime)
├── kubernetes/
│   ├── namespace.yaml                 # Namespace: fusion-gateway
│   ├── serviceaccount.yaml            # SA + Role + RoleBinding
│   ├── secret.yaml                    # 敏感 key (master_key, api_keys)
│   ├── deployment.yaml                # 2 副本,拓扑分布,SA,secretRef
│   ├── service.yaml                   # ClusterIP :11432
│   ├── configmap.yaml                 # 非敏感配置
│   ├── hpa.yaml                       # HPA: CPU 70% / 内存 80%
│   ├── pdb.yaml                       # PDB: minAvailable 1
│   ├── ingress.yaml                   # Nginx Ingress + TLS
│   └── networkpolicy.yaml             # Deny-all + 允许同命名空间
├── helm/fusion-gateway/
│   ├── Chart.yaml                     # Helm chart v0.6.2
│   ├── values.yaml                    # 完整 values,含 HPA/PDB/Ingress/SA/Secret
│   └── templates/                     # 所有资源模板
└── terraform/
    ├── versions.tf                    # Terraform >= 1.5,providers
    ├── variables.tf                   # 通用变量
    ├── outputs.tf                     # 端点输出
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
    └── modules/helm-release/          # 可复用 Helm release module
        ├── main.tf
        ├── variables.tf
        └── outputs.tf
```

### 快速部署

```bash
# 原生 K8s manifests
kubectl apply -f deploy/kubernetes/

# Helm (最小化)
helm install fusion-gateway deploy/helm/fusion-gateway/

# Helm (启用 HPA、Ingress、PDB)
helm install fusion-gateway deploy/helm/fusion-gateway/ \
  --set hpa.enabled=true \
  --set pdb.enabled=true \
  --set ingress.enabled=true \
  --set ingress.hosts[0].host=gw.example.com \
  --set secrets.master_key=your-secure-key
```

### Terraform 部署

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

### HPA 配置

| 参数 | 默认值 | 说明 |
|-----------|---------|-------------|
| `hpa.enabled` | false | 启用 HorizontalPodAutoscaler |
| `hpa.minReplicas` | 2 | 最小副本数 |
| `hpa.maxReplicas` | 10 | 最大副本数 |
| `hpa.targetCPUUtilizationPercentage` | 70 | 扩容的 CPU 阈值 |
| `hpa.targetMemoryUtilizationPercentage` | 80 | 扩容的内存阈值 |

自定义指标 (QPS、延迟) 时,添加 Prometheus Adapter 并在 HPA 中扩展 `metrics` 块。

### PDB 配置

| 参数 | 默认值 | 说明 |
|-----------|---------|-------------|
| `pdb.enabled` | false | 启用 PodDisruptionBudget |
| `pdb.minAvailable` | 1 | 中断期间最小可用 pod 数 |

### Ingress 配置

| 参数 | 默认值 | 说明 |
|-----------|---------|-------------|
| `ingress.enabled` | false | 启用 Ingress |
| `ingress.className` | nginx | Ingress class |
| `ingress.tls` | [] | TLS 配置 |

添加 cert-manager 注解以自动 TLS:`cert-manager.io/cluster-issuer: letsencrypt-prod`

## 管理后台

内置 web 管理后台,位于 `/admin`,通过 Go `embed` 由单二进制提供服务。

**鉴权**:JWT (HS256) + HttpOnly cookie 会话。经 `POST /admin/api/login` 登录。

**Admin API** (`/admin/api/*`):

| 端点 | 方法 | 说明 |
|----------|--------|-------------|
| `/admin/api/login` | POST | 用 admin 凭证登录,获取 JWT cookie |
| `/admin/api/keys` | GET/POST | 列出 / 创建 API key |
| `/admin/api/keys/:name` | GET/PUT/DELETE | 读 / 更新 / 删除 API key |
| `/admin/api/channels` | GET/POST | 列出 / 创建 channel |
| `/admin/api/channels/:name` | GET/PUT/DELETE | 读 / 更新 / 删除 channel |
| `/admin/api/logs` | GET | 带过滤条件查询请求日志 |
| `/admin/api/logs/:id` | GET | 获取单条请求日志 |
| `/admin/api/analytics/tokens` | GET | token 用量统计 |
| `/admin/api/analytics/cost` | GET | 成本统计 |
| `/admin/api/analytics/models` | GET | 模型分布统计 |
| `/admin/api/analytics/latency` | GET | 延迟统计 |
| `/admin/api/analytics/errors` | GET | 错误统计 |
| `/admin/api/dashboard/overview` | GET | 仪表盘概览 (QPS、token、成本、本地命中率) |
| `/admin/api/quota/:key` | GET/PUT | 获取 / 设置 key 配额用量 |
| `/admin/api/config/:domain` | GET/PUT | 读 / 更新配置域 (server、auth、rate-limit、retry、negotiation、cache、cost、cost-markup、pii、cloud-routing、hardware、tokenizer、observability、cors、hot-reload、cluster、realtime、admin、oidc、rbac、semantic-cache、prompt-injection、batch、store、validation) |

**请求日志管道**:每个请求自动记录完整元数据:
- 请求 ID、模型、channel、路由原因、token 数、成本、延迟、TTFT
- 环形缓冲存储,可配置最大长度
- 按时间范围、key、模型、channel、状态、token/成本阈值过滤
- 可导出 JSON

**前端**:React + Ant Design + Vite SPA,嵌入 Go 二进制。

### GUI 配置

所有网关配置均可通过管理后台管理 — 无需手动编辑 YAML。每个配置域有专属页面,通过 GET/PUT API 端点实时读写设置。

**配置 API** (`/admin/api/config/*`):

| 端点 | 方法 | 说明 |
|----------|--------|-------------|
| `/admin/api/config/server` | GET/PUT | 服务端设置 (端口、读写超时) |
| `/admin/api/config/auth` | GET/PUT | 鉴权设置 (enabled、master_key、api_keys) |
| `/admin/api/config/rate-limit` | GET/PUT | 限流 (按 key RPM/TPM) |
| `/admin/api/config/retry` | GET/PUT | 重试与退避设置 |
| `/admin/api/config/negotiation` | GET/PUT | 路由协商规则 |
| `/admin/api/config/cache` | GET/PUT | 缓存设置 (后端、TTL、Redis) |
| `/admin/api/config/cost` | GET/PUT | 成本跟踪与定价 |
| `/admin/api/config/cost-markup` | GET/PUT | 成本加价 / 计费利润 |
| `/admin/api/config/pii` | GET/PUT | PII 检测模式与动作 |
| `/admin/api/config/cloud-routing` | GET/PUT | 云端路由策略与权重 |
| `/admin/api/config/hardware` | GET/PUT | 硬件指标采集 |
| `/admin/api/config/tokenizer` | GET/PUT | Tokenizer 引擎设置 |
| `/admin/api/config/observability` | GET/PUT | 日志、OTel、审计 |
| `/admin/api/config/cors` | GET/PUT | CORS 允许的来源/方法 |
| `/admin/api/config/hot-reload` | GET/PUT | 热重载与排空设置 |
| `/admin/api/config/cluster` | GET/PUT | 集群发现与负载均衡 |
| `/admin/api/config/realtime` | GET/PUT | Realtime API 代理 |
| `/admin/api/config/admin` | GET/PUT | 管理面板设置 (JWT、users) |
| `/admin/api/config/oidc` | GET/PUT | OIDC 身份 provider |
| `/admin/api/config/rbac` | GET/PUT | RBAC 角色映射 |
| `/admin/api/config/semantic-cache` | GET/PUT | 语义缓存设置 |
| `/admin/api/config/prompt-injection` | GET/PUT | prompt 注入检测 |
| `/admin/api/config/batch` | GET/PUT | Batch API 设置 |
| `/admin/api/config/store` | GET/PUT | Store 后端 (memory/Redis) |
| `/admin/api/config/validation` | GET/PUT | 请求校验规则 |

**行为**:
- GET 返回当前内存配置,敏感字段掩码 (`****`)
- PUT 接受部分更新 — 仅更改提供的字段
- 敏感字段空字符串 (如 `api_key: ""`) 表示 "保留当前值"
- 更新先写入磁盘 YAML 配置文件,再由热重载拾取

## 重试与退避

非流式请求的指数退避重试:
- 可配置 `max_retries`、`initial_backoff`、`max_backoff`
- 默认可重试状态码:429、500、502、503
- 连接拒绝 / 超时错误也可重试
- 重试间隔尊重 context 取消

## PII 检测

对请求文本内容的正则 PII 扫描:

| 内置模式 | 说明 |
|-----------------|-------------|
| `email` | 邮箱地址 |
| `phone_cn` | 中国手机号 |
| `phone_us` | 美国电话号 |
| `credit_card` | 信用卡号 |
| `ssn` | 美国社会安全号 |
| `ip_v4` | IPv4 地址 |

三种动作:
- `log` (默认):记录检测,放行请求
- `mask`:记录检测并带掩码意图,放行请求
- `deny`:以 400 错误阻断请求,列出检测到的 PII 类型

通过 `pii.patterns` 配置支持自定义模式。

## 云端路由策略

当有多个云端后端可用时,通过 `cloud_routing.strategy` 选择策略:

| 策略 | 说明 |
|----------|-------------|
| `round-robin` | 顺序轮询后端 (默认) |
| `latency` | 路由到 P95 延迟最低的后端 |
| `cost` | 路由到最便宜的后端 (DeepSeek < Qianfan/Volcengine < OpenAI/Anthropic) |
| `weight` | 经 `cloud_weights` 配置的加权随机选择 |
| `least-busy` | 路由到跟踪样本最少的后端 |

延迟跟踪用 quickselect 算法高效计算 P95 (窗口大小可配置,默认 1000 样本)。

## 配置热更新 (排空/应用/预热)

配置文件变更时,网关执行三阶段重载:

1. **排空 (Drain)**:等待最多 `breaker_drain_timeout` 让在途请求完成
2. **应用 (Apply)**:用新配置重建熔断器,更新路由规则,重建 provider pool
3. **预热 (Warmup)**:本地熔断器置为 `half_open` — 在完全关闭前允许有限的测试请求

当 `observability.config_audit_log: true` 时,配置变更还会被审计 — 字段级 diff (旧/新值) 记录并追加到 `observability.config_audit_file` (JSONL 格式)。

## Admin 端点

| 端点 | 方法 | 说明 |
|----------|--------|-------------|
| `/admin/gc` | POST | 触发 fusion-mlx GC。在途 > 0 时排队 (返回 202);空闲时立即执行 (返回 200) |
| `/admin/config/reload` | POST | 触发配置重载 |
| `/debug/pprof/` | GET | Go pprof 性能分析首页 (需 `enable_pprof` + `master_key`) |
| `/debug/pprof/profile` | GET | CPU profile (需 `enable_pprof` + `master_key`) |
| `/debug/pprof/trace` | GET | 执行 trace (需 `enable_pprof` + `master_key`) |

## 安全 GC

网关安全管理 fusion-mlx KV cache GC:

- **手动**:`POST /admin/gc` — 空闲时立即触发,在途 > 0 时排队待空闲执行
- **空闲定时器**:`inFlightCounter == 0` 持续 `min_idle_since_last_gc` (默认 5 分钟) 后自动触发 GC
- **取消驱动**:请求取消后,若空闲时间足够长,可能触发 GC

## 基准测试

```bash
go test -bench=. -benchmem ./internal/router/
```

Apple M5 Max 典型结果:
- 本地短路径:~148 ns/op
- 云端长路径:~34 ns/op
- 并行:~386 ns/op

## 开发

```bash
# 运行全部测试
go test ./... -v

# 运行单个 package
go test ./internal/router/... -v

# 带覆盖率运行
go test ./... -cover -timeout 180s

# Lint
golangci-lint run

# 构建
go build -o fusion-gateway ./cmd/gateway
```

### 端到端测试 (管理后台)

管理后台 SPA 有 Playwright E2E 测试套件,位于 `tests/e2e/`。覆盖每个后台页面 — 登录、API key、channel、仪表盘/分析/日志,以及全部 26 个配置区段 — 既验证 UI (按钮、输入、表单),也验证运行时耦合 (后台侧配置变更须通过热重载在运行中的网关生效)。

**49 个测试**,跨 5 个 spec 文件:

| Spec | 覆盖 |
|------|--------|
| `01-login.spec.js` | 登录表单、错误/正确凭证、`admin_token` cookie、路由守卫重定向 |
| `02-keys.spec.js` | key 表、UI 创建、运行时生命周期 (key 在 `/v1/models` 可用 → 删除 → 401/403)、空名 400、PUT 编辑持久化、id==name |
| `03-channels.spec.js` | channel 表、UI 表单创建 (name/type/base_url/priority/weight)、UI 删除、空名 400、PUT 编辑持久化 |
| `04-dashboard-analytics.spec.js` | 仪表盘、分析概览 + 利润、日志 + 导出端点 |
| `05-config.spec.js` | 全部 26 个配置区段 GET (渲染) + PUT (持久化) + 热重载往返;CORS preflight 回显;按 key 限流 429;未鉴权 PUT 被拒 |

```bash
# 前提:网关在 :11432 运行且启用 admin
./fusion-gateway --config config.yaml

# 安装 Playwright (一次性)
cd tests/e2e && npm install

# 运行全部 E2E 测试 (chromium,串行)
npx playwright test

# 运行单个 spec
npx playwright test tests/02-keys.spec.js

# 有头模式 (看浏览器)
npx playwright test --headed

# 查看上次运行报告
npx playwright show-report
```

套件在运行前备份一次 `config.yaml` (经 `global-setup.js`),运行后恢复一次 (`global-teardown.js`),因此会变更配置的 PUT 测试绝不留下变更后的运行状态。用户运行中的 key 和配置不受影响。

### 测试覆盖率

所有 package 保持 ≥90% 测试覆盖率:

| Package | 覆盖率 |
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

## 项目结构

```
cmd/gateway/          入口
internal/
  config/             配置加载、版本化快照、热重载、审计日志
  hardware/           硬件指标采集器 (gopsutil + IOKit + MLX)
  tokenizer/          token 计数 + 预算估算 + 校准
  router/             路由决策引擎 + 按后端熔断器 + 云端策略 + 延迟跟踪器
  adapter/            Provider 接口 + fusion-mlx + openai-compatible adapter + pool
  cluster/            集群节点发现、健康检查、负载均衡、节点 adapter
  middleware/         鉴权 (MasterKey + key 过期 + 模型白名单 + 预算阻断)、限流 (RPM/TPM)、PII 检测、重试、请求日志
  cache/              非流式响应的 LRU 内存缓存 (带 TTL)
  cost/               成本跟踪 + 内置模型定价表 + 自定义定价热重载
  store/              Store 接口 (日志、key、channel、分析、仪表盘、配额)
  store/memory/       内存 store 实现 (环形缓冲日志、CRUD、分析、teams/orgs)
batch/              Batch API store + 异步处理
  admin/              Admin API handler + JWT 鉴权 + 登录
  admin/ui/           go:embed 前端资源 (React SPA)
  observability/      Prometheus 指标 + OpenTelemetry 链路追踪
  server/             HTTP 服务 + 路由注册 + SSE 转发 + stream options
web/admin/            管理后台前端 (React + Ant Design + Vite)
config.example.yaml   示例配置
```

## 管理后台页面

| 页面 | 说明 |
|------|-------------|
| Dashboard | 实时概览:QPS、token 用量、成本、本地命中率、路由分布 |
| API Keys | CRUD + 配额管理 + 预算上限 + 按 key 用量分析 |
| Channels | 后端 provider 管理 + 健康检查 + 连通性测试 |
| Request Logs | 完整请求日志,含路由原因、token 数、成本、延迟 |
| Analytics | token 用量趋势、成本跟踪、模型分布、延迟/错误统计 |
| Configuration | 全部网关设置的 24 个配置页面 (Server、Auth、Rate Limit、Retry、Cache、Cost、PII、Cloud Routing、Hardware、Tokenizer、Observability、CORS、Hot Reload、Cluster、Realtime、Admin、OIDC、RBAC、Semantic Cache、Prompt Injection、Batch、Store、Validation、Cost Markup、Negotiation) |

**差异点**:唯一具备**硬件感知路由可视化**与**本地推理节省跟踪**的 AI 网关。

## 审计修复记录

### v0.8.16 — Anthropic /v1/messages 路径现应用模型别名映射 (#52)

| # | 修复 | 详情 |
|---|-----|---------|
| 1 | **`/v1/messages` 转发映射后的模型名,非原始别名** (#52) | claude code (Anthropic SDK) 向网关发送模型别名如 `claude-opus-4-7`。`/v1/chat/completions` 路径已应用 `routing.fallback.model_mapping`,但 `/v1/messages` 经 `resolveCloudProvider(decision, nil, w)` 解析云端 provider — `nil` request 导致 model-mapping 分支被跳过,别名原样转发到云端后端 (glm52 / LiteLLM),被拒:`400: Invalid model name passed in model=claude-opus-4-7` → 网关 502 → claude code 报 `API Error: The response stopped arriving`。 |
| 2 | **抽出 `applyCloudModelMapping` helper** | 映射逻辑原内联于 `resolveCloudProvider` 且 mutate `*ChatRequest`,无法服务 anthropic 路径 (`*AnthropicRequest`)。抽成 `Server.applyCloudModelMapping(model, cloudBackend) string` (返回映射 id 或输入不变,当禁用/未命中时) 并在两路径复用 — chat 无行为变化,messages 新增行为。 |
| 3 | **配置:`routing.fallback.enabled` + `model_mapping`** | 设置 `routing.fallback.enabled: true` 和 `model_mapping: { claude-opus-4-7: glm5.2 }` (已加到 `config.yaml` 并在 `config.example.yaml` 文档化)。此开关 gate 别名→后端 id 转换。该 map 通用 — 可加任意 SDK 别名 → 真实模型 id 对。 |
| 4 | **测试** | 2 个新增回归测试:`TestAnthropicMessages_ModelMappingApplied` (别名 `claude-opus-4-7` → 上游收到 `glm5.2`)、`TestAnthropicMessages_ModelMappingDisabled` (`enabled:false` → 原始别名透传)。实测验证:`/v1/messages` 带 `claude-opus-4-7` 现从 glm52 流式返回 `200` 且 `model:glm5.2`;日志显示 `model mapped for cloud routing local_model=claude-opus-4-7 cloud_model=glm5.2`。2548 测试绿;`go vet` 干净。 |

### v0.8.15 — 极小请求不再被 output/input ratio 误判路由 cloud (#48)

| # | 修复 | 详情 |
|---|-----|---------|
| 1 | **极小请求跳过 output/input ratio 判据** (#48) | 4-token prompt ("say pong") 配 `max_tokens:5` 产生 `predict_output/input = 5/4 = 1.25 > 0.6`,导致 `output_input_ratio_exceeded` 规则 (P4.5) 将本可本地的请求误判到云端 (glm52),而 glm52 不认 `Qwen3.5-9B-4bit` 模型名 → 400 → 网关 502。input 极少时 ratio 统计无意义。P4.5 现在在 `input_tokens < output_input_ratio_min_input_tokens` (默认 32,配置 `routing.output_input_ratio_min_input_tokens`) 时跳过 ratio 判据,fall through 到 P6 模型可用性 → P7 本地。 |
| 2 | **可配置 input-token 下限** | 新增 `routing.output_input_ratio_min_input_tokens` (int,默认 32)。未设置或零/负值回退到内置默认,故既有 `config.yaml` 无需改动即获修复。`config.Validate` 校验非负。 |
| 3 | **跳过日志便于定位** | ratio 判据因 input 下限被跳过时,引擎发出 `output/input ratio skipped: input tokens below floor`,含 `input_tokens`、`min_input_tokens`、`predict_output_tokens`,极小请求路由可诊断。 |
| 4 | **测试** | 3 个新增回归测试:`TestDecide_OutputInputRatioSkippedForTinyInput` (4 input → 本地,非 `output_input_ratio_exceeded`)、`TestDecide_OutputInputRatioSkippedForTinyInput_ExplicitFloor` (显式下限 64 → 50 input 跳过 ratio)、`TestValidate_OutputInputRatioMinInputTokensNegative` (负下限被拒)。既有 `TestDecide_OutputInputRatioThreshold` (100 input,高于下限) 仍路由 cloud 不变。585 个 router/config/admin 测试绿;`go vet` 干净。 |

### v0.8.14 — 修复 "Content block not found":content_block index=0 + 重复/畸形 message_stop (#46)

| # | 修复 | 详情 |
|---|-----|---------|
| 1 | **content_block index=0 不再被丢弃** (#46) | `AnthropicStreamEvent.Index` 的 tag `json:"index,omitempty"` 在 marshal 时丢弃了 `index:0` — 第一个 content block (推理模型如 glm5.2 上始终是 `thinking` block) 被发出时没有 `index` 字段,导致 Anthropic SDK 无法将 `content_block_delta`/`_stop` 事件匹配到打开的 block,抛出 `Content block not found`。新增自定义 `MarshalJSON` (用 alias-marshal 避免递归),仅对 block 作用域事件 (`content_block_start`/`_delta`/`_stop`) 强制输出显式 `"index"` (即使为 0);message 作用域事件 (`message_start`/`message_delta`/`message_stop`) 按 Anthropic SSE 规范仍不带 index。 |
| 2 | **不再有重复 / 畸形 message_stop** (#46) | `handleStreamAnthropicMessages` 在上游 channel 关闭后无条件追加 `event: message_stop\ndata: {}`,因此当上游已发送真实 `message_stop` 时,客户端收到**第二个** (且畸形 — `data:{}` 无 `type`)。现在跟踪 `sawMessageStop`,仅在上游遗漏时合成格式正确的 `{"type":"message_stop"}`。 |
| 3 | **客户端取消不再合成结束事件** (#46) | `ctx.Err() != nil` 时 (客户端流式中途取消,如 4 分钟+ 的长思考),上游 goroutine 提前关闭 channel,content block 可能仍处于 OPEN 状态。旧的合成 `message_stop` 随后给 SDK 一个未匹配的 block (`Content block not found`)。现在取消时抑制任何合成结束事件 — 客户端已放弃。 |
| 4 | **测试** | 4 个新增回归测试:`TestAnthropicStreamEvent_MarshalIndexZeroNotOmitted` (index 0 在 block 事件中存在,message 事件中不存在)、`TestHandleStreamAnthropicMessages_NoDuplicateMessageStop`、`_SynthesizesMissingMessageStop`、`_ClientCancelSuppressesMessageStop`。实时验证网关→LiteLLM 流:1 个 `message_stop`、0 个畸形 `data:{}`、0 个 SDK block 配对错误。`go vet` 干净。 |

### v0.8.13 — 流式 body 不再被后端超时截断 (#44)

| # | 修复 | 详情 |
|---|-----|---------|
| 1 | **流式无超时 client** (#44) | `AnthropicProvider` 新增 `streamHTTPClient`,`Timeout: 0` (body 流无界直到上游关闭) 且 `Transport.ResponseHeaderTimeout = 后端超时` (死上游仍在 headers/connect 阶段快速失败)。`StreamChat` 与 `StreamMessages` 现使用它。此前单一 `httpClient` 有 `http.Client.Timeout = 120s`,限制**包含 body 读取的整个请求**,在 120s 以 `context deadline exceeded (... while reading body)` 强制关闭长推理流,并截断非流式聚合路径 (#42)。 |
| 2 | **非流式不变** | 非流式 `Messages`/`Chat` 保留有界 `httpClient`,因此挂起的非流式上游仍被后端超时封顶。 |
| 3 | **测试** | 新增 `TestAnthropicProvider_StreamMessagesNotTruncatedByClientTimeout`:慢 SSE 上游在最终事件前等待 600 ms (远超 300 ms 测试后端超时),断言到达 `message_stop`。跨 23 个 package 2539 个测试通过;`go vet` 干净。 |

### v0.8.12 — 非流式 /v1/messages 内部流+聚合 (#42)

| # | 修复 | 详情 |
|---|-----|---------|
| 1 | **非流式内部流+聚合** (#42) | 路由到 `MessagesProvider` 的非流式 `/v1/messages` 现在内部以流式 (`stream=true`) 上行,再经新增的 `adapter.AggregateAnthropicStreamEvents` 聚合为单个非流式 `AnthropicResponse`。推理类上游 (如 LiteLLM 代理后的 glm5.2) 扣留非流式响应头直到生成完成 (6-14s+),触发 `Client.Timeout exceeded while awaiting headers` / 客户端取消 502 及 Claude Code 重试风暴。流式路径 TTFB 约 2s,网关不再阻塞在上游响应头。 |
| 2 | **完整 block 重建** | `AggregateAnthropicStreamEvents` 重建 `text` (`text_delta`)、`thinking` (`thinking_delta` + `signature_delta`)、`tool_use` (`input_json_delta` 部分 JSON 累积) content block;流结束时无 stop_reason 则默认 `end_turn`;透出上游 `error` 事件。 |
| 3 | **测试** | 5 个新增 `TestAggregateAnthropicStreamEvents_*` (text、thinking+signature、tool_use、error 事件、空-默认 end_turn);跨 23 个 package 2538 个测试绿;`go vet` 干净。 |

### v0.8.11 — 云端签名 Provider:AWS Bedrock / GCP Vertex / Azure Foundry (#40)

| # | 修复 | 详情 |
|---|-----|---------|
| 1 | **AWS Bedrock provider** (#40) | 新增 `bedrock` 后端类型,以 AWS SigV4 请求签名 (基于标准库 `crypto/hmac`+`crypto/sha256`,无 AWS SDK 依赖) 将 `/v1/messages` 转发到 AWS Bedrock。从网关 env 读取 `AWS_ACCESS_KEY_ID`/`AWS_SECRET_ACCESS_KEY`/`AWS_REGION`/`AWS_SESSION_TOKEN`。经 Bedrock `invoke-with-response-stream` 事件流流式 (将 `{"payload":{...}}` 解包为原生 Anthropic 事件)。model id 中的 `:` 在路径上编码为 `%3A`。 |
| 2 | **Google Vertex AI provider** (#40) | 新增 `vertex` 后端类型,以 OAuth2 service-account 流程将 `/v1/messages` 转发到 Vertex AI `:rawPredict`:自签 RS256 JWT (复用既有 `golang-jwt/jwt/v5` 依赖) 在 SA `token_uri` 换 access token,缓存并过期前 5 分钟刷新。从网关 env 读取 `VERTEX_SERVICE_ACCOUNT_JSON`/`GOOGLE_APPLICATION_CREDENTIALS`/`VERTEX_PROJECT_ID`/`VERTEX_REGION`。 |
| 3 | **Azure AI Foundry provider** (#40) | 新增 `foundry` 后端类型,以 `api-key` 头 (`AZURE_API_KEY`/`AZURE_OPENAI_API_KEY`) 或 Entra `Authorization: Bearer` token (`AZURE_ACCESS_TOKEN`) 将 `/v1/messages` 转发到 Azure AI Foundry。设置 `anthropic-version: 2023-06-01`。 |
| 4 | **MessagesProvider 分发** | `/v1/messages` handler 现基于 `MessagesProvider` 接口 (由 Anthropic + Bedrock + Vertex + Foundry 实现) 分发,而非具体 `*AnthropicProvider`,因此四个后端共享一条原生格式路径。非 Anthropic 后端的 OpenAI 转换回退不变。 |
| 5 | **错误透明** | 三个新 provider 的非 2xx 上游响应以原始状态码、body、`x-request-id`/`request-id` (经 `*MessagesHTTPError`) 透传给客户端 — 无通用 502。 |
| 6 | **测试** | 在共享 helper 覆盖之上,新增 16 个 provider 测试 (SigV4 头结构、OAuth2 token 交换 + 缓存、api-key/bearer 鉴权、Bedrock 事件流解包、原生 SSE 透传、错误透传、缺凭证);跨 23 个 package 2533 个测试绿;`go vet` 干净。 |

### v0.8.10 — Demo key 覆盖 MLX VL 模型 (#37)

| # | 修复 | 详情 |
|---|-----|---------|
| 1 | **Demo key 覆盖 MLX VL 模型** (#37) | 内置 demo key (`config.example.yaml`) 新增 `mlx-community--*` 到 `allowed_models`,因此带 `mlx-community--` 前缀的 MLX 社区模型 — 包括 Computer Use 用的 VL 模型如 `mlx-community--Qwen2.5-VL-7B-Instruct-4bit` — 在默认 demo key 下不再 403。纯配置修复;与既有的大小写不敏感后缀通配匹配器兼容。 |

### v0.8.9 — 大小写不敏感模型白名单、采集器 panic 防护 (#32, #33)

| # | 修复 | 详情 |
|---|-----|---------|
| 1 | **大小写不敏感模型白名单** (#32) | `CheckModelAllowlist` 现大小写不敏感地匹配精确名称与前缀通配 (`qwen*`)。内置 demo key 的 `allowed_models: ["qwen*"]` 现匹配实际的 fusion-mlx 模型名如 `Qwen3.5-9B-4bit`,而非 403。(注意:`qwen*` 不覆盖 `mlx-community--` 前缀模型 — 见 v0.8.10 / #37。) 向后兼容 — 小写 config glob 仍匹配小写模型名。 |
| 2 | **硬件采集器 panic 防护** (#33) | `Collector.Start` 在 `collect_interval` 缺失或非正时默认为 5s,而非在 `time.NewTicker(0)` 中 panic。防止 `config.yaml` 缺少 `hardware.collect_interval` 时启动崩溃。 |

### v0.8.1 — 空模型默认解析 (#28)

| # | 修复 | 详情 |
|---|-----|---------|
| 1 | **空模型回填** | 空/缺失 `model` 的 `/v1/chat/completions` 不再对 fusion-mlx 404。模型从 `routing.default_model` 回填,或在无默认配置时从首个已加载本地模型 (本地 provider 的 `/v1/models`) 自动发现。自动发现仅查询本地 provider,因此慢/不可达的云端后端绝不阻塞 (镜像 #21 修复)。 |

### v0.7.4 — Header 注入、AllowedBackends、模型拦截

| # | 修复 | 详情 |
|---|-----|---------|
| 1 | **X-Fusion-Route header** | 网关在所有转发到 fusion-mlx 的请求上注入 `X-Fusion-Route: gateway-decision`。入站 `X-Fusion-Route` header 透传不变。 |
| 2 | **X-Fusion-Source header** | 网关在所有代理到 model-hub 的请求上注入 `X-Fusion-Source: gateway`。 |
| 3 | **AllowedBackends 强制** | 配置了 `allowed_backends` 的 API key 仅限这些后端。路由到非白名单后端的请求返回 403。通配符 `"*"` 允许全部。 |
| 4 | **模型加载/卸载拦截** | `/v1/models/{id}/load` 与 `/v1/models/{id}/unload` 被拦截并重定向到 model-hub `POST /api/v1/models/{id}/serve`。 |

### v0.6.2 — 架构与健壮性

v0.6.2 解决完整审计中的架构、并发与可维护性发现:

| # | 修复 | 详情 |
|---|-----|---------|
| 1 | **统一 Principal 鉴权模型** (硬伤1) | 三套独立鉴权上下文系统 (APIKey、OIDC、RBAC) 统一为单一 `Principal` 结构体与一个 context key。`EnsurePrincipal` 模式 — 首个中间件访问时惰性创建,后续中间件填充字段。所有访问函数保持向后兼容签名。 |
| 2 | **缓存运行时配置更新** (硬伤2) | Cache `UpdateConfig()` 方法用于热重载 TTL、maxEntries、maxBytes 而无需重启。 |
| 3 | **中间件链一次构建** (M1) | 中间件链在启动时作为 `func(http.Handler) http.Handler` 组合一次构建,重载时重建 — 消除每请求重新组合开销与闭包变量捕获 bug。 |
| 4 | **safeGo panic 恢复** (M2) | 所有后台 goroutine (缓存淘汰、硬件采集、限流清理、模型刷新) 用 `safego.Go()` + `recover()` + 结构化日志 — 防止 goroutine 静默崩溃。 |
| 5 | **未知后端快速失败** (M3) | `BuildProviders` 在未知后端类型时返回错误,而非静默 `slog.Warn + continue`。防止错误配置的后端被静默跳过。 |
| 6 | **多模态 prompt 注入** (L7) | prompt 注入检测从 `image_url`、`input_audio`、`image` 多模态 content 对象提取文本,并处理 `[]interface{}` prompt 字段。 |
| 7 | **限流器按 key mutex** (L2) | `sync.Map` + 按 key mutex 消除高并发限流下的全局锁竞争。 |
| 8 | **P95 结果缓存** (P3) | LatencyTracker 带 TTL 缓存 P95 结果 — 窗口未变时避免每请求重算 quickselect。 |
| 9 | **运行时后端切换回退** (A4) | 本地后端失败在运行时触发云端回退 — 无需人工干预的弹性降级。 |

#### 待办 (P2 — 架构)

| # | 发现 | 范围 |
|---|---------|-------|
| A1 | 全局单例 → 依赖注入 | oidcProvider、jwtSecret — 需 DI 框架或 wire-up 重构 |
| A2 | Server god object 拆分 | 将 server.go 拆为按域文件 |
| A3 | 持久化存储 (Redis/Postgres) | 为多实例部署替换内存 store |

### v0.6.1 — 安全与正确性

v0.6.1 解决完整审计中的安全与正确性问题:

| # | 修复 | 详情 |
|---|-----|---------|
| 1 | Admin 鉴权需 JWT secret + users map | `admin.enabled=true` 时现要求 `admin.jwt_secret` (≥32 字符) 与 `admin.users` map。移除硬编码凭证。 |
| 2 | 集群 shared_token 必需 | 集群节点须经 `cluster.shared_token` 鉴权。未鉴权节点请求被拒。 |
| 3 | 无 embedding 函数时语义缓存禁用 | 不再有确定性哈希伪 embedding 回退。未配置 `EmbedFunc` 时语义缓存跳过,防止假相似匹配。 |
| 4 | 配置重载具回滚语义 | 热重载 handler 在配置快照提交前运行。任一 handler 失败则保留旧配置 — 无部分应用。 |
| 5 | /metrics 需 master_key | `/metrics` 端点现强制 `master_key` 鉴权。未鉴权访问返回 401。 |
| 6 | pprof 默认关闭 | 除非 config 设 `enable_pprof: true`,`/debug/pprof/*` 端点关闭。即使启用也需 `master_key` 鉴权。 |
| 7 | Cache.Get 用 RLock | 快速路径缓存读用 `sync.RWMutex` RLock 而非完整 Lock,消除高 QPS 下读竞争。 |
| 8 | Admin 密码校验 | Admin 密码至少 8 字符。更短密码在登录与配置校验时被拒。 |
| 9 | 未知后端类型跳过注册 | pool 初始化时未识别 `type` 的后端记为 warning 并跳过,而非崩溃。 |
| 10 | 空 prompt 路由到本地 | 空 prompt 内容的请求路由到本地后端,而非消耗云端配额。 |
| 11 | Batch.Get 返回深拷贝 | `Batch.Get()` 返回 batch 记录深拷贝,防止并发 goroutine 访问同一条目时的数据竞争。 |
| 12 | 注入检测中的多模态内容 | prompt 注入检测现正确从多模态 content 数组 (image+text) 提取文本,而非仅纯字符串 prompt。 |

### 破坏性变更 (v0.6.1)

- 启用 admin 时 `admin.jwt_secret` 现为**必需** (≥32 字符)
- 启用 admin 时 `admin.users` map 现为**必需** — 移除硬编码 `admin/admin` 凭证
- `/metrics` 端点需 `master_key` 查询参数或头
- `/debug/pprof/*` 需 config 显式 `enable_pprof: true`
- 除非提供 `EmbedFunc`,语义缓存不激活

## 故障排查

### `/v1/models` 返回 `{"data":[]}` (#29)

网关并发向每个本地 provider 发起 `ListModels`,跳过任何报错或超时的 provider (每个 3s)。若返回空,说明某本地后端拒绝了调用。最常见原因是 fusion-mlx 的 `route_guard` 以 HTTP 403 拒绝网关内部 `/v1/models` 请求。

诊断:
- 跳过以 **Warn** 记录 (`list models failed for provider, skipping`),含 provider 名与错误;403 错误现含响应 body,可见 route_guard 原因。
- 确认运行的**二进制**含 route-header 修复 (#26,commit `42951e8a`) — 陈旧二进制缺 `X-Fusion-Route` 而 403。重建:`go build -o fusion-gateway ./cmd/gateway`。
- 确认 `config.yaml` 设置 `backends.fusion-mlx.api_key` (fusion-mlx 鉴权 key) 与 `negotiation.route_header: X-Fusion-Route` / `route_header_value: gateway-decision`。
- 直接检查后端:`curl -H "X-Fusion-Route: gateway-decision" -H "Authorization: Bearer <mlx_key>" http://127.0.0.1:11434/v1/models`。

客户端**不**发送 `X-Fusion-Route`;网关在所有转发请求上注入,因此 chat 与 list-models 共享同一鉴权链。

### `/admin/api/fine-tune/*` 返回 SPA HTML (#30)

若微调请求返回后台 HTML 而非 JSON,说明路由穿透到了 `/admin/` SPA catch-all — 即 `/admin/api/fine-tune/` 代理路由未注册。这发生在 #30 修复 (commit `<本发布>`) 之前的**陈旧二进制**上。重建:`go build -o fusion-gateway ./cmd/gateway`。

代理将 `/admin/api/fine-tune/*` 1:1 转发到 fusion-mlx `:11434` (method/path/query/body/SSE 保留),内部注入 `Authorization` + `X-Fusion-Route`;客户端用 fg-key 鉴权到网关 (与 `/v1/*` 同一链),无需额外发送。

已知上游限制:`GET /admin/api/fine-tune/jobs/models` 返回 404 (`Job not found: models`),因为 fusion-mlx 的 `/jobs/{id}` 路由遮蔽了静态 `/jobs/models` 路径 — 见 fusion-mlx#397。这是 fusion-mlx 路由 bug,非网关问题;网关正确转发了请求 (已确认:直接调用 `:11434` 同样 404)。

## Fusion 生态

| 项目 | 角色 |
|---------|------|
| fusion-mlx | 本地 MLX 推理引擎 (主要本地后端) |
| fusion-gateway | **本项目** - 推理路由网关 |
| fusion-desk | 桌面自动化平台 |
| fusion-studio | macOS 原生 SwiftUI 客户端 |
| fusion-model-hub | 模型仓库与管理 |
