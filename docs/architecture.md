---
doc_id: fusion-gateway/architecture
product: fusion-gateway
class: core
entry: service
doc_type: architecture
title: { zh: "fusion-gateway 架构", en: "fusion-gateway architecture" }
audience: engineer
status: draft
version: 0.9.20
updated: 2026-11-07
owner: "@dahai"
source_of_truth:
  - README.md
  - internal/server/server.go
  - internal/router/
---

# fusion-gateway 架构（Architecture）

## 请求管线

```text
Clients (VSCode / UI / CLI / Agent)
        │  :11432
        ▼
Ingress Layer        鉴权、解析、标准化、限流
        ▼
Preprocessing        tokenizer 计数、prompt 校验、参数默认值
        ▼
Routing Engine       规则链 P0–P7 + 硬件负载感知（核心差异点）
        ▼
Adapter Pool         全后端统一接口（本地/私有化/云端）
        ▼
MCP Gateway          MCP 集群工具注册、路由、token 预算
        ▼
Stream Forwarding    SSE、取消、重试、KV cache 释放
        ▼
Observability        日志、metrics、热更新
```

## internal/ 包布局（README Project Structure）

```text
config/      加载、版本化快照、热更新、审计
hardware/    gopsutil + IOKit + MLX metrics
tokenizer/   计数与校验
router/      决策引擎 + 逐后端熔断 + 云端策略
adapter/     供应商接口 + fusion-mlx + openai 兼容适配器 + 池
cluster/     节点发现与负载均衡
middleware/  auth / rate limit / PII / retry / logging
cache/       LRU TTL
cost/        价格表 + 自定义定价热更新
store/       memory|redis 后端
batch/ admin/(go:embed React SPA) observability/ server/
```

另有 browser、catalog、connector、crypto、identity、mcp、modelpolicy、persist(pg)、realtime、shell、tryquota、usagealerts 等功能包（README 结构清单之外，见 internal/ 目录）。

## 关键决策

- **Go 实现**：避免与 fusion-mlx 争夺 UMA 内存——推理内存归 MLX，网关只做转发与决策。
- **规则链 + 负载感知路由**：P0–P7 规则链决定本地/云端；`X-Fusion-Route` 携带决策结果，客户端不得伪造。
- **逐后端熔断**：failure_threshold 5，状态暴露在 /readyz；本地失败快速返回（非幂等不重试），云端重试 2 次。
- **配置版本化快照**：热更新产生 config_version，审计可回溯；持久化开关等结构性变更需重启。

## 依赖关系

- 上游后端：fusion-mlx（:11434，主力本地）、llama.cpp、vLLM（私有化）、20+ 云供应商；可选 PostgreSQL（persistence）、Redis（store/cache）。
- 平级集成：fusion-model-hub（webhook 入站 + `/api/v1/` 反代）、fusion-identity（控制面，默认关）。
- 下游消费：fusion-agent-studio、fusion-coder、fusion-web-shell、fusion-cli/bench/trainer、CLI/Agent 客户端。
- 边界：网关不做模型推理、不做工具语义校验；路由/鉴权/限额/观测是其全部职责。

## 一致性

调度归属与浏览器会话调度设计见 `docs/adr-001-agent-scheduling.md`、`docs/superpowers/specs/2026-08-29-browser-scheduling-design.md`。
