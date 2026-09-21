# AGENTS.md — peng_api

面向 AI agent 的项目说明。**修改代码后必须同步更新本文件**（以及 `docs/superpowers/specs/` 里的设计文档，若涉及设计变更）。

## 项目是什么

自用 LLM API 分发网关：多上游渠道 → 统一 OpenAI 格式 `/v1` 出口。Go 单二进制 + SQLite。详细设计见 `docs/superpowers/specs/2026-09-20-peng-api-design.md`。

## 常用命令

- 构建：`go build -o peng_api .`
- 测试：`go test ./...`（转发路径改动后加 `-race` 跑一遍）
- 运行：`./peng_api`（配置见 `.env`，可复制 `.env.example`；也可用 `-admin-password` 等 flag 或 `PENG_*` 环境变量）
- 静态检查：`go vet ./...`

## 目录结构

- `main.go` — 装配入口（config→store→provider→engine→admin→web→server）
- `internal/config` — flag/env/.env 配置解析（优先级：flag > 环境变量 > .env > 默认值）
- `internal/store` — SQLite 访问层（models/channels/tokens/logs 四个文件按资源分）
- `internal/relay` — 转发引擎：`handler.go`（/v1 入口）、`engine.go`（重试循环）、`stream.go`（SSE）、`util.go`（工具）
- `internal/relay/provider` — 上游协议抽象；新增协议（如 anthropic）在此实现 `Provider` 接口并注册进 `main.go` 的 Registry
- `internal/auth` — Bearer 中间件 + 管理端 session
- `internal/admin` — 管理 API（按资源分文件）
- `web/` — Vue3 SPA（无构建步骤，vendor 内嵌）；`embed.go` 负责静态路由

## 关键约定（改了会破坏东西的地方）

- **CGO 必须为 0**：SQLite 只用 modernc.org/sqlite，不引入任何 CGO 依赖
- **请求路径禁止 panic**：错误显式处理；chi Recoverer 兜底
- **时间戳一律 Go 端写 UTC**（time.Now().UTC()），不依赖 DB DEFAULT；扫描 DATETIME 列进 time.Time
- **sql.DB 必须 SetMaxOpenConns(1)**（单写者 + :memory: 测试安全）
- **渠道失败计数用事务内原子自增**，禁止读-改-写
- **发往上游的 model 字段总是被替换**：upstream_model 非空用它，否则用标准模型名（客户端传的可能是别名）
- **上游 4xx（除 429）不重试不计数**，原样透传；5xx/429/网络错误/超时才降级和计数
- **流式**：写出响应头后不再降级；客户端断开的日志不算渠道失败；日志 response_body 存拼接后的完整 SSE 文本
- **DB 写日志/计数用脱离请求 ctx 的 detached ctx（5s 超时）**，防止客户端断开丢日志
- 客户端错误格式 `{"error":{"message","type","code"}}`；管理 API `{"error":"..."}`
- 管理端 session 在内存中，重启失效是预期行为
- 新增/修改管理 API 时同步改 `web/app.js` + `web/index.html`
- **上游协议扩展**：实现 `provider.Provider` 接口（Chat/ListModels），在 main.go 注册；对下游永远暴露 OpenAI 格式

## 测试约定

- store 测试用 `:memory:`；relay/provider 测试用 httptest.Server 假上游
- 新功能先写失败测试再实现（TDD）
