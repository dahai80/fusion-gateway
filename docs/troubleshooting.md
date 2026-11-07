---
doc_id: fusion-gateway/troubleshooting
product: fusion-gateway
class: core
entry: service
doc_type: troubleshooting
title: { zh: "fusion-gateway 故障排查", en: "fusion-gateway troubleshooting" }
audience: engineer
status: draft
version: 0.9.20
updated: 2026-11-07
owner: @dahai
source_of_truth:
  - README.md
  - start.sh
  - docs/runbook-recovery.md
---

# fusion-gateway 故障排查（Troubleshooting）

先收集：`./start.sh status`（进程/二进制新旧）、`curl -s localhost:11432/readyz`（含 local_reasons）、`./start.sh log 100`。深度恢复流程见 [runbook-recovery](runbook-recovery.md)。

## 症状速查

| 症状 | 原因 | 处置 |
|---|---|---|
| 启动被拒，提示占位密钥 | config.Load 拒绝 `fg-*`/`*-change-me`/`your-*-key` 类占位值 | `.env` 或 config.yaml 放真实 master_key / admin jwt_secret |
| 启动 exit 78 | `--check-config` 预检失败（配置致命错误） | 按日志修配置；无法修复时从 `config.yaml.lastgood` 恢复（start.sh 会自动做，坏文件隔离为 `config.yaml.corrupt-*`） |
| `/v1/models` 返回空（#29） | 多为 fusion-mlx route_guard 403；或二进制过期 | 核对 `backends.fusion-mlx.api_key`、`negotiation.route_header: X-Fusion-Route`；重建二进制。客户端不得发送 X-Fusion-Route |
| 管理 API 返回 SPA HTML（#30） | 二进制过期，新路由被 SPA 兜底吞掉 | `go build -o fusion-gateway ./cmd/gateway` 重建后重启；start.sh 源码更新会自动重建 |
| `/v1/chat/completions` 429 | 滑动窗口 rpm/tpm 触发 | 按响应头 `Retry-After` 重试；调 key 的限额或换 key |
| 本地请求偶发失败不重试 | 本地生成非幂等，设计如此（#208） | 重试语义只覆盖云端（max_retries 2）；本地失败由调用方决定重发 |
| 溢出请求返回 503 且 detail 提及 ceiling | 触发 overflow 每日成本上限（UTC 日界） | 等日窗重置，或在 routing.overflow.cost_ceiling 调整预算 |
| 长流式回复被掐断 | graceful_shutdown_timeout 30s 内未完成即被停机截断 | 调大 `graceful_shutdown_timeout`（须高于最长生成时长）；stop 脚本额外等待 +15s |
| readyz 503，reason `model_not_loaded` | 本地后端无已加载模型 | MLX 侧加载模型；或确认降级路由到云端的预期性 |
| readyz 503，reason `circuit_breaker_open` | 后端连续失败 5 次熔断 | 修后端连通性后熔断自动恢复；/health 看逐后端状态 |
| 热更新改了 persistence 不生效 | 结构性变更需重启 | 重启服务；其余路由/限流/后端字段热生效 |

## 排障开关

- `server.enable_pprof: true` + master key 访问 `/debug/pprof/*` 做性能剖析，用完关闭。
- 排水中（stop 进行时）healthz/readyz 报 503 属预期（#449）。

## 仍未解决

带 `./start.sh status`、`readyz` 输出与 `logs/gateway.stdout.log` 最后 100 行提 issue（英文）。
