---
doc_id: fusion-gateway/get-started
product: fusion-gateway
class: core
entry: service
doc_type: get-started
title: { zh: "fusion-gateway 快速上手", en: "fusion-gateway quick start" }
audience: engineer
status: draft
version: 0.9.20
updated: 2026-11-07
owner: @dahai
source_of_truth:
  - README.md
  - start.sh
  - config.example.yaml
---

# fusion-gateway 快速上手（Get Started）

> 目标：本地构建并启动网关，完成一次经网关的健康检查与模型列表调用。

## 1. 前置条件

- Go 1.26（go.mod 声明 toolchain go1.26.10）
- 端口 `11432`（port-registry 注册）
- 可选：本机 fusion-mlx（:11434）作为本地后端；`server.auto_start` 可让网关拉起它

## 2. 构建与启动

```bash
go build -o fusion-gateway ./cmd/gateway
./fusion-gateway --config config.yaml
# 或用生命周期脚本（推荐：自动重建 + 配置预检）：
./start.sh start
```

`start.sh` 行为：源码新于二进制时自动 `go build`；启动前 `--check-config` 预检（致命错误 exit 78）；`config.yaml` 损坏时自动从 `config.yaml.lastgood` 恢复，坏文件隔离为 `config.yaml.corrupt-*`。

确认点：

```bash
curl -s localhost:11432/healthz
# ok
./start.sh status
```

- 日志：`./start.sh log [-f]`（`logs/gateway.stdout.log`）
- 停止：`./start.sh stop`（等待优雅停机，最长 +15s）

## 3. 鉴权与第一次调用

业务 key 走 `Authorization: Bearer <key>` 或 `x-api-key` 头；`auth.master_key` 仅运维面用。
无 key 访问受保护端点返回 401。

```bash
curl -s localhost:11432/v1/status -H "Authorization: Bearer <master-key>"
# uptime/config_version/version/commit/backends/circuit_breakers/stats
curl -s localhost:11432/v1/models -H "Authorization: Bearer <business-key>"
# 模型列表（fan-out 本地供应商，单源 3s 超时）
```

key 分发：`auth.api_keys` 静态配置，或 admin 控制台 `POST /admin/api/keys`（哈希存储）。

## 4. 健康分档

| 端点 | 用途 |
|---|---|
| `GET /healthz` | liveness，`ok`；排水中 503 |
| `GET /readyz` | readiness：熔断/MLX 模型加载/GPU 内存/队列深度；degraded 时给 `local_reasons` |
| `GET /health` | 全量：逐后端 healthy/model_loaded |
| `GET /v1/status` | 运行详情（需 key）：版本、熔断、命中统计 |

## 5. 下一步

- 端点全集：[api-reference](api-reference.md)
- 配置全集：[config-reference](config-reference.md)
- 架构：[architecture](architecture.md)

## 6. 常见问题（最多 3 条）

```yaml
troubleshooting:
  - q: { zh: "启动被拒，提示占位密钥", en: "Startup refuses with placeholder-secret error" }
    a: { zh: "config.Load 拒绝 fg-*/change-me/your-*-key 类占位值；在 .env 或 config.yaml 放真实 master_key/jwt_secret。", en: "config.Load rejects fg-*/change-me/your-*-key placeholders; put real master_key/jwt_secret values in .env or config.yaml." }
  - q: { zh: "/v1/models 返回空", en: "/v1/models returns empty" }
    a: { zh: "最常见原因是 fusion-mlx route_guard 403：确认 backends.fusion-mlx.api_key 已配、negotiation.route_header 为 X-Fusion-Route，且网关二进制非旧版（重建）。", en: "Most often a fusion-mlx route_guard 403: set backends.fusion-mlx.api_key, negotiation.route_header X-Fusion-Route, and rebuild a stale binary." }
  - q: { zh: "管理页返回 HTML 而非 API JSON", en: "Admin API returns HTML instead of JSON" }
    a: { zh: "二进制过期导致 SPA 兜底路由吞掉新 API（#30）；go build 重建后重启。", en: "A stale binary lets the SPA fallback swallow new API routes (#30); rebuild and restart." }
```

## 提交检查清单

- [x] 构建/启动/健康检查命令与 start.sh、main.go 实际一致
- [x] 每步有确认点
- [x] 端口 11432 在 port-registry 注册
- [x] zh/en 成对（命令除外）
- [x] 全篇 ≤ 120 行
