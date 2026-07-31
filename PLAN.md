# Fusion-Gateway v0.3.0 / v0.4.0 / v0.5.0 实施计划

## 总览

三期版本，逐步补齐与 LiteLLM 的所有差距：

| 版本 | 核心目标 | 新增特性数 |
|------|---------|-----------|
| v0.3.0 | GUI + 核心补齐 | Admin Dashboard + StreamOptions + Budget阻断 + 自定义定价 + /v1/images + 请求日志管道 |
| v0.4.0 | 多模态 + Anthropic | /v1/audio/* + /v1/messages + Anthropic适配器 + OpenTelemetry + 更多原生适配器 |
| v0.5.0 | 企业级 | OIDC/SSO + Team/RBAC + 语义缓存 + Prompt Injection + K8s + Cost Markup |

---

## v0.3.0 — GUI + 核心补齐

### 3.1 Store 接口 + 内存实现（internal/store/）

**新建**：
- `internal/store/store.go` — Store 接口定义（Keys/Channels/Logs/Analytics/Dashboard）
- `internal/store/memory/keys.go` — Key CRUD（内存 map，增删改同步写回 YAML config）
- `internal/store/memory/channels.go` — Channel CRUD
- `internal/store/memory/logs.go` — RequestLog 环形缓冲区（10000条），支持查询/筛选/导出
- `internal/store/memory/analytics.go` — 统计聚合（Token/Cost/Model/Latency/Error）
- `internal/store/memory/dashboard.go` — Dashboard 数据聚合
- `internal/store/memory/quota.go` — 额度管理（原子操作，budget 检查/扣减）

**数据模型**：
- `RequestLog`：request_id, key_name, model, channel, route_reason, input_tokens, output_tokens, cost, latency, status_code, timestamp
- `APIKeyEntry`：name, key_prefix, status, quota_type, quota_limit, quota_used, allowed_models, rpm, tpm, expires_at
- `ChannelEntry`：name, type, provider, base_url, status, priority, weight, models

### 3.2 Admin API 后端（internal/admin/）

**新建**：
- `internal/admin/router.go` — Admin 路由注册 + SPA fallback
- `internal/admin/middleware/auth.go` — JWT session + Admin token 认证
- `internal/admin/handler/auth.go` — login/logout/session
- `internal/admin/handler/dashboard.go` — overview/stats/hardware
- `internal/admin/handler/keys.go` — Key CRUD + usage
- `internal/admin/handler/channels.go` — Channel CRUD + test
- `internal/admin/handler/logs.go` — list/detail/export
- `internal/admin/handler/analytics.go` — tokens/cost/models/latency/errors
- `internal/admin/handler/routing.go` — rules/circuit-breakers
- `internal/admin/handler/models.go` — pricing
- `internal/admin/handler/settings.go` — config

**认证方案**：
- Admin token 配置 `admin.token`（首次启动自动生成，打印到日志）
- JWT session（HS256，24h 过期），Cookie: `fg_admin_session`
- Admin 中间件校验 JWT 或 Admin Token Bearer header

**Admin API 端点**（/admin/api/）：
- Auth: POST /auth/login, /auth/logout, GET /auth/session
- Dashboard: GET /dashboard/overview, /dashboard/stats, /dashboard/hardware
- Keys: GET/POST /keys, PUT/DELETE /keys/:name, GET /keys/:name/usage
- Channels: GET/POST /channels, PUT/DELETE /channels/:name, POST /channels/:name/test
- Logs: GET /logs, /logs/:id, /logs/export
- Analytics: GET /analytics/tokens, /analytics/cost, /analytics/models, /analytics/latency, /analytics/errors
- Routing: GET /routing/rules, PUT /routing/rules, GET /routing/circuit-breakers, POST /routing/circuit-breakers/:id/reset
- Models: GET /models/pricing, PUT /models/pricing/:model
- Settings: GET/PUT /settings

### 3.3 请求日志管道

**改动**：`internal/server/server.go`

- 所有 handler 请求完成后追加 RequestLog 到 store
- 新增 `appendRequestLog(...)` 辅助方法
- Stream 模式：遍历 SSE chunk 累计 output_tokens，stream 结束时写入日志
- 非 Stream 模式：直接从 resp.Usage 获取 output_tokens
- 日志内容包含：key_name, model, backend, route_reason, input/output tokens, cost, latency, status

### 3.4 Budget 阻断修复

**改动**：`internal/middleware/auth.go`

- APIKeyAuth 中间件增加 budget 检查：
  - 获取 key config 的 BudgetLimit
  - 如果 `BudgetLimit > 0`，查询 costTracker 该 key 的累计费用
  - 超限返回 429 `budget_exceeded`
- 需要 auth 中间件能访问 costTracker（通过 Server 传入闭包）
- 额度扣减在 handler 完成后由 store.Quota 执行

### 3.5 自定义定价文件

**改动**：`internal/cost/pricing.go`, `cmd/gateway/main.go`

- pricing.go 新增 `LoadFromFile(path string)` — 从 YAML/JSON 文件加载自定义价格
- 合并策略：自定义价格覆盖内置价格
- config.go 的 `CostConfig.PricingFile` 已有字段，启用它
- main.go 启动时如果 PricingFile != "" 则调用 LoadFromFile
- 热重载时也重新加载定价文件

### 3.6 /v1/images/generations 端点

**新增**：
- `internal/adapter/images.go` — ImagesRequest/ImagesResponse 类型
- Provider 接口扩展：新增可选方法 Images()
- OpenAICompatibleProvider 实现：POST /v1/images/generations
- FusionMLXProvider 返回 ErrNotSupported
- server.go 新增 handleImages handler
- 路由：仅走 cloud provider

### 3.7 StreamOptions 支持

**改动**：`internal/adapter/provider.go`, `internal/server/server.go`

- ChatRequest 新增 StreamOptions 字段
- StreamOptionsRequest：IncludeUsage bool
- handleStreamChat 中：如果 IncludeUsage，在遍历 chunk 时累计 output_tokens
- 在 stream 结束后发送包含 usage 的最终 chunk

### 3.8 Admin Dashboard 前端（web/）

**技术栈**：React 18 + TypeScript + Ant Design 5 + Vite 5 + @ant-design/charts

**新建**：
- `web/` — 完整前端项目
- `web/package.json`, `web/vite.config.ts`, `web/tsconfig.json`, `web/index.html`
- `web/src/main.tsx` — 入口
- `web/src/App.tsx` — React Router 路由
- `web/src/layouts/AdminLayout.tsx` — 侧边栏+顶栏+内容区
- `web/src/pages/` — 9个页面模块（Dashboard/Keys/Channels/Logs/Analytics/Routing/Models/Settings/Login）
- `web/src/components/` — StatCard/TrendChart/StatusBadge/JsonViewer
- `web/src/hooks/` — useFetch/useSSE
- `web/src/services/api.ts` — API 调用封装
- `web/src/stores/auth.ts` — Zustand 认证状态
- `web/src/utils/` — format/pricing 工具

**嵌入方式**：
- `internal/web/embed.go` — go:embed dist/
- server.go 注册 /admin/ 路由，SPA fallback 到 index.html
- 开发模式：Vite dev server 代理 :5173 → :8100

### 3.9 配置扩展

**改动**：`internal/config/config.go`

新增配置结构：
- `AdminConfig`：token, session_secret, session_ttl
- `LogsConfig`：enabled, max_entries, retention_days, log_prompt（是否记录 prompt 内容）
- Config 顶层新增 admin/logs 字段

---

## v0.4.0 — 多模态 + Anthropic + OTel

### 4.1 Anthropic 原生适配器（internal/adapter/anthropic.go）

- AnthropicProvider — 原生 Messages API
- 支持：thinking/extended_thinking, tool_use, tool_result, image blocks
- 请求：messages + system + max_tokens + tools + thinking
- SSE 流：message_start, content_block_start, content_block_delta, message_delta, message_stop
- 内部转换：Anthropic 格式 ↔ OpenAI 格式

### 4.2 /v1/messages 端点

- server.go 新增 handleAnthropicMessages handler
- 接收 Anthropic 格式请求 → 路由 → 调用 AnthropicProvider → 返回 Anthropic 格式响应
- 如果路由到非 Anthropic provider，自动转换格式

### 4.3 /v1/audio/transcriptions

- TranscriptionRequest/Response 类型
- Provider 接口新增 Transcription()
- OpenAICompatibleProvider 实现（multipart form）
- handleTranscriptions handler

### 4.4 /v1/audio/speech（TTS）

- SpeechRequest 类型，返回 audio stream
- Provider 接口新增 Speech()
- OpenAICompatibleProvider 实现
- handleSpeech handler — 流式返回音频

### 4.5 /v1/moderations

- ModerationRequest/Response 类型
- Provider 接口新增 Moderation()
- handleModeration handler

### 4.6 原生云适配器

- `internal/adapter/volcengine.go` — 火山引擎（Doubao，签名认证）
- `internal/adapter/qianfan.go` — 百度千帆（ERNIE，access_token 认证）
- `internal/adapter/deepseek.go` — DeepSeek（openai-compatible + 特殊参数）
- `internal/adapter/openrouter.go` — OpenRouter（openai-compatible + provider routing）
- pool.go 新增类型识别

### 4.7 OpenTelemetry

- `internal/observability/otel.go` — OTel Tracer Provider
- Span：每个请求标注 model/backend/latency/tokens
- 配置：observability.otel.enabled, endpoint, service_name

---

## v0.5.0 — 企业级

### 5.1 OIDC/SSO

- `internal/middleware/oidc.go` — OIDC discovery + token 验证
- 支持 Keycloak/Auth0/Okta/Azure AD
- Admin 登录支持 OIDC 回调

### 5.2 Team/Org/RBAC

- `internal/store/memory/teams.go` — Team/Org CRUD
- RBAC：admin/viewer/editor 角色
- Admin API 扩展：/teams, /orgs
- Key 关联 Team，费用按 Team 聚合

### 5.3 语义缓存

- `internal/cache/semantic.go` — SemanticCache 接口
- 本地向量存储或 Qdrant gRPC
- 相似度阈值可配

### 5.4 Prompt Injection 检测

- `internal/middleware/prompt_injection.go` — 正则 + 可选 Lakera API
- 动作：deny/log/tag

### 5.5 K8s/Helm 部署

- `deploy/kubernetes/` — Deployment + Service + ConfigMap + Ingress
- `deploy/helm/` — Chart + values + templates
- `deploy/terraform/` — 基础模块

### 5.6 Cost Markup

- Key 级/全局 markup 百分比
- 费用计算：base_cost * (1 + markup/100)
- Admin 展示 base_cost vs billed_cost

### 5.7 /v1/batches

- BatchCreate/BatchGet/BatchCancel 接口
- 异步处理 + 状态轮询

---

## v0.3.0 实施顺序

```
Phase 1: Store 接口 + 内存实现 + 请求日志管道
  → internal/store/store.go
  → internal/store/memory/*.go
  → server.go 追加请求日志调用
  → 测试

Phase 2: Admin API 后端
  → internal/admin/middleware/auth.go + JWT
  → internal/admin/handler/*.go (所有 CRUD)
  → internal/admin/router.go
  → server.go 注册 /admin/api/* 路由
  → 测试

Phase 3: 核心功能补齐
  → StreamOptions
  → Budget 阻断
  → 自定义定价文件
  → /v1/images/generations
  → 测试

Phase 4: 前端
  → web/ 项目脚手架
  → 所有 9 个页面模块
  → go:embed 集成
  → 端到端测试

Phase 5: 收尾
  → 集成测试
  → README.md 更新
  → config.example.yaml 更新
  → golangci-lint
  → git tag v0.3.0
```
