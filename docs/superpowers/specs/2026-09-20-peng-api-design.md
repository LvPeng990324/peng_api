# peng_api — LLM API 分发网关设计文档

日期：2026-09-20
状态：已与需求方逐节确认

## 1. 目标与范围

自用的 LLM API 分发网关，替代 new-api 中实际用到的约 10% 功能：接入多个渠道多种模型，统一 OpenAI 格式出口，按优先级分发，失败降级重试与渠道自动禁用，完整请求日志供 agent 调试，模型别名聚合集中配置。

### 做

- 统一 `POST /v1/chat/completions` 入口（含 SSE 流式），`GET /v1/models` 聚合模型列表（带 context_length）
- 多个命名 Bearer token 鉴权，日志记录来源 token
- 渠道管理：上游地址 / key / 模型绑定 / 优先级 / 启停
- 标准模型 + 别名表，集中配置，大小写不敏感命中
- 失败按优先级依次降级重试；连续失败自动禁用，冷却（指数退避）后惰性自动恢复
- 完整请求/响应日志（成功失败都记，SSE 拼接完整响应），定期清理
- 从上游拉取模型列表并辅助绑定（自动预匹配标准模型、填充模型参数）
- 渠道连通性测试（用配置的便宜测试模型发 ping）
- Web 管理界面：渠道管理、模型与别名、日志查看（三个标签页 + 登录页）
- 上游协议 Provider 抽象：本期只实现 `openai`，接缝预留 `anthropic` 等扩展

### 明确不做

多用户体系、注册登录、额度计费、兑换码、支付、绘图、审计、通知、Redis、MySQL、CGO。

## 2. 技术栈

- Go 1.22+，单二进制交付
- SQLite 单文件存储：`modernc.org/sqlite`（纯 Go 驱动），`database/sql`，WAL 模式
- 路由：`chi`
- 前端：Vue3 单文件 SPA，vendor 进二进制（不走 CDN），`embed.FS` 嵌入
- 测试：标准库 `testing` + `net/http/httptest`，SQLite 用 `:memory:`

## 3. 总体架构

```
peng_api/
├── main.go                 # 入口：配置加载、DB 初始化、迁移、HTTP server、日志清理定时器
├── internal/
│   ├── config/             # 启动配置：监听地址、管理密码、DB 路径、日志保留天数等
│   ├── store/              # SQLite 数据访问层（database/sql + modernc.org/sqlite）
│   ├── relay/              # 核心转发引擎：模型解析 → 渠道选择 → 转发 → 重试降级
│   │   └── provider/       # 上游协议抽象，本期实现 openai.go
│   ├── auth/               # Bearer token 中间件 + 管理端 session
│   ├── admin/              # 管理 API：登录、渠道/模型/别名/token CRUD、日志查询
│   └── web/                # embed.FS 嵌入前端产物
├── web/                    # Vue3 单文件 SPA 源码
├── docs/
└── README.md
```

运行时单进程。HTTP 入口分三股：

- `/v1/*` — 客户端 API，Bearer token 鉴权
- `/api/*` — 管理 API，session cookie 鉴权（login 除外）
- `/` — 静态 SPA

核心转发引擎无状态；渠道状态（连续失败数、冷却截止时间）存 DB，转发时按需读写。

### Provider 抽象

```go
// internal/relay/provider/provider.go
type Provider interface {
    Name() string
    // 执行一次 chat 请求（stream 标志在 req 里），结果统一转成 OpenAI 格式
    Chat(ctx context.Context, ch *Channel, req *ChatRequest) *Result
    // 拉取上游模型列表
    ListModels(ctx context.Context, ch *Channel) ([]UpstreamModel, error)
}

// 注册表：channels.type → Provider 实例
var registry = map[string]Provider{"openai": OpenAIProvider{}}
```

- 下游对客户端永远暴露 OpenAI 格式；将来加 `anthropic` Provider 时负责双向翻译，主流程零改动。
- 日志中的 response_body 始终是客户端实际收到的内容（OpenAI 格式）。

## 4. 数据库表结构

```sql
-- 标准模型：统一模型视图的核心
CREATE TABLE models (
  id INTEGER PRIMARY KEY AUTOINCREMENT,
  name TEXT NOT NULL UNIQUE,                -- 标准名，如 glm-5.3
  context_length INTEGER,                   -- 上下文长度，NULL=未知
  max_output_tokens INTEGER,                -- 最大输出，NULL=未知
  created_at DATETIME DEFAULT CURRENT_TIMESTAMP
);

-- 别名 → 标准模型（匹配大小写不敏感）
CREATE TABLE model_aliases (
  id INTEGER PRIMARY KEY AUTOINCREMENT,
  alias TEXT NOT NULL UNIQUE,               -- 如 GLM-5.3、glm5.3
  model_id INTEGER NOT NULL REFERENCES models(id) ON DELETE CASCADE
);

-- 渠道
CREATE TABLE channels (
  id INTEGER PRIMARY KEY AUTOINCREMENT,
  name TEXT NOT NULL,
  type TEXT NOT NULL DEFAULT 'openai',      -- 上游协议类型，本期固定 openai
  base_url TEXT NOT NULL,                   -- 如 https://api.bigmodel.cn/coding/paas/v4
  api_key TEXT NOT NULL,
  priority INTEGER NOT NULL DEFAULT 0,      -- 数字越大越优先
  enabled INTEGER NOT NULL DEFAULT 1,       -- 手动启停
  test_model TEXT NOT NULL DEFAULT '',      -- 连通性测试用的上游模型名
  auto_disabled INTEGER NOT NULL DEFAULT 0, -- 被自动禁用标记
  consecutive_failures INTEGER NOT NULL DEFAULT 0,
  disabled_until DATETIME,                  -- 冷却截止时间
  created_at DATETIME DEFAULT CURRENT_TIMESTAMP
);

-- 渠道 ↔ 标准模型（多对多），upstream_model 为该模型在此渠道的上游名
CREATE TABLE channel_models (
  channel_id INTEGER NOT NULL REFERENCES channels(id) ON DELETE CASCADE,
  model_id INTEGER NOT NULL REFERENCES models(id) ON DELETE CASCADE,
  upstream_model TEXT NOT NULL DEFAULT '',  -- 空 = 用标准名请求上游
  PRIMARY KEY (channel_id, model_id)
);

-- API token（多个命名 token）
CREATE TABLE tokens (
  id INTEGER PRIMARY KEY AUTOINCREMENT,
  name TEXT NOT NULL,
  token TEXT NOT NULL UNIQUE,               -- sk-xxx
  enabled INTEGER NOT NULL DEFAULT 1,
  created_at DATETIME DEFAULT CURRENT_TIMESTAMP
);

-- 请求日志：每一次上游尝试一行（重试产生多行，request_id 归组）
CREATE TABLE request_logs (
  id INTEGER PRIMARY KEY AUTOINCREMENT,
  request_id TEXT NOT NULL,                 -- 每个客户端请求一个 UUID，归组多次尝试
  attempt INTEGER NOT NULL,                 -- 第几次尝试
  token_id INTEGER, token_name TEXT,        -- 来源 token
  model_requested TEXT NOT NULL,            -- 客户端原始模型名
  model_canonical TEXT,                     -- 解析出的标准模型
  channel_id INTEGER, channel_name TEXT,    -- 本次尝试的渠道
  stream INTEGER NOT NULL DEFAULT 0,
  status TEXT NOT NULL,                     -- success / failed
  http_status INTEGER,                      -- 上游状态码
  error TEXT,                               -- 失败原因（成功为空）
  request_body TEXT,                        -- 完整请求体
  response_body TEXT,                       -- 完整响应体（SSE 拼接后）
  prompt_tokens INTEGER, completion_tokens INTEGER,  -- 从响应解析，便于列表展示
  latency_ms INTEGER,
  created_at DATETIME DEFAULT CURRENT_TIMESTAMP
);
CREATE INDEX idx_logs_created ON request_logs(created_at DESC);
CREATE INDEX idx_logs_request ON request_logs(request_id);
```

设计要点：

- **模型解析**：请求模型名先匹配标准模型名（大小写不敏感），再匹配别名表；都未命中返回 404 类错误。`glm-5.3`/`GLM-5.3`/`glm5.3` 收敛到同一标准模型。
- **渠道选择**：绑定该标准模型、`enabled=1`、未被自动禁用（冷却过期则顺手恢复）的渠道，按 priority 从大到小排序，同优先级按 id 升序（先创建的先试）。
- **模型参数在标准模型侧**：同一模型在不同渠道的参数差异不分别存储，对外只展示一份（用户可编辑）。
- **惰性恢复**：渠道选择时发现 `disabled_until` 已过期即清除禁用标记，无后台探测协程。

## 5. 核心流程

### 5.1 `POST /v1/chat/completions` 转发主流程

```
1. Bearer 中间件校验 token（不存在/已禁用 → 401）
2. 读取请求体（上限 10MB），解析出 model 和 stream 字段
3. 模型解析：标准模型名（大小写不敏感）→ 别名表 → 未命中返回 404 类错误
4. 渠道选择：绑定该标准模型 且 enabled=1 且 未被自动禁用
   （disabled_until 已过期 → 清除禁用标记，惰性恢复）
   按 priority 从大到小排序，得到候选渠道列表
5. 依次尝试候选渠道：
   a. 构造上游请求：URL = base_url + "/chat/completions"
      Authorization: Bearer <渠道api_key>
      若渠道绑定了 upstream_model（非空），替换请求体里的 model 字段
   b. 发起请求（连接超时 10s；非流式整体超时默认 300s，可配）
   c. 结果分类：
      - 2xx                     → 成功：回传客户端，记日志，渠道失败计数清零
      - 网络错误/超时/5xx/429   → 可重试失败：记日志，失败计数+1（达阈值自动禁用+冷却），试下一渠道
      - 其他 4xx                → 不可重试：记日志，原样透传错误给客户端，停止
6. 所有候选渠道耗尽 → 返回 OpenAI 风格 503 错误（消息含各渠道失败原因）
```

### 5.2 SSE 流式转发（stream: true）

```
- 上游请求带 Accept: text/event-stream
- 首字节超时 60s（可配）：超时未收到数据 → 可重试失败，可降级下一渠道
- 一旦开始向客户端写字节，无法再降级（客户端已收到响应头）
- 转发循环：上游逐行读 → 立即写客户端并 Flush → 同时累积到缓冲区
- 流结束（[DONE] 或连接关闭）→ 累积的完整 SSE 文本作为 response_body 落日志
- 中途上游断流 → 记 failed 日志（含已收到内容），关闭客户端连接
```

### 5.3 失败计数与自动禁用/恢复

```
可重试失败时：consecutive_failures++（单条 UPDATE 原子自增）
  达到阈值（默认 3，可配）→ auto_disabled=1
  冷却时长 = 5min × 2^(连续失败数-阈值)，封顶 60min（指数退避）
成功时：consecutive_failures=0，auto_disabled=0
渠道选择时：auto_disabled=1 且 disabled_until 已过 → 清除标记（惰性恢复）
管理端可手动「重置状态」立即恢复
```

### 5.4 日志落盘与清理

```
- 每次上游尝试写一行 request_logs，request_id（UUID）归组同一次客户端请求
- 非流式：拿到响应后即写；流式：流结束后写（response_body 为拼接的完整 SSE 文本）
- 同步写库（SQLite WAL，写很快）；解析 usage 存入 prompt_tokens/completion_tokens
- 清理：启动时 + 每 24h 定时器，删除 created_at 早于 N 天前的日志（默认 30 天，可配）
```

### 5.5 `GET /v1/models`

返回「至少绑定了一个启用渠道」的标准模型列表，OpenAI 格式并带 Kimi 风格扩展字段（NULL 时省略）：

```json
{"object":"list","data":[
  {"id":"glm-5.3","object":"model","created":1735689600,"owned_by":"peng-api",
   "context_length":131072,"max_output_tokens":8192}
]}
```

下游 agent（如 Hermes）可读取 context_length。

### 5.6 拉取上游模型列表（渠道页「获取模型」按钮）

```
1. POST /api/channels/{id}/fetch-models
   → Provider.ListModels(base_url, api_key) 拉上游 GET /models
   → 返回 [{upstream_id, context_length?, max_output_tokens?,
            suggested_model_id, suggested_name}]    // 按别名/标准名大小写不敏感预匹配
2. 前端弹窗：每行一个上游模型 + 下拉框（匹配到的标准模型 / 新建标准模型）+ 勾选框
3. 确认后 POST /api/channels/{id}/models 批量绑定：
   - 「新建」的先建标准模型（upstream_id 作为标准名，并把自身加为别名）
   - 建 channel_models 绑定（upstream_model = upstream_id）
   - 上游返回了 context_length 且标准模型该字段为空 → 自动填充
4. 上游不支持/失败 → 返回错误提示，可改为手动绑定
```

手动增删绑定 = channel_models 的 CRUD（渠道编辑内）；模型参数在「模型与别名」页编辑。

### 5.7 渠道连通性测试（渠道页「测试」按钮）

```
- 渠道配置的 test_model：从该渠道已绑定的上游模型名中选一个（用户选便宜的）
- POST /api/channels/{id}/test
  → 用 Provider 发极短请求：
    {"model": test_model, "messages":[{"role":"user","content":"ping"}],
     "max_tokens": 1, "stream": false}
  → 返回 {success, latency_ms, http_status, error?, request_body, response_body}
- 不写 request_logs（管理操作非客户端流量），原文在测试弹窗直接展示
- 未配置 test_model → 报错提示先选测试模型
```

## 6. 接口定义

### 6.1 客户端 API（Bearer token 鉴权）

| 方法 | 路径 | 说明 |
|---|---|---|
| POST | `/v1/chat/completions` | 统一聊天入口（见 5.1/5.2） |
| GET | `/v1/models` | 模型聚合列表（见 5.5） |

错误格式与 OpenAI 一致：`{"error":{"message":"...","type":"...","code":"..."}}`。

### 6.2 管理 API（session cookie 鉴权，login 除外）

```
POST   /api/login            {password} → 种 session cookie（内存存储，重启需重登）
POST   /api/logout
GET    /api/me               校验 session 是否有效

GET    /api/channels         渠道列表（含绑定模型、状态、失败计数）
POST   /api/channels         {name, type, base_url, api_key, priority, test_model,
                              models: [{model_id, upstream_model}]}
PUT    /api/channels/{id}    同上，整体更新
DELETE /api/channels/{id}
POST   /api/channels/{id}/toggle        {enabled: true|false}
POST   /api/channels/{id}/reset         手动清除自动禁用状态
POST   /api/channels/{id}/fetch-models  拉取上游模型列表（预匹配建议，不落库）
POST   /api/channels/{id}/models        批量绑定 [{upstream_model, model_id?, new_model_name?}]
POST   /api/channels/{id}/test          连通性测试（用 test_model 发 ping）

GET    /api/models           标准模型列表（含别名数组、绑定渠道数）
POST   /api/models           {name, aliases: [...], context_length?, max_output_tokens?}
PUT    /api/models/{id}      整体更新（别名整体替换）
DELETE /api/models/{id}

GET    /api/tokens           token 列表（token 值打码 sk-****）
POST   /api/tokens           {name} → 生成 sk-xxx，仅此一次完整返回
PUT    /api/tokens/{id}      {name, enabled}
DELETE /api/tokens/{id}

GET    /api/logs             分页+筛选：page/size/model/status/token_id/channel_id
                             （model 同时匹配 model_requested 和 model_canonical）
                             列表行：时间、模型、渠道、状态、HTTP码、耗时、tokens
GET    /api/logs/{id}        详情：完整 request/response 原文 +
                             同 request_id 的所有尝试（降级链路）
```

### 6.3 启动配置（env / flag，不进数据库）

```
-addr                    监听地址，默认 :8080
-db                      SQLite 文件路径，默认 ./peng.db
-admin-password          管理密码（必填，也可用 PENG_ADMIN_PASSWORD 环境变量）
-log-retention-days      日志保留天数，默认 30
-fail-threshold          自动禁用阈值，默认 3
-request-timeout         非流式整体超时秒数，默认 300
-connect-timeout         上游连接超时秒数，默认 10
-stream-first-byte-timeout  流式首字节超时秒数，默认 60
```

## 7. Web 界面

登录页 + 单页 SPA 三个标签页：

- **渠道管理**：渠道列表（名称、类型、地址、优先级、状态、失败计数、启停开关）、新建/编辑（含模型绑定表格、test_model 下拉）、获取模型弹窗、测试弹窗、手动重置状态
- **模型与别名**：标准模型列表（标准名、别名数组、context_length、max_output_tokens、绑定渠道数）、新建/编辑/删除
- **日志查看**：筛选（模型/状态/token/渠道）+ 分页列表；详情展开完整 request/response 原文与同 request_id 降级链路

## 8. 错误处理

- 请求路径禁止 panic：顶层 recover 中间件兜底，panic → 500 + 记日志；转发循环内所有错误显式处理。
- 上游 4xx（除 429）原样透传客户端，不重试、不计渠道失败。
- 客户端错误一律 OpenAI 风格 `{"error":{...}}`；管理 API 用 `{"error":"..."}` + 合适状态码。
- SQLite 开 WAL；日志落盘与失败计数为同步短写，靠 SQLite 串行化，不自建锁。
- 渠道状态更新用单条原子 UPDATE（如 `SET consecutive_failures = consecutive_failures + 1`），避免读-改-写竞态。
- 管理端 session：内存 map + 过期时间，cookie SameSite=Lax；密码用 constant-time 比较；session token 用 crypto/rand。

## 9. 测试策略

单测集中关键路径，`httptest.Server` 做假上游，SQLite 用 `:memory:`：

| 模块 | 测试点 |
|---|---|
| 模型解析 | 标准名/别名命中、大小写不敏感、未命中 404 |
| 渠道选择 | 优先级排序、跳过禁用、冷却过期惰性恢复 |
| 转发+重试 | 首渠道 5xx 降级成功；4xx 透传不降级；全部失败 503；upstream_model 替换 |
| 失败计数 | 达阈值自动禁用、成功清零、冷却时长指数退避 |
| SSE | 逐 chunk 透传+flush、完整内容拼接落日志、首字节超时降级、中途断流 |
| 日志 | 每尝试一行、request_id 归组、usage 解析 |
| 管理 API | 登录/session、CRUD 冒烟、fetch-models 预匹配、test 连通性 |
| Provider | OpenAI 实现用假上游验证请求构造与响应解析 |

验证命令：`go test ./...`，转发路径加 `-race` 跑一遍。

## 10. 交付物

- `go build` 可产出的完整单二进制代码
- 简短 README（构建、启动参数、基本使用）
- `AGENTS.md`：面向 AI agent 的项目文档（架构概述、目录结构、关键约定、开发/测试命令）。本项目由 agent 负责维护，代码或约定变更时必须同步更新该文件
