# AGENTS.md — peng_api

面向 AI agent 的项目说明。**修改代码、架构或约定后必须同步更新本文件**；详细实现以代码为准，不再另维护重复的设计文档。

## 项目是什么

自用 LLM API 分发网关：多上游渠道 → 统一 OpenAI 格式 `/v1` 出口。Go 单二进制 + SQLite，按优先级分发、失败降级，记录完整请求日志。

## 常用命令

- 构建：`go build -o peng_api .`
- 前端构建：`cd web && npm run build`（`web/dist/` 不进 git，go build 前先构建；线上打包用 `npm ci && npm run build`；`npm run dev` 起 Vite 开发服务器，`/api` 代理到 :8080）
- 测试：`go test ./...`（转发路径改动后加 `-race` 跑一遍）
- 运行：`./peng_api`（配置见 `.env`，可复制 `.env.example`；也可用 `-admin-password` 等 flag 或 `PENG_*` 环境变量）
- 静态检查：`go vet ./...`

## 目录结构

- `main.go` — 装配入口（config→store→provider→engine→admin→web→server）
- `internal/config` — flag/env/.env 配置解析（优先级：flag > 环境变量 > .env > 默认值）
- `internal/store` — SQLite 访问层（channels/models/mappings/tokens/logs 按资源分文件；models 是渠道下的模型实体，mappings 是标准名模型映射）；`migrate.go` 维护静态补列清单（`schema_test.go` 校验与 DDL 一致），`errors.go` 定义领域错误，`logs.go` 用 `logColumns` 作为唯一列清单
- `internal/relay` — 转发引擎：`handler.go`（/v1 入口）、`request.go`（请求体唯一载体）、`engine.go`（重试循环）、`stream.go`（SSE）、`util.go`（工具）
- `internal/relay/provider` — 上游协议抽象；新增协议（如 anthropic）在此实现 `Provider` 接口并注册进 `main.go` 的 Registry
- `internal/auth` — Bearer 中间件 + 管理端 session
- `internal/httpx` — 两类 HTTP 响应与内部错误的唯一实现
- `internal/admin` — 管理 API（`handler.go` 是组合根，按资源持有最小依赖的子 handler；`helpers.go` 提供 id/渠道解析）
- `web/` — Vue3 SPA（Vite 工程：Vue 3 SFC + Ant Design Vue 4 按需引入）；`views/` 放页面，`components/` 放各页拆出的弹窗组件；`state.js`/`selectors.js`/`resources.js` 分别是状态、选择器、加载逻辑，`store.js` 只做 re-export；`npm run build` 产出 `dist/` 并由 `embed.go` 经 `//go:embed all:dist` 打进二进制；`dist/` 不提交进 git（本地/线上构建时生成），`node_modules/` 忽略；非 API 未命中路径回退 `index.html`

## 关键约定（改了会破坏东西的地方）

- **HTTP 入口边界**：`/v1/*` 是 Bearer token 鉴权的客户端 API，`/api/*` 是 session 鉴权的管理 API（login 除外），`/` 是静态 SPA
- **CGO 必须为 0**：SQLite 只用 modernc.org/sqlite，不引入任何 CGO 依赖
- **请求路径禁止 panic**：错误显式处理；chi Recoverer 兜底
- **时间戳一律 Go 端写 UTC**（time.Now().UTC()），不依赖 DB DEFAULT；扫描 DATETIME 列进 time.Time
- **sql.DB 必须 SetMaxOpenConns(1)**（单写者 + :memory: 测试安全）
- **旧库迁移**：检测到 `model_aliases` 时 DROP 重建 `models/model_aliases/channel_models/channels`，保留 tokens/request_logs；新增列要同时改 `schema` DDL 与 `addedColumns` 静态表（由 `TestAddedColumnsMatchSchema` 强制校验一致），再经 `ensureColumn` 幂等补齐
- **数据模型是三级结构**：`channels 1─n models(模型实体) n─n model_mappings(标准名)`；渠道只与模型实体关联，标准名只与模型实体关联；context_length/max_output_tokens/parameter_overrides 参数放在标准名侧
- **绑定关系可双向编辑**：映射页 `PUT /api/mappings/{id}` 的 `model_ids` 按标准名维度全量替换；模型页 `PUT /api/models/{id}` 的 `mapping_ids`（`*[]int64`，nil=不动绑定、非 nil 含空数组=全量替换）按模型维度全量替换，`GET /api/models` 返回 `mapping_ids` 供回显。改名与绑定替换在同一事务内，失败整体回滚（无效 id 返回 400、模型不存在返回 404）。`POST /api/models` 创建是幂等的（重名返回已有实体），**不接受** `mapping_ids`，避免误改已有模型的绑定
- **强制参数覆盖**：`model_mappings.parameter_overrides`（JSON 文本列，旧库 ensureColumn 补列）存标准名侧强制覆盖参数（如 temperature 固定为 1）；转发时覆盖客户端同名字段，对所有降级候选生效，model 字段除外（始终被替换）
- **渠道协议与额度类型分离**：`channels.type` 是转发协议，保存渠道时用 `Registry.Has` 校验，未知协议直接 400，不推迟到转发时才降级成 503；`channels.quota_type` 是额度查询类型（由 `admin/quotaSources` 注册，当前 openrouter/kimi/deepseek）。额度通过 `GET /api/channels/{id}/quota` 实时查询，不落库；可用类型经 `GET /api/quota-types` 下发给前端下拉；实现见 `internal/admin/quotas.go`
- **请求匹配**：客户端 model 与标准名大小写敏感精确匹配（无别名），未命中返回 404 → 展开绑定的模型实体，按渠道 priority DESC、模型 id ASC 逐个尝试；无可用模型返回 503
- **管理端绑定模型列表**：`bound_models` 顺序须与 `SelectModels` 的真实调用顺序对齐（`c.enabled DESC, c.priority DESC, m.id ASC`），禁用渠道模型带 `channel_enabled=false`/`channel_priority` 排在末尾但**不可剔除**（编辑映射的 `model_ids` 由该列表生成并全量替换，剔除会导致保存时误删绑定）
- **模型可用性 = 渠道 enabled 动态推导**（JOIN channels 查询时过滤），不落库；无渠道自动禁用、无连续失败统计，只有手动启停
- **`GET /v1/models`**：只返回至少绑定一个可用模型实体的标准名，OpenAI 格式附带标准名侧参数（NULL 省略）
- **发往上游的 model 字段总是被替换**为模型实体的 name（上游真实模型名）
- **上游 4xx（除 429）不重试**，原样透传；5xx/429/网络错误/超时才降级到下一模型
- **流式**：写出响应头后不再降级；客户端断开只记日志；日志 response_body 存拼接后的完整 SSE 文本
- **日志按上游尝试记录**：每次尝试一行，request_id（UUID）归组同次客户端请求；启动时及每 24h 清理过期日志
- **日志模型三列**：`model_requested`（客户端原始名）/`model_canonical`（命中标准名）/`model_actual`（本次尝试发往上游的模型实体名，由 engine 写入 cand.ModelName）；`model_actual` 对旧库用 ensureColumn 幂等补列，历史日志为空串，模型模糊筛选同时匹配三列；日志页模型列主行显示标准名、下方用 `.ch-remark` 灰色小字显示实际名
- **DB 写日志用脱离请求 ctx 的 detached ctx（5s 超时）**，防止客户端断开丢日志
- **日志 `agent` 字段**记录下游调用方：`X-Client-Name` 请求头优先，缺省回落 `User-Agent`
- 客户端错误格式 `{"error":{"message","type","code"}}`；管理 API `{"error":"..."}`
- 管理端 session 在内存中，重启失效是预期行为；Token 响应统一用 `tokenView`（列表 `maskedTokenView` 打码，创建与详情 `fullTokenView` 给完整值）
- 新增/修改管理 API 时同步改 `web/src/` 下对应页面（views/ 与 components/）、`api.js`，以及 `resources.js`（加载）/ `selectors.js`（派生数据）；`store.js` 只做 re-export
- **上游协议扩展**：实现 `provider.Provider` 接口（Chat/ListModels），在 main.go 注册；对下游永远暴露 OpenAI 格式

## 测试约定

- store 测试用 `:memory:`；relay/provider 测试用 httptest.Server 假上游
- 新功能先写失败测试再实现（TDD）

## 代码结构约定

- **HTTP 响应与内部错误只有一处实现** `internal/httpx`：`JSON` / `Error`（管理端）/ `OpenAIError`（`/v1`）/ `DecodeAdminJSON` / `InternalError` / `InternalOpenAIError`；内部错误统一记日志、不回显细节，admin 不再有委托别名
- **store 错误类型化且按错误码分类**：`ErrNotFound` / `ErrConflict` / `ErrInvalidReference`；`classifyConstraintErr` 用 `errors.As` 取 `*sqlite.Error.Code()`（`SQLITE_CONSTRAINT_UNIQUE` / `FOREIGNKEY`），不看错误文本；上层用 `errors.Is` 决定 400/404/500
- **配置/启动期 fail-fast**：`envOrInt` 非法数值返回 error；`web/index.html` 缺失 panic；`rand.Read` 错误上抛；`channelInput.validate` 用 `Registry.Has` 挡住未知协议
- **重试循环职责分离**：`dispatch` 只被 `run` / `tryCandidate` 看见；辅助函数（`transportFailure` / `writeSuccess` / `httpFailure`）返回 `attemptResult{done, failure}`，不接触全知上下文；`finishStream` 只回填 entry，日志统一由 `tryCandidate` 的 defer 落盘（流式与非流式同一条路径）
- **协议层不认识 DB 实体**：`Provider` 只收 `Endpoint{BaseURL, APIKey}`，`Chat/ListModels` 返回 `(Result, error)`，2xx 走 `Result.OK()`；`Chat` 拆成 `chatNonStream` / `chatStream` 两条路径
- **请求体只解析一次且 stream 单一真相源**：`requestBody` 只存 `model` 与 `fields`，`isStream()` 从 fields 读；`applyOverrides` 改 map、`bytesFor` 只替换 model，保证上游 body 与 provider 的 Stream 开关一致
- **store 单一列清单**：`logColumns` 派生 INSERT / SELECT / Scan 目标（`logValues` / `logTargets`），`schema_test.go` 校验对齐；补列用静态 `addedColumns` 表 + 一致性测试，不在生产代码里解析 DDL；`replaceBindings` 双向复用；`CreateMapping` / `UpdateMapping` 收 `MappingInput` 配置对象（调用点只写非零字段）
- **转发候选是精简视图**：`Candidate` 用 `ChannelRef`（ID/Name/Type/BaseURL/APIKey），不带仅测试需要的字段；`/v1/models` 用 `PublicModel`
- **admin 按依赖差异分组**：`Handler` 只是组合根，只拆 `authAPI`（session）/ `dataAPI`（纯 CRUD）/ `channelAPI`（+ providers）/ `quotaAPI`（+ quotaService）四类，共用一个 `storeAPI`；`storeAPI.loadChannel` 返回 `(渠道, bool)`；分页归一化只在 `LogFilter.Normalize`
- **admin DTO 与状态**：`channelInput.toChannel` 是输入→实体的唯一转换；`tokenView` 是 Token 唯一响应形状；`quotaService.urlFor` 默认取 `quotaSources`，测试用 `setURL` 只登记覆盖项
- **额度类型单一注册点** `admin/quotaSources`：白名单、fetch 分发、`GET /api/quota-types` 下拉从此派生
- **main 只做装配**：日志清理调度抽到 `startLogCleaner(ctx, st, retentionDays)`；超时/周期提为命名常量
- **前端分层**：`state.js`（reactive 状态）/ `selectors.js`（派生数据）/ `resources.js`（`ensureLoaded` 幂等加载），`store.js` 只 re-export；`api.js` 不持有全局回调；`resources.js` 的 `reportError` 负责错误反馈与 401 登出，`guard` 返回 fn 结果让调用方感知失败，`loadInitial` 用 `loaded` 标记返回就绪状态；各页弹窗都在 `components/`；日志格式化集中在 `utils.js`
