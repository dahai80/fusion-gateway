---
doc_id: fusion-gateway/config-reference
product: fusion-gateway
class: core
entry: service
doc_type: config-reference
title: { zh: "fusion-gateway 配置参考", en: "fusion-gateway configuration reference" }
audience: engineer
status: draft
version: 0.9.20
updated: 2026-11-07
owner: @dahai
source_of_truth:
  - config.example.yaml
  - README.md
  - start.sh
---

# fusion-gateway 配置参考（Configuration Reference）

主配置 `config.yaml`（启动参数 `--config` 指定）；完整字段与注释见 `config.example.yaml`（42K 全量参考）。密钥放 `.env`（git-ignored），viper 支持环境变量覆盖。热更新：`hot_reload.enabled: true`（fsnotify，500ms 防抖），配置版本化快照。

## 顶层键（config.example.yaml 顺序）

| 键 | 说明 |
|---|---|
| `server` | host/port（默认 :11432）、fusion-mlx auto_start、graceful_shutdown_timeout 30s、enable_pprof、persistence（PostgreSQL，需 `FUSION_GW_PG_DSN`） |
| `encryption` | at-rest AES-256 主密钥（`FG_ENCRYPTION_MASTER_KEY`） |
| `auth` | master_key、api_keys、accept_backend_keys（默认关） |
| `identity` | fusion-identity 控制面（默认关） |
| `routing` | mode local\|cloud\|hybrid、token_threshold 8000、local_priority 内存配比、熔断/限流/重试/fallback 与 overflow（cost_ceiling + cooldown） |
| `cache` | LRU/Redis 响应缓存 |
| `pii` | PII 处理 |
| `cloud_routing` | 云端策略：latency\|cost\|weight\|least-busy\|round-robin |
| `backends` | 逐供应商：base_url/api_key[_env]/timeout（本地默认 300s，云端 30s）；fusion-mlx 需 api_key + `negotiation.route_header: X-Fusion-Route` |
| `hardware` | 采集间隔 2s、mlx_metrics.url |
| `tokenizer` / `observability` / `cors` | 分词、Prometheus + OTel、跨域 |
| `hot_reload` | 热更新开关与防抖 |
| `cluster` / `realtime` / `admin` | 集群、实时、admin（jwt_secret 必填） |
| `store` | memory\|redis，data_dir 磁盘持久化 |
| `usage_alerts` / `mcp` / `browser` / `web_shell` / `service_routes` / `auth_portal` / `try_quota` | 用量告警、MCP、浏览器调度、web-shell、服务路由、认证门户、试用配额 |

## 环境变量覆盖

| 变量 | 说明 |
|---|---|
| `FUSION_GATEWAY_PORT` | 端口覆盖 |
| `FUSION_MLX_URL` | fusion-mlx 地址覆盖 |
| `FG_MASTER_KEY` | master key |
| `FG_ENCRYPTION_MASTER_KEY` | at-rest 加密主密钥 |
| `FG_ADMIN_JWT_SECRET` | admin JWT 密钥 |
| `FUSION_GW_PG_DSN` | `persistence.enabled: true` 时必填，缺失拒绝启动 |

## 约束与注意事项

- config.Load 启动即拒绝占位密钥（`fg-*`、`*-change-me`、`your-*-key`）。
- `persistence.enabled` 翻转经热更新无效，必须重启。
- `config.yaml` 损坏时 start.sh 自动从 `config.yaml.lastgood` 恢复，坏文件隔离为 `config.yaml.corrupt-<timestamp>`；`--check-config` 预检致命错误 exit 78。
- 容器部署用 `config.container.yaml`（auth 关、0.0.0.0），生产切勿原样上公网。
