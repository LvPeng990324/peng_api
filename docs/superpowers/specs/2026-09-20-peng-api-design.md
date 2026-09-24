# peng_api — LLM API 分发网关设计文档

日期：2026-09-20（2026-09-24 重构修订）
状态：已与需求方逐节确认

> 2026-09-24 重构：数据模型从「渠道 ↔ 标准模型 ↔ 别名」改为「渠道 1─n 模型实体 n─n 模型映射（标准名）」三级结构；取消渠道自动禁用与连续失败统计，只保留手动启停（渠道禁用则其模型不可用，查询时动态推导）；请求匹配改为标准名大小写敏感精确匹配。

## 1. 目标与范围

自用的 LLM API 分发网关，替代 new-api 中实际用到的约 10% 功能：接入多个渠道多种模型，统一 OpenAI 格式出口，按优先级分发，失败降级重试，完整请求日志供 agent 调试，模型映射（标准名）集中配置。

### 做

- 统一 `POST /v1/chat/completions` 入口（含 SSE 流式），`GET /v1/models` 聚合模型列表（带 context_length）
- 多个命名 Bearer token 鉴权，日志记录来源 token
- 渠道管理：上游地址 / key / 优先级 / 启停；渠道是模型实体的容器，不与其它实体直接关联
- 模型实体：挂在渠道下，只有上游模型名；可用性完全跟随渠道（渠道禁用 → 其模型不可被请求到）
- 模型映射（标准名）：请求入口，大小写敏感精确匹配；与模型实体多对多绑定，未绑定模型的标准名无法被请求到；模型参数（context_length / max_output_tokens）在标准名侧
- 失败按优先级依次降级重试；只保留手动启停，无自动禁用
- 完整请求/响应日志（成功失败都记，SSE 拼接完整响应），定期清理
- 从上游拉取模型列表，一键创建为渠道下的模型实体
- 渠道连通性测试（用配置的便宜测试模型发 ping）
- Web 管理界面：渠道、模型、模型映射、Token、日志（五个标签页 + 登录页）
- 上游协议 Provider 抽象：本期只实现 `openai`，接缝预留 `anthropic` 等扩展

### 明确不做

多用户体系、注册登录、额度计费、兑换码、支付、绘图、审计、通知、Redis、MySQL、CGO、渠道自动禁用、模型粒度的独立禁用（模型可用性 = 渠道 enabled）。

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
│   ├── relay/              # 核心转发引擎：标准名解析 → 模型选择 → 转发 → 重试降级
│   │   └── provider/       # 上游协议抽象，本期实现 openai.go
│   ├── auth/               # Bearer token 中间件 + 管理端 session
│   ├── admin/              # 管理 API：登录、渠道/模型/模型映射/token CRUD、日志查询
│   └── web/                # embed.FS 嵌入前端产物
├── web/                    # Vue3 单文件 SPA 源码
├── docs/
└── README.md
```

运行时单进程。HTTP 入口分三股：

- `/v1/*` — 客户端 API，Bearer token 鉴权
- `/api/*` — 管理 API，session cookie 鉴权（login 除外）
- `/` — 静态 SPA

核心转发引擎无状态；渠道只有手动启停开关，无运行时健康状态。

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
-- 渠道：模型实体的容器，只提供连接信息与优先级
CREATE TABLE channels (
  id INTEGER PRIMARY KEY AUTOINCREMENT,
  name TEXT NOT NULL,
  type TEXT NOT NULL DEFAULT 'openai',      -- 上游协议类型，本期固定 openai
  base_url TEXT NOT NULL,                   -- 如 https://api.bigmodel.cn/coding/paas/v4
  api_key TEXT NOT NULL,
  priority INTEGER NOT NULL DEFAULT 0,      -- 数字越大越优先
  enabled INTEGER NOT NULL DEFAULT 1,       -- 手动启停（唯一开关，无自动禁用）
  test_model TEXT NOT NULL DEFAULT '',      -- 连通性测试用的上游模型名
  created_at DATETIME DEFAULT CURRENT_TIMESTAMP
);

-- 模型实体：挂在渠道下，name 为上游真实模型名
CREATE TABLE models (
  id INTEGER PRIMARY KEY AUTOINCREMENT,
  channel_id INTEGER NOT NULL REFERENCES channels(id) ON DELETE CASCADE,
  name TEXT NOT NULL,                       -- 上游模型名，请求时替换 body 的 model 字段
  created_at DATETIME DEFAULT CURRENT_TIMESTAMP,
  UNIQUE(channel_id, name)
);

-- 模型映射（标准名）：请求入口，模型参数在这侧
CREATE TABLE model_mappings (
  id INTEGER PRIMARY KEY AUTOINCREMENT,
  name TEXT NOT NULL UNIQUE,                -- 标准名，如 glm-5.3（大小写敏感精确匹配）
  context_length INTEGER,                   -- 上下文长度，NULL=未知
  max_output_tokens INTEGER,                -- 最大输出，NULL=未知
  created_at DATETIME DEFAULT CURRENT_TIMESTAMP
);

-- 模型映射 ↔ 模型实体（多对多）
CREATE TABLE model_mapping_models (
  mapping_id INTEGER NOT NULL REFERENCES model_mappings(id) ON DELETE CASCADE,
  model_id INTEGER NOT NULL REFERENCES models(id) ON DELETE CASCADE,
  PRIMARY KEY (mapping_id, model_id)
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
  model_canonical TEXT,                     -- 匹配到的标准名
  channel_id INTEGER, channel_name TEXT,    -- 本次尝试的渠道（模型实体所属渠道）
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

- **三级结构**：`channels 1─n models n─n model_mappings`。渠道只与模型实体关联；标准名只与模型实体关联。
- **模型解析**：请求模型名与标准名**大小写敏感精确匹配**，未命中返回 404 类错误。无别名概念。
- **模型选择**：展开标准名绑定的模型实体，JOIN channels 取 `enabled=1` 的渠道，按 channel.priority 从大到小排序，同优先级按模型 id 升序（先创建的先试）。渠道被禁用 → 其模型实体在此查询中被排除（动态推导，不落库）。
- **模型参数在标准名侧**：同一标准名只对外展示一份参数（用户可编辑）；不同渠道同名模型的差异不分别存储。
- **旧库处理**：启动时检测到 legacy 表（`model_aliases`）存在 → DROP `models/model_aliases/channel_models/channels` 四张表重建；tokens、request_logs 保留。

## 5. 核心流程

### 5.1 `POST /v1/chat/completions` 转发主流程

```
1. Bearer 中间件校验 token（不存在/已禁用 → 401）
2. 读取请求体（上限 10MB），解析出 model 和 stream 字段
3. 标准名解析：与 model_mappings.name 大小写敏感精确匹配 → 未命中返回 404 类错误
4. 模型选择：展开该标准名绑定的模型实体，仅保留所属渠道 enabled=1 的，
   按渠道 priority 从大到小、模型 id 升序排序，得到候选模型列表；
   列表为空（未绑定模型 / 绑定模型的渠道全被禁用）→ 503 "no available model"
5. 依次尝试候选模型：
   a. 构造上游请求：URL = 渠道base_url + "/chat/completions"
      Authorization: Bearer <渠道api_key>
      用模型实体的 name 替换请求体里的 model 字段
   b. 发起请求（连接超时 10s；非流式整体超时默认 300s，可配）
   c. 结果分类：
      - 2xx                     → 成功：回传客户端，记日志
      - 网络错误/超时/5xx/429   → 可重试失败：记日志，试下一模型
      - 其他 4xx                → 不可重试：记日志，原样透传错误给客户端，停止
6. 所有候选模型耗尽 → 返回 OpenAI 风格 503 错误（消息含各模型失败原因）
```

### 5.2 SSE 流式转发（stream: true）

```
- 上游请求带 Accept: text/event-stream
- 首字节超时 60s（可配）：超时未收到数据 → 可重试失败，可降级下一模型
- 一旦开始向客户端写字节，无法再降级（客户端已收到响应头）
- 转发循环：上游逐行读 → 立即写客户端并 Flush → 同时累积到缓冲区
- 流结束（[DONE] 或连接关闭）→ 累积的完整 SSE 文本作为 response_body 落日志
- 中途上游断流 → 记 failed 日志（含已收到内容），关闭客户端连接
```

### 5.3 手动启停与模型可用性

```
渠道只有 enabled 一个开关（管理端手动切换）：
- 渠道禁用 → 其下所有模型实体在转发查询中被排除（JOIN channels 时动态推导，
  不落库、无级联更新）；启用 → 自动恢复可用
- 不做模型粒度的独立禁用；不做自动禁用、连续失败统计、冷却恢复
```

### 5.4 日志落盘与清理

```
- 每次上游尝试写一行 request_logs，request_id（UUID）归组同一次客户端请求
- 非流式：拿到响应后即写；流式：流结束后写（response_body 为拼接的完整 SSE 文本）
- 同步写库（SQLite WAL，写很快）；解析 usage 存入 prompt_tokens/completion_tokens
- 清理：启动时 + 每 24h 定时器，删除 created_at 早于 N 天前的日志（默认 30 天，可配）
```

### 5.5 `GET /v1/models`

返回「至少绑定了一个可用模型实体」的标准名列表，OpenAI 格式并带 Kimi 风格扩展字段（NULL 时省略）：

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
   → 返回 [{name, context_length?, max_output_tokens?, exists}]
     exists = 该上游模型是否已存在为本渠道下的模型实体
2. 前端弹窗：按 exists 分「可增加」（默认勾选）/「已有」（纯展示）两组
3. 确认后 POST /api/channels/{id}/models {names: [...]} 批量创建模型实体
   （同渠道重名幂等跳过）
4. 模型实体创建后还需在「模型映射」页绑定到标准名，才能被客户端请求到
5. 上游不支持/失败 → 返回错误提示，可改为在「模型」页手动创建
```

模型的增删改在「模型」页（模型实体 CRUD）；标准名与模型的绑定、模型参数在「模型映射」页维护。

### 5.7 渠道连通性测试（渠道页「测试」按钮）

```
- 渠道配置的 test_model：从该渠道的模型实体名中选一个（用户选便宜的）
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

GET    /api/channels         渠道列表（无绑定/健康状态字段）
POST   /api/channels         {name, type, base_url, api_key, priority, test_model, enabled}
PUT    /api/channels/{id}    同上，整体更新
DELETE /api/channels/{id}    旗下模型实体级联删除
POST   /api/channels/{id}/toggle        {enabled: true|false}
POST   /api/channels/{id}/fetch-models  拉取上游模型列表（含 exists 标记，不落库）
POST   /api/channels/{id}/models        {names: [...]} 批量创建模型实体（幂等）
POST   /api/channels/{id}/test          连通性测试（用 test_model 发 ping）

GET    /api/models           模型实体列表（含渠道名、绑定的标准名列表）
POST   /api/models           {channel_id, name}（同渠道重名幂等）
PUT    /api/models/{id}      {name} 改名
DELETE /api/models/{id}      绑定关系级联清理

GET    /api/mappings         模型映射列表（含 bound_models: [{id, name, channel_name}]）
POST   /api/mappings         {name, context_length?, max_output_tokens?, model_ids: [...]}
PUT    /api/mappings/{id}    整体更新（含绑定全量替换）
DELETE /api/mappings/{id}    绑定关系级联清理

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
-request-timeout         非流式整体超时秒数，默认 300
-connect-timeout         上游连接超时秒数，默认 10
-stream-first-byte-timeout  流式首字节超时秒数，默认 60
```

## 7. Web 界面

登录页 + 单页 SPA 五个标签页：

- **渠道**：渠道列表（名称、地址、优先级、模型数、状态、启停）、新建/编辑（连接信息 + priority + test_model）、获取模型弹窗（勾选创建模型实体）、测试弹窗
- **模型**：模型实体列表（名称、所属渠道、绑定标准名）、新建（选渠道 + 名称）/改名/删除
- **模型映射**：标准名列表（标准名、context_length、max_output_tokens、绑定模型数）、新建/编辑（参数 + 按渠道分组的模型多选绑定）/删除
- **Token**：token 列表、新建（完整值仅此一次显示）、启停、删除
- **日志**：筛选（模型/状态/token/渠道）+ 分页列表；详情展开完整 request/response 原文与同 request_id 降级链路

## 8. 错误处理

- 请求路径禁止 panic：顶层 recover 中间件兜底，panic → 500 + 记日志；转发循环内所有错误显式处理。
- 上游 4xx（除 429）原样透传客户端，不重试、不降级。
- 客户端错误一律 OpenAI 风格 `{"error":{...}}`；管理 API 用 `{"error":"..."}` + 合适状态码。
- SQLite 开 WAL；日志落盘为同步短写，靠 SQLite 串行化，不自建锁。
- 管理端 session：内存 map + 过期时间，cookie SameSite=Lax；密码用 constant-time 比较；session token 用 crypto/rand。

## 9. 测试策略

单测集中关键路径，`httptest.Server` 做假上游，SQLite 用 `:memory:`：

| 模块 | 测试点 |
|---|---|
| 标准名解析 | 精确匹配命中、大小写敏感（大小写不同 404）、未绑定模型 503 |
| 模型选择 | 渠道优先级排序、跳过禁用渠道、模型 id 升序 |
| 转发+重试 | 首模型 5xx 降级成功；4xx 透传不降级；全部失败 503；模型实体名替换 |
| 绑定关系 | 多对多绑定、全量替换、级联清理（删渠道/模型/标准名） |
| SSE | 逐 chunk 透传+flush、完整内容拼接落日志、首字节超时降级、中途断流 |
| 日志 | 每尝试一行、request_id 归组、usage 解析 |
| 管理 API | 登录/session、CRUD 冒烟、fetch-models exists 标记、批量创建幂等、test 连通性 |
| Provider | OpenAI 实现用假上游验证请求构造与响应解析 |

验证命令：`go test ./...`，转发路径加 `-race` 跑一遍（注：Windows 宿主机 race runtime 加载失败时 `-race` 不可用，属环境问题）。

## 10. 交付物

- `go build` 可产出的完整单二进制代码
- 简短 README（构建、启动参数、基本使用）
- `AGENTS.md`：面向 AI agent 的项目文档（架构概述、目录结构、关键约定、开发/测试命令）。本项目由 agent 负责维护，代码或约定变更时必须同步更新该文件
