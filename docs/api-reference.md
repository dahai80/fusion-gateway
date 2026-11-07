---
doc_id: fusion-gateway/api-reference
product: fusion-gateway
class: core
entry: service
doc_type: api-reference
title: { zh: "fusion-gateway API 参考", en: "fusion-gateway API reference" }
audience: engineer
status: draft
version: 0.9.20
updated: 2026-11-07
owner: "@dahai"
source_of_truth:
  - README.md
  - internal/server/server.go
  - internal/middleware/auth.go
---

# fusion-gateway API 参考（API Reference）

Base URL `http://127.0.0.1:11432`。鉴权：`Authorization: Bearer <key>` 或 `x-api-key` 头；`/healthz` `/readyz` `/livez` `/health` 公开。限流 429 带 `Retry-After` 与 `X-RateLimit-Remaining`。

## 推理端点

| 端点 | 说明 |
|---|---|
| `POST /v1/chat/completions` | OpenAI 兼容对话（SSE 流式支持） |
| `POST /v1/completions` | OpenAI 兼容补全 |
| `POST /v1/embeddings` / `POST /v1/rerank` | 向量与重排 |
| `POST /v1/messages` | Anthropic 原生格式（自动转换） |
| `POST /v1/solve-stream` | K-12 BNUP SSE |
| `WS /v1/realtime` | 实时语音/流会话 |
| `POST /v1/images/generations` / `audio/transcriptions` / `audio/speech` / `moderations` | 多模态与审核 |
| `/v1/batches*` | 批处理 |

## 模型与运维

| 端点 | 鉴权 | 说明 |
|---|---|---|
| `GET /v1/models` | key | fan-out 本地供应商（单源 3s 超时） |
| `POST /v1/models/{id}/load` / `unload` | key | 经 model-hub serve 装卸 |
| `POST /v1/admin/models/{name}/load` | admin | 直连 MLX 加载 |
| `GET /v1/hardware` / `GET /stats` / `GET /v1/cost` | key | 硬件/MLX 统计/成本 |
| `GET /v1/recommend` / `v1/analyze[-batch]` / `v1/migration-level` / `v1/convert` / `v1/quantize*` / `v1/benchmarks` | key | 模型工具面 |
| `GET /metrics` | **master_key** | Prometheus |
| `POST /admin/gc` / `POST /admin/config/reload` | admin JWT | 运维动作 |
| `GET /debug/pprof/*` | master_key + `server.enable_pprof` | 性能剖析 |

## 管理面

| 端点组 | 说明 |
|---|---|
| `/admin/api/login` / `logout` / `sso` | admin JWT 会话 |
| `/admin/api/config/backends[/{name}]` | 后端供应商 CRUD（热生效） |
| `/admin/teams*` / `orgs*` / `api/try-quota/*` / `api/reports/governance` | 团队/组织/配额/治理报表 |
| `/admin/api/fine-tune/*` | MLX 微调代理 |
| `/api/v1/keys`（+ `/ext-runner`、rotate） | key CRUD（master 门禁；用户 key 为 `fk-` 前缀，带 module×action scope） |
| `/gateway/v1/connector/*`、`connection*`、`oauth2/authorize|callback` | 连接器与 OAuth2 |
| `/mcp/v1/tools` / `call` / `stats` / `health` | MCP 工具面 |
| `/api/v1/shell/*`、`ANY /shell/{module_id}/` | web-shell 会话/manifest/审计与模块代理（`web_shell.enabled`） |
| `ANY /api/v1/` | model-hub 反向代理（模块权限校验） |
| `POST /webhooks/model-hub` | HMAC-SHA256（`X-Webhook-Signature`），非 API-key 鉴权 |

## 约定

- 路由协商头 `X-Fusion-Route` 为网关内部决策头，客户端不得发送（见 README #29）。
- 云端重试 max_retries 2；本地生成失败快速返回，不重试。
- 溢出上云触发日成本上限时返回显式 503，detail 指明 ceiling。
- 错误形状遵循 OpenAI 兼容惯例；详细字段以 `/v1/` 实现与 README 端点表为准。
