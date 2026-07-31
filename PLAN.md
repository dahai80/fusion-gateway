# Fusion-Gateway v0.2.0 — 补齐 LiteLLM 差距实施计划

> 目标: 在保持 Go 单二进制 + 硬件感知 + 本地优先核心优势的前提下, 分 4 个 Phase 补齐与 LiteLLM 的关键功能差距
> 版本: v0.2.0
> 日期: 2026-07-31
>
> 用户原始指令: "你的目标就是取代litellm，你做一个补齐所有和litellm对比差距的计划，马上落地实施"
> 受影响调用方: server.go (所有 handler), auth.go (认证链), engine.go (路由决策), provider.go (接口), metrics.go (指标)
> 数据模式变更: AuthKeyConfig 增加 ExpiresAt/Metadata/BudgetLimit; 新增 RateLimitConfig/CacheConfig/CostConfig/PIIConfig/RetryConfig/CloudRoutingConfig

---

## Phase 1: 安全基础设施 (P0) — 预计 3 天

目标: 补齐最关键的安全和访问控制缺口, 让网关不再"裸奔"。

### 1.1 RPM/TPM 限流中间件
- **新建** `internal/middleware/ratelimit.go`
- 滑动窗口算法 (纯内存, 无 Redis 依赖)
- 按 Key RPM/TPM 限流, 读取 `AuthKeyConfig.RPM` / `AuthKeyConfig.TPM`
- 全局 RPM/TPM 限流 (可配置)
- 配置结构: `RateLimitConfig` 加入 `RoutingConfig`
- 429 响应 + `Retry-After` header + `X-RateLimit-Remaining` headers
- **修改**: `internal/config/config.go` — 增加 `RateLimitConfig`
- **修改**: `internal/middleware/auth.go` — 认证成功后将 key config 存入 context
- **修改**: `internal/server/server.go` — 在 middleware chain 注入限流中间件

### 1.2 Key 级模型白名单实施
- **修改**: `internal/middleware/auth.go` — 认证成功后检查 `AllowedBackends`
- **修改**: `internal/server/server.go` — handleChatCompletions 中根据 key 的 AllowedBackends 过滤路由目标
- 非法模型请求返回 403 + 明确错误信息
- 通配符支持: `["*"]` = 全部允许

### 1.3 Key 过期时间 + Metadata
- **修改**: `internal/config/config.go` — `AuthKeyConfig` 增加 `ExpiresAt`, `Metadata`, `BudgetLimit`
- **修改**: `internal/middleware/auth.go` — 检查 Key 过期时间
- 通过 context 传递完整的 `AuthKeyConfig` 给下游 handler

### 1.4 Master Key 支持
- **修改**: `internal/config/config.go` — `AuthConfig` 增加 `MasterKey`
- **修改**: `internal/middleware/auth.go` — Master Key 跳过白名单/限流检查

---

## Phase 2: 可靠性与效率 (P1) — 预计 3 天

目标: 补齐缓存、成本追踪、重试 — 提升网关实用价值。

### 2.1 内存缓存
- **新建** `internal/cache/cache.go` — LRU 缓存, sha256(request_body) 作为 key
- 可配置 TTL, 最大条目数, 最大内存
- 仅缓存非流式请求
- 缓存命中返回 `X-Cache: HIT` header
- **新建** `internal/cache/cache_test.go`
- **修改**: `internal/config/config.go` — 增加 `CacheConfig`
- **修改**: `internal/server/server.go` — 在 handleNonStreamChat 中加入缓存检查
- **修改**: `internal/observability/metrics.go` — 增加缓存命中/未命中指标

### 2.2 成本追踪
- **新建** `internal/cost/tracker.go` — 按 Key/Backend/Model 统计 token 消耗和成本
- 内置模型定价表 (可热重载覆盖)
- 每次 Chat/Embedding 完成后异步记录
- 暴露 `/v1/cost` API 查询花费
- 按日/按 Key/按 Model 聚合
- **新建** `internal/cost/pricing.go` — 模型定价数据 (OpenAI/Anthropic/DeepSeek 主流模型)
- **修改**: `internal/config/config.go` — 增加 `CostConfig`
- **修改**: `internal/server/server.go` — 在 handleNonStreamChat/handleStreamChat 完成后调用 cost tracker
- **修改**: `internal/observability/metrics.go` — 增加成本指标

### 2.3 重试 + 指数退避
- **新建** `internal/middleware/retry.go` — 重试中间件/包装器
- 可配置: max_retries, initial_backoff, max_backoff, retryable_errors
- 仅对 5xx / timeout / connection refused 重试
- 流式请求不重试 (仅非流式)
- **修改**: `internal/config/config.go` — `RoutingConfig` 增加 `RetryConfig`
- **修改**: `internal/server/server.go` — 在 provider.Chat 调用外包重试逻辑

### 2.4 上下文窗口 Fallback
- **修改**: `internal/router/engine.go` — P6 模型不可用时, 查找 context_window_fallback_dict
- **修改**: `internal/config/config.go` — `FallbackConfig` 增加 `ContextWindowFallback map[string]string`

---

## Phase 3: 云路由策略增强 (P2) — 预计 2 天

目标: 补齐云到云路由能力, 让云端请求也能智能分配。

### 3.1 云路由策略引擎
- **新建** `internal/router/cloud_strategy.go` — 云端路由策略
- 策略类型:
  - `latency` — 延迟优先, 选择历史 P95 最低的 backend
  - `cost` — 成本优先, 选择最便宜的 backend
  - `weight` — 权重分配, 按配置比例分发
  - `round-robin` — 轮询
  - `least-busy` — 最少忙碌 (in-flight)
- **修改**: `internal/config/config.go` — 增加 `CloudRoutingConfig`
- **修改**: `internal/router/engine.go` — resolveCloudByTier 升级为支持策略
- **修改**: `internal/server/server.go` — resolveCloudProvider 使用策略引擎

### 3.2 延迟追踪
- **新建** `internal/router/latency_tracker.go` — 滑动窗口记录各 backend 延迟
- **修改**: `internal/server/server.go` — 记录每次请求延迟到 tracker

### 3.3 PII 脱敏 (轻量级)
- **新建** `internal/middleware/pii.go` — 正则表达式 PII 检测
- 覆盖: 邮箱/电话/信用卡/SSN/IP 地址
- 可配置: 开关 + 正则列表 + 替换策略 (mask/deny/log)
- **修改**: `internal/config/config.go` — 增加 `PIIConfig`
- **修改**: `internal/server/server.go` — 注入 PII 中间件

---

## Phase 4: 功能广度扩展 (P3) — 预计 4 天

目标: 扩展 API 端点和 Provider 覆盖, 提升竞争力。

### 4.1 多模态端点
- **新建** `internal/adapter/multimodal.go` — 图片/音频请求/响应结构体
- **修改**: `internal/adapter/provider.go` — Provider 接口增加 `ImageGeneration`, `AudioTranscription`, `AudioSpeech`
- **修改**: `internal/server/server.go` — 增加 `/v1/images/generations`, `/v1/audio/transcriptions`, `/v1/audio/speech`, `/v1/moderations` 端点
- openai-compatible adapter 实现: 透传到 OpenAI/兼容 API

### 4.2 更多 Provider 原生适配
- **新建** `internal/adapter/anthropic.go` — Anthropic 原生 Messages API 适配器
- **新建** `internal/adapter/vertex.go` — Google Vertex AI 适配器 (via openai-compatible)
- **新建** `internal/adapter/bedrock.go` — AWS Bedrock 适配器 (via openai-compatible)
- 预配置: 内置主流 Provider 的 base_url / auth_header / 模型列表

### 4.3 /v1/completions (Legacy)
- **修改**: `internal/server/server.go` — 增加 `/v1/completions` 端点
- 转换为 chat 格式后路由

### 4.4 Batch API
- **修改**: `internal/server/server.go` — 增加 `/v1/batches` 端点
- 异步处理: 接收 batch → 写入本地文件 → 后台逐个处理 → 返回结果
- 状态查询: `/v1/batches/{id}`

### 4.5 Stream Options (usage)
- **修改**: `internal/adapter/provider.go` — `ChatRequest` 增加 `StreamOptions`
- **修改**: `internal/server/server.go` — 流式结束时发送 usage chunk

### 4.6 可观测性增强
- **修改**: `internal/observability/metrics.go` — 增加 Key 维度指标, 成本指标, 缓存指标
- **新建** `internal/observability/callback.go` — 回调钩子框架 (类似 LiteLLM callback 但更轻量)
- 支持自定义 Webhook 回调 (请求完成时 POST 到指定 URL)

---

## 配置结构变更汇总

```yaml
# Phase 1 新增
auth:
  master_key: "fg-master-xxx"
  api_keys:
    - key: "fg-xxx"
      name: "user1"
      allowed_backends: ["fusion-mlx", "openai"]
      rpm: 60
      tpm: 100000
      expires_at: "2027-01-01T00:00:00Z"
      budget_limit: 100.0
      metadata:
        team: "engineering"

routing:
  rate_limit:
    enabled: true
    global_rpm: 1000
    global_tpm: 10000000
    key_enforcement: true

# Phase 2 新增
cache:
  enabled: true
  max_entries: 10000
  ttl: 300s
  max_memory_mb: 256

cost:
  enabled: true
  pricing_file: ""
  budget_alert_threshold: 0.8

routing:
  retry:
    max_retries: 2
    initial_backoff: 1s
    max_backoff: 30s
    retryable_status_codes: [429, 500, 502, 503]
  fallback:
    context_window_fallback:
      "gpt-4": "gpt-4-32k"
      "claude-3": "claude-3-200k"

# Phase 3 新增
routing:
  cloud_strategy: "latency"
  cloud_weights:
    openai: 60
    anthropic: 40
pii:
  enabled: true
  action: "mask"
  patterns:
    - name: "email"
      regex: "[a-zA-Z0-9._%+-]+@[a-zA-Z0-9.-]+\\.[a-zA-Z]{2,}"
    - name: "phone_cn"
      regex: "1[3-9]\\d{9}"
```

---

## 文件变更清单

### 新建文件 (~15)
| 文件 | Phase | 说明 |
|------|:--:|------|
| `internal/middleware/ratelimit.go` | 1 | 限流中间件 |
| `internal/middleware/ratelimit_test.go` | 1 | 限流测试 |
| `internal/middleware/pii.go` | 3 | PII 脱敏中间件 |
| `internal/middleware/pii_test.go` | 3 | PII 测试 |
| `internal/middleware/retry.go` | 2 | 重试逻辑 |
| `internal/middleware/retry_test.go` | 2 | 重试测试 |
| `internal/cache/cache.go` | 2 | LRU 缓存 |
| `internal/cache/cache_test.go` | 2 | 缓存测试 |
| `internal/cost/tracker.go` | 2 | 成本追踪器 |
| `internal/cost/pricing.go` | 2 | 模型定价数据 |
| `internal/cost/tracker_test.go` | 2 | 成本测试 |
| `internal/router/cloud_strategy.go` | 3 | 云路由策略 |
| `internal/router/latency_tracker.go` | 3 | 延迟追踪 |
| `internal/adapter/anthropic.go` | 4 | Anthropic 适配器 |
| `internal/adapter/multimodal.go` | 4 | 多模态结构体 |

### 修改文件 (~8)
| 文件 | Phase | 变更 |
|------|:--:|------|
| `internal/config/config.go` | 1-3 | 增加 RateLimit/Cache/Cost/PII/Retry/CloudStrategy 配置 |
| `internal/middleware/auth.go` | 1 | Key 白名单/过期/MasterKey/context 传递 |
| `internal/server/server.go` | 1-4 | 新端点 + 中间件注入 + 缓存/成本/重试集成 |
| `internal/router/engine.go` | 2-3 | Context fallback + 云策略集成 |
| `internal/observability/metrics.go` | 2-4 | 新指标 (缓存/成本/Key维度) |
| `internal/adapter/provider.go` | 4 | 多模态接口 + StreamOptions |
| `internal/adapter/openai_compatible.go` | 4 | 多模态方法实现 |
| `go.mod` | 1-4 | 可能增加依赖 |

---

## 不做的事 (保持架构优势)

1. **不引入 Redis/PostgreSQL** — 保持单二进制零依赖
2. **不做语义缓存** — 需要向量数据库, 与零依赖冲突
3. **不做完整 RBAC/SSO** — 过度工程, 留给 P5
4. **不做 Admin UI** — 已有独立规划, 不在此版本
5. **不做 K8s/Helm** — 单机设计是核心优势
6. **不做 30+ Guardrail 集成** — 仅做轻量 PII, 完整框架留给 P5

---

## 验收标准

- [ ] 所有现有 73 个测试通过
- [ ] 新增测试覆盖率 > 80%
- [ ] golangci-lint 全绿
- [ ] `config.yaml.example` 更新包含所有新配置
- [ ] README.md 更新新功能说明
- [ ] 版本升级到 v0.2.0
