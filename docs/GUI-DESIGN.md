# Fusion-Gateway GUI 设计方案

> 版本: v1.0 | 日期: 2026-07-31
> 状态: Draft — 待评审

---

## 一、竞品 GUI 能力矩阵

### 1.1 竞品概览

| 竞品 | Stars | 语言 | 前端技术 | GUI 方式 | 目标客户 |
|------|-------|------|----------|----------|----------|
| **One API** | 36k | Go | React + MUI | Go embed SPA | 个人/小团队 |
| **New API** | 44k | Go | React + MUI | Go embed SPA | 小企业/团队 |
| **One Hub** | 2.9k | Go | React + MUI | Go embed SPA | 小企业 |
| **LiteLLM** | 55k | Python | React + Next.js | Python embed SPA | 企业/团队 |
| **Bifrost** | 6.9k | Go | Next.js (Vite) | Go embed SPA | 企业 |
| **Portkey** | 12.6k | TypeScript | 自带 Console | 内置 Web UI | 企业 |
| **GoModel** | 1k | Go | 无 | 无 GUI | 开发者 |
| **CoAI** | 9.3k | Python | React | Python serve | 企业 |

### 1.2 GUI 功能能力矩阵

| 能力维度 | One API | New API | LiteLLM | Bifrost | Portkey | Fusion-Gateway 现状 |
|---------|---------|---------|---------|---------|---------|-------------------|
| **仪表盘总览** | ✅ | ✅ 增强 | ✅ | ✅ | ✅ | ❌ |
| **请求日志** | ✅ 基础 | ✅ 增强 | ✅ 详细 | ✅ | ✅ 详细 | ❌ |
| **Token 用量统计** | ✅ | ✅ | ✅ | ✅ | ✅ | 半成品(仅Prometheus) |
| **费用/账单追踪** | ✅ | ✅ 精细 | ✅ | ✅ 预算 | ✅ | ❌ |
| **API Key 管理** | ✅ | ✅ | ✅ | ✅ | ✅ | ✅ YAML静态 |
| **用户/团队管理** | ✅ 基础 | ✅ 增强 | ✅ | ✅ OIDC | ✅ | ❌ |
| **渠道/后端管理** | ✅ | ✅ | ✅ Provider | ✅ | ✅ | ✅ YAML静态 |
| **路由规则配置** | ❌ | ❌ | ✅ | ✅ | ✅ Config | ✅ YAML静态 |
| **实时监控** | ✅ | ✅ | ✅ | ✅ Prometheus | ✅ | ✅ Prometheus文本 |
| **模型价格管理** | ✅ | ✅ | ✅ | ✅ | ✅ | ❌ |
| **通知告警** | ❌ | ❌ | ✅ | ✅ | ✅ | ❌ |
| **系统设置** | ✅ | ✅ | ✅ | ✅ | ✅ | ✅ YAML |
| **Playground** | ❌ | ❌ | ✅ | ❌ | ✅ | ❌ |

### 1.3 竞品核心洞察

**One API / New API 家族** — 中国市场事实标准
- 前端 React + MUI，Go embed 打包为单二进制，零依赖部署
- 核心价值：API Key 分发 + 额度管理 + 渠道负载均衡 + 用量统计
- New API 在 One API 基础上增强：更细粒度的额度控制、更完善的统计页面、Midjourney 支持
- 缺陷：无 Playground、路由规则偏简单、国际化差

**LiteLLM** — 海外最流行
- Python + React/Next.js，proxy 模式内置完整 Dashboard
- 核心价值：Virtual Key + Spend Tracking + Team Budget + Request Logs
- 强项：费用追踪极细（per-key per-model per-team）、Guardrails、Prompt Management
- 缺陷：Python 内存占用大（与本地推理竞争 UMA）、延迟高

**Bifrost** — Go 性能标杆
- Go + Next.js(Vite)，UI 目录独立，Go embed 打包
- 核心价值：极低延迟（11μs 开销）、Web UI 可视化配置、实时监控
- 强项：性能、语义缓存、MCP Gateway、OIDC 用户管理
- 缺陷：社区较新、文档偏企业导向

**Portkey** — 企业级全功能
- TypeScript Gateway + 内置 Console
- 核心价值：Guardrails（40+）、MCP Gateway、1600+ 模型
- 强项：Observability、Cost Tracking、Prompt Template Management
- 缺陷：核心 GUI 功能在 Hosted 版本、OSS 版功能受限

### 1.4 Fusion-Gateway 的差异化定位

| 维度 | One API/New API | LiteLLM | **Fusion-Gateway** |
|------|----------------|---------|-------------------|
| 语言 | Go ✅ | Python ❌ | **Go ✅（不争UMA）** |
| 本地推理 | ❌ | ❌ | **✅ fusion-mlx 联动** |
| 硬件感知路由 | ❌ | ❌ | **✅ 内存/Swap/GPU 感知** |
| 部署复杂度 | 单二进制 ✅ | 需 Python 环境 | **单二进制 ✅** |
| 费用追踪 | 基础 | 精细 | **需补齐（对标LiteLLM）** |
| 中国云厂商 | ✅ | 部分 | **✅ 火山/千帆/DeepSeek** |

**核心差异化**：唯一一个 **Go 实现 + 本地推理联动 + 硬件感知路由 + 中国云厂商全覆盖** 的 AI Gateway

---

## 二、GUI 架构方案

### 2.1 技术选型

**决策：React + Ant Design + Vite → Go:embed 单二进制**

| 选型 | 方案 | 理由 |
|------|------|------|
| 前端框架 | React 18 + TypeScript | 生态最大、One API/New API/Bifrost 同技术栈 |
| UI 组件库 | Ant Design 5 | 中后台标杆、开箱即用高级组件(Table/Form/Chart)、中文友好 |
| 图表 | @ant-design/charts (基于 G2) | 与 Ant Design 深度集成、主题统一 |
| 构建工具 | Vite 5 | 极速 HMR、Bifrost 也选用 |
| 打包方式 | Go `embed` + `http.FileServer` | 单二进制零依赖、One API 模式已验证 |
| API 通信 | RESTful JSON + fetch | 简单可靠、无需 GraphQL 复杂度 |
| 实时数据 | SSE (Server-Sent Events) | 复用已有 SSE 基础设施 |

**不选 Vue 的理由**：One API(New API) 和 Bifrost 都是 React，如果需要参考/借鉴代码，React 生态更直接。

**不选独立前端服务的理由**：用户要求单二进制部署（小企业没有前端运维能力），Go embed 是唯一选择。

### 2.2 整体架构

```
┌─────────────────────────────────────────────────────┐
│                  Fusion-Gateway (:8100)              │
├──────────────────────┬──────────────────────────────┤
│   Inference API      │      Admin API (/admin/*)    │
│   /v1/chat/*         │      /admin/api/*            │
│   /v1/embeddings/*   │         ├─ Dashboard         │
│   /v1/rerank/*       │         ├─ Keys              │
│   /v1/realtime       │         ├─ Channels          │
│                      │         ├─ Logs              │
│                      │         ├─ Analytics         │
│                      │         ├─ Routing           │
│                      │         ├─ Settings          │
│                      │         └─ System            │
├──────────────────────┴──────────────────────────────┤
│              Go:embed Static Files                   │
│         /admin/* → React SPA (index.html)            │
└─────────────────────────────────────────────────────┘
```

### 2.3 后端 Admin API 设计

所有 Admin API 在 `/admin/api/` 路径下，与 `/v1/` 推理 API 隔离。

#### 认证
- Admin API 使用独立的 Admin Token（配置文件设置，非 API Key）
- 支持 Session Cookie（登录后签发 JWT）
- 首次启动自动生成 admin token，打印到日志

#### API 端点清单

| 模块 | 方法 | 路径 | 说明 |
|------|------|------|------|
| **Auth** | POST | /admin/api/auth/login | 管理员登录 |
| | POST | /admin/api/auth/logout | 登出 |
| | GET | /admin/api/auth/session | 获取当前会话 |
| **Dashboard** | GET | /admin/api/dashboard/overview | 总览数据 |
| | GET | /admin/api/dashboard/stats | 实时统计(QPS/延迟/命中率) |
| | GET | /admin/api/dashboard/hardware | 硬件状态(内存/Swap/GPU) |
| **Keys** | GET | /admin/api/keys | 列出所有 API Key |
| | POST | /admin/api/keys | 创建 Key |
| | PUT | /admin/api/keys/:id | 更新 Key |
| | DELETE | /admin/api/keys/:id | 删除 Key |
| | GET | /admin/api/keys/:id/usage | Key 用量明细 |
| **Channels** | GET | /admin/api/channels | 列出所有渠道/后端 |
| | POST | /admin/api/channels | 创建渠道 |
| | PUT | /admin/api/channels/:id | 更新渠道 |
| | DELETE | /admin/api/channels/:id | 删除渠道 |
| | POST | /admin/api/channels/:id/test | 测试渠道连通性 |
| **Logs** | GET | /admin/api/logs | 请求日志列表(分页/筛选) |
| | GET | /admin/api/logs/:id | 请求日志详情 |
| | GET | /admin/api/logs/export | 导出日志(CSV/JSON) |
| **Analytics** | GET | /admin/api/analytics/tokens | Token 用量趋势 |
| | GET | /admin/api/analytics/cost | 费用统计 |
| | GET | /admin/api/analytics/models | 模型使用分布 |
| | GET | /admin/api/analytics/latency | 延迟分布 |
| | GET | /admin/api/analytics/errors | 错误统计 |
| **Routing** | GET | /admin/api/routing/rules | 路由规则 |
| | PUT | /admin/api/routing/rules | 更新路由规则 |
| | GET | /admin/api/routing/circuit-breakers | 熔断器状态 |
| | POST | /admin/api/routing/circuit-breakers/:id/reset | 重置熔断器 |
| **Models** | GET | /admin/api/models | 可用模型列表 |
| | GET | /admin/api/models/pricing | 模型价格表 |
| | PUT | /admin/api/models/pricing/:model | 更新模型价格 |
| **Settings** | GET | /admin/api/settings | 系统配置 |
| | PUT | /admin/api/settings | 更新配置(热重载) |
| | GET | /admin/api/settings/version | 版本信息 |
| **System** | GET | /admin/api/system/info | 系统信息(Go版本/内存/运行时间) |
| | GET | /admin/api/system/metrics | Prometheus 指标(JSON格式) |
| | POST | /admin/api/system/reload | 热重载配置 |

---

## 三、GUI 页面设计

### 3.1 页面导航结构

```
Fusion-Gateway Admin
├─ 📊 仪表盘 (Dashboard)
│   ├─ 总览面板
│   └─ 硬件监控
├─ 🔑 API Key 管理
│   ├─ Key 列表
│   └─ Key 详情/用量
├─ 🌐 渠道管理
│   ├─ 渠道列表
│   └─ 渠道测试/编辑
├─ 📝 请求日志
│   ├─ 日志列表
│   └─ 日志详情
├─ 📈 数据分析
│   ├─ Token 用量
│   ├─ 费用统计
│   ├─ 模型分布
│   ├─ 延迟分析
│   └─ 错误统计
├─ 🔀 路由管理
│   ├─ 路由规则
│   └─ 熔断器状态
├─ 💰 模型价格
│   ├─ 价格表
│   └─ 价格编辑
├─ ⚙️ 系统设置
│   ├─ 基础配置
│   ├─ 认证配置
│   └─ 系统信息
```

### 3.2 各页面详细设计

#### 📊 仪表盘 — 总览面板

**布局**: 顶部统计卡片 + 中部趋势图 + 底部实时请求流

```
┌──────────────────────────────────────────────────────────┐
│  [今日请求]    [今日Token]    [今日费用]    [本地命中率]     │
│   12,584       1.2M          ¥342.50       67.3%         │
│   ↑12.3%      ↑8.7%         ↑15.2%        ↑3.1%         │
├──────────────────────────────────────────────────────────┤
│  ┌─ 请求量趋势 (24h) ─────────┐ ┌─ Token用量趋势 ────────┐│
│  │  ▁▂▃▅▇█▇▅▃▂▁▂▃▅▇█▇▅▃   │ │  ▁▂▃▅▇█▇▅▃▂▁         ││
│  │  本地 ▓ 云端 ░             │ │  输入 ▓ 输出 ░         ││
│  └────────────────────────────┘ └────────────────────────┘│
├──────────────────────────────────────────────────────────┤
│  ┌─ 硬件状态 ─────────────────┐ ┌─ 路由分布 ────────────┐│
│  │  内存: ████████░░ 80%      │ │  Local: 67.3%        ││
│  │  Swap: ██░░░░░░░░ 20%      │ │  Cluster: 15.2%      ││
│  │  GPU:  ██████░░░░ 60%      │ │  Cloud: 17.5%        ││
│  │  并发: 5/8                  │ │  熔断: 0 active      ││
│  └────────────────────────────┘ └────────────────────────┘│
├──────────────────────────────────────────────────────────┤
│  ┌─ 实时请求流 (最近20条) ──────────────────────────────┐│
│  │ 14:23:01 POST /v1/chat/completions  gpt-4o  2.3s  ✅  ││
│  │ 14:22:58 POST /v1/chat/completions  qwen-72b 0.8s  ✅ ││
│  │ 14:22:55 POST /v1/embeddings        bge-large  0.1s ✅││
│  └──────────────────────────────────────────────────────┘│
└──────────────────────────────────────────────────────────┘
```

#### 📊 仪表盘 — 硬件监控

Apple Silicon 专项硬件面板：
- UMA 统一内存用量（已用/总量/分给 GPU 的比例）
- Swap 使用量（Swap > 0 触发熔断的关键指标）
- GPU 活动度
- fusion-mlx 进程状态与内存占用
- 推理并发数 vs 配置上限

#### 🔑 API Key 管理

参考 One API + LiteLLM：
- Key 列表：名称、Key（掩码显示）、状态、已用额度、额度上限、过期时间、创建时间
- 创建 Key：设置额度上限（Token 数或金额）、设置过期时间、绑定允许的模型
- Key 详情页：用量趋势图、最近请求日志、费用明细
- Key 操作：启用/禁用、重置额度、删除

**额度模型**（对标 New API 精细化）：
```
Key 级别:
├─ 额度上限 (总额度: Token 数 or 金额)
├─ 已用额度 (实时扣减)
├─ 模型白名单 (可选，限制 Key 可调用的模型)
├─ 速率限制 (RPM/TPM，可选)
└─ 过期时间 (可选)
```

#### 🌐 渠道管理

渠道 = 一个推理后端实例：
- 渠道列表：名称、类型(local/cluster/cloud)、地址、状态(在线/离线)、优先级、权重
- 创建渠道：选择类型→填写配置→测试连通性→保存
- 渠道类型：
  - **Local**: fusion-mlx (自动发现 :11434)
  - **Cluster**: vLLM-ascend / vLLM-cuda
  - **Cloud**: 火山引擎 / 千帆 / DeepSeek / OpenAI / Claude / OpenRouter
- 渠道健康检查：定时探活、延迟检测、错误率追踪

#### 📝 请求日志

**这是客户最关心的核心页面** — "必须让客户能够知道通过你干了什么"

- 日志列表：时间、Key、模型、渠道、Token 数(输入/输出)、费用、延迟、状态
- 筛选条件：时间范围、Key、模型、渠道、状态(成功/失败)、最小 Token 数、最小费用
- 日志详情：
  - 请求：完整 prompt（可配置脱敏）、模型参数、Key 信息
  - 响应：完整 completion（可配置脱敏）、Token 统计、首 Token 时间
  - 路由：命中规则、选中的渠道、路由原因（token预算/硬件/熔断）
  - 性能：总延迟、首 Token 延迟、Token/s
- 导出：CSV / JSON

**日志存储策略**：
- 内存环形缓冲区（最近 10000 条，零依赖）
- 可选 SQLite 持久化（编译标签 `sqlite`）
- 保留天数可配置

#### 📈 数据分析

5 个子页面：

**Token 用量**：
- 时间维度趋势图（小时/天/周/月）
- 按 Key/模型/渠道的分组柱状图
- 输入 Token vs 输出 Token 比例

**费用统计**：
- 时间维度费用趋势
- 按 Key/模型/渠道的费用占比（饼图）
- 费用预测（基于近 7 天趋势）
- **本地节省金额**（Fusion-Gateway 独有：本地推理 vs 同等云端调用的费用对比）

**模型分布**：
- 模型调用次数排行
- 模型 Token 用量排行
- 模型费用排行

**延迟分析**：
- P50/P90/P99 延迟趋势
- 按渠道的延迟对比
- 首 Token 延迟(TTFT)分布

**错误统计**：
- 错误率趋势
- 按渠道/模型的错误分布
- 熔断触发次数
- 4xx/5xx/超时 分类

#### 🔀 路由管理

**路由规则**（可视化配置，替代 YAML 手写）：
- 规则列表：优先级、条件、目标渠道、状态
- 规则编辑：
  - 条件：Token 范围、模型名称、Key 标签
  - 动作：路由到指定渠道/渠道组
  - 熔断条件：内存超限、Swap 触发、模型离线
- 路由决策日志：最近路由决策及原因

**熔断器状态**：
- 各渠道熔断器当前状态（Closed/Open/HalfOpen）
- 触发原因与时间
- 手动重置按钮

#### 💰 模型价格

**价格表** — 费用计算的基础：
- 内置默认价格（主流模型，参考 Portkey-AI/models 数据）
- 支持自定义覆盖（用户可修改任意模型价格）
- 价格维度：输入 Token 单价、输出 Token 单价（每百万 Token）
- 支持 CNY 和 USD 双币种
- 本地模型标注为 ¥0.00（但显示节省金额）

#### ⚙️ 系统设置

**基础配置**（对应 config.yaml，Web 化）：
- 服务端口、日志级别
- 路由：硬件感知开关、Token 阈值、本地最大内存比、本地最大并发
- 熔断器：各触发条件开关与阈值

**认证配置**：
- Admin 密码修改
- API Key 策略（默认额度、默认过期）

**系统信息**：
- 版本号、Go 版本、编译时间
- 运行时长、内存占用
- 配置文件路径、数据目录

---

## 四、数据模型设计

### 4.1 请求日志记录

```go
type RequestLog struct {
    ID            string    `json:"id"`
    RequestID     string    `json:"request_id"`
    Timestamp     time.Time `json:"timestamp"`

    // 请求信息
    APIKeyID      string    `json:"api_key_id"`
    APIKeyName    string    `json:"api_key_name"`
    Model         string    `json:"model"`
    RequestType   string    `json:"request_type"`   // chat/completion/embedding/rerank
    IsStream      bool      `json:"is_stream"`

    // 路由信息
    ChannelID     string    `json:"channel_id"`
    ChannelName   string    `json:"channel_name"`
    ChannelType   string    `json:"channel_type"`   // local/cluster/cloud
    RouteReason   string    `json:"route_reason"`   // token_budget/hardware_healthy/circuit_breaker/fallback

    // Token 统计
    InputTokens   int       `json:"input_tokens"`
    OutputTokens  int       `json:"output_tokens"`
    TotalTokens   int       `json:"total_tokens"`
    CachedTokens  int       `json:"cached_tokens"`

    // 费用
    Cost          float64   `json:"cost"`            // CNY
    CostUSD       float64   `json:"cost_usd"`
    LocalSavings  float64   `json:"local_savings"`   // 本地推理节省金额

    // 性能
    Latency       float64   `json:"latency"`         // ms, 总延迟
    TTFT          float64   `json:"ttft"`            // ms, 首 Token 延迟

    // 状态
    StatusCode    int       `json:"status_code"`
    IsSuccess     bool      `json:"is_success"`
    ErrorMessage  string    `json:"error_message,omitempty"`
}
```

### 4.2 API Key

```go
type APIKey struct {
    ID            string    `json:"id"`
    Name          string    `json:"name"`
    Key           string    `json:"key"`             // 仅创建时返回完整值
    KeyPrefix     string    `json:"key_prefix"`      // fk-xxxx****
    Status        string    `json:"status"`          // active/disabled/expired

    // 额度
    QuotaType     string    `json:"quota_type"`      // tokens/cost/unlimited
    QuotaLimit    float64   `json:"quota_limit"`
    QuotaUsed     float64   `json:"quota_used"`
    QuotaRemaining float64  `json:"quota_remaining"`

    // 限制
    AllowedModels []string  `json:"allowed_models"`  // 空=全部
    RPM           int       `json:"rpm"`             // 0=不限
    TPM           int       `json:"tpm"`             // 0=不限

    // 时间
    ExpiresAt     *time.Time `json:"expires_at"`
    CreatedAt     time.Time  `json:"created_at"`
    UpdatedAt     time.Time  `json:"updated_at"`
}
```

### 4.3 渠道

```go
type Channel struct {
    ID            string    `json:"id"`
    Name          string    `json:"name"`
    Type          string    `json:"type"`            // local/cluster/cloud
    Provider      string    `json:"provider"`        // fusion_mlx/vllm/volcengine/qianfan/deepseek/openai/anthropic/openrouter
    BaseURL       string    `json:"base_url"`
    APIKey        string    `json:"api_key"`         // 写入时显示，读取时脱敏
    Models        []string  `json:"models"`          // 支持的模型列表

    // 路由参数
    Priority      int       `json:"priority"`        // 优先级(1最高)
    Weight        int       `json:"weight"`          // 负载均衡权重

    // 状态
    Status        string    `json:"status"`          // online/offline/error
    Latency       float64   `json:"latency_ms"`     // 最近平均延迟
    ErrorRate     float64   `json:"error_rate"`      // 最近错误率

    CreatedAt     time.Time `json:"created_at"`
    UpdatedAt     time.Time `json:"updated_at"`
}
```

### 4.4 模型价格

```go
type ModelPricing struct {
    Model          string  `json:"model"`
    Provider       string  `json:"provider"`
    InputPrice     float64 `json:"input_price"`      // 每百万Token(CNY)
    OutputPrice    float64 `json:"output_price"`     // 每百万Token(CNY)
    InputPriceUSD  float64 `json:"input_price_usd"`
    OutputPriceUSD float64 `json:"output_price_usd"`
    IsLocal        bool    `json:"is_local"`
    IsCustom       bool    `json:"is_custom"`         // 用户自定义 vs 内置
}
```

---

## 五、后端实现架构

### 5.1 新增 Go Package 结构

```
internal/
├─ admin/                    # Admin API (新增)
│   ├─ handler/              # HTTP handlers
│   │   ├─ auth.go
│   │   ├─ dashboard.go
│   │   ├─ keys.go
│   │   ├─ channels.go
│   │   ├─ logs.go
│   │   ├─ analytics.go
│   │   ├─ routing.go
│   │   ├─ models.go
│   │   └─ settings.go
│   ├─ middleware/            # Admin 认证中间件
│   │   └─ auth.go
│   └─ router.go             # Admin 路由注册
├─ store/                    # 数据存储 (新增)
│   ├─ store.go              # Store 接口
│   ├─ memory/               # 内存存储(默认)
│   │   ├─ logs.go           # 环形缓冲区
│   │   ├─ keys.go
│   │   ├─ channels.go
│   │   └─ analytics.go
│   └─ sqlite/               # SQLite 持久化(可选)
│       ├─ logs.go
│       ├─ keys.go
│       ├─ channels.go
│       └─ analytics.go
├─ pricing/                  # 模型价格 (新增)
│   ├─ pricing.go            # 价格查询接口
│   └─ default.go            # 内置价格表
└─ web/                      # 前端构建产物 (新增)
    └─ dist/                 # go:embed 目标
        ├─ index.html
        └─ assets/
```

### 5.2 Store 接口

```go
type Store interface {
    // Keys
    CreateKey(key *APIKey) error
    GetKey(id string) (*APIKey, error)
    ListKeys() ([]*APIKey, error)
    UpdateKey(key *APIKey) error
    DeleteKey(id string) error

    // Channels
    CreateChannel(ch *Channel) error
    GetChannel(id string) (*Channel, error)
    ListChannels() ([]*Channel, error)
    UpdateChannel(ch *Channel) error
    DeleteChannel(id string) error

    // Logs
    AppendLog(log *RequestLog) error
    QueryLogs(filter LogFilter) ([]*RequestLog, int, error)
    GetLog(id string) (*RequestLog, error)

    // Analytics
    GetTokenStats(from, to time.Time, groupBy string) ([]*TokenStat, error)
    GetCostStats(from, to time.Time, groupBy string) ([]*CostStat, error)
    GetModelStats(from, to time.Time) ([]*ModelStat, error)
    GetLatencyStats(from, to time.Time) ([]*LatencyStat, error)
    GetErrorStats(from, to time.Time) ([]*ErrorStat, error)

    // Dashboard
    GetDashboardOverview() (*DashboardOverview, error)
}
```

### 5.3 Token 计费管道

当前问题：Stream 模式下输出 Token 未记录，Embedding/Rerank 无 Token 统计。

修复方案：

```
请求进入 → Tokenizer 计数(input) → 路由决策 → 后端调用 →
  ├─ 非Stream: 直接统计 output tokens → 记录日志 → 扣减额度
  └─ Stream:   SSE 逐 chunk 收集 usage → 完成后统计 → 记录日志 → 扣减额度
```

关键修改点：
1. `internal/adapter/` 所有 adapter 的 StreamChat 返回中追加 usage 聚合
2. `internal/server/handleStreamChat` 中在 stream 结束时汇总 output tokens
3. `internal/observability/metrics.go` 的 RecordTokens 补充 output tokens
4. 新增 `internal/admin/` 在请求完成后调用 `store.AppendLog()`

### 5.4 额度扣减流程

```
请求进入 → Auth 中间件
  ├─ 验证 Key 有效性
  ├─ 检查 Key 额度 (quota_used < quota_limit)
  ├─ 检查 Key 速率限制 (RPM/TPM)
  └─ 通过 → 注入 Key 上下文

请求完成 → 记录日志
  ├─ 计算 Token 费用 (pricing.Lookup(model) * tokens)
  ├─ 扣减 Key 额度 (原子操作)
  ├─ 追加请求日志 (store.AppendLog)
  └─ 更新 Prometheus 指标
```

---

## 六、前端工程结构

### 6.1 项目结构

```
web/                              # 前端项目根目录
├─ package.json
├─ vite.config.ts
├─ tsconfig.json
├─ index.html
├─ public/
│   └─ favicon.svg
└─ src/
    ├─ main.tsx                   # 入口
    ├─ App.tsx                    # 路由配置
    ├─ layouts/
    │   └─ AdminLayout.tsx        # 侧边栏+顶栏+内容区
    ├─ pages/
    │   ├─ Dashboard/
    │   │   ├─ Overview.tsx
    │   │   └─ Hardware.tsx
    │   ├─ Keys/
    │   │   ├─ KeyList.tsx
    │   │   └─ KeyDetail.tsx
    │   ├─ Channels/
    │   │   ├─ ChannelList.tsx
    │   │   └─ ChannelForm.tsx
    │   ├─ Logs/
    │   │   ├─ LogList.tsx
    │   │   └─ LogDetail.tsx
    │   ├─ Analytics/
    │   │   ├─ TokenUsage.tsx
    │   │   ├─ CostAnalysis.tsx
    │   │   ├─ ModelDistribution.tsx
    │   │   ├─ LatencyAnalysis.tsx
    │   │   └─ ErrorStats.tsx
    │   ├─ Routing/
    │   │   ├─ RuleList.tsx
    │   │   └─ CircuitBreakers.tsx
    │   ├─ Models/
    │   │   └─ Pricing.tsx
    │   └─ Settings/
    │       ├─ General.tsx
    │       ├─ Auth.tsx
    │       └─ System.tsx
    ├─ components/
    │   ├─ StatCard.tsx            # 统计卡片
    │   ├─ TrendChart.tsx          # 趋势图
    │   ├─ StatusBadge.tsx         # 状态徽标
    │   └─ JsonViewer.tsx          # JSON 查看器(日志详情)
    ├─ hooks/
    │   ├─ useFetch.ts
    │   └─ useSSE.ts               # SSE 实时数据
    ├─ services/
    │   └─ api.ts                  # API 调用封装
    ├─ stores/
    │   └─ auth.ts                 # 认证状态(Zustand)
    └─ utils/
        ├─ format.ts               # 数字/时间格式化
        └─ pricing.ts              # 费用计算
```

### 6.2 构建与嵌入

```go
// internal/web/embed.go
//go:embed dist
var DistFS embed.FS

func RegisterAdminUI(mux *http.ServeMux) {
    sub, _ := fs.Sub(DistFS, "dist")
    mux.Handle("/admin/", http.StripPrefix("/admin/", http.FileServer(http.FS(sub))))
}
```

```bash
# 前端构建
cd web && npm run build   # → dist/

# Go 编译时嵌入
go build -o fusion-gateway ./cmd/gateway
```

---

## 七、分阶段实施计划

### Phase 1: 基础框架 + 仪表盘 (2 周)

**目标**: 能看到数据

- [ ] Admin API 框架：路由注册、认证中间件、统一错误处理
- [ ] Store 接口 + 内存实现（环形缓冲区日志、Key/Channel CRUD）
- [ ] Dashboard 总览 API（从现有 Prometheus 指标聚合）
- [ ] 硬件监控 API（复用 internal/hardware 的指标）
- [ ] 前端项目脚手架：Vite + React + Ant Design + Router
- [ ] 仪表盘页面：统计卡片 + 趋势图 + 硬件状态 + 实时请求流
- [ ] Go embed 集成
- [ ] 登录页面 + Session 管理

**交付物**: 单二进制包含完整仪表盘，`/admin` 可访问

### Phase 2: Key + 渠道 + 日志 (2 周)

**目标**: 能管资源、能看日志

- [ ] API Key CRUD + 额度管理 + 速率限制
- [ ] 渠道 CRUD + 连通性测试 + 健康检查
- [ ] 请求日志记录管道（补齐 stream output tokens）
- [ ] 日志列表 + 筛选 + 详情页
- [ ] Key 用量明细页
- [ ] 渠道管理页面

**交付物**: 完整的 Key/Channel/Log 管理

### Phase 3: 数据分析 + 费用追踪 (2 周)

**目标**: 能算账

- [ ] 模型价格表（内置 + 自定义覆盖）
- [ ] Token 费用计算管道
- [ ] Key 额度扣减（原子操作）
- [ ] 5 个分析页面（Token/Cost/Model/Latency/Error）
- [ ] 本地推理节省金额展示
- [ ] 费用预测
- [ ] 日志导出（CSV/JSON）

**交付物**: 完整的账单与分析能力

### Phase 4: 路由管理 + 系统设置 (1 周)

**目标**: 可视化配置

- [ ] 路由规则可视化编辑
- [ ] 熔断器状态监控 + 手动重置
- [ ] 系统设置页面（配置热重载）
- [ ] 模型价格管理页面
- [ ] 系统信息页

**交付物**: 全功能 GUI

### Phase 5: 可选持久化 + 优化 (1 周)

**目标**: 生产就绪

- [ ] SQLite 持久化存储（编译标签 `sqlite`）
- [ ] 数据老化策略（日志保留天数）
- [ ] 仪表盘数据缓存（减少实时计算压力）
- [ ] 国际化（i18n，中文/英文）
- [ ] 暗色主题
- [ ] 移动端适配

**交付物**: 生产级别 Admin Dashboard

---

## 八、安全考量

| 风险 | 对策 |
|------|------|
| Admin API 未授权访问 | 独立 Admin Token + JWT Session，与 API Key 隔离 |
| 日志中泄露 Prompt 内容 | 默认脱敏（仅记录 Token 数），可选开启完整日志 |
| API Key 明文存储 | 加密存储（AES-256-GCM），仅创建时返回完整值 |
| 费用计算被篡改 | 服务端计算，不依赖客户端上报 |
| 熔断器手动重置 | 需 Admin 权限，操作记录审计日志 |
| XSS | React 默认转义 + CSP Header |
| CSRF | JWT Token + SameSite Cookie |

---

## 九、与竞品的最终对比

| 维度 | One API | New API | LiteLLM | Bifrost | **Fusion-Gateway** |
|------|---------|---------|---------|---------|-------------------|
| 部署方式 | 单二进制 | 单二进制 | Python环境 | 单二进制 | **单二进制** |
| 语言 | Go | Go | Python | Go | **Go** |
| 本地推理 | ❌ | ❌ | ❌ | ❌ | **✅ fusion-mlx** |
| 硬件感知 | ❌ | ❌ | ❌ | ❌ | **✅ UMA/Swap/GPU** |
| 费用追踪 | 基础 | 精细 | 精细 | 预算制 | **精细+本地节省** |
| 请求日志 | 基础 | 增强 | 详细 | 详细 | **详细+路由原因** |
| 中国云厂商 | ✅ | ✅ | 部分 | 部分 | **✅ 全覆盖** |
| 熔断器 | ❌ | ❌ | ❌ | ✅ | **✅ 硬件感知熔断** |
| 路由可视化 | ❌ | ❌ | 部分 | ✅ | **✅ 硬件感知路由** |

**Fusion-Gateway 的 GUI 核心竞争力**：

1. **唯一的 Go + 本地推理联动 GUI** — 不争 UMA 内存
2. **硬件感知路由可视化** — 这是其他竞品都没有的差异化
3. **本地推理节省金额** — 客户最关心的 ROI 指标
4. **中国云厂商 + 本地推理 + 硬件感知** 三位一体的路由决策可视化
5. **单二进制零依赖部署** — 小企业友好
