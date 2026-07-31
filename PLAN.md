# Cloud Backend 配置 GUI

## 问题
- 路由规则 "output/input ratio > threshold → 转云端" 已实现
- 但云端 provider 的 URL 和 API Key 只能在 config.yaml 手动改，GUI 无法管理
- 现有 Channels 页面管理的是 store 里的 ChannelEntry（没有 API Key 字段），和路由引擎用的 config.yaml backends 脱节

## 方案
在 Settings 页面新增 **Cloud Backends** 卡片，直接读写 config.yaml 的 `backends` 段，通过 hot reload 生效。

### 后端
1. `GET /admin/api/config/backends` — 返回所有 backends（API Key 脱敏，只显示后4位）
2. `PUT /admin/api/config/backends/:name` — 更新单个 backend 的 base_url / api_key / enabled / timeout
3. 写入 yaml → hot reload 自动生效

### 前端
4. Settings.tsx 新增 Cloud Backends 卡片
   - Table: Name / Type / Base URL / API Key(脱敏) / Enabled / Actions
   - Edit Modal: 编辑 base_url、api_key、enabled
   - 保存调 PUT /admin/api/config/backends/:name

### 不改
- 不改 ChannelEntry/store — 那是另一套体系
- 不改路由引擎 — resolveCloudProvider 已经正确工作
- 不改 adapter pool — hot reload 后 pool 会重建
