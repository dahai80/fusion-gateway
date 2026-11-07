---
doc_id: fusion-gateway/guide
product: fusion-gateway
class: core
entry: service
doc_type: guide
title: { zh: "路由模式计划", en: "Routing mode plan" }
audience: engineer
status: draft
version: 0.9.20
updated: 2026-11-07
owner: @dahai
source_of_truth:
  - plan.md
---

# Plan: Routing Mode (local/cloud/hybrid)

## 需求
- 新增路由模式标记位 `mode`: `local` / `cloud` / `hybrid`
- `local`: 所有请求强制走本地（fusion-mlx），忽略硬件/并发等保护规则
- `cloud`: 所有请求强制走云端
- `hybrid`（默认）: 保持现有智能路由逻辑（硬件感知 + token budget + ratio 分发）

## 改动清单

### 1. Config — `internal/config/config.go`
- `RoutingConfig` 新增 `Mode string` 字段，mapstructure tag `"mode"`
- `validate()` 校验 mode 值必须为 `local`/`cloud`/`hybrid`
- `DefaultConfig()` 默认 `mode: "hybrid"`

### 2. Router Engine — `internal/router/engine.go`
- `decideLocked()` 最顶部（P0之前）新增 mode 快速路径：
  - `mode == "local"` → 直接返回 `LocalBackend, reason: "mode_local"`
  - `mode == "cloud"` → 直接返回 `CloudBackend, reason: "mode_cloud"`
  - `mode == "hybrid"` → 走现有逻辑（不变）
- `decideEmbeddingLocked()` 和 `decideRerankLocked()` 同理加 mode 快速路径

### 3. Admin API — `internal/admin/handler.go`
- `routingConfigResponse` 新增 `Mode string` 字段
- `routingConfigUpdate` 新增 `Mode *string` 字段
- GET/PUT handler 读写 `cfg.Routing.Mode`

### 4. Frontend — `web/admin/src/pages/Settings.tsx`
- `RoutingConfig` interface 新增 `mode: string`
- Routing Rules Card 顶部新增 Select（local/cloud/hybrid），带说明 tooltip

### 5. Config YAML — `config.yaml`
- `routing:` 下新增 `mode: hybrid` 示例行

### 6. Tests
- `internal/router/engine_test.go` — 新增 TestDecide_ModeLocal / ModeCloud / ModeHybrid
- `internal/admin/handler_test.go` — 新增 mode 字段读写测试

### 7. README.md — 更新路由模式文档

## 不变
- circuit breaker、session affinity 等在 hybrid 模式下行为完全不变
- local 模式下跳过所有保护规则（用户显式选择承担风险）
- 不新增 API 端点，复用现有 `/admin/api/config/routing`
