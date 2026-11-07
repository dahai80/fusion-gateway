---
doc_id: fusion-gateway/product-page
product: fusion-gateway
class: core
entry: service
doc_type: product-page
title: { zh: "fusion-gateway 产品页", en: "fusion-gateway product page" }
audience: engineer
status: draft
version: 0.9.20
updated: 2026-11-07
owner: @dahai
source_of_truth:
  - README.md
  - internal/server/server.go
  - config.example.yaml
---

# fusion-gateway

```yaml
slug: gateway
name: fusion-gateway
tagline:
  zh: 异构推理统一网关：本地 MLX 与 20+ 云端供应商一个入口，规则路由按硬件负载选路
  en: Unified hybrid inference gateway: local MLX and 20+ cloud providers behind one entry, routed by rules and hardware load
cta:
  zh: 一个地址、一把 key，本地优先、云端兜底
  en: One address, one key; local first, cloud as fallback
repo: https://github.com/dahai80/fusion-gateway
```

## 定位

```yaml
product:
  type: core-service
  serves: fusion-agent-studio、fusion-coder、fusion-web-shell、CLI/Agent 客户端（核心流量入口）
  builds_on:
    - fusion-mlx（:11434，本地主力后端，可 auto_start 拉起）
    - 云端供应商（OpenAI/Claude/DeepSeek/火山/千帆/国产系等 20+，adapters 统一接口）
    - 可选：PostgreSQL（persistence）、Redis（store/cache 多实例）、fusion-identity（控制面）
  hosts: 鉴权与限流、路由决策（规则链 + 硬件负载感知）、适配器池、MCP 网关、admin 控制台、用量与成本统计
  integration_surface: HTTP（:11432，OpenAI/Anthropic 兼容 + admin/keys/MCP/shell 面）+ Prometheus /metrics
  deployment: 本机或内网单实例（Go 二进制，不与 MLX 抢 UMA 内存）；容器镜像含 config.container.yaml
```

## capabilities

```yaml
capabilities:
  - icon: 🧭
    name: { zh: 混合路由引擎, en: Hybrid routing engine }
    tag:
      zh: 本地/云端按规则链与硬件负载选路，应用方不写 fallback 逻辑
      en: Rules plus hardware load pick local or cloud; apps write no fallback logic
    points:
      - zh: routing.mode local|cloud|hybrid；token_threshold 决定溢出上云，failover 带 cooldown 与每日 cost_ceiling
        en: routing.mode local|cloud|hybrid; token_threshold overflows to cloud, failover with cooldown and daily cost_ceiling
      - zh: 约束：重试只对云端（max_retries 2），本地生成非幂等、快速失败
        en: Constraint: retries are cloud-only (max_retries 2); local generation is non-idempotent and fails fast
    viz: []
  - icon: 🔑
    name: { zh: Key 与配额治理, en: Key and quota governance }
    tag:
      zh: 分发、限额、模型白名单一把 key 管完
      en: Issue, throttle, and scope keys from one place
    points:
      - zh: Bearer 或 x-api-key；per-key rpm/tpm/allowed_models/allowed_backends/expires_at/budget_limit
        en: Bearer or x-api-key; per-key rpm/tpm/allowed_models/allowed_backends/expires_at/budget_limit
      - zh: 约束：master_key 绕过限流并独占 /metrics 等运维面，不可用于业务调用
        en: Constraint: master_key bypasses rate limits and gates ops endpoints; never use it for business calls
    viz: []
  - icon: 🩺
    name: { zh: 分级健康检查, en: Tiered health checks }
    tag:
      zh: liveness/readiness/全量诊断分开，编排器各取所需
      en: Liveness, readiness, and full diagnostics split for orchestrators
    points:
      - zh: /healthz /readyz /health /v1/status 四档；readyz 检查熔断、MLX 模型加载、GPU 内存、队列深度
        en: Four tiers (/healthz /readyz /health /v1/status); readyz covers breaker, MLX model_loaded, GPU memory, queue depth
      - zh: 约束：本地无已加载模型时 readyz 给 degraded/503 与 local_reasons，而非静默 200
        en: Constraint: with no local model loaded, readyz reports degraded/503 with local_reasons instead of a silent 200
    viz: []
  - icon: 🔌
    name: { zh: MCP 与工具面, en: MCP and tool surface }
    tag:
      zh: MCP 集群工具注册与调用经网关统一记账
      en: MCP cluster tool registry and calls booked through the gateway
    points:
      - zh: /mcp/v1/tools|call|stats|health 统一入口，token 预算网关管
        en: Single entry /mcp/v1/tools|call|stats|health with gateway-managed token budget
      - zh: 约束：MCP 工具集由注册内容决定，网关不做工具语义校验
        en: Constraint: the tool set is whatever is registered; the gateway does not validate tool semantics
    viz: []
  - icon: 🖥
    name: { zh: Admin 控制台, en: Admin console }
    tag:
      zh: 内嵌 React SPA 管后端/key/团队/报表，不拼 curl
      en: Embedded React SPA for backends, keys, teams, and reports
    points:
      - zh: /admin/ SPA + /admin/api/*（JWT 登录），config/backends CRUD 热生效
        en: /admin/ SPA plus /admin/api/* (JWT login); backend CRUD hot-applies
      - zh: 约束：admin JWT secret 必须显式配置，占位密钥启动即拒绝
        en: Constraint: the admin JWT secret must be set explicitly; placeholder secrets refuse to boot
    viz: []
  - icon: 🔁
    name: { zh: 热更新配置, en: Hot config reload }
    tag:
      zh: 改 yaml 不重启，fsnotify 监听 500ms 防抖
      en: Edit yaml without restart; fsnotify watcher with 500ms debounce
    points:
      - zh: hot_reload.enabled + /admin/config/reload；配置版本化快照（config_version）
        en: hot_reload.enabled plus /admin/config/reload; versioned snapshots (config_version)
      - zh: 约束：persistence.enabled 翻转需重启才生效
        en: Constraint: flipping persistence.enabled takes effect only after restart
    viz: []
```

## workflow

```yaml
workflow:
  - name: { zh: 构建/启动, en: Build and start }
    action: { zh: go build 或 ./start.sh start（源码新于二进制自动重建）, en: go build or ./start.sh start (auto-rebuild when source is newer) }
    input: { zh: config.yaml（.env 放密钥）, en: config.yaml (secrets in .env) }
    output: { zh: :11432 /healthz ok, en: /healthz returns ok on :11432 }
  - name: { zh: 配置后端与 key, en: Configure backends and keys }
    action: { zh: backends 填 fusion-mlx/云端供应商，发业务 key, en: Fill backends for fusion-mlx/cloud, issue business keys }
    input: { zh: 供应商 base_url/api_key、key 配额, en: Provider base_url/api_key, key quotas }
    output: { zh: /v1/models 列出可用模型, en: /v1/models lists available models }
  - name: { zh: 业务接入, en: Onboard clients }
    action: { zh: OpenAI/Anthropic 兼容端点接入 agent-studio/coder 等, en: Point agent-studio/coder at the OpenAI/Anthropic-compatible endpoints }
    input: { zh: 业务 key + 模型名, en: Business key and model name }
    output: { zh: 本地优先路由 + 用量统计, en: Local-first routing with usage stats }
  - name: { zh: 运维观测, en: Operate and observe }
    action: { zh: /metrics、admin 报表、/health /readyz 巡检, en: /metrics, admin reports, /health /readyz checks }
    input: { zh: Prometheus/admin JWT, en: Prometheus or admin JWT }
    output: { zh: 成本/熔断/队列可视化, en: Cost, breaker, and queue visibility }
```

## useCases

```yaml
useCases:
  - name: { zh: 本地优先推理, en: Local-first inference }
    headline: { zh: 长上下文自动上云、短请求留在 MLX, en: Long contexts overflow to cloud while short requests stay on MLX }
    body:
      zh: routing.token_threshold 触发溢出，超日成本上限显式 503。差别：应用不写任何本地/云端切换代码，成本有硬顶。
      en: routing.token_threshold triggers overflow; the daily ceiling fails closed with an explicit 503. Difference: zero local/cloud switching code in apps, with a hard cost cap.
  - name: { zh: 多客户端统一供给, en: One supplier for many clients }
    headline: { zh: agent-studio/coder/web-shell 共用一套 key 与模型白名单, en: agent-studio, coder, and web-shell share one key and model-allowlist scheme }
    body:
      zh: fk- 用户 key 带 module×action scope，X-Fusion-Principal 透传下游。差别：权限语义一份实现，审计在网关。
      en: fk- user keys carry module×action scopes; X-Fusion-Principal propagates downstream. Difference: one permission semantic, audited at the gateway.
```

## techDepth

```yaml
techDepth:
  - category: { zh: 可靠性纪律, en: Reliability disciplines }
    items:
      - name: { zh: 熔断器, en: Circuit breaker }
        desc: { zh: 每后端独立熔断，failure_threshold 5，readyz 反映状态, en: Per-backend breaker, failure_threshold 5, reflected in readyz }
        steps:
          - zh: 见 internal/router/, en: See internal/router/
      - name: { zh: 配置自愈, en: Config self-healing }
        desc: { zh: 损坏 config.yaml 自动从 .lastgood 恢复，坏文件隔离为 corrupt-*, en: Corrupt config.yaml auto-restores from .lastgood; bad copies quarantined as corrupt-* }
        steps:
          - zh: 见 start.sh --check-config 预检, en: See the --check-config preflight in start.sh
      - name: { zh: 优雅停机, en: Graceful shutdown }
        desc: { zh: graceful_shutdown_timeout 30s；排水中 healthz/readyz 报 503, en: graceful_shutdown_timeout 30s; draining reports 503 on healthz/readyz }
        steps:
          - zh: 见 README Troubleshooting, en: See README Troubleshooting
```

## limits（已知边界）

- zh: 单实例网关（多实例需 Redis store/cache + 外部 LB）；本地生成不重试
- en: Single instance (multi-instance needs Redis store/cache plus an external LB); local generation is never retried

## 提交检查清单

- [x] 所有字段 zh/en 成对
- [x] 每条能力回答"应用方少写了什么"
- [x] 端点/配置名与 internal/server/server.go、config.example.yaml 一致
- [x] 数字有出处（熔断阈值/超时/重试次数来自 README）
- [x] 无营销形容词
