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

完整参考见 `config.example.yaml`;`config.simple.yaml` 是一份可直接运行的极简完整示例。关键配置项:

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
| `routing.token_threshold` | 8000 | token 数阈值:低于走本地,高于走云端 |
| `routing.output_input_ratio_threshold` | 0.6 | 预测输出/input token 比例上限,超过则路由云端 (低于 `output_input_ratio_min_input_tokens` 时跳过) |
| `routing.output_input_ratio_min_input_tokens` | 32 | output/input ratio 判据的最小 input token 数;低于此值跳过 ratio (避免极小请求误判, #48) |
| `routing.mode` | hybrid | 路由模式:`local` (全本地)、`cloud` (全云端)、`hybrid` (按 token/比例/硬件智能路由)。mode 快路径覆盖整个 P0–P7 链 — 见下方"部署模式 profile"(#290) |
| `routing.local_priority.max_system_memory_ratio` | 0.9 | 系统内存占比上限,超过则强制走云端 |
| `routing.local_priority.max_mlx_memory_ratio` | 0.7 | MLX/GPU 内存占比上限,超过则强制走云端 |
| `routing.circuit_breaker.failure_threshold` | 5 | 连续失败次数,超过则熔断器打开 |
| `routing.rate_limit.enabled` | true | 启用按 key 的 RPM/TPM 限流 |
| `routing.retry.max_retries` | 2 | 非流式请求最大重试次数 (仅云端 — 本地生成非幂等,快速失败, #208) |
| `backends.<name>.response_header_timeout` | 30s (云端) / 300s (本地) | 传输层等待上游响应头的时长。本地后端 (loopback base_url、UDS `socket_path` 或 `type: fusion-mlx`) 默认 300s — 本地引擎非流式调用在生成完成后才发响应头 (#208)。短于该值时受 `backends.<name>.timeout` 约束。显式配置始终优先。 |
| `routing.fallback.context_window_fallback` | {} | 模型 → 更大模型的映射,用于上下文溢出回退 |
| `routing.fallback.enabled` | false | 启用下方 `model_mapping`(别名 → 云端模型 id) |
| `routing.fallback.model_mapping` | {} | 将客户端/SDK 模型别名(如 `claude-opus-4-7`)映射到云端后端真实模型 id(如 `glm5.2`);在转发前应用于 `/v1/messages` 和 `/v1/chat/completions`(避免上游 400 → 502 "response stopped arriving",#52) |
| `routing.fallback.overflow.enabled` | true | 允许本地后端请求中途失败时运行时逃逸到云端(默认 fail-open;`false` = fail closed,原样返回本地错误)。见下文"本地优先云端溢流"(#301) |
| `routing.fallback.overflow.cooldown` | 0 (关闭) | 溢流触发后,在窗口期内将本地/集群决策直接改道云端(reason `overflow_cooldown`),请求不再重复支付已知失效的本地超时延迟 |
| `routing.fallback.overflow.cost_ceiling` | 0 (不限) | 每日(UTC)溢流累计成本上限(美元)。在云端调用前检查、响应成功后记账——越线的那次请求仍会完成;下一次溢流返回明确指出上限的 503,绝不静默回落到已损坏的本地后端 |
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
| `admin.enabled` | true | 启用管理后台与 API |
| `admin.log_max_len` | 10000 | 请求日志最大条数 (环形缓冲) |
| `admin.jwt_secret` | "" | 管理后台鉴权的 JWT 签名密钥（推荐用环境变量 `FG_ADMIN_JWT_SECRET` 注入，避免明文写入 config.yaml，#175） |
| `observability.otel_enabled` | false | 启用 OpenTelemetry 链路追踪 |
| `observability.otel_endpoint` | localhost:4317 | OTel collector 端点 |
| `observability.otel_protocol` | grpc | OTel 导出协议:grpc 或 http |
| `observability.otel_service_name` | fusion-gateway | OTel 链路中的服务名 |
| `file_log.enabled` | true | 在 stderr 之外启用按日滚动的结构化文件日志 |
| `file_log.dir` | ~/.fusion-gateway/logs | 日志目录;文件名 `gateway-YYYY-MM-DD.log`,本地跨午夜后首次写入时轮转 |
| `file_log.retain_days` | 14 | 轮转时清理超过 N 天的旧文件;0 表示永久保留 (上限 365) |
| `file_log.level` | info | 文件侧日志阈值:debug/info/warn/error;留空继承 `server.log_level` |
| `rbac.enabled` | false | 启用 RBAC 基于角色的访问控制 |
| `rbac.default_role` | viewer | 无 OIDC claims 时的默认角色:admin、editor、viewer |
| `rbac.trust_oidc_claims` | false | 可选:将 OIDC role/groups claims 映射到 RBAC 角色。默认关闭,防止 claim 污染获得 admin |
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
| `/v1/solve-stream` | POST | K-12 BNUP 流式端点 — 无论 `routing.bnup_mode` 是否开启,强制使用 `bnup_model` + `bnup_grammar`,始终 SSE (#203) |
| `/v1/completions` | POST | 传统 completions (自动转换为 chat 格式) |
| `/v1/embeddings` | POST | 文本 embedding (本地优先,批量时分片到集群) |
| `/v1/rerank` | POST | 文档重排 (默认走云端,本地有模型时走本地) |
| `/v1/cost` | GET | 成本跟踪汇总 (可选 `?key=<name>` 过滤) |
| `/v1/realtime` | WebSocket | Realtime API 代理 (双向 WebSocket 中继) |
| `/v1/models` | GET | 列出可用模型 (并发按 provider 拉取,各 3s 超时,失败跳过;`routing.mode: local` 时仅列本地 provider) |
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
| `/api/v1/shell/session` | POST | Union Web Shell:签发本地单租户会话 token (`web_shell.enabled`,#215) |
| `/api/v1/shell/session/logout` | POST | Union Web Shell:吊销当前会话 token |
| `/api/v1/shell/me` | GET | Union Web Shell:返回调用方 claims(用户、角色、模块权限) |
| `/api/v1/shell/manifests` | GET/POST | Union Web Shell:带存活状态的 manifest 列表 / 模块幂等注册(fg-key 鉴权,entry 来源白名单校验) |
| `/api/v1/shell/manifests/{id}/heartbeat` | POST | Union Web Shell:模块心跳保活(心跳超时标记 unreachable,不删除)。鉴权(#261):模块的网关签发 `fm-` 服务密钥、`web_shell.admin_key` 或 master key。请求体可选(#284):空体 = 仅保活;携带模块当前 manifest 时原子收敛扩展字段(pages/searchProviders/notifications/home),校验规则与注册一致,服务密钥完整保留(`fm-` key_id 不变,不重复展示明文);校验失败返回 400 `manifest_refresh_rejected`,但保活仍然生效 |
| `/api/v1/shell/manifests/{id}/key/rotate` | POST | Union Web Shell(#261 K1,admin-plane):轮换模块服务密钥 — 新明文仅返回一次;旧密钥在 `web_shell.key_rotation_grace`(默认 24h)双活窗口内有效,或直到首个携带新密钥的心跳到达 |
| `/api/v1/shell/manifests/{id}/key` | DELETE | Union Web Shell(#261 K1,admin-plane):吊销模块服务密钥 — 心跳 403,代理降级为结构化 503 `module_key_revoked`,Shell 主机置灰模块 |
| `/api/v1/shell/manifests/{id}` | DELETE | Union Web Shell:manifest 注销(#253)— 与注册相同的 admin-plane 鉴权;动态注册删除并持久化存储文件;未知 id 404 `manifest_unknown`,config 静态条目 409 `manifest_static` |
| `/shell/{module_id}/` | ANY | Union Web Shell:模块代理 — 转发到模块 upstream,失败返回标准化错误信封 `{code, message, module_id, retryable}`(后端宕机不裸 502) |
| `/api/v1/shell/users` | GET/POST | Union Web Shell RBAC:账号列表 / 创建账号(需 admin 角色;密码仅存 bcrypt 哈希) |
| `/api/v1/shell/users/{id}` | PATCH/DELETE | Union Web Shell RBAC:修改 display_name/disabled/permissions/roles / 删除账号(保护最后一个可用管理员) |
| `/api/v1/shell/users/{id}/password-reset` | POST | Union Web Shell RBAC:生成随机临时密码并强制 `must_change_password`(需 admin 角色) |
| `/api/v1/shell/keys` | GET/POST | Union Web Shell(#271,需 admin 角色):会话鉴权的用户 API Key 管理(Settings → API Keys 页),复用 K2 处理器 |
| `/api/v1/shell/keys/{name}/rotate` | POST | Union Web Shell(#271,需 admin 角色):轮换一个用户 key |
| `/api/v1/shell/keys/{name}` | DELETE | Union Web Shell(#271,需 admin 角色):吊销一个用户 key |
| `/api/v1/shell/adapters` | GET | E6 管理面(#275,需 admin 角色):适配器凭据库列表 — 仅返回掩码投影,明文永不回传 |
| `/api/v1/shell/adapters/{platform}` | PUT | E6 管理面(#275,需 admin 角色):存储平台适配器凭据(AES-256-GCM 静态加密,密钥派生自 master_key)+ 限速/抖动/启用 |
| `/api/v1/shell/adapters/{platform}/test` | POST | E6 管理面(#275,需 admin 角色):校验存储凭据在当前 master_key 下可解密 |
| `/api/v1/shell/connectors` | GET | E6 管理面(#275,需 admin 角色):配置式连接器文档列表,附带服务端校验问题 |
| `/api/v1/shell/connectors/{id}` | PUT | E6 管理面(#275,需 admin 角色):upsert 连器文档(`credential_ref` 从适配器凭据库解析,密钥不内联) |
| `/api/v1/shell/connectors/{id}/test` | POST | E6 管理面(#275,需 admin 角色):连接器自检(有效、启用、凭据可解析),返回耗时 |
| `/api/v1/shell/config/{key}` | GET/PUT | E6 管理面(#275,需 admin 角色):策略覆盖 — 返回 `{defaults, overrides}`;PUT 立即生效(模块运行时在模板默认值上合并) |
| `/api/v1/shell/audit` | GET | E6 管理面(#275,需 admin 或 auditor 角色):哈希链审计轨迹 — 支持 `user`/`action_class` 过滤,`integrity_ok:false` 表示链被篡改 |
| `/api/v1/shell/roles` | GET/POST | E6 管理面(#275,需 admin 角色):内建 + 自定义角色列表(写类、实时用户数)/ 创建自定义角色(内建冲突 409) |
| `/api/v1/shell/roles/{name}` | DELETE | E6 管理面(#275,需 admin 角色):删除自定义角色 — 内建固定(409),仍有用户 assigned 时带原因拒绝 |
| `/api/v1/shell/menu-permissions` | GET/PUT | E6 管理面(#275,需 admin 角色):角色 ↔ 菜单矩阵 — 默认值从 manifest 注册表派生,覆盖值整体替换;未知角色拒绝 |
| `/api/v1/password` | POST | Union Web Shell:修改调用方自身密码(shell 会话鉴权;清除 `must_change_password`) |

## Union Web Shell (#215)

可选子系统,支撑 fusion-web-shell 宿主(PRD R-1)。默认**关闭** — 不注册路由、不启动轮询 worker,完全惰性:

```yaml
web_shell:
  enabled: true
  entry_origin_allowlist:            # 必填 — A7 fail-closed
    - "http://127.0.0.1:11432"
  session:
    ttl: 12h
    secret: ""                       # 留空 = 从 auth.master_key 派生(域分隔摘要)
    users:                           # 留空 = 引导管理员(随机密码打印一次,强制首登改密)
      - username: admin
        password_bcrypt: "$2a$..."   # 明文 password 也支持,但会 WARN
        roles: [admin]
  manifests:                         # 静态兜底菜单(离线模块仍然可见)
    - id: fusion-mlx
      title: MLX 推理监控
      path: /mlx
      entry: http://127.0.0.1:11432/shell/fusion-mlx/
      devices: [desktop]
      category: platform
  heartbeat_timeout: 30s
  health_poll_interval: 30s
  modules:
    fusion-mlx:
      upstream: http://127.0.0.1:11434
      api_key: "<上游凭证,以 Authorization: Bearer 注入>"
  spa_dir: ""                        # 可选(#247):网关根路径托管 shell SPA dist
```

- **Manifest 注册中心** — 模块经 `POST /api/v1/shell/manifests` 自注册(按 id 幂等 upsert,fg-key 鉴权);心跳保活;心跳过期标记 `unreachable`(菜单置灰,不删除);静态兜底保证菜单非空。动态注册持久化到 `store.data_dir/shell_manifests.json`(原子写,0600,#230),启动时回载 — 重启不再丢失已注册模块;回载保留原心跳时间戳,过期条目重启后仍为 `unreachable`,离线标记继续生效。不再满足当前 manifest 规则 / entry 白名单的存量条目跳过并 WARN。注销(#253):`DELETE /api/v1/shell/manifests/{id}`(同 admin-plane 鉴权)删除动态注册并持久化存储;未知 id 404,config 静态条目与网关内建 `fusion-admin` 拒绝 409 `manifest_static`(不属于 API 管辖)。
- **Manifest 列表按权限过滤**(#229)— `GET /api/v1/shell/manifests` 仅返回会话已授权的 manifest 子集(`*` 见全部;空权限看不到任何条目,fail-closed),受限会话无法枚举无权访问的模块 id/标题/健康 URL。宿主菜单无需再客户端过滤。
- **Schema v1 扩展字段**(#250)— manifest(静态兜底或动态注册)可携带 `searchProviders` / `pages` / `notifications`(shell#24):shell 宿主的 ⌘K 跨模块搜索、子页面深链、通知中心轮询源。endpoint/页面 route 必须为网关绝对路径 `/...`(拒绝穿越、拒绝 `#`),通知 kind 限定 `approvals|runs|engine`,每个数组上限 32 条;非法形状在注册时 403 拒绝,而非在 shell 加载时静默丢弃。列表 API 原样透传已校验字段(不含任何密钥)。JSON 线上键为驼峰(`searchProviders`、`pages`、`notifications`,与 shell 宿主 DTO 一致);config.yaml 静态条目用蛇形(`search_providers`、`pages`、`notifications`)。**Permissions-Policy 委托**(shell#66):manifest 还可声明 `permissionsPolicy`(线上键)/ `permissions_policy`(config)— shell 宿主渲染为 iframe `allow` 属性的特性列表,注册与心跳刷新均按网关白名单(`microphone`、`camera`)校验;缺省 = 不委托(默认)。
- **Schema v2 菜单分组**(#297)— manifest 可携带 `group` / `group_title` / `group_title_en` / `sort`:仅供 shell 宿主两级菜单的展示聚合(如 fusion-office 五个应用收进一个 `Fusion Office` 顶级菜单)。`group` 为兄弟模块共享的稳定 id(`^[a-z0-9-]{1,64}$`;留空 = 不分组,按现状顶级渲染 — 完全向后兼容);标题上限 128 字符;`sort` 为组内排序提示(稳定排序,id 决胜)。兄弟模块组标题须一致 — 先注册者生效,不一致输出节流 WARN,绝不拒绝。字段不承载任何组级 RBAC:模块可见性/权限仍按模块执行(#229/#221/#289)。注册与心跳刷新时与其他 manifest 字段一同校验;`List()` 原样透传(仅 `api_key` 置零)。
- **会话签发** — `POST /api/v1/shell/session` 校验本地账号表(优先 bcrypt)或首启引导管理员;token 为 HMAC 签名,与管理员 JWT、API key 处于独立密钥空间;`tenant_id` claim 预留(单租户固定 `local`)。
- **RBAC 账号存储** — 持久账号存储(`store.data_dir/shell_users.json`,仅 bcrypt),支撑 `/api/v1/shell/users` CRUD + 密码重置(需 admin 角色,保护最后一个可用管理员)与 `POST /api/v1/password`(自助改密,清除 `must_change_password`)。config 用户仅作种子数据:存储文件一旦存在即为唯一事实源。
- **错误信封** — `/shell/{module}/` 代理失败一律返回 `{code, message, module_id, retryable}`(503 `backend_unavailable` / 404 `module_not_found`),宿主据此弹非阻塞 Toast 而非无限 Loading(A5)。
- **健康轮询** — lifecycle 管理的 worker 轮询每个 manifest 的 `health` URL,探测结果汇入状态面。
- **Entry 来源白名单** — `entry` 来源不在 `entry_origin_allowlist` 内的注册直接 403 拒绝(A7)。网关托管的相对路径 entry(`/shell/<id>/`,PRD §5.1 生产约定)天然同源,无需来源校验直接接受;路径穿越(`..`)拒绝。核心端点只经网关访问:模块 `upstream` 指向网关,不要把引擎端口暴露给浏览器。
- **iframe 会话 Cookie**(#220,F-1)— 会话签发同时下发 HttpOnly `fusion_shell_session` Cookie(SameSite=Strict,Max-Age = token TTL,TLS 下 Secure),`/shell/*` 在无 Authorization 头时回退校验 Cookie,iframe 导航请求由此通过鉴权。登出吊销 Cookie token 并清除。
- **代理层模块级 RBAC**(#221,F-2/S-8)— `/shell/<module>/` 转发前再次校验会话的模块权限(`*` 通配);未授权模块直接 403 `module_forbidden`,不触达上游。菜单隐藏只是第一层。
- **上游凭证注入**(#222,F-3)— `web_shell.modules.<id>` 支持 `api_key`;代理剥离调用方的 `Authorization` 与 `Cookie` 头,向上游注入 `Authorization: Bearer <api_key>`。shell 会话 token 永不出网关。**凭证优先级**(#294):动态注册与静态条目指向同一 upstream 时,代理注入动态注册的凭证(模块自带 api_key 或模块实际校验的网关签发 fm- key — 轮换后自动保持新鲜),过期的硬编码静态 `api_key` 仅作兜底;分叉时输出节流 WARN。upstream 不同(#256 引擎 vs bridge)时保留静态凭证 — 它鉴权的是静态 upstream,不是模块自注册的那个。
- **动态代理路由**(#232)— manifest 注册可携带 `upstream`(及 `api_key`),自注册模块无需改配置+重启即可经 `/shell/<id>/` 代理;静态 `web_shell.modules` 仍优先生效(override)。按路径例外(#256):manifest 在 ext 字段声明的端点(`searchProviders`/`notifications`,如引擎仅 web bridge 提供的 `/api/search/models` 与 `/api/notifications`)改走自注册 upstream,即使静态配置把模块钉在别处(引擎 vs bridge);其余路径保持静态 upstream,各 upstream 注入各自的凭证。`upstream` 必须为绝对 http(s) URL;entry 来源白名单与凭证注入语义完全一致。manifest 列表 API 永不外泄 `api_key`(服务端置零)。WebSocket 升级经代理双向透传(原始握手测试验证),与 SSE 并存。**自持密钥模块**(#296):上游自行校验自身 API key 的模块(如 fusion-security)按静态方式接线 — `web_shell.modules.<id>.api_key` 必须是上游自己的 key(含上游要求的租户/角色),而非网关 `fm-`/`fg-` key;同时配静态兜底 manifest,菜单项在模块重启期间仍可见;模块自注册 env 保持关闭,直到其注册体带上同一 `api_key`(否则 #294 的动态凭证优先会把上游不认的网关 `fm-` key 注入进去)。
- **同源 SPA 托管**(#247)— `web_shell.spa_dir` 指向构建好的 shell SPA dist(fusion-web-shell `apps/shell` 构建产物),网关即在其根路径托管该 SPA,未命中路径回退 `index.html`(深链刷新不丢页面)。所有 API 命名空间优先(ServeMux 最长模式匹配:`/v1/*`、`/shell/`、`/admin/`、`/api/v1/shell/*`、`/health*` ...),单端口单进程同时承载 shell、模块代理与 API;cookie SSO 同源即通(#220)。SPA 无需鉴权即可加载(首屏即 shell 登录页),目录列表被抑制,`index.html` 发 `no-store`、`assets/*` 长缓存 immutable。`index.html` 回退仅服务于浏览器导航 — GET/HEAD 且 Accept 偏好 text/html(#249);其余未命中(API 形态方法、JSON/`*/*` fetch,如打错的 `/v1/*` 调用)一律 404 + 标准化 JSON 错误信封,不再以 200 HTML 静默掩盖真实 404、导致 `JSON.parse` 失败。留空(默认)不注册任何路由 — 开发态(vite dev server `:11430` 代理 `:11432`)照常;目录配置错误仅 WARN,网关保持原有 404 行为。

### 按用户授权模块(#289)

`web_shell.session.users[].modules` 就是"让用户只看到自己的模块"的机制 — 一个字段实现多租户隔离。不要与 `web_shell.modules` 混淆(那只是代理 upstream/凭证注册表,不是可见性控制)。

- **config 种子键 `modules`,线上/API 键 `permissions`** — `web_shell.session.users` 用户条目写 `modules: [<manifest id>, ...]`;运行时同一列表以 `permissions` 读写(`GET /api/v1/shell/me` claims、`PATCH /api/v1/shell/users/{id}` 请求体)。
- **语义** — 仅精确 manifest id,外加 `*` 通配(全部模块)。`admin` 角色无视该字段,隐含全部模块。无显式 modules 的普通 `user` 角色看到空侧边栏与"暂无被授权的模块"提示 — 设计上 fail-closed。
- **三层强制** — 按权限过滤的 `GET /api/v1/shell/manifests`(#229,受限会话连其他模块 id 都无法枚举)、`/shell/<module>/` 代理层的模块级 RBAC 复检(#221,未授权模块 403 `module_forbidden`,不触达上游)、以及会话 claims 本身。
- **运行时授权仅 API** — `PATCH /api/v1/shell/users/{id}` 携带 `{"permissions": ["fusion-charactervoice"]}`(需 admin 角色)。shell AdminUsers 界面尚未暴露该字段(fusion-web-shell#65 跟踪)。config 用户仅是种子数据:`shell_users.json` 一旦存在即为唯一事实源。

实战示例 — 下游模块(fusion-charactervoice)把它的运营账号收窄到本模块 + 公共项:

```yaml
web_shell:
  enabled: true
  entry_origin_allowlist:
    - "http://127.0.0.1:11432"
  session:
    users:
      - username: admin
        password_bcrypt: "$2a$..."
        roles: [admin]                  # admin 角色隐含全部模块
      - username: cv-operator
        password_bcrypt: "$2a$..."
        roles: [user]                   # 普通 user 角色
        modules: [fusion-charactervoice]  # 仅此模块 + 公共项
```

登录后 `cv-operator` 的侧边栏收窄为公共入口 + fusion-charactervoice;其余模块代理层 403、manifest 列表不可见。

### 管理控制台作为 shell 远程模块(#216)

内置管理控制台本身就是一个 shell 远程模块(PRD R-5),无需手动注册:

- **单一事实源 manifest** — `web/admin/public/manifest.json` 构建时随 UI 一起拷入内嵌产物(vite `outDir: internal/admin/ui/dist`);`adminui.Manifest()` 解析内嵌文件,网关启动时自动注册 `fusion-admin`。`entry`/`health` 由首个 `entry_origin_allowlist` 来源在运行时拼出(manifest 文件无法预知部署来源);白名单为空则跳过注册并 WARN(A7 fail-closed)。
- **内嵌模式** — 管理 SPA 由网关在 `/admin/` 提供,由 shell 宿主以 iframe 渲染。iframe 导航无法携带 Bearer 头,因此模块 entry 直接指向 `/admin/`,SPA 保留自己的 admin-JWT 登录;API 层调用方(SDK 中转请求)可改走下面的 shell 会话桥接。
- **Shell 会话桥接** — `/admin/api/*` 路由上有效 shell 会话 Bearer,在账号于实时用户存储中仍持有 admin 角色时桥接为 admin Principal(服务端角色校验 — 被降级或禁用的账号即使 token 有效也 403)。在 admin-JWT 桥接之后执行,真实 admin JWT 优先。
- **开发端口** — shell 宿主 dev server `:11430`,admin dev server `:11431`(`/admin/api` 代理到 `:11432`);均遵循 monorepo 端口注册约定。

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

### 本地优先云端溢流 (#301)

本地优先路由意味着本地后端偶尔会在请求中途失败(引擎挂起、槽位耗尽、超时)。此时网关会**溢流到云端**:请求改用 `routing.fallback.cloud_default` 重试,用户仍能拿到结果而不是死请求。方向是显式的——**只有本地 → 云端**;云端失败绝不会静默重试到本地。

溢流的代价与约束:

- **默认 fail-open。** `routing.fallback.overflow.enabled: true` 保留逃逸通道;设为 `false` 则 fail closed,原样返回本地错误(未开启即不产生云端花费)。
- **每次溢流都有审计。** 每次触发都会向 `fg_gateway_events` 写入 `cloud_overflow` warn 事件(Admin → Insights → Events),携带触发原因(`local_failure` / `local_slot_full` / `cluster_failure` / `overflow_cooldown`)、源后端、目标后端、模型和成本增量——"这条请求为什么花钱"始终可查。响应携带 `X-Route-Decision: cloud:fallback_from_local` 和 `X-Fusion-Routed: cloud`。
- **成本受每日上限约束。** `routing.fallback.overflow.cost_ceiling`(美元,UTC 日)在当日溢流累计花费越线后拦截下一次溢流——越线的那次在途请求仍会完成;随后的请求得到明确指出上限的 `503` 外加 `ceiling_reached` 审计事件,而不是静默回落到已损坏的本地后端。成本按基础模型定价计算(计费加价属于计费侧,不进入策略)。
- **冷却期阻止重复付费。** 没有冷却期时,本地故障期间每个请求都要先支付一遍本地超时延迟才能逃逸。`routing.fallback.overflow.cooldown`(时长)在每次溢流后开启窗口;窗口期内本地/集群决策直接改道云端(`reason: overflow_cooldown`),请求立即逃逸。
- **流式仅在建立阶段错误时溢流。** 首个 token 之后的失败无法透明重试(客户端已收到部分输出),因此流中失败按错误返回;建立阶段失败会溢流,并按保守的仅输入 token 成本估算记账。

三个开关都在 `routing.fallback.overflow` 下,随配置快照热加载,并在加载时校验(时长/金额非负)。

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

### 部署模式 profile 与按项目审计导出(#290)

面向需要固定部署形态的消费方(如 EnviPermit:云端 SaaS vs 敏感文档不出内网的纯本地部署)。

**1. 部署模式 profile — 纯配置切换,客户端零改动。** `routing.mode` 即受支持的开关。mode 快路径先于整个 P0–P7 链执行(见 `Engine.Decide`):

- `hybrid`(默认)— 按 token/比例/硬件负载智能路由。
- `local` — 所有 chat/embedding 请求钉在本地 fusion-mlx;云端 adapter 永不被拨号。本地模型不可用或过载 = 向客户端返回结构化错误,绝不静默回落云端(fail-closed)。
- `cloud` — 所有请求走云端默认后端(配置了集群则优先集群)。

纯本地(全内网)配方 — 连可泄露的云端 adapter 都不存在:

```yaml
routing:
  mode: local
  fallback:
    enabled: false        # 不做别名 → 云端模型映射
backends:
  fusion-mlx:             # 唯一的 backend 条目 — 省略全部云端后端
    type: fusion-mlx
    base_url: http://127.0.0.1:11434
```

模式切换纯配置且热重载感知 — `routing.mode` 每请求从配置快照读取,改 `config.yaml`(或 `PUT /admin/api/config/routing`)即切换形态,无需重启,客户端零改动。

**2. 按项目 LLM 调用审计导出。** 每个请求记录 model、路由(`channel_name`/`channel_type`:本地 vs 云端 provider)、`route_reason`、token 用量、费用、延迟、状态、时间戳 — 以及调用方 `X-Fusion-Project-Id` 头的值(`project_id`)。在每次 LLM 调用上携带 `X-Fusion-Project-Id: <project>` 与你自己的审计日志对齐,然后程序化查询:

```bash
# admin JWT(POST /admin/api/login)以 Cookie 或 Authorization: Bearer 携带
curl -H "Authorization: Bearer $ADMIN_JWT" \
  "http://127.0.0.1:11432/admin/api/logs?project_id=envipermit&from=2026-10-01T00:00:00Z&to=2026-11-01T00:00:00Z&page=1&limit=100"

# 全量导出 — json 或 csv(CSV 含 project_id 列)
curl -H "Authorization: Bearer $ADMIN_JWT" \
  "http://127.0.0.1:11432/admin/api/logs/export?project_id=envipermit&format=csv" -o audit.csv
```

每次调用字段:`request_id, timestamp, api_key_name, model, request_type, is_stream, channel_name, channel_type, route_reason, project_id, chat_id, space_id, input_tokens, output_tokens, total_tokens, cost, cost_usd, latency_ms, ttft_ms, status_code, is_success, error_message`。其他过滤:`key_name`、`model`、`channel`、`status`、`min_tokens`、`min_cost`。Prometheus `/metrics` 暴露聚合计数(本地/云端 QPS、熔断次数)— 逐调用审计用 logs API,看板用 metrics。

**保留期提示:** 内存日志存储是有界环形缓冲(默认 10,000 条)— 合规留存要么周期性轮询导出端点,要么把持久存储切到 Redis(`store.backend: "redis"`)。

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

### 集群准入控制与共享熔断器 (#163)

在多节点 MLX 集群下,多个客户端进程并发运行时 (例如多个 fusion-code 实例),进程内的并发信号量或进程级熔断器不够用:N 个进程各自重新发现故障节点并反复冲击它才退避。网关是所有流量都能经过的单一入口,因此由它代客户端持有**集群级准入预算**和**聚合的每节点熔断器状态**。

- **每节点并发预算** — 网关对每个 MLX 节点强制执行
  `routing.local_priority.max_concurrent` (本地适配器内 RR4 硬 CAS 上限;集群节点在 `SelectNodeByModel` 中跳过已达上限的节点)。节点满额则转移到下一个节点或云端 — 不会出现惊群。
- **共享的每节点熔断器** — 每个节点有独立熔断器 (RR5),一个节点故障不会污染健康的 N-1 个。连续失败后熔断器打开,R8 协调器将该节点标记为熔断旁路 (`selectable() == false`),路由器对所有流量绕开它。恢复后熔断器关闭,节点重新可选。
- **客户端可见性** — 客户端轮询**一个**来源,而非各自重新发现故障:

  ```
  GET /gateway/v1/cluster/health      (fg-key 鉴权)
  ```

  ```json
  {
    "max_concurrent": 2,
    "total_nodes": 4,
    "routable_nodes": 3,
    "total_in_flight": 1,
    "nodes": [
      {
        "id": "node-1", "address": "http://localhost:9001",
        "state": "healthy", "in_flight": 1,
        "routable": true, "breaker_bypassed": false,
        "max_concurrent": 2, "breaker_state": "closed"
      },
      {
        "id": "node-2", "address": "http://localhost:9002",
        "state": "healthy", "in_flight": 0,
        "routable": false, "breaker_bypassed": true,
        "max_concurrent": 2, "breaker_state": "open"
      }
    ]
  }
  ```

  同样的每节点字段也出现在 `GET /v1/status` 的 `cluster` 键下。**客户端契约:** 客户端**应当**跳过任何 `routable` 为 `false` (或 `breaker_state` 为 `"open"`) 的节点,改路由到 `routable` 节点,从而集群级地避开故障节点,无需各进程重复发现。集群禁用时该端点返回 `404`。

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

## 可观测性

所有指标在 `/metrics` 导出 (Prometheus 格式,`fusion_gateway_*` 前缀),需 master_key 鉴权。

### 意图分类器 SLO (#204)

意图分类器有 **15ms 延迟 SLO**。两个指标暴露:

- `fusion_gateway_classifier_latency_seconds` (直方图,标签 `source`) — 完整延迟分布 (`laya` / `laya_fallback` / `laya_trivial` / `llm` / `router_light`)。
- `fusion_gateway_classifier_slo_breach_total` (计数器,标签 `source`) — 分类超过 15ms 时递增。告警规则:`rate(...[5m]) > 0`。

分类器在 SLO 超限时 **fail open** — 仍返回结果 (或回退到规则链);SLO 是可见性信号,不是硬拒绝。

### 缓存分层 (#204)

KV prefix cache (推理引擎内部的 prompt 前缀复用) 属于 **fusion-mlx**,不在网关 — 网关是非侵入的 (不做推理计算)。网关提供两层缓存:

1. **语义缓存** (`routing.semantic_cache`,余弦相似度 ≥ 阈值,默认 0.95) — 重复/近似 prompt 直接返回,不访问 fusion-mlx。完全匹配 (相似度 1.0) 即网关层的 prefix-cache 命中。
2. **AC9 trivial 短路** (laya_classifier.go) — trivial 查询 ("hi" / "thanks" / "ok") 的精确匹配快速路径,无 HTTP 往返。

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

### 用户 API Key(`/api/v1/keys`,#262)

面向外部调用方的网关签发 `fk-` 凭据——自助式 scope 授权,在 `/v1/*` 上强制执行。CRUD 接口仅 master key 可访问(Bearer = `master_key`):

| 接口 | 方法 | 说明 |
|------|------|------|
| `/api/v1/keys` | GET | 列出 key——尾 4 位前缀展示、scope、配额计数。不返回哈希与明文 |
| `/api/v1/keys` | POST | 创建:`{name, scopes, tenant?, quota_limit?, rpm?, tpm?, allowed_models?}`。`raw_key` 仅返回一次 |
| `/api/v1/keys/{name}/rotate` | POST | 轮换:一次性返回新 `raw_key`;旧 key 在宽限期内仍有效 |
| `/api/v1/keys/{name}` | DELETE | 吊销(软删除):status 置为 `revoked`,鉴权立即拒绝,记录保留供审计 |

- **Key 格式**:`fk-<48 位 hex>`(24 随机字节)。存储仅保留 SHA-256 哈希(`key_hash`);明文只在创建/轮换响应中出现一次。列表仅展示尾 4 位(`fk-…ab12`)。
- **Scope**(创建时必填):模块×动作矩阵,如 `["chat", "rag:embed", "cowork:runs:create", "*"]`。`middleware.ScopeEnforce` 将 `/v1/chat|completions|messages` 映射为 `chat`,embeddings/rerank 为 `rag`,images 为 `design`,connector 为 `agent`;key 持有 scope 但均未授权该模块时返回 `403 permission_error`。空 scope(legacy/admin key)行为不变、直接放行。
- **轮换宽限期**:`auth.key_rotation_grace`(默认 `24h`)。轮换记录 `key_prev_hash` + `key_rotated_at`;宽限期内新旧 key 同时可用(双活),期满后旧 key 401。与 K1 `fm-` 模块服务 key 语义一致。
- **租户绑定**:创建时可选 `tenant`,将 key 绑定到 team(`BindKeyToTeam`);请求路径从该绑定派生 `X-Fusion-Tenant`,绝不信任客户端伪造头。
- **身份透传**:每个鉴权通过的 `/v1/*` 请求向下游携带 `X-Fusion-Principal: <key name>`,上游模块无需重新鉴权即可归因调用方。
- **吊销为软删除**:key 永不物理删除(审计 + 保留策略);`revoked` 状态即刻鉴权失败。

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
- **OpenAI 兼容后端回程桥接** (#234) — 路由目标不是 `MessagesProvider` (如 fusion-mlx) 时,Anthropic→OpenAI 请求转换后由 OpenAI 格式的 chat handler 生成响应;桥接层 (`internal/server/anthropic_openai_bridge.go`) 转换回程,客户端始终收到协议合法的 Anthropic 载荷:非流式 ChatCompletion JSON → Message (content blocks、`tool_calls` → `tool_use`、`stop_reason` 映射 length→max_tokens / tool_calls→tool_use、usage prompt/completion→input/output),OpenAI chunk SSE → Anthropic 事件 SSE (`message_start` 随首个上游 chunk 立即发出、block 生命周期、`message_delta`+`message_stop` 终止对 — 流未以 `[DONE]` 结束也合成终止),错误体 (`{"detail":...}`、OpenAI `{"error":{...}}`、流前失败) → `{"type":"error","error":{...}}` 信封。回程路径回显 `X-Request-ID`。
- **双向转换**:system 消息提取、工具格式翻译 (OpenAI functions ↔ Anthropic tools)、content block 映射
- **System 扁平化**(#293)— Anthropic `system` 字段可为纯字符串或 content block 数组(Claude Code 恒为数组形式)。自动转换路径将数组扁平化为纯文本(text block 以空行连接),不再整体 JSON 序列化;以 `x-anthropic-billing-header:` 开头的 block 被丢弃,对齐 fusion-mlx 原生路由的 `_BILLING_HEADER_PREFIX` 剥离 — 每请求随机的 billing 值不再进入模型 prompt(污染 prompt + 破坏 prefix cache)。
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
trust_oidc_claims: false  # 可选:OIDC role/groups claims 映射到 RBAC 角色
team:
    enabled: true
    default_team: "default"   # 无 OIDC team claim 时分配的团队
```

- OIDC claims `role`、`groups`、`team` 仅在 `rbac.trust_oidc_claims: true` 时映射到 RBAC 角色（默认 `false`:自注册或被入侵的 IdP 无法通过伪造 claim 提权到 admin）
- master API key 始终获得 admin 角色
- 通过 `/admin/teams` 团队成本聚合 — 按团队跟踪配额
- key 与团队绑定:TeamsStore 中的 `BindKeyToTeam(apiKey, teamID)`

## 多租户隔离

网关是每个代理请求的**权威租户身份来源**。请求的租户由凭据派生（api_key → key→team 绑定，经 `GetTeamByKey` 解析），从不来自客户端提供的 header。这关闭了企业多租户部署中的跨租户伪造向量（#150 Gap 1）：租户 A 的 api_key 无法通过伪造 `X-Space-Id` 访问租户 B 的后端数据，因为网关在认证凭据后加盖权威租户。

### 网关 → 后端契约

在每个出站上游请求上，网关注入两个 header：

| Header | 值 | 含义 |
|--------|-------|---------|
| `X-Fusion-Tenant` | `<team_id>` | 此请求的**权威租户**，仅由凭据派生。不在客户端透传集合中——客户端无法设置或覆盖。仅对无 team 绑定的 master/legacy key 省略（单租户默认）。 |
| `X-Fusion-Route` | `gateway-decision` | **来源信号**——请求经过了网关的认证 + 租户派生。后端收到的请求若缺少此 header，则未经过网关。 |

后端（fusion-mlx、fusion-memory、fusion-model-hub 等）按以下方式消费：

- **`X-Fusion-Tenant` 即租户**——按此值限定所有按租户隔离的状态（模型访问、KV cache、memory 记录、KG 节点、KB 库、RAG 索引）。忽略任何客户端提供的 `X-Space-Id`（非权威透传）。
- **`X-Fusion-Route` 即来源门**——当后端启用租户隔离时，拒绝缺少 `X-Fusion-Route: gateway-decision` 的 `/v1/*`（或等价）请求并返回 403。这阻止直连端口的调用者完全绕过网关的租户派生。

### 直连端口拒绝（后端侧，上游）

CLI 和本地客户端可在后端服务的**直连本地端口**（fusion-mlx :11434、fusion-memory :11435 等）上访问它们，而无需经过网关。网关无法自行关闭此路径——每个后端必须自行强制来源门。已作为上游 issue 跟踪：

- fusion-mlx#756 — 直连端口拒绝 + `X-Fusion-Tenant` 作用域
- fusion-memory#16 — 直连端口拒绝 + 租户作用域 memory/KG 存储
- fusion-model-hub#53 — **已实现**（gateway-origin + `X-Fusion-Tenant` 强制已发布）
- fusion-rag#61 — **已实现**（fusion-kb 改名 fusion-rag；gateway-origin + 租户作用域已发布）

在后端发布其来源门之前，该后端的直连端口仍是租户隔离边界，运维人员需在网络层保护（绑定 loopback、防火墙端口，或仅通过网关运行）。

### 多实例 fusion-mlx 的 HA 路由

当部署多个 fusion-mlx 实例时，启用 `cluster.ha_routing`（默认关闭），网关在 P0.25 将 `/v1/*` 在健康的 mac 平台节点间负载均衡，先于单本地上游路径——对客户端透明的故障转移。参见[路由逻辑](#路由逻辑)与 `cluster.ha_routing` 配置块。

## 租户配额、优先级队列、双主负载均衡 (#159)

三个面向多租户 local-first 部署的可选特性。全部默认关闭；无租户配置的单节点部署行为不变。

### 按租户日配额 (#159-A)

每个团队（租户）可设置日成本上限（`DailyQuotaLimit`，美元）与按 key 的请求预算。网关在推理池**之上**强制执行——耗尽日预算的租户在 `BudgetBlock` 中间件处被阻断（fail-closed），不消耗任何本地槽位。配额在所有成本记录点（chat、流式、embedding、rerank）通过单一 `recordAndCharge` 槽自动扣减，内存 store（日期比对滚动，无后台扫描）与 Redis store（Lua 原子 `INCRBYFLOAT` + 按日期 key 的 `EXPIRE`）均生效。租户的 `Tier` 标签（heavy/general/light）从凭据绑定的团队带入请求 `Principal`。

```yaml
# 团队经 /admin/api/teams 管理；每个团队可设：
#   daily_quota_limit: 5.0      # 美元/日上限（0 = 不限）
#   tier: "heavy"               # 准入类别（见优先级队列）
```

`BudgetBlock` 中间件在按 key 检查**之后**、请求进入路由器**之前**检查团队日配额：超限 → 403，store 错误 → 503（fail-closed）。日计数器在日期变更时隐式滚动——无需 reaper goroutine。

### 三级优先级准入队列 (#159-B)

本地模式竞争场景下，可选优先级队列（`routing.tier_queue`）按类别准入——**heavy**（diffusion、重模型、或标记 heavy 的租户）获得队头派发，使短聊天突发不会饿死其后的长生成；**light**（embedding、短聊天）优先级最低；**general** 为默认。同一类别内保持 FIFO。租户 `Tier` 标签优先于粗粒度意图启发式（付费购买 heavy 类别的租户无论请求形态都以 heavy 优先级准入）。

```yaml
routing:
  tier_queue:
    enabled: true               # 默认 false
    max_concurrent: 2           # 本地槽位上限（钳制 >=1）
    queue_timeout: 30s          # 超时无槽位释放则 429
    heavy_guarantee: 1          # 为 heavy 预留槽位（总和 <= max_concurrent）
    light_guarantee: 0          # 为 light 预留槽位
```

`committed` 标志消除派发竞争：在槽位交接**之后**到达的客户端取消或超时不会窃取或泄漏槽位——goroutine 返回释放闭包使槽位最终被释放。队列在热重载时重建（RR6 对齐）。仅在 `mode: local` 生效；hybrid 模式回退到云端而非排队。

### 双主主备活跃负载均衡 (#159-C)

集群 `master` 模式下，网关现可前置**多个** fusion-multi-node master（如 Traefik 后的 `:11452` + `:11453`，或直连），采用最少连接池 + 健康检查故障转移。每个 master 同步调用（`/api/nodes`、`/api/routing/summary`）路由到在途请求最少的池内 master；出错时在返回前故障转移到下一候选。失败 master 以指数退避（5s → 2m 上限）熔断冷却，并被选择跳过，除非无健康对等节点（fail-open）。单数 `cluster.master.address` 仍向后兼容——仅设置它时，池退化为与 #159 前行为一致的单元素列表。

```yaml
cluster:
  mode: master
  master:
    addresses:                  # #159-C: 双主主备活跃
      - "http://127.0.0.1:11452"
      - "http://127.0.0.1:11453"
    shared_token: "..."
    # address: "http://127.0.0.1:11452"   # 旧单数（仍可用）
```

## 身份控制面 (#157)

与 [`fusion-identity`](https://github.com/dahai80/fusion-identity) 的可选集成——一个高频 gRPC 控制面，在推理到达 fusion-mlx 之前原子地完成认证 + 获取并发租约。默认关闭（`identity.enabled: false`）；关闭或 identity 不可达且 `fallback_to_local: true` 时，本地 `APIKeyAuthWithStore` 路径仍是认证路径。

```yaml
identity:
    enabled: true
    endpoint: "127.0.0.1:11440"      # fusion-identity gRPC 监听（仅环回）
    deadline_ms: 10                  # AuthorizeAndAcquire RPC 超时（热路径）
    keepalive_sec: 30
    fallback_to_local: false         # true = identity 故障时降级到本地认证
    breaker_threshold: 5             # 连续失败多少次熔断器打开
    breaker_open_sec: 30             # 打开时长，之后放一个半开探测
```

启用后，推理热路径经过 `IdentityAuth`（位于本地 key 认证之后、RBAC 之前）：

1. **`AuthorizeAndAcquire`**——将已解析的 api key + 目标模块/模型发给 identity。允许时，identity 派生的租户为权威（覆盖本地 key→team 绑定），租约写入请求上下文。拒绝时，错误码映射为 HTTP（`INVALID_API_KEY→401`、`TENANT_DISABLED/MODULE_UNAUTHORIZED/MODEL_UNAUTHORIZED→403`、`CONCURRENCY_LIMIT_EXCEEDED→429`、`DAILY_QUOTA_EXCEEDED→402`、`RATE_LIMIT_EXCEEDED→429`）。
   - **跨租户防护 (#160)**——网关将凭据解析出的租户（`p.Team.ID`，由 `APIKeyAuthWithStore` 经 `GetTeamByKey` 在上游绑定）作为 `tenant_id` 发到 RPC，而非客户端断言值。identity 服务端将其与 api-key 的真实租户比对，不一致则拒绝（`MODEL_UNAUTHORIZED` → 403）。这闭合了 P2-3 跨租户冒充路径：租户 A 的 key 不能通过断言别的租户来获取租户 B 的租约。
2. **熔断器**——连续传输失败打开熔断器；新请求随后返回 `503`（严格模式）或降级到本地认证（`fallback_to_local: true`）。活跃流继续运行——熔断器只门控新请求。认证拒绝（identity 回答"否"）不会触发熔断。
3. **租约生命周期**——请求结束时，`ReleaseLease` 释放并发槽，`ReportUsage` 将 token 消耗计量到 identity 的配额计数器。两者均为异步 + 尽力而为（独立 context、fire-and-forget），慢/不可达的 identity 不会阻塞已发送的响应。
4. **VRAM 优先级 + KV cache-tag 隔离**——租约的 `priority`（1=低、2=正常、3=高）与 `max_allowed_tokens` 作为 `X-Fusion-Priority` + `X-Fusion-Max-Tokens` 头转发给 fusion-mlx。网关非侵入（此处无分配器或 KV 缓存）；这些头是 fusion-mlx 在其侧应用 P0 抢占/P2 空闲排队调度与 `tenant:{tenant_id}:` 前缀缓存隔离的信号契约。

Admin-JWT 与 master-key 请求绕过身份控制面（管理面不属于推理租户域）。本地 key-store 降级路径保留为优雅降级，而非替代——启用时 identity 是租户上下文的唯一权威来源。

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

**PostgreSQL 持久化** (`persistence.enabled: true`,默认关闭):请求元数据 +
延迟信号 (TTFT/TPOT/P95/分片数/tokens per sec) + 捕获的请求/响应体写入
PostgreSQL,重启不丢日志。表 `fg_request_logs` / `fg_request_payloads` 按 `ts`
日分区,保留期到点整分区 `DROP` (O(1) 无膨胀)。`fg_request_logs` 共 33 列,
含失败请求的 `error_message` (上游错误截断 512 字符) 和由响应后 guard-PII
扫描打上的 `contains_pii`。开启 `capture.stream_chunks` (默认关闭) 时每个
SSE 分片额外落一行 `fg_stream_chunks` (seq/ts/delta_ms/bytes),保留
`stream_chunks_days` (默认 7 天)。有效 JSON 体入 `req_body` /
`resp_body` (jsonb 可查询);非 JSON 捕获 (SSE 帧、二进制) 入 `req_raw` /
`resp_raw`,并以 `resp_raw_mime` 记录来源类型。DSN 走环境变量
`FUSION_GW_PG_DSN` (bindSecretEnv 模式,绝不写进 config.yaml);开启但缺 DSN
直接启动 FATAL。写入链路:非阻塞内存队列 (容量 8192,满则丢最旧),批量
`COPY FROM` (500 条或 500ms),PG 故障指数退避重试,停机时合并批内 + 队列
drain。计数器 `fusion_gateway_persist_{enqueued,written,dropped}_total`。
捕获体默认脱敏 (api_key/apiKey/authorization/password 字段剥离),单方向
512 KiB 截断并置 truncation 标志。

实测开销 (隔离压测:对确定性 mock 后端做 ON/OFF 交错轮次,并发 20,
流式/非流式混合):中位数 **+0.13 ms**、p99 **+0.04 ms** 端到端、RSS
**+0.3 MB** —— 远优于 <1 ms / <300 MB 的验收标准。热路径成本只有
TeeReader + 响应写入镜像 (内存拷贝);PII 扫描、字段剥离、落库全部发生在
响应完成后的 finalize defer 中。真实模型 c=20 端到端测试的波动主要来自
fusion-mlx 排队,而非持久化链路。

**Insights 只读 API (P3a)**:基于持久化表的 admin 专属分析/检索端点
(`requireAdminRoleStrict`;持久化关闭时 503)。查询走独立只读连接池,
`statement_timeout=10s`,时间窗口强制约束 (默认 24h,上限 31 天):

| 方法 | 路径 | 用途 |
|---|---|---|
| GET | `/admin/api/insights/latency` | TTFT/TPOT p50/p95/p99 时序;`granularity=minute\|hour\|day`,过滤 `backend`/`model`/`project_id` |
| GET | `/admin/api/insights/compare` | backend×model 横向对比:延迟分位、成功率、tokens/sec、成本 |
| POST | `/admin/api/insights/logs/search` | Explorer 检索 DSL:时间窗、`q` (model/错误/request-id 子串)、endpoint/backend/model/project/key/状态/流式/成功/最小时长、分页 (limit ≤ 200) |
| GET | `/admin/api/insights/logs/{id}/payload` | 单条日志的捕获请求/响应体 (jsonb + raw 双列) |
| GET | `/admin/api/insights/logs/{id}/chunks` | 单条请求的逐分片流式时序,来自 `fg_stream_chunks` (空列表表示该请求未开启分片捕获,非错误) |
| POST | `/admin/api/insights/logs/{id}/replay` | 服务端重放:把存储的请求体重新发往网关自身的 `/v1` 端点 (强制关闭流式,master-key 鉴权,300s 上限),完整生产链路 (鉴权 → 路由 → 适配 → 日志) 真实执行;重放本身会作为新行出现在 Explorer 中 |
| GET | `/admin/api/persistence/test` | 5s 连通性 ping |

生成类请求以 `ttft_ms > 0` 隔离——Go 零值落库为 `0.0` (非 NULL),否则
`/v1/models` 等探针会把 TTFT 分位拖到零。SQL 集成测试
(`internal/insights/sql_integration_test.go`) 由 `FUSION_GW_INSIGHTS_TEST_DSN`
门控,CI 无 PG 自动跳过。

**硬件采样 + 网关事件 (P3b)**:随持久化 schema 一并建的两张低量级普通表
(`internal/persist/pg/copy.go`),保留期由小时级 sweep 的 DELETE 清理:

- **`fg_hw_samples`** — 每 30s 一条硬件快照 (`StartHWSampler`,
  `lifecycle.Worker`):系统/MLX 内存比率、swap 换页率、GPU 利用率、
  已加载模型数、采集错误标记。`loaded_models` 是计数——采集器只暴露
  `MLXModelsLoaded int`,非名称列表。
- **`fg_gateway_events`** — 结构化生命周期审计:`gateway_started` /
  `gateway_stopped` (停机路径用阻塞式 `RecordEventSync`——异步版本会和
  pool Close 竞争)、`config_reloaded`、`persist_degraded` /
  `persist_recovered` (每次故障一条事件,而非每条重试一条)。

保留期:`persistence.retention.hw_samples_days` (30)、
`gateway_events_days` (180)。admin 专属只读端点 (持久化关闭时 503):

| 方法 | 路径 | 用途 |
|---|---|---|
| GET | `/admin/api/insights/hw-samples` | 硬件时序;`from`/`to` (默认 24h,上限 31 天)、`limit` (默认 2880,上限 10000) |
| GET | `/admin/api/events` | 网关事件流;过滤 `level`/`event`、`limit` (默认 100,上限 1000) |

**异常检测 (P3b-2)**:5 分钟一次的后台检测器
(`internal/insights/detector.go`,`lifecycle.Worker`)扫描
`fg_request_logs` + `fg_gateway_events`,检测结论写入 `fg_anomalies`。
五个检测器,均以"当前截断时间桶 vs 过去 7 天同时段基线"对比:

| kind | 触发条件 | 严重级 | 窗口 |
|---|---|---|---|
| `error_storm` | 归一化错误类计数 ≥ 50 (按 backend+model) | high | 5 分钟 |
| `error_spike` | 错误率 > 1% 绝对值 且 > 基线 2 倍 | high | 15 分钟 |
| `ttft_shift` | TTFT P95 > 基线均值 1.5 倍 | medium | 1 小时 |
| `traffic` | 全局请求量偏离基线 > ±60% | medium | 1 小时 |
| `breaker` | 当日熔断次数 > 前 7 天日均 3 倍 (无基线时 ≥ 3 次) | medium | 1 天 |

设计要点:

- **去重在数据库而非内存**:签名 (kind, backend, model) 已存在 `ts >= 桶起点`
  或 `muted` 且 `muted_until > now()` 的行则跳过。重启/慢 tick 不产生重复行;
  抑制检查失败时放行 (fail open)。
- **错误归一化** (`normalizeError`):有序子串匹配 (特殊在前),映射到稳定
  错误类 (`connection_reset`、`context_deadline`、`eof`、`http_5xx` 等);
  未知消息按截断 80 字符小写形式聚簇。
- **熔断事件生产者**:`router.Engine.SetEventRecorder` 把熔断翻转
  (强制 Trip 与失败阈值两种)以 `breaker_tripped` 事件写入
  `fg_gateway_events`,熔断检测器消费这些事件。
- **小样本防护**:每个基线对比都要求最小当前样本量与基线桶数,
  小样本比率不触发。
- 每次触发同时写一条 `anomaly_detected` 网关事件。

admin 端点 (strict 角色,持久化关闭时 503):

| 方法 | 路径 | 用途 |
|---|---|---|
| GET | `/admin/api/anomalies` | 检测列表;过滤 `status`/`kind`/`backend`、`limit` (默认 100,上限 500) |
| POST | `/admin/api/anomalies/{id}/ack` | 标记已处理;body `{"by": "..."}` (默认取 JWT 用户名) |
| POST | `/admin/api/anomalies/{id}/mute` | 屏蔽该签名的重复检测;body `{"days": 7}` (默认 7,上限 90) |

ack 与 mute 均写审计事件 (`anomaly_acked` / `anomaly_muted`) 到
`fg_gateway_events`。被 mute 的签名跨重启持续抑制直至 `muted_until` 到期;
对 muted 行执行 ack 会被拒绝 (静默解除屏蔽会重新武装操作员已压制的检测器)。

### 优化规则引擎 (P3b-3)

每小时一次的后台 worker (`internal/insights/rules.go`,`lifecycle.Worker`)
对已持久化的流量评估六条优化规则,把可执行的结论写入
`fg_optimization_findings`。相同 rule + scope 在 24 小时内在数据库侧去重,
重启不会产生重复行。

| 规则 | 触发条件 (含护栏) | 动作 |
|---|---|---|
| `MIGRATE_TO_LOCAL` | 云端占比 > 10% 且本地 TTFT P95 < 0.7× 云端 P95;≥ 50 请求、≥ 10 本地生成样本、本地错误率 ≤ 2% | 可机执行:apply 写入 `models.supply.<model> = ["fusion-mlx", ...existing]` 并热加载 |
| `KEEP_CLOUD` | 本地错误率 > 2% 且本地请求 ≥ 50 | 建议性 |
| `MIGRATE_TO_CLOUD` | 某个小时段错误率 > 5% 且本地请求 ≥ 20 | 建议性 (按时段分流) |
| `TRIM_TOKENS` | > 5% 的请求 input > 50× output tokens;≥ 100 请求 | 建议性 |
| `DEDUP_PROMPT` | 24h 内相同请求体发送 > 3 次 (聊天端点,SQL 内 md5) | 建议性 (启用语义缓存) |
| `PRELOAD_MODEL` | 7 天内 ≥ 3 个不同本地模型 且平均系统内存占用 < 85% | 建议性 (预加载在 fusion-mlx) |

管理端点 (strict 角色,persistence 关闭时 503):

| 方法 | 路径 | 用途 |
|---|---|---|
| GET | `/admin/api/optimization/findings` | 结论列表;过滤 `status` (`new`/`applied`/`dismissed`)、`rule`、`limit` |
| POST | `/admin/api/optimization/findings/{id}/apply` | 应用 `MIGRATE_TO_LOCAL` 结论:把 `fusion-mlx` 前插进 `models.supply.<model>`,热加载后打 `applied` + `applied_config` |
| POST | `/admin/api/optimization/findings/{id}/dismiss` | 标记不予处理 |

apply 顺序:先写配置并热加载,**之后**才写数据库状态 —— 热加载失败时结论
保持 `new` 可重试,不会留下虚假的 `applied` 行。只有 `new` 状态可流转;
其他规则返回 400 (建议性)。两种操作均写审计事件
(`finding_applied` / `finding_dismissed`) 到 `fg_gateway_events`。

附带修复:模型名带点 (如 `mlx-community/Qwen3.5-4B-MLX-4bit`) 时
`models.supply`/`models.aliases` 的 key 被 viper 拍平机制拆嵌套导致重载失败 ——
这两个映射现在绕过 viper,直接从原始 YAML 解析。同时所有管理端配置写入
(PUT 处理器、finding apply) 落盘前先经 `config.ProbeValidate` 干跑校验:
无法加载的修改直接拒绝,`config.yaml` 保持原样,不再把坏 YAML 写上磁盘。

### 定期报告 (P3b-3c)

后台 worker (`internal/insights/reports.go`,`lifecycle.Worker`) 定期生成
Markdown 运维报告写入 `fg_reports`,并在 `~/.fusion-gateway/reports/`
落一份文件副本:**日报** 覆盖上一个自然日,**周报** 覆盖上一个完整的
周一至周日,均在本地时间 08:00 后发布。以 `(period, range_start)` 去重,
重启不会重复生成;数据库行是权威,文件副本尽力而为。

报告内容:总量 (请求数 + 成功率,含与上一窗口的环比)、延迟 (全局 TTFT
P95 及环比、最差模型 top3)、异常 (当前 open 数 + 按类型分布)、成本
(云端花费、本地节省、超大 prompt 浪费估算)、优化 (本期新结论 + 已应用
结论的采纳回访 —— 对比该模型 7 天本地/云端流量占比,验证 supply 是否
真的把流量搬到了本地)。

管理端点 (strict 角色,persistence 关闭时 503):

| 方法 | 路径 | 用途 |
|---|---|---|
| GET | `/admin/api/reports` | 报告列表 (仅元信息,不含正文);过滤 `period`、`limit` |
| GET | `/admin/api/reports/{id}` | 完整报告,含 `content_md` |
| POST | `/admin/api/reports/generate` | 手动触发,body `{"period":"daily"\|"weekly"}` —— 幂等,已生成则直接返回已有行 |

保留策略:`fg_reports` 随其他 insights 表按
`persistence.retention.insights_days` (默认 180 天) 清理。

### 洞察仪表盘 (P4)

管理后台新增 **Insights** 菜单组,把上述持久化分析全部可视化 (insights
端点返回 503 时页面显示友好的"持久化未开启"提示):

| 页面 | 路由 | 内容 |
|---|---|---|
| Latency | `/admin/insights/latency` | TTFT / TPOT p50+p95 时间序列 (请求/错误数为副轴);支持 backend/model/project 过滤;粒度随窗口自动选择 (≤24h 按小时,否则按天) |
| Compare | `/admin/insights/compare` | 跨 provider 的 TTFT p95 柱状图 + 可排序明细表 (成功率、TPOT、tok/s、平均 token、成本),local/cloud 标签 |
| Explorer | `/admin/insights/explorer` | 全量日志检索 (支持全部搜索 DSL 过滤、服务端分页、URL 参数预填 —— Optimize/Anomalies 页会带该结论的 model/backend 深链到此);行展开查看捕获的请求/响应 payload + 逐分片流式时序表 (`fg_stream_chunks`,未开启捕获时置灰),并提供 **Replay** 按钮 —— 把存储的请求体经完整网关链路重发 (强制关闭流式) 并内联展示响应 |
| Optimize | `/admin/insights/optimize` | 优化结论列表,支持 Apply (MIGRATE_TO_LOCAL 会把 fusion-mlx 前插进 `models.supply`) / Dismiss |
| Anomalies | `/admin/insights/anomalies` | 异常列表,支持 Ack 和 Mute (1–90 天),严重度标签 |
| Reports | `/admin/insights/reports` | 周期报告列表,支持幂等的按需生成 + Markdown 查看器 |
| Capacity | `/admin/insights/capacity` | 请求量热力图 (24h × 7d 小时桶,强度渐变),基于 `fg_request_logs`;硬件容量曲线 (系统/MLX 内存占比、GPU 利用率、已加载模型数),基于 `fg_hw_samples` |

配置区的 **Persistence** 页面对应 GET/PUT
`/admin/api/config/persistence`,覆盖整个 `persistence.*` 命名空间
(postgres 连接池、写队列、body 捕获、保留天数)。连接池/队列/捕获改动
需重启生效;`postgres.dsn_env` 指的是承载 DSN 的环境变量名 —— DSN 本身
永远不落 `config.yaml`。PUT 响应从重载后的快照回读,因此
`validatePersistence` 的归一化结果会体现在响应里。

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
| `/admin/api/config/persistence` | GET/PUT | PostgreSQL 持久化 (连接池、队列、捕获、保留) |

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
| `/admin/config/reload` | POST | 触发确定性配置重载。返回 `{"status":"reloaded","version":N}`。fsnotify 文件监听在 macOS 上不可靠，此端点是可靠的重载路径 (issue #57)。 |
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

**75 个测试**,跨 6 个 spec 文件:

| Spec | 覆盖 |
|------|--------|
| `01-login.spec.js` | 登录表单、错误/正确凭证、`admin_token` cookie、路由守卫重定向 |
| `02-keys.spec.js` | key 表、UI 创建、运行时生命周期 (key 在 `/v1/models` 可用 → 删除 → 401/403)、空名 400、PUT 编辑持久化、id==name |
| `03-channels.spec.js` | channel 表、UI 表单创建 (name/type/base_url/priority/weight)、UI 删除、空名 400、PUT 编辑持久化 |
| `04-dashboard-analytics.spec.js` | 仪表盘、分析概览 + 利润、日志 + 导出端点 |
| `05-config.spec.js` | 全部 26 个配置区段 GET (渲染) + PUT (持久化) + 热重载往返;CORS preflight 回显;按 key 限流 429;未鉴权 PUT 被拒 |
| `06-insights.spec.js` | 六个 insights 页面渲染 (persistence 开启态不得出现 503 提示);insights API 面 (latency/compare/logs search/hw-samples/events/anomalies/findings/reports);persistence 配置 GET + 幂等 PUT 往返 |

注:`06-insights.spec.js` 要求网关开启 PostgreSQL 持久化,且通过 `FG_ADMIN_PASS` 传入真实管理员密码 (fixtures 默认值是占位符;5 次登录失败会触发 15 分钟账号锁定)。

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

### v0.9.69 — 序列化架构治理落地 (P1–P5 + G 系列)

完成序列化架构审计 (`audit/fusion-gateway-seraization-archi-report-1015.md`,
repo 外围文档) 的全部整改。功能细节见上方各节 (TTFT/TPOT 采集、PostgreSQL
持久化、Insights 只读 API、硬件采样与网关事件、异常检测、优化规则引擎、
定期报告、洞察仪表盘);本条目记录验收演练及演练本身暴露的 bug。

| # | 条目 | 详情 |
|---|-----|---------|
| 1 | **P1–P4 平台** | 全部生成路径的 TTFT/TPOT 采集;PG 持久化 (`fg_request_logs` + `fg_request_payloads`,分区 DROP 保留);insights 读 API + 六个管理页;异常检测、优化规则引擎、定期报告 (见对应章节)。 |
| 2 | **G9 文件日志** | `file_log.*` 命名空间:`~/.fusion-gateway/logs` 下按日滚动的 `gateway-YYYY-MM-DD.log`,本地跨午夜首次写入时轮转,轮转时按 `retain_days` 清理,支持文件侧日志级别。 |
| 3 | **G6 PII 打标** | `fg_request_logs` 新增 `contains_pii` 列,由响应后的 guard 模式扫描 (sync.Once 编译的内置 SSOT) 打标 —— Explorer 中可检索。 |
| 4 | **G10 流式分片捕获** | `capture.stream_chunks` (默认关闭):每个 SSE 分片一行 `fg_stream_chunks` (seq/ts/delta_ms/bytes),保留 7 天;`GET /admin/api/insights/logs/{id}/chunks`;Explorer 逐行渲染分片时序。 |
| 5 | **G13 重放 + 容量 + p99** | `POST /admin/api/insights/logs/{id}/replay` 把存储的请求体经网关自身 `/v1` 链路重发 (强制关闭流式,300s 上限);Optimize/Anomalies 页通过 URL 参数预填深链 Explorer;新增 Capacity 页 (24h×7d 请求量热力图 + 硬件容量曲线);latency API 增加 p99 分位。 |
| 6 | **G12 极简配置** | `config.simple.yaml` —— 一份可直接运行的完整示例,展示新部署所需的最小配置。 |
| 7 | **修复:失败请求字段缺失** | 失败请求落库时 `model`/`backend`/`error_message` 为空 (错误路径在日志条目打标前就返回),导致错误风暴分析和 Explorer 错误检索看到空字段。现所有失败路径都打标这三个字段 (上游错误截断 512 字符) —— AC20 错误风暴演练发现。 |
| 8 | **修复:保留期分区 DROP 从未生效** | 分区命名为 `<table>_YYYY_MM_DD`,但保留期 sweep 从 `_20` 扩展后的 LIKE 前缀切日期 —— 把年份的世纪部分截掉了 —— 导致解析全部失败,过期分区从未被 DROP。`partitionDay` 改为从表前缀重新切分;AC10 保留期演练发现。 |
| 9 | **F8 key 软删除** | `DELETE /admin/api/keys/{name}` 是软删除 (行翻转为 Disabled),绝不硬删 —— key 可恢复,请求日志保留 `api_key_name` 关联。 |
| 10 | **AC6 持久化开销演练** | 对确定性 mock 后端做 ON/OFF 交错轮次 (并发 20,流式/非流式混合):中位数 **+0.13 ms**、p99 **+0.04 ms**、RSS **+0.3 MB** —— 远优于 <1 ms / <300 MB 标准。真实模型 c=20 端到端波动由 fusion-mlx 排队主导,非持久化链路。 |
| 11 | **AC10/AC11/AC20 演练** | 保留期 (过期分区 DROP 验证)、热加载 (持久化开关下一请求即生效,无需重启)、错误风暴 (检测器触发、事件落库、Explorer 展示 `error_message`) —— 均在真实网关上验证。 |
| 12 | **上游阻塞** | G4/AC12 (`X-Fusion-Via` 头、`direct_calls_blocked`、fusion-mlx 严格鉴权) 仍阻塞于 fusion-mlx#1381。 |

**验证**:`go vet ./...` 干净;`go build ./...` 通过;`go test ./... -count=1 -race` 全绿;73+ Playwright e2e 对真实网关全绿;AC6/AC10/AC11/AC20 演练在生产环境执行,演练数据事后清理。

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
