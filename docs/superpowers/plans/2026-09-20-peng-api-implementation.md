# peng_api 实现计划

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** 从零实现一个自用 LLM API 分发网关：统一 OpenAI 格式入口、多渠道优先级分发与失败降级、模型别名聚合、完整请求日志、Web 管理界面。

**Architecture:** Go 单二进制 + SQLite（modernc.org/sqlite 纯 Go 驱动）。HTTP 分三股：`/v1/*`（Bearer 鉴权的客户端 API）、`/api/*`（session 鉴权的管理 API）、`/`（embed 嵌入的 Vue SPA）。转发引擎与上游协议解耦（Provider 接口，本期实现 openai）。

**Tech Stack:** Go 1.22+、chi、modernc.org/sqlite、embed.FS、Vue3（vendor 进二进制，无构建步骤）。

**Spec:** `docs/superpowers/specs/2026-09-20-peng-api-design.md`

## Global Constraints

- Go 1.22+；CGO 禁用（纯 Go SQLite 驱动）；最终可 `go build` 出单二进制
- 路由用 `github.com/go-chi/chi/v5`；SQLite 驱动用 `modernc.org/sqlite`（database/sql 接口）
- 请求路径禁止 panic：chi `middleware.Recoverer` 兜底 + 转发循环内错误显式处理
- 所有时间戳由 Go 端显式写入（`time.Now().UTC()`），不依赖 DB DEFAULT；DATETIME 列扫描进 `time.Time`
- SQLite 开 WAL；`*sql.DB` 必须 `SetMaxOpenConns(1)`（单写者 + 内存库测试安全）
- 渠道状态更新用事务内原子自增，禁止读-改-写
- 客户端 API 错误格式：`{"error":{"message":"...","type":"...","code":"..."}}`；管理 API 错误格式：`{"error":"..."}`
- 测试：`httptest.Server` 做假上游；SQLite 用 `:memory:`；关键路径（转发、SSE、重试）必须有单测
- 功能范围严格限定在 spec 第 1 节「做」的清单内，不添加多用户/计费/通知等功能
- 每个任务完成后 `go build ./...` 必须通过；按任务 git commit（项目初始无 git，Task 1 先 `git init`；如不想用 git 则跳过所有 Commit 步骤）

## File Structure

```
peng_api/
├── go.mod                              # module pengapi
├── main.go                             # 装配：config→store→provider→engine→admin→web→server
├── main_test.go                        # 端到端集成冒烟测试
├── internal/
│   ├── config/config.go                # flag/env 解析 → Config
│   ├── store/
│   │   ├── store.go                    # Open/Close、PRAGMA、schema 迁移
│   │   ├── tokens.go                   # token CRUD + FindTokenByValue
│   │   ├── models.go                   # 标准模型/别名 CRUD、ResolveModel、ListModelsWithEnabledChannels
│   │   ├── channels.go                 # 渠道 CRUD、SelectChannels、RecordFailure/Success、Cooldown、BindModels
│   │   ├── logs.go                     # InsertLog、ListLogs、GetLog、GetLogGroup、DeleteLogsBefore
│   │   └── store_test.go               # store 全部单测
│   ├── relay/
│   │   ├── provider/
│   │   │   ├── provider.go             # Provider 接口、Registry、ChatRequest/Result/UpstreamModel
│   │   │   ├── openai.go               # OpenAI Provider：Chat（流式/非流式）、ListModels
│   │   │   └── provider_test.go
│   │   ├── handler.go                  # Engine、ChatCompletions、Models（/v1 入口）
│   │   ├── engine.go                   # 尝试循环、结果分类、失败计数、日志落盘
│   │   ├── stream.go                   # SSE 透传 + 累积
│   │   ├── util.go                     # writeOpenAIError、replaceModel、newUUID、parseUsage(SSE)
│   │   └── relay_test.go
│   ├── auth/
│   │   ├── bearer.go                   # Bearer 中间件 + TokenFrom
│   │   ├── session.go                  # SessionStore（内存）+ RequireSession 中间件
│   │   └── auth_test.go
│   ├── admin/
│   │   ├── handler.go                  # Handler、RegisterRoutes、JSON 辅助函数
│   │   ├── auth.go                     # login/logout/me
│   │   ├── models.go                   # 模型 CRUD API
│   │   ├── tokens.go                   # token CRUD API
│   │   ├── channels.go                 # 渠道 CRUD + toggle/reset/fetch-models/models/test
│   │   ├── logs.go                     # 日志列表/详情 API
│   │   └── admin_test.go
│   └── ...
├── web/
│   ├── embed.go                        # //go:embed + RegisterRoutes
│   ├── index.html                      # SPA 标记（登录 + 四个标签页）
│   ├── app.js                          # Vue 逻辑
│   ├── style.css
│   └── vendor/vue.global.prod.js       # Vue3 生产构建（curl 下载 vendor）
├── README.md
├── AGENTS.md
└── docs/superpowers/{specs,plans}/
```

> 说明：spec 第 7 节只列了三个标签页，但 token 只能管理端创建，没有 UI 会形成死角，故 SPA 增加「Token」标签页（第四个 tab）。

---

### Task 1: 项目骨架与配置解析

**Files:**
- Create: `go.mod`
- Create: `internal/config/config.go`
- Create: `internal/config/config_test.go`
- Create: `main.go`

**Interfaces:**
- Produces（后续所有任务依赖）:
  ```go
  package config

  type Config struct {
      Addr                   string        // -addr，默认 ":8080"
      DBPath                 string        // -db，默认 "./peng.db"
      AdminPassword          string        // -admin-password 或 PENG_ADMIN_PASSWORD，必填
      LogRetentionDays       int           // -log-retention-days，默认 30
      FailThreshold          int           // -fail-threshold，默认 3
      RequestTimeout         time.Duration // -request-timeout（秒），默认 300s
      ConnectTimeout         time.Duration // -connect-timeout（秒），默认 10s
      StreamFirstByteTimeout time.Duration // -stream-first-byte-timeout（秒），默认 60s
  }

  func Parse(args []string) (Config, error) // 缺管理密码返回 error
  ```

- [ ] **Step 1: git init + go mod init**

```bash
cd /d/self_codes/peng_api
git init
go mod init pengapi
```

`.gitignore` 写入：

```
peng_api
peng_api.exe
peng.db
peng.db-*
/tmp/
```

- [ ] **Step 2: 写失败测试 `internal/config/config_test.go`**

```go
package config

import (
	"testing"
	"time"
)

func TestParseDefaults(t *testing.T) {
	cfg, err := Parse([]string{"-admin-password", "secret"})
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}
	if cfg.Addr != ":8080" || cfg.DBPath != "./peng.db" {
		t.Errorf("unexpected defaults: %+v", cfg)
	}
	if cfg.LogRetentionDays != 30 || cfg.FailThreshold != 3 {
		t.Errorf("unexpected numeric defaults: %+v", cfg)
	}
	if cfg.RequestTimeout != 300*time.Second || cfg.ConnectTimeout != 10*time.Second || cfg.StreamFirstByteTimeout != 60*time.Second {
		t.Errorf("unexpected timeouts: %+v", cfg)
	}
}

func TestParseMissingPassword(t *testing.T) {
	if _, err := Parse([]string{}); err == nil {
		t.Fatal("expected error when admin password missing")
	}
}

func TestParsePasswordFromEnv(t *testing.T) {
	t.Setenv("PENG_ADMIN_PASSWORD", "envpass")
	cfg, err := Parse(nil)
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}
	if cfg.AdminPassword != "envpass" {
		t.Errorf("AdminPassword = %q", cfg.AdminPassword)
	}
}

func TestParseFlagOverridesEnv(t *testing.T) {
	t.Setenv("PENG_ADMIN_PASSWORD", "envpass")
	cfg, err := Parse([]string{"-admin-password", "flagpass", "-addr", ":9090", "-request-timeout", "60"})
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}
	if cfg.AdminPassword != "flagpass" || cfg.Addr != ":9090" || cfg.RequestTimeout != 60*time.Second {
		t.Errorf("unexpected: %+v", cfg)
	}
}
```

- [ ] **Step 3: 运行测试确认失败**

Run: `go test ./internal/config/`
Expected: FAIL（`config.go` 不存在，编译错误）

- [ ] **Step 4: 实现 `internal/config/config.go`**

```go
package config

import (
	"flag"
	"fmt"
	"os"
	"time"
)

type Config struct {
	Addr                   string
	DBPath                 string
	AdminPassword          string
	LogRetentionDays       int
	FailThreshold          int
	RequestTimeout         time.Duration
	ConnectTimeout         time.Duration
	StreamFirstByteTimeout time.Duration
}

func Parse(args []string) (Config, error) {
	fs := flag.NewFlagSet("peng_api", flag.ContinueOnError)
	var (
		addr         = fs.String("addr", ":8080", "listen address")
		dbPath       = fs.String("db", "./peng.db", "sqlite file path")
		password     = fs.String("admin-password", "", "admin password (or PENG_ADMIN_PASSWORD)")
		retention    = fs.Int("log-retention-days", 30, "days to keep request logs")
		threshold    = fs.Int("fail-threshold", 3, "consecutive failures before auto-disable")
		reqTimeout   = fs.Int("request-timeout", 300, "non-stream upstream timeout in seconds")
		connTimeout  = fs.Int("connect-timeout", 10, "upstream connect timeout in seconds")
		firstByte    = fs.Int("stream-first-byte-timeout", 60, "stream first-byte timeout in seconds")
	)
	if err := fs.Parse(args); err != nil {
		return Config{}, err
	}
	cfg := Config{
		Addr:                   *addr,
		DBPath:                 *dbPath,
		AdminPassword:          *password,
		LogRetentionDays:       *retention,
		FailThreshold:          *threshold,
		RequestTimeout:         time.Duration(*reqTimeout) * time.Second,
		ConnectTimeout:         time.Duration(*connTimeout) * time.Second,
		StreamFirstByteTimeout: time.Duration(*firstByte) * time.Second,
	}
	if cfg.AdminPassword == "" {
		cfg.AdminPassword = os.Getenv("PENG_ADMIN_PASSWORD")
	}
	if cfg.AdminPassword == "" {
		return Config{}, fmt.Errorf("admin password required: use -admin-password or PENG_ADMIN_PASSWORD")
	}
	return cfg, nil
}
```

`main.go`（占位，Task 19 装配时替换）：

```go
package main

import (
	"fmt"
	"os"

	"pengapi/internal/config"
)

func main() {
	cfg, err := config.Parse(os.Args[1:])
	if err != nil {
		fmt.Fprintln(os.Stderr, "config:", err)
		os.Exit(1)
	}
	fmt.Println("peng_api starting on", cfg.Addr)
}
```

- [ ] **Step 5: 运行测试确认通过**

Run: `go test ./internal/config/ && go build ./...`
Expected: PASS，build 成功

- [ ] **Step 6: Commit**

```bash
git add -A
git commit -m "feat(config): flag/env parsing with required admin password"
```

---

### Task 2: store 打开与 schema 迁移

**Files:**
- Create: `internal/store/store.go`
- Create: `internal/store/store_test.go`

**Interfaces:**
- Consumes: 无（第一个持久层任务）
- Produces:
  ```go
  package store

  type Store struct{ db *sql.DB }

  func Open(path string) (*Store, error) // path 可为 ":memory:"；建库+PRAGMA+迁移
  func (s *Store) Close() error
  ```

- [ ] **Step 1: 添加 SQLite 驱动依赖**

```bash
go get modernc.org/sqlite@latest
```

注意：首次 `go test`/`go build` 会编译 SQLite 的 Go 转译源码，耗时 1-3 分钟属正常。若 latest 要求的 Go 版本高于本机，`go list -m -versions modernc.org/sqlite` 选一个兼容版本，例如 `go get modernc.org/sqlite@v1.34.5`。

- [ ] **Step 2: 写失败测试 `internal/store/store_test.go`**

```go
package store

import (
	"testing"
)

func openTest(t *testing.T) *Store {
	t.Helper()
	s, err := Open(":memory:")
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	t.Cleanup(func() { s.Close() })
	return s
}

func TestOpenCreatesTables(t *testing.T) {
	s := openTest(t)
	for _, table := range []string{"models", "model_aliases", "channels", "channel_models", "tokens", "request_logs"} {
		var name string
		err := s.db.QueryRow(`SELECT name FROM sqlite_master WHERE type='table' AND name=?`, table).Scan(&name)
		if err != nil {
			t.Errorf("table %s missing: %v", table, err)
		}
	}
}

func TestOpenIdempotent(t *testing.T) {
	s := openTest(t) // 第一次
	s.Close()
	dir := t.TempDir()
	s1, err := Open(dir + "/test.db")
	if err != nil {
		t.Fatalf("Open 1: %v", err)
	}
	s1.Close()
	s2, err := Open(dir + "/test.db") // 第二次打开同一文件，迁移须幂等
	if err != nil {
		t.Fatalf("Open 2: %v", err)
	}
	s2.Close()
}
```

- [ ] **Step 3: 运行测试确认失败**

Run: `go test ./internal/store/`
Expected: FAIL（`store.go` 不存在）

- [ ] **Step 4: 实现 `internal/store/store.go`**

```go
package store

import (
	"database/sql"

	_ "modernc.org/sqlite"
)

type Store struct{ db *sql.DB }

const schema = `
CREATE TABLE IF NOT EXISTS models (
  id INTEGER PRIMARY KEY AUTOINCREMENT,
  name TEXT NOT NULL UNIQUE,
  context_length INTEGER,
  max_output_tokens INTEGER,
  created_at DATETIME DEFAULT CURRENT_TIMESTAMP
);
CREATE TABLE IF NOT EXISTS model_aliases (
  id INTEGER PRIMARY KEY AUTOINCREMENT,
  alias TEXT NOT NULL UNIQUE,
  model_id INTEGER NOT NULL REFERENCES models(id) ON DELETE CASCADE
);
CREATE TABLE IF NOT EXISTS channels (
  id INTEGER PRIMARY KEY AUTOINCREMENT,
  name TEXT NOT NULL,
  type TEXT NOT NULL DEFAULT 'openai',
  base_url TEXT NOT NULL,
  api_key TEXT NOT NULL,
  priority INTEGER NOT NULL DEFAULT 0,
  enabled INTEGER NOT NULL DEFAULT 1,
  test_model TEXT NOT NULL DEFAULT '',
  auto_disabled INTEGER NOT NULL DEFAULT 0,
  consecutive_failures INTEGER NOT NULL DEFAULT 0,
  disabled_until DATETIME,
  created_at DATETIME DEFAULT CURRENT_TIMESTAMP
);
CREATE TABLE IF NOT EXISTS channel_models (
  channel_id INTEGER NOT NULL REFERENCES channels(id) ON DELETE CASCADE,
  model_id INTEGER NOT NULL REFERENCES models(id) ON DELETE CASCADE,
  upstream_model TEXT NOT NULL DEFAULT '',
  PRIMARY KEY (channel_id, model_id)
);
CREATE TABLE IF NOT EXISTS tokens (
  id INTEGER PRIMARY KEY AUTOINCREMENT,
  name TEXT NOT NULL,
  token TEXT NOT NULL UNIQUE,
  enabled INTEGER NOT NULL DEFAULT 1,
  created_at DATETIME DEFAULT CURRENT_TIMESTAMP
);
CREATE TABLE IF NOT EXISTS request_logs (
  id INTEGER PRIMARY KEY AUTOINCREMENT,
  request_id TEXT NOT NULL,
  attempt INTEGER NOT NULL,
  token_id INTEGER,
  token_name TEXT NOT NULL DEFAULT '',
  model_requested TEXT NOT NULL,
  model_canonical TEXT NOT NULL DEFAULT '',
  channel_id INTEGER,
  channel_name TEXT NOT NULL DEFAULT '',
  stream INTEGER NOT NULL DEFAULT 0,
  status TEXT NOT NULL,
  http_status INTEGER,
  error TEXT NOT NULL DEFAULT '',
  request_body TEXT NOT NULL DEFAULT '',
  response_body TEXT NOT NULL DEFAULT '',
  prompt_tokens INTEGER,
  completion_tokens INTEGER,
  latency_ms INTEGER NOT NULL DEFAULT 0,
  created_at DATETIME DEFAULT CURRENT_TIMESTAMP
);
CREATE INDEX IF NOT EXISTS idx_logs_created ON request_logs(created_at DESC);
CREATE INDEX IF NOT EXISTS idx_logs_request ON request_logs(request_id);
`

func Open(path string) (*Store, error) {
	db, err := sql.Open("sqlite", path)
	if err != nil {
		return nil, err
	}
	// 单连接：SQLite 单写者；同时保证 :memory: 测试库不被连接池分裂
	db.SetMaxOpenConns(1)
	for _, pragma := range []string{
		"PRAGMA journal_mode=WAL",
		"PRAGMA foreign_keys=ON",
		"PRAGMA busy_timeout=5000",
	} {
		if _, err := db.Exec(pragma); err != nil {
			db.Close()
			return nil, err
		}
	}
	if _, err := db.Exec(schema); err != nil {
		db.Close()
		return nil, err
	}
	return &Store{db: db}, nil
}

func (s *Store) Close() error { return s.db.Close() }
```

- [ ] **Step 5: 运行测试确认通过**

Run: `go test ./internal/store/`
Expected: PASS

- [ ] **Step 6: Commit**

```bash
git add -A
git commit -m "feat(store): open sqlite with WAL and idempotent schema migration"
```

---

### Task 3: token 存取

**Files:**
- Create: `internal/store/tokens.go`
- Modify: `internal/store/store_test.go`（追加 token 测试）

**Interfaces:**
- Consumes: `store.Store`（Task 2）
- Produces:
  ```go
  type Token struct {
      ID        int64
      Name      string
      Token     string // "sk-" + 48 位十六进制
      Enabled   bool
      CreatedAt time.Time
  }

  func (s *Store) CreateToken(ctx context.Context, name string) (*Token, error)
  func (s *Store) ListTokens(ctx context.Context) ([]Token, error)
  func (s *Store) UpdateToken(ctx context.Context, id int64, name string, enabled bool) error
  func (s *Store) DeleteToken(ctx context.Context, id int64) error
  // 找到且 enabled 才返回；未找到/已禁用返回 (nil, nil)
  func (s *Store) FindTokenByValue(ctx context.Context, value string) (*Token, error)
  ```

- [ ] **Step 1: 写失败测试（追加到 `internal/store/store_test.go`）**

```go
func TestTokenCRUD(t *testing.T) {
	s := openTest(t)
	ctx := context.Background()

	tok, err := s.CreateToken(ctx, "cline")
	if err != nil {
		t.Fatalf("CreateToken: %v", err)
	}
	if !strings.HasPrefix(tok.Token, "sk-") || len(tok.Token) != 51 {
		t.Errorf("unexpected token format: %q", tok.Token)
	}

	list, err := s.ListTokens(ctx)
	if err != nil || len(list) != 1 {
		t.Fatalf("ListTokens: %v len=%d", err, len(list))
	}

	found, err := s.FindTokenByValue(ctx, tok.Token)
	if err != nil || found == nil || found.Name != "cline" {
		t.Fatalf("FindTokenByValue: %v %+v", err, found)
	}

	// 禁用后应查不到
	if err := s.UpdateToken(ctx, tok.ID, "cline2", false); err != nil {
		t.Fatalf("UpdateToken: %v", err)
	}
	found, err = s.FindTokenByValue(ctx, tok.Token)
	if err != nil || found != nil {
		t.Fatalf("disabled token should not be found: %v %+v", err, found)
	}

	// 不存在的值
	found, err = s.FindTokenByValue(ctx, "sk-nonexistent")
	if err != nil || found != nil {
		t.Fatalf("expected nil token: %v %+v", err, found)
	}

	if err := s.DeleteToken(ctx, tok.ID); err != nil {
		t.Fatalf("DeleteToken: %v", err)
	}
	list, _ = s.ListTokens(ctx)
	if len(list) != 0 {
		t.Fatalf("expected empty list, got %d", len(list))
	}
}

func TestTokenValueUnique(t *testing.T) {
	s := openTest(t)
	ctx := context.Background()
	a, _ := s.CreateToken(ctx, "a")
	b, _ := s.CreateToken(ctx, "b")
	if a.Token == b.Token {
		t.Fatal("tokens must be unique random values")
	}
}
```

- [ ] **Step 2: 运行测试确认失败**

Run: `go test ./internal/store/`
Expected: FAIL（CreateToken 未定义）

- [ ] **Step 3: 实现 `internal/store/tokens.go`**

```go
package store

import (
	"context"
	"crypto/rand"
	"database/sql"
	"encoding/hex"
	"time"
)

type Token struct {
	ID        int64     `json:"id"`
	Name      string    `json:"name"`
	Token     string    `json:"token"`
	Enabled   bool      `json:"enabled"`
	CreatedAt time.Time `json:"created_at"`
}

func newRandomKey() (string, error) {
	b := make([]byte, 24)
	if _, err := rand.Read(b); err != nil {
		return "", err
	}
	return "sk-" + hex.EncodeToString(b), nil
}

func (s *Store) CreateToken(ctx context.Context, name string) (*Token, error) {
	value, err := newRandomKey()
	if err != nil {
		return nil, err
	}
	now := time.Now().UTC()
	res, err := s.db.ExecContext(ctx,
		`INSERT INTO tokens (name, token, enabled, created_at) VALUES (?, ?, 1, ?)`,
		name, value, now)
	if err != nil {
		return nil, err
	}
	id, _ := res.LastInsertId()
	return &Token{ID: id, Name: name, Token: value, Enabled: true, CreatedAt: now}, nil
}

func (s *Store) ListTokens(ctx context.Context) ([]Token, error) {
	rows, err := s.db.QueryContext(ctx,
		`SELECT id, name, token, enabled, created_at FROM tokens ORDER BY id`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []Token
	for rows.Next() {
		var t Token
		if err := rows.Scan(&t.ID, &t.Name, &t.Token, &t.Enabled, &t.CreatedAt); err != nil {
			return nil, err
		}
		out = append(out, t)
	}
	return out, rows.Err()
}

func (s *Store) UpdateToken(ctx context.Context, id int64, name string, enabled bool) error {
	_, err := s.db.ExecContext(ctx,
		`UPDATE tokens SET name=?, enabled=? WHERE id=?`, name, enabled, id)
	return err
}

func (s *Store) DeleteToken(ctx context.Context, id int64) error {
	_, err := s.db.ExecContext(ctx, `DELETE FROM tokens WHERE id=?`, id)
	return err
}

func (s *Store) FindTokenByValue(ctx context.Context, value string) (*Token, error) {
	var t Token
	err := s.db.QueryRowContext(ctx,
		`SELECT id, name, token, enabled, created_at FROM tokens WHERE token=? AND enabled=1`,
		value).Scan(&t.ID, &t.Name, &t.Token, &t.Enabled, &t.CreatedAt)
	if err == sql.ErrNoRows {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	return &t, nil
}
```

- [ ] **Step 4: 运行测试确认通过**

Run: `go test ./internal/store/`
Expected: PASS

- [ ] **Step 5: Commit**

```bash
git add -A
git commit -m "feat(store): token CRUD with random sk- keys"
```

---

### Task 4: 标准模型与别名存取、模型解析

**Files:**
- Create: `internal/store/models.go`
- Modify: `internal/store/store_test.go`（追加模型测试）

**Interfaces:**
- Consumes: `store.Store`（Task 2）
- Produces:
  ```go
  type Model struct {
      ID              int64
      Name            string
      ContextLength   *int64  // nil = 未知
      MaxOutputTokens *int64
      Aliases         []string
      BoundChannels   int     // 仅 ListModels 填充
      CreatedAt       time.Time
  }

  func (s *Store) CreateModel(ctx context.Context, name string, aliases []string, contextLength, maxOutputTokens *int64) (*Model, error)
  func (s *Store) UpdateModel(ctx context.Context, id int64, name string, aliases []string, contextLength, maxOutputTokens *int64) error // 别名整体替换
  func (s *Store) DeleteModel(ctx context.Context, id int64) error
  func (s *Store) ListModels(ctx context.Context) ([]Model, error)
  // 先按标准名（大小写不敏感）匹配，再按别名匹配；未命中返回 (nil, nil)
  func (s *Store) ResolveModel(ctx context.Context, name string) (*Model, error)
  // 至少绑定了一个 enabled 渠道的标准模型（供 /v1/models）
  func (s *Store) ListModelsWithEnabledChannels(ctx context.Context) ([]Model, error)
  ```

- [ ] **Step 1: 写失败测试（追加到 `internal/store/store_test.go`）**

```go
func ptrI64(v int64) *int64 { return &v }

func TestModelCRUDAndResolve(t *testing.T) {
	s := openTest(t)
	ctx := context.Background()

	m, err := s.CreateModel(ctx, "glm-5.3", []string{"GLM-5.3", "glm5.3"}, ptrI64(131072), ptrI64(8192))
	if err != nil {
		t.Fatalf("CreateModel: %v", err)
	}

	// 标准名命中（大小写不敏感）
	got, err := s.ResolveModel(ctx, "GLM-5.3")
	if err != nil || got == nil || got.ID != m.ID {
		t.Fatalf("resolve by alias failed: %v %+v", err, got)
	}
	// 别名命中
	got, err = s.ResolveModel(ctx, "Glm5.3")
	if err != nil || got == nil || got.ID != m.ID {
		t.Fatalf("resolve by alias case-insensitive failed: %v %+v", err, got)
	}
	// 标准名本身也要命中
	got, err = s.ResolveModel(ctx, "gLM-5.3")
	if err != nil || got == nil || got.ID != m.ID {
		t.Fatalf("resolve by canonical name failed: %v %+v", err, got)
	}
	// 未命中
	got, err = s.ResolveModel(ctx, "no-such-model")
	if err != nil || got != nil {
		t.Fatalf("expected nil for unknown model: %v %+v", err, got)
	}

	// 列表含别名
	list, err := s.ListModels(ctx)
	if err != nil || len(list) != 1 {
		t.Fatalf("ListModels: %v", err)
	}
	if len(list[0].Aliases) != 2 || list[0].ContextLength == nil || *list[0].ContextLength != 131072 {
		t.Errorf("unexpected model: %+v", list[0])
	}

	// 整体替换别名
	if err := s.UpdateModel(ctx, m.ID, "glm-5.3", []string{"glm53"}, nil, nil); err != nil {
		t.Fatalf("UpdateModel: %v", err)
	}
	got, _ = s.ResolveModel(ctx, "glm5.3")
	if got != nil {
		t.Error("old alias should be gone after replace")
	}
	got, _ = s.ResolveModel(ctx, "GLM53")
	if got == nil {
		t.Error("new alias should resolve")
	}

	if err := s.DeleteModel(ctx, m.ID); err != nil {
		t.Fatalf("DeleteModel: %v", err)
	}
	got, _ = s.ResolveModel(ctx, "glm53")
	if got != nil {
		t.Error("alias should cascade-delete with model")
	}
}

func TestAliasUniqueAcrossModels(t *testing.T) {
	s := openTest(t)
	ctx := context.Background()
	if _, err := s.CreateModel(ctx, "a-model", []string{"shared"}, nil, nil); err != nil {
		t.Fatalf("CreateModel: %v", err)
	}
	if _, err := s.CreateModel(ctx, "b-model", []string{"shared"}, nil, nil); err == nil {
		t.Fatal("duplicate alias must fail (UNIQUE constraint)")
	}
}

func TestListModelsWithEnabledChannels(t *testing.T) {
	s := openTest(t)
	ctx := context.Background()
	withCh, _ := s.CreateModel(ctx, "has-channel", nil, nil, nil)
	without, _ := s.CreateModel(ctx, "no-channel", nil, nil, nil)

	ch, err := s.CreateChannel(ctx, Channel{Name: "c1", Type: "openai", BaseURL: "http://x", APIKey: "k"})
	if err != nil {
		t.Fatalf("CreateChannel: %v", err)
	}
	if err := s.BindModels(ctx, ch.ID, []BindingInput{{UpstreamModel: "has-channel", ModelID: &withCh.ID}}); err != nil {
		t.Fatalf("BindModels: %v", err)
	}

	list, err := s.ListModelsWithEnabledChannels(ctx)
	if err != nil || len(list) != 1 || list[0].Name != "has-channel" {
		t.Fatalf("ListModelsWithEnabledChannels: %v %+v", err, list)
	}

	// 渠道禁用后模型不再出现
	if err := s.SetChannelEnabled(ctx, ch.ID, false); err != nil {
		t.Fatalf("SetChannelEnabled: %v", err)
	}
	list, _ = s.ListModelsWithEnabledChannels(ctx)
	if len(list) != 0 {
		t.Fatalf("expected empty, got %+v (model %d should be hidden)", list, without.ID)
	}
}
```

- [ ] **Step 2: 运行测试确认失败**

Run: `go test ./internal/store/`
Expected: FAIL（CreateModel 未定义；注意 `CreateChannel`/`BindModels`/`SetChannelEnabled` 也未定义——先实现 Task 5 的 channels.go 后本测试才能编译，或按顺序先跳过此测试）

> **执行顺序提示**：`TestListModelsWithEnabledChannels` 依赖 Task 5 的渠道函数。实现时可在本任务先注释掉该测试，Task 5 完成后取消注释；或直接连做 Task 5 后一起跑。推荐后者。

- [ ] **Step 3: 实现 `internal/store/models.go`**

```go
package store

import (
	"context"
	"database/sql"
	"time"
)

type Model struct {
	ID              int64     `json:"id"`
	Name            string    `json:"name"`
	ContextLength   *int64    `json:"context_length"`
	MaxOutputTokens *int64    `json:"max_output_tokens"`
	Aliases         []string  `json:"aliases"`
	BoundChannels   int       `json:"bound_channels"`
	CreatedAt       time.Time `json:"created_at"`
}

func (s *Store) CreateModel(ctx context.Context, name string, aliases []string, contextLength, maxOutputTokens *int64) (*Model, error) {
	now := time.Now().UTC()
	res, err := s.db.ExecContext(ctx,
		`INSERT INTO models (name, context_length, max_output_tokens, created_at) VALUES (?, ?, ?, ?)`,
		name, contextLength, maxOutputTokens, now)
	if err != nil {
		return nil, err
	}
	id, _ := res.LastInsertId()
	if err := s.replaceAliases(ctx, id, aliases); err != nil {
		return nil, err
	}
	return &Model{ID: id, Name: name, ContextLength: contextLength, MaxOutputTokens: maxOutputTokens, Aliases: aliases, CreatedAt: now}, nil
}

func (s *Store) UpdateModel(ctx context.Context, id int64, name string, aliases []string, contextLength, maxOutputTokens *int64) error {
	if _, err := s.db.ExecContext(ctx,
		`UPDATE models SET name=?, context_length=?, max_output_tokens=? WHERE id=?`,
		name, contextLength, maxOutputTokens, id); err != nil {
		return err
	}
	return s.replaceAliases(ctx, id, aliases)
}

func (s *Store) replaceAliases(ctx context.Context, modelID int64, aliases []string) error {
	if _, err := s.db.ExecContext(ctx, `DELETE FROM model_aliases WHERE model_id=?`, modelID); err != nil {
		return err
	}
	for _, a := range aliases {
		if a == "" {
			continue
		}
		if _, err := s.db.ExecContext(ctx, `INSERT INTO model_aliases (alias, model_id) VALUES (?, ?)`, a, modelID); err != nil {
			return err
		}
	}
	return nil
}

func (s *Store) DeleteModel(ctx context.Context, id int64) error {
	_, err := s.db.ExecContext(ctx, `DELETE FROM models WHERE id=?`, id)
	return err // 别名、渠道绑定由外键 ON DELETE CASCADE 清理
}

func (s *Store) ListModels(ctx context.Context) ([]Model, error) {
	rows, err := s.db.QueryContext(ctx, `
		SELECT m.id, m.name, m.context_length, m.max_output_tokens, m.created_at,
		       (SELECT COUNT(*) FROM channel_models cm WHERE cm.model_id = m.id) AS bound
		FROM models m ORDER BY m.name`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []Model
	for rows.Next() {
		var m Model
		var cl, mo sql.NullInt64
		if err := rows.Scan(&m.ID, &m.Name, &cl, &mo, &m.CreatedAt, &m.BoundChannels); err != nil {
			return nil, err
		}
		m.ContextLength = nullI64(cl)
		m.MaxOutputTokens = nullI64(mo)
		out = append(out, m)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	for i := range out {
		aliases, err := s.aliasesOf(ctx, out[i].ID)
		if err != nil {
			return nil, err
		}
		out[i].Aliases = aliases
	}
	return out, nil
}

func (s *Store) aliasesOf(ctx context.Context, modelID int64) ([]string, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT alias FROM model_aliases WHERE model_id=? ORDER BY alias`, modelID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []string
	for rows.Next() {
		var a string
		if err := rows.Scan(&a); err != nil {
			return nil, err
		}
		out = append(out, a)
	}
	return out, rows.Err()
}

func (s *Store) ResolveModel(ctx context.Context, name string) (*Model, error) {
	const q = `
		SELECT id, name, context_length, max_output_tokens, created_at FROM models
		WHERE LOWER(name) = LOWER(?)
		UNION
		SELECT m.id, m.name, m.context_length, m.max_output_tokens, m.created_at
		FROM models m JOIN model_aliases a ON a.model_id = m.id
		WHERE LOWER(a.alias) = LOWER(?)
		LIMIT 1`
	var m Model
	var cl, mo sql.NullInt64
	err := s.db.QueryRowContext(ctx, q, name, name).Scan(&m.ID, &m.Name, &cl, &mo, &m.CreatedAt)
	if err == sql.ErrNoRows {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	m.ContextLength = nullI64(cl)
	m.MaxOutputTokens = nullI64(mo)
	return &m, nil
}

func (s *Store) ListModelsWithEnabledChannels(ctx context.Context) ([]Model, error) {
	rows, err := s.db.QueryContext(ctx, `
		SELECT DISTINCT m.id, m.name, m.context_length, m.max_output_tokens, m.created_at
		FROM models m
		JOIN channel_models cm ON cm.model_id = m.id
		JOIN channels c ON c.id = cm.channel_id
		WHERE c.enabled = 1
		ORDER BY m.name`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []Model
	for rows.Next() {
		var m Model
		var cl, mo sql.NullInt64
		if err := rows.Scan(&m.ID, &m.Name, &cl, &mo, &m.CreatedAt); err != nil {
			return nil, err
		}
		m.ContextLength = nullI64(cl)
		m.MaxOutputTokens = nullI64(mo)
		out = append(out, m)
	}
	return out, rows.Err()
}

func nullI64(v sql.NullInt64) *int64 {
	if !v.Valid {
		return nil
	}
	return &v.Int64
}
```

- [ ] **Step 4: 运行测试确认通过**

Run: `go test ./internal/store/ -run 'TestModel|TestAlias'`
Expected: `TestModelCRUDAndResolve`、`TestAliasUniqueAcrossModels` PASS；`TestListModelsWithEnabledChannels` 编译失败属预期（依赖 Task 5）

- [ ] **Step 5: Commit**

```bash
git add -A
git commit -m "feat(store): canonical models, aliases, case-insensitive resolution"
```

---

### Task 5: 渠道存取、选择、失败计数与冷却

**Files:**
- Create: `internal/store/channels.go`
- Modify: `internal/store/store_test.go`（追加渠道测试）

**Interfaces:**
- Consumes: `store.Store`（Task 2）、`store.Model`（Task 4）
- Produces:
  ```go
  type ChannelModelBinding struct {
      ModelID       int64
      ModelName     string // 仅查询展示用
      UpstreamModel string
  }

  type Channel struct {
      ID                  int64
      Name                string
      Type                string // 本期固定 "openai"
      BaseURL             string
      APIKey              string
      Priority            int
      Enabled             bool
      TestModel           string
      AutoDisabled        bool
      ConsecutiveFailures int
      DisabledUntil       *time.Time
      Models              []ChannelModelBinding
      CreatedAt           time.Time
  }

  func (s *Store) CreateChannel(ctx context.Context, ch Channel) (*Channel, error)
  func (s *Store) UpdateChannel(ctx context.Context, ch Channel) error // 整体替换绑定，保留失败状态
  func (s *Store) DeleteChannel(ctx context.Context, id int64) error
  func (s *Store) GetChannel(ctx context.Context, id int64) (*Channel, error)
  func (s *Store) ListChannels(ctx context.Context) ([]Channel, error)
  func (s *Store) SetChannelEnabled(ctx context.Context, id int64, enabled bool) error
  func (s *Store) ResetChannelState(ctx context.Context, id int64) error

  type Candidate struct {
      Channel       Channel
      UpstreamModel string
  }

  // 选择可用渠道：enabled 且未自动禁用；先惰性恢复冷却过期的渠道；priority 降序、id 升序
  func (s *Store) SelectChannels(ctx context.Context, modelID int64) ([]Candidate, error)

  // 可重试失败计数；达阈值自动禁用并按指数退避设冷却
  func (s *Store) RecordFailure(ctx context.Context, channelID int64, threshold int) error
  func (s *Store) RecordSuccess(ctx context.Context, channelID int64) error

  // 冷却时长：5min × 2^(failures-threshold)，封顶 1h
  func Cooldown(failures, threshold int) time.Duration

  type BindingInput struct {
      UpstreamModel  string
      ModelID        *int64 // 绑定已有标准模型；与 NewModelName 二选一
      NewModelName   string // 非空则新建标准模型（并把 upstream 名加为别名）
      ContextLength  *int64 // 新建或已有模型该字段为空时自动填充
      MaxOutputTokens *int64
  }

  func (s *Store) BindModels(ctx context.Context, channelID int64, bindings []BindingInput) error
  ```

- [ ] **Step 1: 写失败测试（追加到 `internal/store/store_test.go`）**

```go
func TestChannelCRUD(t *testing.T) {
	s := openTest(t)
	ctx := context.Background()
	m, _ := s.CreateModel(ctx, "glm-5.3", nil, nil, nil)

	ch, err := s.CreateChannel(ctx, Channel{
		Name: "zhipu", Type: "openai", BaseURL: "https://api.bigmodel.cn/coding/paas/v4",
		APIKey: "key1", Priority: 10, TestModel: "glm-5.3-free",
		Models: []ChannelModelBinding{{ModelID: m.ID, UpstreamModel: "glm-5.3-free"}},
	})
	if err != nil {
		t.Fatalf("CreateChannel: %v", err)
	}

	got, err := s.GetChannel(ctx, ch.ID)
	if err != nil || got == nil {
		t.Fatalf("GetChannel: %v", err)
	}
	if got.Name != "zhipu" || got.Priority != 10 || !got.Enabled || got.AutoDisabled {
		t.Errorf("unexpected channel: %+v", got)
	}
	if len(got.Models) != 1 || got.Models[0].UpstreamModel != "glm-5.3-free" || got.Models[0].ModelName != "glm-5.3" {
		t.Errorf("unexpected bindings: %+v", got.Models)
	}

	// 整体更新：换绑定，失败状态保留
	s.RecordFailure(ctx, ch.ID, 3)
	got2, _ := s.GetChannel(ctx, ch.ID)
	if err := s.UpdateChannel(ctx, Channel{
		ID: ch.ID, Name: "zhipu2", Type: "openai", BaseURL: got.BaseURL, APIKey: "key2",
		Priority: 5, Enabled: true, TestModel: "m2",
		Models: []ChannelModelBinding{{ModelID: m.ID, UpstreamModel: "m2"}},
	}); err != nil {
		t.Fatalf("UpdateChannel: %v", err)
	}
	got3, _ := s.GetChannel(ctx, ch.ID)
	if got3.Name != "zhipu2" || got3.APIKey != "key2" || got3.ConsecutiveFailures != 1 {
		t.Errorf("update lost state or fields: %+v", got3)
	}
	if len(got3.Models) != 1 || got3.Models[0].UpstreamModel != "m2" {
		t.Errorf("bindings not replaced: %+v", got3.Models)
	}

	if err := s.DeleteChannel(ctx, ch.ID); err != nil {
		t.Fatalf("DeleteChannel: %v", err)
	}
	got4, _ := s.GetChannel(ctx, ch.ID)
	if got4 != nil {
		t.Error("channel should be deleted")
	}
}

func TestSelectChannelsOrderAndLazyRecovery(t *testing.T) {
	s := openTest(t)
	ctx := context.Background()
	m, _ := s.CreateModel(ctx, "glm-5.3", nil, nil, nil)

	low, _ := s.CreateChannel(ctx, Channel{Name: "low", Type: "openai", BaseURL: "http://low", APIKey: "k", Priority: 1})
	high, _ := s.CreateChannel(ctx, Channel{Name: "high", Type: "openai", BaseURL: "http://high", APIKey: "k", Priority: 10})
	off, _ := s.CreateChannel(ctx, Channel{Name: "off", Type: "openai", BaseURL: "http://off", APIKey: "k", Priority: 100})
	s.BindModels(ctx, low.ID, []BindingInput{{UpstreamModel: "glm-low", ModelID: &m.ID}})
	s.BindModels(ctx, high.ID, []BindingInput{{UpstreamModel: "glm-high", ModelID: &m.ID}})
	s.BindModels(ctx, off.ID, []BindingInput{{UpstreamModel: "glm-off", ModelID: &m.ID}})
	s.SetChannelEnabled(ctx, off.ID, false)

	cands, err := s.SelectChannels(ctx, m.ID)
	if err != nil || len(cands) != 2 {
		t.Fatalf("SelectChannels: %v len=%d", err, len(cands))
	}
	if cands[0].Channel.Name != "high" || cands[1].Channel.Name != "low" {
		t.Errorf("priority order wrong: %v, %v", cands[0].Channel.Name, cands[1].Channel.Name)
	}
	if cands[0].UpstreamModel != "glm-high" {
		t.Errorf("upstream model not carried: %+v", cands[0])
	}

	// high 连续失败达阈值 → 自动禁用，选择时跳过
	for i := 0; i < 3; i++ {
		if err := s.RecordFailure(ctx, high.ID, 3); err != nil {
			t.Fatalf("RecordFailure: %v", err)
		}
	}
	cands, _ = s.SelectChannels(ctx, m.ID)
	if len(cands) != 1 || cands[0].Channel.Name != "low" {
		t.Fatalf("auto-disabled channel must be skipped: %+v", cands)
	}
	st, _ := s.GetChannel(ctx, high.ID)
	if !st.AutoDisabled || st.DisabledUntil == nil {
		t.Errorf("channel should be auto-disabled with cooldown: %+v", st)
	}

	// 冷却过期 → 惰性恢复
	past := time.Now().UTC().Add(-time.Minute)
	s.db.ExecContext(ctx, `UPDATE channels SET disabled_until=? WHERE id=?`, past, high.ID)
	cands, _ = s.SelectChannels(ctx, m.ID)
	if len(cands) != 2 {
		t.Fatalf("expired cooldown should lazily recover: %+v", cands)
	}
	st, _ = s.GetChannel(ctx, high.ID)
	if st.AutoDisabled {
		t.Error("auto_disabled flag should be cleared")
	}
}

func TestRecordSuccessResets(t *testing.T) {
	s := openTest(t)
	ctx := context.Background()
	ch, _ := s.CreateChannel(ctx, Channel{Name: "c", Type: "openai", BaseURL: "http://x", APIKey: "k"})
	s.RecordFailure(ctx, ch.ID, 3)
	s.RecordFailure(ctx, ch.ID, 3)
	if err := s.RecordSuccess(ctx, ch.ID); err != nil {
		t.Fatalf("RecordSuccess: %v", err)
	}
	got, _ := s.GetChannel(ctx, ch.ID)
	if got.ConsecutiveFailures != 0 || got.AutoDisabled || got.DisabledUntil != nil {
		t.Errorf("success must reset failure state: %+v", got)
	}
}

func TestCooldownBackoff(t *testing.T) {
	// threshold=3：第 3 次失败冷却 5min，第 4 次 10min，第 5 次 20min，封顶 60min
	cases := []struct {
		failures, threshold int
		want                time.Duration
	}{
		{3, 3, 5 * time.Minute},
		{4, 3, 10 * time.Minute},
		{5, 3, 20 * time.Minute},
		{10, 3, time.Hour}, // 封顶
		{1, 3, 5 * time.Minute},
	}
	for _, c := range cases {
		if got := Cooldown(c.failures, c.threshold); got != c.want {
			t.Errorf("Cooldown(%d,%d) = %v, want %v", c.failures, c.threshold, got, c.want)
		}
	}
}

func TestBindModelsNewModelAutoAliasAndFill(t *testing.T) {
	s := openTest(t)
	ctx := context.Background()
	ch, _ := s.CreateChannel(ctx, Channel{Name: "c", Type: "openai", BaseURL: "http://x", APIKey: "k"})

	// NewModelName：自动建标准模型 + 自身为别名 + 填充参数
	err := s.BindModels(ctx, ch.ID, []BindingInput{{
		UpstreamModel: "glm-5.3-x", NewModelName: "glm-5.3",
		ContextLength: ptrI64(128000),
	}})
	if err != nil {
		t.Fatalf("BindModels: %v", err)
	}
	m, err := s.ResolveModel(ctx, "GLM-5.3-X") // 别名大小写不敏感命中
	if err != nil || m == nil || m.Name != "glm-5.3" {
		t.Fatalf("auto alias failed: %v %+v", err, m)
	}
	if m.ContextLength == nil || *m.ContextLength != 128000 {
		t.Errorf("context_length not filled: %+v", m)
	}

	// 重复绑定同一模型 → 更新 upstream_model（幂等）
	err = s.BindModels(ctx, ch.ID, []BindingInput{{UpstreamModel: "glm-5.3-y", ModelID: &m.ID}})
	if err != nil {
		t.Fatalf("re-BindModels: %v", err)
	}
	got, _ := s.GetChannel(ctx, ch.ID)
	if len(got.Models) != 1 || got.Models[0].UpstreamModel != "glm-5.3-y" {
		t.Errorf("binding should upsert: %+v", got.Models)
	}
}
```

- [ ] **Step 2: 运行测试确认失败**

Run: `go test ./internal/store/`
Expected: FAIL（Channel/BindModels 等未定义）

- [ ] **Step 3: 实现 `internal/store/channels.go`**

```go
package store

import (
	"context"
	"database/sql"
	"time"
)

type ChannelModelBinding struct {
	ModelID       int64  `json:"model_id"`
	ModelName     string `json:"model_name"`
	UpstreamModel string `json:"upstream_model"`
}

type Channel struct {
	ID                  int64                 `json:"id"`
	Name                string                `json:"name"`
	Type                string                `json:"type"`
	BaseURL             string                `json:"base_url"`
	APIKey              string                `json:"api_key"`
	Priority            int                   `json:"priority"`
	Enabled             bool                  `json:"enabled"`
	TestModel           string                `json:"test_model"`
	AutoDisabled        bool                  `json:"auto_disabled"`
	ConsecutiveFailures int                   `json:"consecutive_failures"`
	DisabledUntil       *time.Time            `json:"disabled_until"`
	Models              []ChannelModelBinding `json:"models"`
	CreatedAt           time.Time             `json:"created_at"`
}

func (s *Store) CreateChannel(ctx context.Context, ch Channel) (*Channel, error) {
	if ch.Type == "" {
		ch.Type = "openai"
	}
	now := time.Now().UTC()
	res, err := s.db.ExecContext(ctx, `
		INSERT INTO channels (name, type, base_url, api_key, priority, enabled, test_model, created_at)
		VALUES (?, ?, ?, ?, ?, 1, ?, ?)`,
		ch.Name, ch.Type, ch.BaseURL, ch.APIKey, ch.Priority, ch.TestModel, now)
	if err != nil {
		return nil, err
	}
	ch.ID, _ = res.LastInsertId()
	ch.Enabled = true
	ch.CreatedAt = now
	if err := s.replaceBindings(ctx, ch.ID, ch.Models); err != nil {
		return nil, err
	}
	return &ch, nil
}

func (s *Store) UpdateChannel(ctx context.Context, ch Channel) error {
	if ch.Type == "" {
		ch.Type = "openai"
	}
	// 不动 consecutive_failures / auto_disabled / disabled_until
	if _, err := s.db.ExecContext(ctx, `
		UPDATE channels SET name=?, type=?, base_url=?, api_key=?, priority=?, enabled=?, test_model=?
		WHERE id=?`,
		ch.Name, ch.Type, ch.BaseURL, ch.APIKey, ch.Priority, ch.Enabled, ch.TestModel, ch.ID); err != nil {
		return err
	}
	return s.replaceBindings(ctx, ch.ID, ch.Models)
}

func (s *Store) replaceBindings(ctx context.Context, channelID int64, models []ChannelModelBinding) error {
	if _, err := s.db.ExecContext(ctx, `DELETE FROM channel_models WHERE channel_id=?`, channelID); err != nil {
		return err
	}
	for _, b := range models {
		if _, err := s.db.ExecContext(ctx,
			`INSERT INTO channel_models (channel_id, model_id, upstream_model) VALUES (?, ?, ?)`,
			channelID, b.ModelID, b.UpstreamModel); err != nil {
			return err
		}
	}
	return nil
}

func (s *Store) DeleteChannel(ctx context.Context, id int64) error {
	_, err := s.db.ExecContext(ctx, `DELETE FROM channels WHERE id=?`, id)
	return err
}

func (s *Store) GetChannel(ctx context.Context, id int64) (*Channel, error) {
	const q = `SELECT id, name, type, base_url, api_key, priority, enabled, test_model,
		auto_disabled, consecutive_failures, disabled_until, created_at FROM channels WHERE id=?`
	var c Channel
	var du sql.NullTime
	err := s.db.QueryRowContext(ctx, q, id).Scan(&c.ID, &c.Name, &c.Type, &c.BaseURL, &c.APIKey,
		&c.Priority, &c.Enabled, &c.TestModel, &c.AutoDisabled, &c.ConsecutiveFailures, &du, &c.CreatedAt)
	if err == sql.ErrNoRows {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	if du.Valid {
		c.DisabledUntil = &du.Time
	}
	models, err := s.bindingsOf(ctx, c.ID)
	if err != nil {
		return nil, err
	}
	c.Models = models
	return &c, nil
}

func (s *Store) ListChannels(ctx context.Context) ([]Channel, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT id, name, type, base_url, api_key, priority, enabled, test_model,
		auto_disabled, consecutive_failures, disabled_until, created_at FROM channels ORDER BY id`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []Channel
	for rows.Next() {
		var c Channel
		var du sql.NullTime
		if err := rows.Scan(&c.ID, &c.Name, &c.Type, &c.BaseURL, &c.APIKey, &c.Priority,
			&c.Enabled, &c.TestModel, &c.AutoDisabled, &c.ConsecutiveFailures, &du, &c.CreatedAt); err != nil {
			return nil, err
		}
		if du.Valid {
			c.DisabledUntil = &du.Time
		}
		out = append(out, c)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	for i := range out {
		models, err := s.bindingsOf(ctx, out[i].ID)
		if err != nil {
			return nil, err
		}
		out[i].Models = models
	}
	return out, nil
}

func (s *Store) bindingsOf(ctx context.Context, channelID int64) ([]ChannelModelBinding, error) {
	rows, err := s.db.QueryContext(ctx, `
		SELECT cm.model_id, m.name, cm.upstream_model
		FROM channel_models cm JOIN models m ON m.id = cm.model_id
		WHERE cm.channel_id=? ORDER BY m.name`, channelID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []ChannelModelBinding{}
	for rows.Next() {
		var b ChannelModelBinding
		if err := rows.Scan(&b.ModelID, &b.ModelName, &b.UpstreamModel); err != nil {
			return nil, err
		}
		out = append(out, b)
	}
	return out, rows.Err()
}

func (s *Store) SetChannelEnabled(ctx context.Context, id int64, enabled bool) error {
	_, err := s.db.ExecContext(ctx, `UPDATE channels SET enabled=? WHERE id=?`, enabled, id)
	return err
}

func (s *Store) ResetChannelState(ctx context.Context, id int64) error {
	_, err := s.db.ExecContext(ctx,
		`UPDATE channels SET auto_disabled=0, consecutive_failures=0, disabled_until=NULL WHERE id=?`, id)
	return err
}

type Candidate struct {
	Channel       Channel
	UpstreamModel string
}

func (s *Store) SelectChannels(ctx context.Context, modelID int64) ([]Candidate, error) {
	now := time.Now().UTC()
	// 惰性恢复：冷却过期即解除自动禁用
	if _, err := s.db.ExecContext(ctx,
		`UPDATE channels SET auto_disabled=0, disabled_until=NULL
		 WHERE auto_disabled=1 AND disabled_until IS NOT NULL AND disabled_until < ?`, now); err != nil {
		return nil, err
	}
	rows, err := s.db.QueryContext(ctx, `
		SELECT c.id, c.name, c.type, c.base_url, c.api_key, c.priority, c.enabled, c.test_model,
		       c.auto_disabled, c.consecutive_failures, c.created_at, cm.upstream_model
		FROM channels c JOIN channel_models cm ON cm.channel_id = c.id
		WHERE cm.model_id=? AND c.enabled=1 AND c.auto_disabled=0
		ORDER BY c.priority DESC, c.id ASC`, modelID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []Candidate
	for rows.Next() {
		var c Candidate
		if err := rows.Scan(&c.Channel.ID, &c.Channel.Name, &c.Channel.Type, &c.Channel.BaseURL,
			&c.Channel.APIKey, &c.Channel.Priority, &c.Channel.Enabled, &c.Channel.TestModel,
			&c.Channel.AutoDisabled, &c.Channel.ConsecutiveFailures, &c.Channel.CreatedAt,
			&c.UpstreamModel); err != nil {
			return nil, err
		}
		out = append(out, c)
	}
	return out, rows.Err()
}

func (s *Store) RecordFailure(ctx context.Context, channelID int64, threshold int) error {
	// 单连接 + 事务保证计数准确
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	if _, err := tx.ExecContext(ctx,
		`UPDATE channels SET consecutive_failures = consecutive_failures + 1 WHERE id=?`, channelID); err != nil {
		return err
	}
	var failures int
	if err := tx.QueryRowContext(ctx,
		`SELECT consecutive_failures FROM channels WHERE id=?`, channelID).Scan(&failures); err != nil {
		return err
	}
	if failures >= threshold {
		until := time.Now().UTC().Add(Cooldown(failures, threshold))
		if _, err := tx.ExecContext(ctx,
			`UPDATE channels SET auto_disabled=1, disabled_until=? WHERE id=?`, until, channelID); err != nil {
			return err
		}
	}
	return tx.Commit()
}

func (s *Store) RecordSuccess(ctx context.Context, channelID int64) error {
	_, err := s.db.ExecContext(ctx,
		`UPDATE channels SET consecutive_failures=0, auto_disabled=0, disabled_until=NULL WHERE id=?`, channelID)
	return err
}

func Cooldown(failures, threshold int) time.Duration {
	over := failures - threshold
	if over < 0 {
		over = 0
	}
	if over > 7 { // 5min << 7 = 640min，远超封顶
		over = 7
	}
	d := 5 * time.Minute << uint(over)
	if d > time.Hour {
		d = time.Hour
	}
	return d
}

type BindingInput struct {
	UpstreamModel   string `json:"upstream_model"`
	ModelID         *int64 `json:"model_id"`
	NewModelName    string `json:"new_model_name"`
	ContextLength   *int64 `json:"context_length"`
	MaxOutputTokens *int64 `json:"max_output_tokens"`
}

func (s *Store) BindModels(ctx context.Context, channelID int64, bindings []BindingInput) error {
	for _, b := range bindings {
		var modelID int64
		switch {
		case b.ModelID != nil:
			modelID = *b.ModelID
			// 已有模型参数为空且本次带了参数 → 自动填充
			if b.ContextLength != nil || b.MaxOutputTokens != nil {
				if _, err := s.db.ExecContext(ctx, `
					UPDATE models SET
						context_length   = COALESCE(context_length, ?),
						max_output_tokens = COALESCE(max_output_tokens, ?)
					WHERE id=?`, b.ContextLength, b.MaxOutputTokens, modelID); err != nil {
					return err
				}
			}
		case b.NewModelName != "":
			m, err := s.CreateModel(ctx, b.NewModelName, []string{b.UpstreamModel}, b.ContextLength, b.MaxOutputTokens)
			if err != nil {
				return err
			}
			modelID = m.ID
		default:
			continue
		}
		if _, err := s.db.ExecContext(ctx, `
			INSERT INTO channel_models (channel_id, model_id, upstream_model) VALUES (?, ?, ?)
			ON CONFLICT (channel_id, model_id) DO UPDATE SET upstream_model=excluded.upstream_model`,
			channelID, modelID, b.UpstreamModel); err != nil {
			return err
		}
	}
	return nil
}
```

- [ ] **Step 4: 运行测试确认通过**

Run: `go test ./internal/store/`
Expected: PASS（含 Task 4 的 `TestListModelsWithEnabledChannels`）

- [ ] **Step 5: Commit**

```bash
git add -A
git commit -m "feat(store): channels, selection with lazy recovery, failure counting, bindings"
```

---

### Task 6: 请求日志存取

**Files:**
- Create: `internal/store/logs.go`
- Modify: `internal/store/store_test.go`（追加日志测试）

**Interfaces:**
- Consumes: `store.Store`（Task 2）
- Produces:
  ```go
  type LogEntry struct {
      ID               int64
      RequestID        string
      Attempt          int
      TokenID          *int64
      TokenName        string
      ModelRequested   string
      ModelCanonical   string
      ChannelID        *int64
      ChannelName      string
      Stream           bool
      Status           string // "success" / "failed"
      HTTPStatus       *int
      Error            string
      RequestBody      string
      ResponseBody     string
      PromptTokens     *int
      CompletionTokens *int
      LatencyMS        int64
      CreatedAt        time.Time
  }

  func (s *Store) InsertLog(ctx context.Context, e LogEntry) (int64, error)

  type LogFilter struct {
      Model     string // 同时 LIKE 匹配 model_requested / model_canonical
      Status    string // "" = 全部
      TokenID   *int64
      ChannelID *int64
      Page      int    // 从 1 开始
      Size      int    // 默认 20，上限 200
  }

  func (s *Store) ListLogs(ctx context.Context, f LogFilter) ([]LogEntry, int, error) // (rows, total, err)，按 id DESC
  func (s *Store) GetLog(ctx context.Context, id int64) (*LogEntry, error)
  func (s *Store) GetLogGroup(ctx context.Context, requestID string) ([]LogEntry, error) // 按 attempt ASC
  func (s *Store) DeleteLogsBefore(ctx context.Context, cutoff time.Time) (int64, error)
  ```

- [ ] **Step 1: 写失败测试（追加到 `internal/store/store_test.go`）**

```go
func TestLogInsertListGetCleanup(t *testing.T) {
	s := openTest(t)
	ctx := context.Background()

	base := LogEntry{
		RequestID: "req-1", Attempt: 1, TokenName: "cline",
		ModelRequested: "GLM5.3", ModelCanonical: "glm-5.3",
		ChannelName: "zhipu", Status: "failed", Error: "upstream 500",
		RequestBody: `{"model":"GLM5.3"}`, ResponseBody: `{"error":"boom"}`,
		LatencyMS: 120, CreatedAt: time.Now().UTC().Add(-time.Hour),
	}
	id1, err := s.InsertLog(ctx, base)
	if err != nil {
		t.Fatalf("InsertLog: %v", err)
	}
	e2 := base
	e2.Attempt = 2
	e2.Status = "success"
	e2.Error = ""
	e2.HTTPStatus = new(int)
	*e2.HTTPStatus = 200
	e2.PromptTokens = new(int)
	*e2.PromptTokens = 10
	e2.CreatedAt = time.Now().UTC()
	if _, err := s.InsertLog(ctx, e2); err != nil {
		t.Fatalf("InsertLog 2: %v", err)
	}

	// 分页 + 总数
	rows, total, err := s.ListLogs(ctx, LogFilter{Page: 1, Size: 10})
	if err != nil || total != 2 || len(rows) != 2 {
		t.Fatalf("ListLogs: %v total=%d len=%d", err, total, len(rows))
	}
	if rows[0].ID < rows[1].ID {
		t.Error("logs should be id DESC")
	}

	// 状态筛选
	rows, total, _ = s.ListLogs(ctx, LogFilter{Status: "failed", Page: 1, Size: 10})
	if total != 1 || rows[0].Status != "failed" {
		t.Errorf("status filter: total=%d", total)
	}

	// 模型筛选同时匹配 requested / canonical
	_, total, _ = s.ListLogs(ctx, LogFilter{Model: "glm5.3", Page: 1, Size: 10})
	if total != 2 {
		t.Errorf("model filter should match requested: total=%d", total)
	}
	_, total, _ = s.ListLogs(ctx, LogFilter{Model: "glm-5.3", Page: 1, Size: 10})
	if total != 2 {
		t.Errorf("model filter should match canonical: total=%d", total)
	}

	// 详情 + 降级链路归组
	got, err := s.GetLog(ctx, id1)
	if err != nil || got == nil || got.Error != "upstream 500" {
		t.Fatalf("GetLog: %v %+v", err, got)
	}
	group, err := s.GetLogGroup(ctx, "req-1")
	if err != nil || len(group) != 2 || group[0].Attempt != 1 || group[1].Attempt != 2 {
		t.Fatalf("GetLogGroup: %v len=%d", err, len(group))
	}

	// 清理：删 30 天前的 → 两条都还新，一条不删
	deleted, err := s.DeleteLogsBefore(ctx, time.Now().UTC().AddDate(0, 0, -30))
	if err != nil || deleted != 0 {
		t.Fatalf("DeleteLogsBefore: %v deleted=%d", err, deleted)
	}
	deleted, err = s.DeleteLogsBefore(ctx, time.Now().UTC().Add(time.Minute))
	if err != nil || deleted != 2 {
		t.Fatalf("DeleteLogsBefore all: %v deleted=%d", err, deleted)
	}
}
```

- [ ] **Step 2: 运行测试确认失败**

Run: `go test ./internal/store/ -run TestLog`
Expected: FAIL（LogEntry 未定义）

- [ ] **Step 3: 实现 `internal/store/logs.go`**

```go
package store

import (
	"context"
	"database/sql"
	"strings"
	"time"
)

type LogEntry struct {
	ID               int64     `json:"id"`
	RequestID        string    `json:"request_id"`
	Attempt          int       `json:"attempt"`
	TokenID          *int64    `json:"token_id"`
	TokenName        string    `json:"token_name"`
	ModelRequested   string    `json:"model_requested"`
	ModelCanonical   string    `json:"model_canonical"`
	ChannelID        *int64    `json:"channel_id"`
	ChannelName      string    `json:"channel_name"`
	Stream           bool      `json:"stream"`
	Status           string    `json:"status"`
	HTTPStatus       *int      `json:"http_status"`
	Error            string    `json:"error"`
	RequestBody      string    `json:"request_body"`
	ResponseBody     string    `json:"response_body"`
	PromptTokens     *int      `json:"prompt_tokens"`
	CompletionTokens *int      `json:"completion_tokens"`
	LatencyMS        int64     `json:"latency_ms"`
	CreatedAt        time.Time `json:"created_at"`
}

func (s *Store) InsertLog(ctx context.Context, e LogEntry) (int64, error) {
	if e.CreatedAt.IsZero() {
		e.CreatedAt = time.Now().UTC()
	}
	res, err := s.db.ExecContext(ctx, `
		INSERT INTO request_logs
		(request_id, attempt, token_id, token_name, model_requested, model_canonical,
		 channel_id, channel_name, stream, status, http_status, error,
		 request_body, response_body, prompt_tokens, completion_tokens, latency_ms, created_at)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		e.RequestID, e.Attempt, e.TokenID, e.TokenName, e.ModelRequested, e.ModelCanonical,
		e.ChannelID, e.ChannelName, e.Stream, e.Status, e.HTTPStatus, e.Error,
		e.RequestBody, e.ResponseBody, e.PromptTokens, e.CompletionTokens, e.LatencyMS, e.CreatedAt)
	if err != nil {
		return 0, err
	}
	return res.LastInsertId()
}

type LogFilter struct {
	Model     string
	Status    string
	TokenID   *int64
	ChannelID *int64
	Page      int
	Size      int
}

func (f LogFilter) where() (string, []any) {
	var conds []string
	var args []any
	if f.Model != "" {
		like := "%" + f.Model + "%"
		conds = append(conds, "(model_requested LIKE ? OR model_canonical LIKE ?)")
		args = append(args, like, like)
	}
	if f.Status != "" {
		conds = append(conds, "status = ?")
		args = append(args, f.Status)
	}
	if f.TokenID != nil {
		conds = append(conds, "token_id = ?")
		args = append(args, *f.TokenID)
	}
	if f.ChannelID != nil {
		conds = append(conds, "channel_id = ?")
		args = append(args, *f.ChannelID)
	}
	if len(conds) == 0 {
		return "", nil
	}
	return " WHERE " + strings.Join(conds, " AND "), args
}

func (s *Store) ListLogs(ctx context.Context, f LogFilter) ([]LogEntry, int, error) {
	if f.Page < 1 {
		f.Page = 1
	}
	if f.Size < 1 || f.Size > 200 {
		f.Size = 20
	}
	where, args := f.where()
	var total int
	if err := s.db.QueryRowContext(ctx,
		`SELECT COUNT(*) FROM request_logs`+where, args...).Scan(&total); err != nil {
		return nil, 0, err
	}
	q := `SELECT id, request_id, attempt, token_id, token_name, model_requested, model_canonical,
		channel_id, channel_name, stream, status, http_status, error, request_body, response_body,
		prompt_tokens, completion_tokens, latency_ms, created_at
		FROM request_logs` + where + ` ORDER BY id DESC LIMIT ? OFFSET ?`
	rows, err := s.db.QueryContext(ctx, q, append(args, f.Size, (f.Page-1)*f.Size)...)
	if err != nil {
		return nil, 0, err
	}
	defer rows.Close()
	out := []LogEntry{}
	for rows.Next() {
		e, err := scanLog(rows)
		if err != nil {
			return nil, 0, err
		}
		out = append(out, *e)
	}
	return out, total, rows.Err()
}

func (s *Store) GetLog(ctx context.Context, id int64) (*LogEntry, error) {
	row := s.db.QueryRowContext(ctx, `
		SELECT id, request_id, attempt, token_id, token_name, model_requested, model_canonical,
		       channel_id, channel_name, stream, status, http_status, error, request_body, response_body,
		       prompt_tokens, completion_tokens, latency_ms, created_at
		FROM request_logs WHERE id=?`, id)
	e, err := scanLog(row)
	if err == sql.ErrNoRows {
		return nil, nil
	}
	return e, err
}

func (s *Store) GetLogGroup(ctx context.Context, requestID string) ([]LogEntry, error) {
	rows, err := s.db.QueryContext(ctx, `
		SELECT id, request_id, attempt, token_id, token_name, model_requested, model_canonical,
		       channel_id, channel_name, stream, status, http_status, error, request_body, response_body,
		       prompt_tokens, completion_tokens, latency_ms, created_at
		FROM request_logs WHERE request_id=? ORDER BY attempt ASC`, requestID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []LogEntry{}
	for rows.Next() {
		e, err := scanLog(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, *e)
	}
	return out, rows.Err()
}

func (s *Store) DeleteLogsBefore(ctx context.Context, cutoff time.Time) (int64, error) {
	res, err := s.db.ExecContext(ctx, `DELETE FROM request_logs WHERE created_at < ?`, cutoff.UTC())
	if err != nil {
		return 0, err
	}
	return res.RowsAffected()
}

type logScanner interface {
	Scan(dest ...any) error
}

func scanLog(row logScanner) (*LogEntry, error) {
	var e LogEntry
	var tokenID, channelID sql.NullInt64
	var httpStatus, promptTokens, completionTokens sql.NullInt64
	err := row.Scan(&e.ID, &e.RequestID, &e.Attempt, &tokenID, &e.TokenName,
		&e.ModelRequested, &e.ModelCanonical, &channelID, &e.ChannelName, &e.Stream,
		&e.Status, &httpStatus, &e.Error, &e.RequestBody, &e.ResponseBody,
		&promptTokens, &completionTokens, &e.LatencyMS, &e.CreatedAt)
	if err != nil {
		return nil, err
	}
	if tokenID.Valid {
		e.TokenID = &tokenID.Int64
	}
	if channelID.Valid {
		e.ChannelID = &channelID.Int64
	}
	if httpStatus.Valid {
		v := int(httpStatus.Int64)
		e.HTTPStatus = &v
	}
	if promptTokens.Valid {
		v := int(promptTokens.Int64)
		e.PromptTokens = &v
	}
	if completionTokens.Valid {
		v := int(completionTokens.Int64)
		e.CompletionTokens = &v
	}
	return &e, nil
}
```

- [ ] **Step 4: 运行测试确认通过**

Run: `go test ./internal/store/`
Expected: PASS

- [ ] **Step 5: Commit**

```bash
git add -A
git commit -m "feat(store): request log insert, filtered paging, groups, cleanup"
```

---

### Task 7: Provider 接口、注册表与 OpenAI 非流式实现

**Files:**
- Create: `internal/relay/provider/provider.go`
- Create: `internal/relay/provider/openai.go`
- Create: `internal/relay/provider/provider_test.go`

**Interfaces:**
- Consumes: `store.Channel`（Task 5）
- Produces:
  ```go
  package provider

  type ChatRequest struct {
      Model  string // 上游模型名（已完成别名解析和 upstream_model 替换）
      Body   []byte // 完整请求体（model 字段已替换好）
      Stream bool
  }

  type Result struct {
      HTTPStatus int           // 传输层失败时为 0
      Header     http.Header
      Body       []byte        // 非流式：完整响应体；流式非 2xx：错误响应体
      Stream     io.ReadCloser // 仅流式 2xx：上游 SSE 流（Close 会取消底层 ctx）
      Err        error         // 传输层/超时错误（HTTP 层错误不置 Err，置 HTTPStatus）
  }

  type UpstreamModel struct {
      ID              string
      ContextLength   *int64
      MaxOutputTokens *int64
  }

  type Provider interface {
      Name() string // "openai"
      Chat(ctx context.Context, ch store.Channel, req ChatRequest) Result
      ListModels(ctx context.Context, ch store.Channel) ([]UpstreamModel, error)
  }

  var ErrStreamFirstByteTimeout = errors.New("stream first byte timeout")

  type Registry struct{ /* map[string]Provider */ }
  func NewRegistry(providers ...Provider) *Registry
  func (r *Registry) For(typ string) (Provider, bool)

  func NewOpenAI(client *http.Client, requestTimeout, streamFirstByteTimeout time.Duration) *OpenAI
  ```

- [ ] **Step 1: 写失败测试 `internal/relay/provider/provider_test.go`**

```go
package provider

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"pengapi/internal/store"
)

func testClient() *http.Client { return &http.Client{} }

func testChannel(baseURL string) store.Channel {
	return store.Channel{ID: 1, Name: "fake", Type: "openai", BaseURL: baseURL, APIKey: "upkey"}
}

func TestOpenAIChatNonStreamSuccess(t *testing.T) {
	var gotAuth, gotBody string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotAuth = r.Header.Get("Authorization")
		b, _ := io.ReadAll(r.Body)
		gotBody = string(b)
		w.Header().Set("Content-Type", "application/json")
		io.WriteString(w, `{"id":"x","choices":[{"message":{"content":"hi"}}]}`)
	}))
	defer srv.Close()

	p := NewOpenAI(testClient(), 5*time.Second, time.Second)
	res := p.Chat(context.Background(), testChannel(srv.URL), ChatRequest{
		Model: "glm-5.3", Body: []byte(`{"model":"glm-5.3","messages":[]}`),
	})
	if res.Err != nil {
		t.Fatalf("Chat: %v", res.Err)
	}
	if res.HTTPStatus != 200 || !json.Valid(res.Body) {
		t.Errorf("unexpected result: status=%d body=%s", res.HTTPStatus, res.Body)
	}
	if gotAuth != "Bearer upkey" {
		t.Errorf("Authorization = %q", gotAuth)
	}
	if gotBody != `{"model":"glm-5.3","messages":[]}` {
		t.Errorf("body not passed through: %s", gotBody)
	}
}

func TestOpenAIChatNonStreamUpstreamError(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(500)
		io.WriteString(w, `{"error":{"message":"server boom"}}`)
	}))
	defer srv.Close()

	p := NewOpenAI(testClient(), 5*time.Second, time.Second)
	res := p.Chat(context.Background(), testChannel(srv.URL), ChatRequest{Model: "m", Body: []byte(`{}`)})
	if res.Err != nil {
		t.Fatalf("HTTP 500 is not a transport error: %v", res.Err)
	}
	if res.HTTPStatus != 500 || string(res.Body) != `{"error":{"message":"server boom"}}` {
		t.Errorf("status/body not preserved: %d %s", res.HTTPStatus, res.Body)
	}
}

func TestOpenAIChatConnectError(t *testing.T) {
	p := NewOpenAI(testClient(), time.Second, time.Second)
	res := p.Chat(context.Background(), testChannel("http://127.0.0.1:1"), ChatRequest{Model: "m", Body: []byte(`{}`)})
	if res.Err == nil {
		t.Fatal("connection refused must produce Err")
	}
	if res.HTTPStatus != 0 {
		t.Errorf("HTTPStatus should be 0 on transport error, got %d", res.HTTPStatus)
	}
}

func TestOpenAIListModels(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/models" {
			t.Errorf("unexpected path: %s", r.URL.Path)
		}
		if r.Header.Get("Authorization") != "Bearer upkey" {
			t.Error("missing auth header")
		}
		io.WriteString(w, `{"object":"list","data":[
			{"id":"glm-5.3","object":"model"},
			{"id":"kimi-k2","object":"model","context_length":262144,"max_output_tokens":16384}
		]}`)
	}))
	defer srv.Close()

	p := NewOpenAI(testClient(), 5*time.Second, time.Second)
	models, err := p.ListModels(context.Background(), testChannel(srv.URL))
	if err != nil {
		t.Fatalf("ListModels: %v", err)
	}
	if len(models) != 2 || models[0].ID != "glm-5.3" {
		t.Fatalf("unexpected models: %+v", models)
	}
	if models[0].ContextLength != nil {
		t.Errorf("plain openai response should have nil context_length: %+v", models[0])
	}
	if models[1].ContextLength == nil || *models[1].ContextLength != 262144 {
		t.Errorf("kimi-style context_length not parsed: %+v", models[1])
	}
}

func TestOpenAIListModelsFailure(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(401)
	}))
	defer srv.Close()
	p := NewOpenAI(testClient(), 5*time.Second, time.Second)
	_, err := p.ListModels(context.Background(), testChannel(srv.URL))
	if err == nil {
		t.Fatal("401 must return error")
	}
}

func TestRegistry(t *testing.T) {
	reg := NewRegistry(NewOpenAI(testClient(), time.Second, time.Second))
	p, ok := reg.For("openai")
	if !ok || p.Name() != "openai" {
		t.Fatal("openai provider must be registered")
	}
	if _, ok := reg.For("anthropic"); ok {
		t.Fatal("anthropic must not exist yet")
	}
}
```

- [ ] **Step 2: 运行测试确认失败**

Run: `go test ./internal/relay/provider/`
Expected: FAIL（包不存在）

- [ ] **Step 3: 实现 `internal/relay/provider/provider.go`**

```go
package provider

import (
	"context"
	"errors"
	"io"
	"net/http"

	"pengapi/internal/store"
)

type ChatRequest struct {
	Model  string
	Body   []byte
	Stream bool
}

type Result struct {
	HTTPStatus int
	Header     http.Header
	Body       []byte
	Stream     io.ReadCloser
	Err        error
}

type UpstreamModel struct {
	ID              string `json:"id"`
	ContextLength   *int64 `json:"context_length"`
	MaxOutputTokens *int64 `json:"max_output_tokens"`
}

type Provider interface {
	Name() string
	Chat(ctx context.Context, ch store.Channel, req ChatRequest) Result
	ListModels(ctx context.Context, ch store.Channel) ([]UpstreamModel, error)
}

var ErrStreamFirstByteTimeout = errors.New("stream first byte timeout")

type Registry struct {
	m map[string]Provider
}

func NewRegistry(providers ...Provider) *Registry {
	r := &Registry{m: map[string]Provider{}}
	for _, p := range providers {
		r.m[p.Name()] = p
	}
	return r
}

func (r *Registry) For(typ string) (Provider, bool) {
	p, ok := r.m[typ]
	return p, ok
}
```

- [ ] **Step 4: 实现 `internal/relay/provider/openai.go`**

```go
package provider

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"

	"pengapi/internal/store"
)

type OpenAI struct {
	client                 *http.Client
	requestTimeout         time.Duration
	streamFirstByteTimeout time.Duration
}

func NewOpenAI(client *http.Client, requestTimeout, streamFirstByteTimeout time.Duration) *OpenAI {
	return &OpenAI{client: client, requestTimeout: requestTimeout, streamFirstByteTimeout: streamFirstByteTimeout}
}

func (p *OpenAI) Name() string { return "openai" }

func (p *OpenAI) endpoint(ch store.Channel, path string) string {
	return strings.TrimSuffix(ch.BaseURL, "/") + path
}

func (p *OpenAI) Chat(ctx context.Context, ch store.Channel, req ChatRequest) Result {
	r, err := http.NewRequestWithContext(ctx, http.MethodPost,
		p.endpoint(ch, "/chat/completions"), bytes.NewReader(req.Body))
	if err != nil {
		return Result{Err: err}
	}
	r.Header.Set("Content-Type", "application/json")
	r.Header.Set("Authorization", "Bearer "+ch.APIKey)

	if !req.Stream {
		// 非流式：整体超时；body 在超时内读完，随后取消 ctx 无副作用
		tctx, cancel := context.WithTimeout(ctx, p.requestTimeout)
		defer cancel()
		resp, err := p.client.Do(r.WithContext(tctx))
		if err != nil {
			return Result{Err: err}
		}
		defer resp.Body.Close()
		body, err := io.ReadAll(io.LimitReader(resp.Body, 32<<20))
		if err != nil {
			return Result{HTTPStatus: resp.StatusCode, Err: err}
		}
		return Result{HTTPStatus: resp.StatusCode, Header: resp.Header, Body: body}
	}

	// 流式：竞速实现首字节（响应头）超时；超时后取消 in-flight 请求
	r.Header.Set("Accept", "text/event-stream")
	rctx, cancel := context.WithCancel(ctx)
	type outcome struct {
		resp *http.Response
		err  error
	}
	done := make(chan outcome, 1)
	go func() {
		resp, err := p.client.Do(r.WithContext(rctx))
		done <- outcome{resp, err}
	}()
	timer := time.NewTimer(p.streamFirstByteTimeout)
	defer timer.Stop()
	select {
	case o := <-done:
		if o.err != nil {
			cancel()
			return Result{Err: o.err}
		}
		// 流式非 2xx：读掉错误 body，与非流式失败统一处理
		if o.resp.StatusCode < 200 || o.resp.StatusCode >= 300 {
			defer o.resp.Body.Close()
			defer cancel() // 必须在 ReadAll 之后执行，否则大错误 body 被截断
			body, _ := io.ReadAll(io.LimitReader(o.resp.Body, 1<<20))
			return Result{HTTPStatus: o.resp.StatusCode, Header: o.resp.Header, Body: body}
		}
		return Result{HTTPStatus: o.resp.StatusCode, Header: o.resp.Header,
			Stream: cancelReadCloser{ReadCloser: o.resp.Body, cancel: cancel}}
	case <-timer.C:
		cancel()
		return Result{Err: ErrStreamFirstByteTimeout}
	case <-ctx.Done():
		cancel()
		return Result{Err: ctx.Err()}
	}
}

// Close 同时取消请求 ctx，释放底层连接资源
type cancelReadCloser struct {
	io.ReadCloser
	cancel context.CancelFunc
}

func (c cancelReadCloser) Close() error {
	err := c.ReadCloser.Close()
	c.cancel()
	return err
}

func (p *OpenAI) ListModels(ctx context.Context, ch store.Channel) ([]UpstreamModel, error) {
	tctx, cancel := context.WithTimeout(ctx, p.requestTimeout)
	defer cancel()
	r, err := http.NewRequestWithContext(tctx, http.MethodGet, p.endpoint(ch, "/models"), nil)
	if err != nil {
		return nil, err
	}
	r.Header.Set("Authorization", "Bearer "+ch.APIKey)
	resp, err := p.client.Do(r)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	body, err := io.ReadAll(io.LimitReader(resp.Body, 4<<20))
	if err != nil {
		return nil, err
	}
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return nil, fmt.Errorf("upstream returned %d: %s", resp.StatusCode, truncate(string(body), 200))
	}
	var parsed struct {
		Data []UpstreamModel `json:"data"`
	}
	if err := json.Unmarshal(body, &parsed); err != nil {
		return nil, fmt.Errorf("parse models list: %w", err)
	}
	return parsed.Data, nil
}

func truncate(s string, n int) string {
	if len(s) > n {
		return s[:n] + "..."
	}
	return s
}
```

- [ ] **Step 5: 运行测试确认通过**

Run: `go test ./internal/relay/provider/`
Expected: PASS

- [ ] **Step 6: Commit**

```bash
git add -A
git commit -m "feat(provider): provider interface, registry, openai non-stream chat and list-models"
```

---

### Task 8: OpenAI 流式（SSE）路径测试与补全

Task 7 已实现流式代码，本任务补齐流式行为的测试（TDD 反向任务：代码已在，测试验证并锁定行为）。

**Files:**
- Modify: `internal/relay/provider/provider_test.go`（追加流式测试）

**Interfaces:**
- Consumes: Task 7 的 `OpenAI.Chat`、`Result.Stream`
- Produces: 无新接口

- [ ] **Step 1: 写流式测试（追加到 `provider_test.go`，import 加 `"errors"` 和 `"strings"`）**

```go
func TestOpenAIChatStreamSuccess(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Accept") != "text/event-stream" {
			t.Error("stream request must set Accept")
		}
		w.Header().Set("Content-Type", "text/event-stream")
		fl := w.(http.Flusher)
		for _, chunk := range []string{
			`data: {"choices":[{"delta":{"content":"你"}}]}` + "\n\n",
			`data: {"choices":[{"delta":{"content":"好"}}]}` + "\n\n",
			"data: [DONE]\n\n",
		} {
			io.WriteString(w, chunk)
			fl.Flush()
		}
	}))
	defer srv.Close()

	p := NewOpenAI(testClient(), 5*time.Second, 5*time.Second)
	res := p.Chat(context.Background(), testChannel(srv.URL),
		ChatRequest{Model: "m", Body: []byte(`{"stream":true}`), Stream: true})
	if res.Err != nil {
		t.Fatalf("Chat stream: %v", res.Err)
	}
	if res.Stream == nil {
		t.Fatal("Stream must be non-nil for 2xx stream")
	}
	defer res.Stream.Close()
	body, _ := io.ReadAll(res.Stream)
	if !strings.Contains(string(body), `"content":"你"`) || !strings.Contains(string(body), "[DONE]") {
		t.Errorf("stream body incomplete: %q", body)
	}
}

func TestOpenAIChatStreamHTTPErrorReadsBody(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(429)
		io.WriteString(w, `{"error":{"message":"rate limited"}}`)
	}))
	defer srv.Close()

	p := NewOpenAI(testClient(), 5*time.Second, 5*time.Second)
	res := p.Chat(context.Background(), testChannel(srv.URL),
		ChatRequest{Model: "m", Body: []byte(`{"stream":true}`), Stream: true})
	if res.Err != nil {
		t.Fatalf("429 is HTTP-layer, not Err: %v", res.Err)
	}
	if res.HTTPStatus != 429 || !strings.Contains(string(res.Body), "rate limited") {
		t.Errorf("error body should be captured: %d %s", res.HTTPStatus, res.Body)
	}
	if res.Stream != nil {
		t.Error("Stream must be nil on HTTP error")
	}
}

func TestOpenAIChatStreamFirstByteTimeout(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		time.Sleep(2 * time.Second) // 不返回响应头
	}))
	defer srv.Close()

	p := NewOpenAI(testClient(), 5*time.Second, 100*time.Millisecond)
	start := time.Now()
	res := p.Chat(context.Background(), testChannel(srv.URL),
		ChatRequest{Model: "m", Body: []byte(`{"stream":true}`), Stream: true})
	if !errors.Is(res.Err, ErrStreamFirstByteTimeout) {
		t.Fatalf("expected ErrStreamFirstByteTimeout, got %v", res.Err)
	}
	if time.Since(start) > time.Second {
		t.Error("timeout should fire quickly")
	}
}

func TestOpenAIChatStreamClientCancel(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		time.Sleep(2 * time.Second)
	}))
	defer srv.Close()

	ctx, cancel := context.WithCancel(context.Background())
	p := NewOpenAI(testClient(), 5*time.Second, 5*time.Second)
	go func() { time.Sleep(100 * time.Millisecond); cancel() }()
	res := p.Chat(ctx, testChannel(srv.URL),
		ChatRequest{Model: "m", Body: []byte(`{"stream":true}`), Stream: true})
	if res.Err == nil {
		t.Fatal("cancelled ctx must produce Err")
	}
}
```

- [ ] **Step 2: 运行测试确认通过**

Run: `go test ./internal/relay/provider/ -race`
Expected: PASS

- [ ] **Step 3: Commit**

```bash
git add -A
git commit -m "test(provider): openai stream passthrough, error body capture, first-byte timeout, cancel"
```

---

### Task 9: Bearer token 鉴权中间件

**Files:**
- Create: `internal/auth/bearer.go`
- Create: `internal/auth/auth_test.go`

**Interfaces:**
- Consumes: `store.Store.FindTokenByValue`（Task 3）、`store.Token`
- Produces:
  ```go
  package auth

  // 校验 Authorization: Bearer <token>，通过则把 *store.Token 注入 ctx
  func Bearer(st *store.Store) func(http.Handler) http.Handler

  // 从 ctx 取 token（未经 Bearer 时返回 nil）
  func TokenFrom(ctx context.Context) *store.Token
  ```

- [ ] **Step 1: 写失败测试 `internal/auth/auth_test.go`**

```go
package auth

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"pengapi/internal/store"
)

func openStore(t *testing.T) *store.Store {
	t.Helper()
	st, err := store.Open(":memory:")
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	t.Cleanup(func() { st.Close() })
	return st
}

func TestBearer(t *testing.T) {
	st := openStore(t)
	tok, _ := st.CreateToken(context.Background(), "cline")

	var gotName string
	inner := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotName = TokenFrom(r.Context()).Name
		io.WriteString(w, "ok")
	})
	h := Bearer(st)(inner)

	cases := []struct {
		name       string
		header     string
		wantStatus int
		wantName   string
	}{
		{"valid", "Bearer " + tok.Token, 200, "cline"},
		{"missing header", "", 401, ""},
		{"wrong scheme", "Basic abc", 401, ""},
		{"bad token", "Bearer sk-nope", 401, ""},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			gotName = ""
			req := httptest.NewRequest(http.MethodPost, "/v1/chat/completions", nil)
			if c.header != "" {
				req.Header.Set("Authorization", c.header)
			}
			rec := httptest.NewRecorder()
			h.ServeHTTP(rec, req)
			if rec.Code != c.wantStatus {
				t.Errorf("status = %d, want %d", rec.Code, c.wantStatus)
			}
			if gotName != c.wantName {
				t.Errorf("token name = %q, want %q", gotName, c.wantName)
			}
			if c.wantStatus == 401 && !strings.Contains(rec.Body.String(), `"error"`) {
				t.Errorf("401 body should be OpenAI-style error: %s", rec.Body.String())
			}
		})
	}

	// 禁用后的 token 拒绝
	st.UpdateToken(context.Background(), tok.ID, "cline", false)
	req := httptest.NewRequest(http.MethodPost, "/v1/chat/completions", nil)
	req.Header.Set("Authorization", "Bearer "+tok.Token)
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	if rec.Code != 401 {
		t.Errorf("disabled token should be rejected: %d", rec.Code)
	}
}
```

- [ ] **Step 2: 运行测试确认失败**

Run: `go test ./internal/auth/`
Expected: FAIL（包不存在）

- [ ] **Step 3: 实现 `internal/auth/bearer.go`**

```go
package auth

import (
	"context"
	"encoding/json"
	"net/http"
	"strings"

	"pengapi/internal/store"
)

type ctxKey int

const tokenCtxKey ctxKey = 0

func Bearer(st *store.Store) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			h := r.Header.Get("Authorization")
			if !strings.HasPrefix(h, "Bearer ") {
				writeOpenAIError(w, http.StatusUnauthorized, "missing bearer token")
				return
			}
			tok, err := st.FindTokenByValue(r.Context(), strings.TrimPrefix(h, "Bearer "))
			if err != nil {
				writeOpenAIError(w, http.StatusInternalServerError, "internal error")
				return
			}
			if tok == nil {
				writeOpenAIError(w, http.StatusUnauthorized, "invalid token")
				return
			}
			next.ServeHTTP(w, r.WithContext(context.WithValue(r.Context(), tokenCtxKey, tok)))
		})
	}
}

func TokenFrom(ctx context.Context) *store.Token {
	t, _ := ctx.Value(tokenCtxKey).(*store.Token)
	return t
}

// 与 relay 包的错误格式保持一致（独立小函数避免 auth→relay 反向依赖）
func writeOpenAIError(w http.ResponseWriter, status int, msg string) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	json.NewEncoder(w).Encode(map[string]any{
		"error": map[string]any{"message": msg, "type": "api_error", "code": nil},
	})
}
```

- [ ] **Step 4: 运行测试确认通过**

Run: `go test ./internal/auth/`
Expected: PASS

- [ ] **Step 5: Commit**

```bash
git add -A
git commit -m "feat(auth): bearer token middleware"
```

---

### Task 10: 转发引擎（非流式）：模型解析、降级重试、日志

**Files:**
- Create: `internal/relay/util.go`
- Create: `internal/relay/handler.go`
- Create: `internal/relay/engine.go`
- Create: `internal/relay/relay_test.go`

**Interfaces:**
- Consumes: `store`（Task 3-6）、`provider.Registry/ChatRequest/Result`（Task 7）、`auth.TokenFrom`（Task 9）
- Produces:
  ```go
  package relay

  type Engine struct { /* store, providers, failThreshold */ }
  func NewEngine(st *store.Store, reg *provider.Registry, failThreshold int) *Engine
  func (e *Engine) ChatCompletions(w http.ResponseWriter, r *http.Request)
  func (e *Engine) Models(w http.ResponseWriter, r *http.Request) // Task 11 实现，本任务先空函数占位？—— 不，本任务只建 ChatCompletions，Models 在 Task 11 加入同一文件

  // util.go（包内私有，除测试外不导出）
  func writeOpenAIError(w http.ResponseWriter, status int, msg string)
  func replaceModel(body []byte, model string) ([]byte, error)
  func newUUID() string
  func parseUsage(body []byte) (prompt, completion *int)
  func parseUsageSSE(text string) (prompt, completion *int)
  ```

**关键行为（spec 5.1/5.4 的落实）：**
- 请求体上限 10MB；model 字段缺失或 JSON 非法 → 400 OpenAI 错误
- 模型解析：标准名→别名，大小写不敏感；未命中 → 404
- 发往上游的 model 字段**总是**被替换：`upstream_model` 非空用它，否则用标准模型名（客户端可能传的是别名）
- 可重试失败（传输错误、超时、5xx、429）→ 记日志 + RecordFailure + 试下一渠道；全部耗尽 → 503 汇总
- 其他 4xx → 记日志（不计渠道失败）+ 原样透传状态码和 body，停止
- 客户端中途断开（r.Context().Err() != nil）→ 记日志，不计渠道失败，直接结束
- 日志/计数的 DB 写使用脱离请求生命周期的 ctx（5s 超时），避免客户端断开导致日志丢失

- [ ] **Step 1: 实现 `internal/relay/util.go`（纯函数，先写工具测试）**

测试（`internal/relay/relay_test.go` 第一部分）：

```go
package relay

import (
	"strings"
	"testing"
)

func TestReplaceModel(t *testing.T) {
	out, err := replaceModel([]byte(`{"model":"GLM5.3","messages":[{"role":"user","content":"hi"}]}`), "glm-5.3-free")
	if err != nil {
		t.Fatalf("replaceModel: %v", err)
	}
	s := string(out)
	if !strings.Contains(s, `"model":"glm-5.3-free"`) || !strings.Contains(s, `"content":"hi"`) {
		t.Errorf("unexpected: %s", s)
	}
	if _, err := replaceModel([]byte(`not json`), "m"); err == nil {
		t.Error("invalid json should error")
	}
}

func TestNewUUIDFormat(t *testing.T) {
	a, b := newUUID(), newUUID()
	if a == b {
		t.Error("uuids must differ")
	}
	if len(a) != 36 || a[8] != '-' || a[13] != '-' || a[14] != '4' {
		t.Errorf("bad uuid v4 format: %s", a)
	}
}

func TestParseUsage(t *testing.T) {
	p, c := parseUsage([]byte(`{"usage":{"prompt_tokens":10,"completion_tokens":5,"total_tokens":15}}`))
	if p == nil || *p != 10 || c == nil || *c != 5 {
		t.Errorf("usage: %v %v", p, c)
	}
	p, c = parseUsage([]byte(`{"choices":[]}`))
	if p != nil || c != nil {
		t.Errorf("no usage should return nils: %v %v", p, c)
	}
	p, c = parseUsage([]byte(`broken`))
	if p != nil || c != nil {
		t.Errorf("bad json should return nils")
	}
}

func TestParseUsageSSE(t *testing.T) {
	text := "data: {\"choices\":[{\"delta\":{\"content\":\"a\"}}]}\n\n" +
		"data: {\"choices\":[],\"usage\":{\"prompt_tokens\":3,\"completion_tokens\":1}}\n\n" +
		"data: [DONE]\n\n"
	p, c := parseUsageSSE(text)
	if p == nil || *p != 3 || c == nil || *c != 1 {
		t.Errorf("sse usage: %v %v", p, c)
	}
	p, c = parseUsageSSE("data: [DONE]\n\n")
	if p != nil || c != nil {
		t.Errorf("no usage chunk should return nils")
	}
}
```

实现 `internal/relay/util.go`：

```go
package relay

import (
	"crypto/rand"
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
)

func writeOpenAIError(w http.ResponseWriter, status int, msg string) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	json.NewEncoder(w).Encode(map[string]any{
		"error": map[string]any{"message": msg, "type": "api_error", "code": nil},
	})
}

func replaceModel(body []byte, model string) ([]byte, error) {
	var m map[string]any
	if err := json.Unmarshal(body, &m); err != nil {
		return nil, err
	}
	m["model"] = model
	return json.Marshal(m)
}

func newUUID() string {
	var b [16]byte
	rand.Read(b[:])
	b[6] = (b[6] & 0x0f) | 0x40 // version 4
	b[8] = (b[8] & 0x3f) | 0x80 // variant 10
	return fmt.Sprintf("%x-%x-%x-%x-%x", b[0:4], b[4:6], b[6:8], b[8:10], b[10:16])
}

type usage struct {
	PromptTokens     int `json:"prompt_tokens"`
	CompletionTokens int `json:"completion_tokens"`
}

func parseUsage(body []byte) (prompt, completion *int) {
	var v struct {
		Usage *usage `json:"usage"`
	}
	if json.Unmarshal(body, &v) != nil || v.Usage == nil {
		return nil, nil
	}
	return &v.Usage.PromptTokens, &v.Usage.CompletionTokens
}

func parseUsageSSE(text string) (prompt, completion *int) {
	for _, line := range strings.Split(text, "\n") {
		line = strings.TrimSpace(line)
		if !strings.HasPrefix(line, "data:") {
			continue
		}
		data := strings.TrimSpace(strings.TrimPrefix(line, "data:"))
		if data == "[DONE]" {
			continue
		}
		if p, c := parseUsage([]byte(data)); p != nil {
			prompt, completion = p, c
		}
	}
	return
}
```

- [ ] **Step 2: 写引擎测试（追加到 `relay_test.go`）**

```go
// ---------- 引擎测试辅助 ----------

type fakeUpstream struct {
	srv     *httptest.Server
	handler http.HandlerFunc
	lastReq atomic.Value // string：最近一次收到的请求体
	hits    atomic.Int64
}

func newFakeUpstream(h http.HandlerFunc) *fakeUpstream {
	f := &fakeUpstream{}
	f.handler = func(w http.ResponseWriter, r *http.Request) {
		f.hits.Add(1)
		b, _ := io.ReadAll(r.Body)
		f.lastReq.Store(string(b))
		h(w, r)
	}
	f.srv = httptest.NewServer(f.handler)
	return f
}

func (f *fakeUpstream) close()          { f.srv.Close() }
func (f *fakeUpstream) url() string     { return f.srv.URL }
func (f *fakeUpstream) lastBody() string {
	if v := f.lastReq.Load(); v != nil {
		return v.(string)
	}
	return ""
}

type testEnv struct {
	st     *store.Store
	tok    *store.Token
	client *httptest.Server
}

func newTestEnv(t *testing.T, fbTimeout time.Duration) *testEnv {
	t.Helper()
	st, err := store.Open(":memory:")
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	t.Cleanup(func() { st.Close() })
	tok, err := st.CreateToken(context.Background(), "tester")
	if err != nil {
		t.Fatalf("CreateToken: %v", err)
	}
	reg := provider.NewRegistry(provider.NewOpenAI(&http.Client{}, 10*time.Second, fbTimeout))
	engine := NewEngine(st, reg, 3)
	r := chi.NewRouter()
	r.Route("/v1", func(r chi.Router) {
		r.Use(auth.Bearer(st))
		r.Post("/chat/completions", engine.ChatCompletions)
		r.Get("/models", engine.Models)
	})
	srv := httptest.NewServer(r)
	t.Cleanup(srv.Close)
	return &testEnv{st: st, tok: tok, client: srv}
}

func (e *testEnv) addChannel(t *testing.T, name, baseURL string, priority int, modelID int64, upstreamModel string) *store.Channel {
	t.Helper()
	ch, err := e.st.CreateChannel(context.Background(), store.Channel{
		Name: name, Type: "openai", BaseURL: baseURL, APIKey: "upkey", Priority: priority,
	})
	if err != nil {
		t.Fatalf("CreateChannel: %v", err)
	}
	if err := e.st.BindModels(context.Background(), ch.ID, []store.BindingInput{{
		UpstreamModel: upstreamModel, ModelID: &modelID,
	}}); err != nil {
		t.Fatalf("BindModels: %v", err)
	}
	return ch
}

func (e *testEnv) chat(t *testing.T, body string) *http.Response {
	t.Helper()
	req, _ := http.NewRequest(http.MethodPost, e.client.URL+"/v1/chat/completions", strings.NewReader(body))
	req.Header.Set("Authorization", "Bearer "+e.tok.Token)
	req.Header.Set("Content-Type", "application/json")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("chat request: %v", err)
	}
	return resp
}

func readBody(t *testing.T, resp *http.Response) string {
	t.Helper()
	defer resp.Body.Close()
	b, _ := io.ReadAll(resp.Body)
	return string(b)
}

// ---------- 非流式引擎测试 ----------

func TestEngineSuccessAliasAndUpstreamRename(t *testing.T) {
	up := newFakeUpstream(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		io.WriteString(w, `{"choices":[{"message":{"content":"pong"}}],"usage":{"prompt_tokens":3,"completion_tokens":1}}`)
	})
	defer up.close()

	env := newTestEnv(t, 5*time.Second)
	m, _ := env.st.CreateModel(context.Background(), "glm-5.3", []string{"GLM5.3"}, nil, nil)
	env.addChannel(t, "zhipu", up.url(), 1, m.ID, "glm-5.3-free")

	resp := env.chat(t, `{"model":"GLM5.3","messages":[{"role":"user","content":"ping"}]}`)
	body := readBody(t, resp)
	if resp.StatusCode != 200 {
		t.Fatalf("status = %d body=%s", resp.StatusCode, body)
	}
	if !strings.Contains(body, "pong") {
		t.Errorf("upstream body not passed through: %s", body)
	}
	// 上游收到的 model 必须是 upstream_model，不是客户端的别名
	if !strings.Contains(up.lastBody(), `"model":"glm-5.3-free"`) {
		t.Errorf("upstream got wrong model: %s", up.lastBody())
	}
	// 日志：成功一条，记录别名→标准模型、token、usage
	logs, total, err := env.st.ListLogs(context.Background(), store.LogFilter{Page: 1, Size: 10})
	if err != nil || total != 1 {
		t.Fatalf("logs: %v total=%d", err, total)
	}
	l := logs[0]
	if l.Status != "success" || l.ModelRequested != "GLM5.3" || l.ModelCanonical != "glm-5.3" ||
		l.TokenName != "tester" || l.ChannelName != "zhipu" || l.Attempt != 1 {
		t.Errorf("bad log entry: %+v", l)
	}
	if l.PromptTokens == nil || *l.PromptTokens != 3 {
		t.Errorf("usage not parsed: %+v", l)
	}
}

func TestEngineFailoverOn500(t *testing.T) {
	bad := newFakeUpstream(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(500)
		io.WriteString(w, `{"error":{"message":"boom"}}`)
	})
	defer bad.close()
	good := newFakeUpstream(func(w http.ResponseWriter, r *http.Request) {
		io.WriteString(w, `{"choices":[{"message":{"content":"ok"}}]}`)
	})
	defer good.close()

	env := newTestEnv(t, 5*time.Second)
	m, _ := env.st.CreateModel(context.Background(), "m1", nil, nil, nil)
	chBad := env.addChannel(t, "bad", bad.url(), 10, m.ID, "m1") // 优先级高，先试
	env.addChannel(t, "good", good.url(), 1, m.ID, "m1")

	resp := env.chat(t, `{"model":"m1","messages":[]}`)
	if resp.StatusCode != 200 {
		t.Fatalf("should failover to good channel: %d %s", resp.StatusCode, readBody(t, resp))
	}
	// 两次尝试、同一 request_id
	logs, total, _ := env.st.ListLogs(context.Background(), store.LogFilter{Page: 1, Size: 10})
	if total != 2 {
		t.Fatalf("expected 2 attempt logs, got %d", total)
	}
	if logs[0].RequestID != logs[1].RequestID {
		t.Error("attempts must share request_id")
	}
	var failed, success *store.LogEntry
	for i := range logs {
		if logs[i].Status == "failed" {
			failed = &logs[i]
		} else {
			success = &logs[i]
		}
	}
	if failed == nil || success == nil || failed.ChannelName != "bad" || success.Attempt != 2 {
		t.Errorf("bad attempts: %+v", logs)
	}
	// bad 计一次失败；good 无失败
	got, _ := env.st.GetChannel(context.Background(), chBad.ID)
	if got.ConsecutiveFailures != 1 {
		t.Errorf("bad channel failures = %d, want 1", got.ConsecutiveFailures)
	}
}

func TestEngine400PassthroughNoFailoverNoCount(t *testing.T) {
	up := newFakeUpstream(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(400)
		io.WriteString(w, `{"error":{"message":"bad request: max_tokens too large"}}`)
	})
	defer up.close()
	other := newFakeUpstream(func(w http.ResponseWriter, r *http.Request) {
		io.WriteString(w, `{"choices":[]}`)
	})
	defer other.close()

	env := newTestEnv(t, 5*time.Second)
	m, _ := env.st.CreateModel(context.Background(), "m1", nil, nil, nil)
	ch := env.addChannel(t, "ch", up.url(), 10, m.ID, "m1")
	env.addChannel(t, "other", other.url(), 1, m.ID, "m1")

	resp := env.chat(t, `{"model":"m1","messages":[]}`)
	body := readBody(t, resp)
	if resp.StatusCode != 400 || !strings.Contains(body, "max_tokens too large") {
		t.Errorf("4xx must pass through verbatim: %d %s", resp.StatusCode, body)
	}
	if other.hits.Load() != 0 {
		t.Error("4xx must not trigger failover")
	}
	got, _ := env.st.GetChannel(context.Background(), ch.ID)
	if got.ConsecutiveFailures != 0 {
		t.Error("4xx must not count as channel failure")
	}
}

func TestEngineAllFail503AndAutoDisable(t *testing.T) {
	up := newFakeUpstream(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(502)
	})
	defer up.close()

	env := newTestEnv(t, 5*time.Second) // failThreshold=3
	m, _ := env.st.CreateModel(context.Background(), "m1", nil, nil, nil)
	ch := env.addChannel(t, "only", up.url(), 1, m.ID, "m1")

	for i := 0; i < 3; i++ {
		resp := env.chat(t, `{"model":"m1","messages":[]}`)
		body := readBody(t, resp)
		if resp.StatusCode != 503 || !strings.Contains(body, "all channels failed") {
			t.Fatalf("attempt %d: %d %s", i, resp.StatusCode, body)
		}
	}
	got, _ := env.st.GetChannel(context.Background(), ch.ID)
	if !got.AutoDisabled || got.DisabledUntil == nil {
		t.Fatalf("channel should be auto-disabled after 3 failures: %+v", got)
	}
	// 第 4 次：唯一渠道已禁用 → 503 no available channel
	resp := env.chat(t, `{"model":"m1","messages":[]}`)
	body := readBody(t, resp)
	if resp.StatusCode != 503 || !strings.Contains(body, "no available channel") {
		t.Errorf("expected no-channel 503: %d %s", resp.StatusCode, body)
	}
}

func TestEngineUnknownModelAndBadRequests(t *testing.T) {
	env := newTestEnv(t, 5*time.Second)

	resp := env.chat(t, `{"model":"nope","messages":[]}`)
	if resp.StatusCode != 404 {
		t.Errorf("unknown model: %d", resp.StatusCode)
	}
	readBody(t, resp)

	resp = env.chat(t, `{"messages":[]}`)
	if resp.StatusCode != 400 {
		t.Errorf("missing model: %d", resp.StatusCode)
	}
	readBody(t, resp)

	resp = env.chat(t, `not-json`)
	if resp.StatusCode != 400 {
		t.Errorf("bad json: %d", resp.StatusCode)
	}
	readBody(t, resp)

	req, _ := http.NewRequest(http.MethodPost, env.client.URL+"/v1/chat/completions", strings.NewReader(`{}`))
	resp, err := http.DefaultClient.Do(req) // 无 token
	if err != nil {
		t.Fatal(err)
	}
	if resp.StatusCode != 401 {
		t.Errorf("no token: %d", resp.StatusCode)
	}
	readBody(t, resp)
}
```

- [ ] **Step 3: 运行测试确认失败**

Run: `go test ./internal/relay/`
Expected: FAIL（handler.go/engine.go 不存在）

- [ ] **Step 4: 实现 `internal/relay/handler.go` 和 `internal/relay/engine.go`**

`handler.go`：

```go
package relay

import (
	"encoding/json"
	"io"
	"net/http"

	"pengapi/internal/auth"
	"pengapi/internal/relay/provider"
	"pengapi/internal/store"
)

type Engine struct {
	store         *store.Store
	providers     *provider.Registry
	failThreshold int
}

func NewEngine(st *store.Store, reg *provider.Registry, failThreshold int) *Engine {
	return &Engine{store: st, providers: reg, failThreshold: failThreshold}
}

func (e *Engine) ChatCompletions(w http.ResponseWriter, r *http.Request) {
	tok := auth.TokenFrom(r.Context())
	body, err := io.ReadAll(http.MaxBytesReader(w, r.Body, 10<<20))
	if err != nil {
		writeOpenAIError(w, http.StatusBadRequest, "invalid request body")
		return
	}
	var meta struct {
		Model  string `json:"model"`
		Stream bool   `json:"stream"`
	}
	if err := json.Unmarshal(body, &meta); err != nil || meta.Model == "" {
		writeOpenAIError(w, http.StatusBadRequest, "invalid request: model is required")
		return
	}
	model, err := e.store.ResolveModel(r.Context(), meta.Model)
	if err != nil {
		writeOpenAIError(w, http.StatusInternalServerError, "internal error")
		return
	}
	if model == nil {
		writeOpenAIError(w, http.StatusNotFound, "model not found: "+meta.Model)
		return
	}
	candidates, err := e.store.SelectChannels(r.Context(), model.ID)
	if err != nil {
		writeOpenAIError(w, http.StatusInternalServerError, "internal error")
		return
	}
	if len(candidates) == 0 {
		writeOpenAIError(w, http.StatusServiceUnavailable, "no available channel for model: "+model.Name)
		return
	}
	e.run(w, r, tok, model, candidates, body, meta.Stream)
}
```

`engine.go`：

```go
package relay

import (
	"context"
	"fmt"
	"log"
	"net/http"
	"strings"
	"time"

	"pengapi/internal/relay/provider"
	"pengapi/internal/store"
)

// 客户端断开后日志仍要落盘，故 DB 操作用脱离请求生命周期的 ctx
func detachedCtx() (context.Context, context.CancelFunc) {
	return context.WithTimeout(context.Background(), 5*time.Second)
}

func (e *Engine) insertLog(entry *store.LogEntry) {
	ctx, cancel := detachedCtx()
	defer cancel()
	if _, err := e.store.InsertLog(ctx, *entry); err != nil {
		log.Printf("insert request log: %v", err)
	}
}

func (e *Engine) recordSuccess(channelID int64) {
	ctx, cancel := detachedCtx()
	defer cancel()
	if err := e.store.RecordSuccess(ctx, channelID); err != nil {
		log.Printf("record channel success: %v", err)
	}
}

func (e *Engine) recordFailure(channelID int64) {
	ctx, cancel := detachedCtx()
	defer cancel()
	if err := e.store.RecordFailure(ctx, channelID, e.failThreshold); err != nil {
		log.Printf("record channel failure: %v", err)
	}
}

func (e *Engine) run(w http.ResponseWriter, r *http.Request, tok *store.Token,
	model *store.Model, candidates []store.Candidate, body []byte, stream bool) {

	requestID := newUUID()
	var failures []string

	for i, cand := range candidates {
		p, ok := e.providers.For(cand.Channel.Type)
		if !ok {
			failures = append(failures, fmt.Sprintf("%s: unknown channel type %q", cand.Channel.Name, cand.Channel.Type))
			continue
		}

		// model 总是替换：upstream_model 优先，否则用标准名（客户端可能传的是别名）
		upstreamName := cand.UpstreamModel
		if upstreamName == "" {
			upstreamName = model.Name
		}
		reqBody, err := replaceModel(body, upstreamName)
		if err != nil {
			writeOpenAIError(w, http.StatusBadRequest, "invalid request body")
			return
		}

		entry := store.LogEntry{
			RequestID: requestID, Attempt: i + 1,
			TokenID: &tok.ID, TokenName: tok.Name,
			ModelRequested: model.Name, ModelCanonical: model.Name,
			ChannelID: &cand.Channel.ID, ChannelName: cand.Channel.Name,
			Stream: stream, RequestBody: string(reqBody),
		}
		// 客户端原始模型名单独保留（ModelRequested 语义）
		if orig := originalModelName(body); orig != "" {
			entry.ModelRequested = orig
		}

		start := time.Now()
		res := p.Chat(r.Context(), cand.Channel, provider.ChatRequest{
			Model: upstreamName, Body: reqBody, Stream: stream,
		})
		entry.LatencyMS = time.Since(start).Milliseconds()

		// 传输层失败
		if res.Err != nil {
			entry.Status = "failed"
			entry.Error = res.Err.Error()
			if r.Context().Err() != nil {
				entry.Error = "client aborted: " + res.Err.Error()
				e.insertLog(&entry)
				return // 客户端已走，不计渠道失败，不再降级
			}
			e.insertLog(&entry)
			e.recordFailure(cand.Channel.ID)
			failures = append(failures, cand.Channel.Name+": "+res.Err.Error())
			continue
		}

		entry.HTTPStatus = &res.HTTPStatus

		// 成功
		if res.HTTPStatus >= 200 && res.HTTPStatus < 300 {
			if stream {
				e.finishStream(w, res.Stream, &entry, cand.Channel.ID)
				return
			}
			ct := res.Header.Get("Content-Type")
			if ct == "" {
				ct = "application/json"
			}
			w.Header().Set("Content-Type", ct)
			w.WriteHeader(res.HTTPStatus)
			w.Write(res.Body)
			entry.Status = "success"
			entry.ResponseBody = string(res.Body)
			entry.PromptTokens, entry.CompletionTokens = parseUsage(res.Body)
			e.insertLog(&entry)
			e.recordSuccess(cand.Channel.ID)
			return
		}

		entry.ResponseBody = string(res.Body)

		// 可重试的 HTTP 失败
		if res.HTTPStatus == 429 || res.HTTPStatus >= 500 {
			entry.Status = "failed"
			entry.Error = fmt.Sprintf("upstream %d", res.HTTPStatus)
			e.insertLog(&entry)
			e.recordFailure(cand.Channel.ID)
			failures = append(failures, fmt.Sprintf("%s: upstream %d", cand.Channel.Name, res.HTTPStatus))
			continue
		}

		// 其余 4xx：透传，不降级，不计渠道失败
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(res.HTTPStatus)
		w.Write(res.Body)
		entry.Status = "failed"
		entry.Error = fmt.Sprintf("upstream %d (not retryable)", res.HTTPStatus)
		e.insertLog(&entry)
		return
	}

	writeOpenAIError(w, http.StatusServiceUnavailable, "all channels failed: "+strings.Join(failures, "; "))
}

func originalModelName(body []byte) string {
	var m struct {
		Model string `json:"model"`
	}
	if json.Unmarshal(body, &m) != nil {
		return ""
	}
	return m.Model
}
```

注意 `engine.go` 需要在 import 中加 `"encoding/json"`（`originalModelName` 用到）。

- [ ] **Step 5: 运行测试确认通过**

Run: `go test ./internal/relay/ -race -run 'TestReplaceModel|TestNewUUID|TestParseUsage|TestEngine'`
Expected: PASS（`relay_test.go` 的 import 需要：`context`、`io`、`net/http`、`net/http/httptest`、`strings`、`sync/atomic`、`testing`、`time`、`pengapi/internal/auth`、`pengapi/internal/relay/provider`、`pengapi/internal/store`、`github.com/go-chi/chi/v5`。`engine.go` 需要加 `"encoding/json"` import。`Models` handler 此时未实现：在 `handler.go` 先加空实现 `func (e *Engine) Models(w http.ResponseWriter, r *http.Request) {}`，Task 11 补全）

- [ ] **Step 6: Commit**

```bash
git add -A
git commit -m "feat(relay): non-stream failover engine with attempt logging and failure counting"
```

---

### Task 11: SSE 流式透传与 `/v1/models`

**Files:**
- Create: `internal/relay/stream.go`
- Modify: `internal/relay/handler.go`（补全 `Models`）
- Modify: `internal/relay/relay_test.go`（追加流式与 models 测试）

**Interfaces:**
- Consumes: Task 10 的 `Engine`、`parseUsageSSE`；Task 4 的 `ListModelsWithEnabledChannels`
- Produces:
  ```go
  // stream.go：透传上游 SSE 到客户端，累积全文；结束后落日志/计数
  // 客户端断开不计渠道失败；上游中途断流计失败但不降级（已写出响应头）
  func (e *Engine) finishStream(w http.ResponseWriter, src io.ReadCloser, entry *store.LogEntry, channelID int64)
  ```

- [ ] **Step 1: 写流式与 models 测试（追加到 `relay_test.go`）**

```go
func TestEngineStreamPassthroughAndLog(t *testing.T) {
	up := newFakeUpstream(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		fl := w.(http.Flusher)
		io.WriteString(w, "data: {\"choices\":[{\"delta\":{\"content\":\"你\"}}]}\n\n")
		fl.Flush()
		io.WriteString(w, "data: {\"choices\":[{\"delta\":{\"content\":\"好\"}}]}\n\n")
		fl.Flush()
		io.WriteString(w, "data: [DONE]\n\n")
		fl.Flush()
	})
	defer up.close()

	env := newTestEnv(t, 5*time.Second)
	m, _ := env.st.CreateModel(context.Background(), "m1", nil, nil, nil)
	env.addChannel(t, "ch", up.url(), 1, m.ID, "m1")

	resp := env.chat(t, `{"model":"m1","messages":[],"stream":true}`)
	body := readBody(t, resp)
	if resp.StatusCode != 200 {
		t.Fatalf("status=%d body=%s", resp.StatusCode, body)
	}
	if !strings.Contains(body, `"content":"你"`) || !strings.Contains(body, "[DONE]") {
		t.Errorf("stream body incomplete: %q", body)
	}
	if ct := resp.Header.Get("Content-Type"); ct != "text/event-stream" {
		t.Errorf("Content-Type = %q", ct)
	}
	logs, total, _ := env.st.ListLogs(context.Background(), store.LogFilter{Page: 1, Size: 10})
	if total != 1 || !logs[0].Stream || logs[0].Status != "success" {
		t.Fatalf("stream log: %+v", logs)
	}
	if !strings.Contains(logs[0].ResponseBody, "[DONE]") {
		t.Errorf("assembled SSE body missing: %q", logs[0].ResponseBody)
	}
}

func TestEngineStreamFirstByteTimeoutFailover(t *testing.T) {
	slow := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		time.Sleep(2 * time.Second) // 永不及时返回响应头
	}))
	defer slow.Close()
	fast := newFakeUpstream(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		io.WriteString(w, "data: {\"choices\":[{\"delta\":{\"content\":\"快\"}}]}\n\ndata: [DONE]\n\n")
	})
	defer fast.close()

	env := newTestEnv(t, 150*time.Millisecond) // 首字节超时 150ms
	m, _ := env.st.CreateModel(context.Background(), "m1", nil, nil, nil)
	env.addChannel(t, "slow", slow.URL, 10, m.ID, "m1")
	env.addChannel(t, "fast", fast.url(), 1, m.ID, "m1")

	resp := env.chat(t, `{"model":"m1","messages":[],"stream":true}`)
	body := readBody(t, resp)
	if resp.StatusCode != 200 || !strings.Contains(body, "快") {
		t.Fatalf("should failover to fast channel: %d %q", resp.StatusCode, body)
	}
	logs, total, _ := env.st.ListLogs(context.Background(), store.LogFilter{Page: 1, Size: 10})
	if total != 2 {
		t.Fatalf("expected 2 attempts, got %d", total)
	}
}

// finishStream 单元级：上游中途断流
func TestFinishStreamUpstreamInterrupt(t *testing.T) {
	st, err := store.Open(":memory:")
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	ch, _ := st.CreateChannel(context.Background(), store.Channel{Name: "c", Type: "openai", BaseURL: "http://x", APIKey: "k"})
	reg := provider.NewRegistry()
	e := NewEngine(st, reg, 3)

	src := io.NopCloser(&failAfterReader{data: []byte("data: partial\n\n")})
	entry := &store.LogEntry{RequestID: "r1", Attempt: 1, Status: "success", Stream: true}
	rec := httptest.NewRecorder()
	e.finishStream(rec, src, entry, ch.ID)

	if entry.Status != "failed" || !strings.Contains(entry.Error, "stream interrupted") {
		t.Errorf("entry: %+v", entry)
	}
	if !strings.Contains(rec.Body.String(), "partial") {
		t.Errorf("partial content must reach client: %q", rec.Body.String())
	}
	logs, total, _ := st.ListLogs(context.Background(), store.LogFilter{Page: 1, Size: 10})
	if total != 1 || logs[0].ResponseBody != "data: partial\n\n" {
		t.Errorf("partial body must be logged: %+v", logs)
	}
	got, _ := st.GetChannel(context.Background(), ch.ID)
	if got.ConsecutiveFailures != 1 {
		t.Error("mid-stream failure must count")
	}
}

type failAfterReader struct {
	data []byte
	done bool
}

func (f *failAfterReader) Read(p []byte) (int, error) {
	if f.done {
		return 0, errors.New("connection reset")
	}
	f.done = true
	return copy(p, f.data), nil
}

// finishStream 单元级：客户端断开不计渠道失败
func TestFinishStreamClientDisconnect(t *testing.T) {
	st, _ := store.Open(":memory:")
	defer st.Close()
	ch, _ := st.CreateChannel(context.Background(), store.Channel{Name: "c", Type: "openai", BaseURL: "http://x", APIKey: "k"})
	e := NewEngine(st, provider.NewRegistry(), 3)

	src := io.NopCloser(strings.NewReader("data: a\n\ndata: b\n\n"))
	entry := &store.LogEntry{RequestID: "r2", Attempt: 1, Stream: true}
	e.finishStream(&brokenWriter{header: http.Header{}}, src, entry, ch.ID)

	if entry.Status != "failed" || !strings.Contains(entry.Error, "client disconnected") {
		t.Errorf("entry: %+v", entry)
	}
	got, _ := st.GetChannel(context.Background(), ch.ID)
	if got.ConsecutiveFailures != 0 {
		t.Error("client disconnect must NOT count as channel failure")
	}
}

type brokenWriter struct {
	header http.Header
}

func (b *brokenWriter) Header() http.Header       { return b.header }
func (b *brokenWriter) WriteHeader(int)           {}
func (b *brokenWriter) Write([]byte) (int, error) { return 0, errors.New("broken pipe") }
func (b *brokenWriter) Flush()                    {}

func TestModelsEndpoint(t *testing.T) {
	up := newFakeUpstream(func(w http.ResponseWriter, r *http.Request) {})
	defer up.close()

	env := newTestEnv(t, 5*time.Second)
	m, _ := env.st.CreateModel(context.Background(), "glm-5.3", nil, ptr(131072), ptr(8192))
	env.st.CreateModel(context.Background(), "hidden", nil, nil, nil) // 无渠道绑定
	env.addChannel(t, "ch", up.url(), 1, m.ID, "")

	req, _ := http.NewRequest(http.MethodGet, env.client.URL+"/v1/models", nil)
	req.Header.Set("Authorization", "Bearer "+env.tok.Token)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	body := readBody(t, resp)
	if resp.StatusCode != 200 {
		t.Fatalf("status=%d", resp.StatusCode)
	}
	if !strings.Contains(body, `"id":"glm-5.3"`) || strings.Contains(body, "hidden") {
		t.Errorf("models list wrong: %s", body)
	}
	if !strings.Contains(body, `"context_length":131072`) {
		t.Errorf("context_length missing: %s", body)
	}
}

func ptr(v int64) *int64 { return &v }
```

- [ ] **Step 2: 运行测试确认失败**

Run: `go test ./internal/relay/`
Expected: FAIL（finishStream 不存在；Models 空实现导致断言失败）

- [ ] **Step 3: 实现 `internal/relay/stream.go`，补全 `handler.go` 的 `Models`**

`stream.go`：

```go
package relay

import (
	"bufio"
	"bytes"
	"io"
	"net/http"

	"pengapi/internal/store"
)

func (e *Engine) finishStream(w http.ResponseWriter, src io.ReadCloser, entry *store.LogEntry, channelID int64) {
	defer src.Close()
	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Cache-Control", "no-cache")
	w.Header().Set("Connection", "keep-alive")
	w.WriteHeader(http.StatusOK)

	flusher, ok := w.(http.Flusher)
	if !ok {
		entry.Status = "failed"
		entry.Error = "streaming unsupported by response writer"
		e.insertLog(entry)
		return
	}

	var acc bytes.Buffer
	reader := bufio.NewReaderSize(src, 64<<10)
	for {
		line, err := reader.ReadBytes('\n')
		if len(line) > 0 {
			if _, werr := w.Write(line); werr != nil {
				// 客户端断开：不惩罚渠道
				entry.Status = "failed"
				entry.Error = "client disconnected: " + werr.Error()
				entry.ResponseBody = acc.String()
				e.insertLog(entry)
				return
			}
			flusher.Flush()
			acc.Write(line)
		}
		if err != nil {
			if err == io.EOF {
				entry.Status = "success"
				entry.ResponseBody = acc.String()
				entry.PromptTokens, entry.CompletionTokens = parseUsageSSE(acc.String())
				e.insertLog(entry)
				e.recordSuccess(channelID)
			} else {
				// 上游中途断流：已写出响应头，无法降级；计渠道失败
				entry.Status = "failed"
				entry.Error = "stream interrupted: " + err.Error()
				entry.ResponseBody = acc.String()
				e.insertLog(entry)
				e.recordFailure(channelID)
			}
			return
		}
	}
}
```

`handler.go` 的 `Models`（替换 Task 10 的空实现）：

```go
func (e *Engine) Models(w http.ResponseWriter, r *http.Request) {
	models, err := e.store.ListModelsWithEnabledChannels(r.Context())
	if err != nil {
		writeOpenAIError(w, http.StatusInternalServerError, "internal error")
		return
	}
	data := make([]map[string]any, 0, len(models))
	for _, m := range models {
		item := map[string]any{
			"id": m.Name, "object": "model", "created": m.CreatedAt.Unix(), "owned_by": "peng-api",
		}
		if m.ContextLength != nil {
			item["context_length"] = *m.ContextLength
		}
		if m.MaxOutputTokens != nil {
			item["max_output_tokens"] = *m.MaxOutputTokens
		}
		data = append(data, item)
	}
	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(map[string]any{"object": "list", "data": data})
}
```

- [ ] **Step 4: 运行测试确认通过**

Run: `go test ./internal/relay/ -race`
Expected: PASS

- [ ] **Step 5: Commit**

```bash
git add -A
git commit -m "feat(relay): SSE passthrough with assembled logging, first-byte failover, /v1/models"
```

---

### Task 12: 管理端 session 与登录

**Files:**
- Create: `internal/auth/session.go`
- Modify: `internal/auth/auth_test.go`（追加 session 测试）
- Create: `internal/admin/handler.go`
- Create: `internal/admin/auth.go`
- Create: `internal/admin/admin_test.go`

**Interfaces:**
- Consumes: `store.Store`、`provider.Registry`（Task 7）
- Produces:
  ```go
  package auth

  const SessionCookie = "peng_session"

  type SessionStore struct{ /* 内存 map + 互斥锁 + TTL */ }
  func NewSessionStore(ttl time.Duration) *SessionStore
  func (s *SessionStore) Create() string
  func (s *SessionStore) Validate(token string) bool // 命中则滑动续期
  func (s *SessionStore) Delete(token string)
  // cookie 校验中间件，失败 401 {"error":"unauthorized"}
  func RequireSession(ss *SessionStore) func(http.Handler) http.Handler
  ```
  ```go
  package admin

  type Handler struct { /* store, sessions, adminPassword, providers */ }
  func New(st *store.Store, ss *auth.SessionStore, adminPassword string, reg *provider.Registry) *Handler
  func (h *Handler) RegisterRoutes(r chi.Router) // 本任务只挂 login/logout/me，后续任务往里加

  // 包内 JSON 辅助（后续任务共用）
  func writeJSON(w http.ResponseWriter, status int, v any)
  func writeErr(w http.ResponseWriter, status int, msg string)
  func decodeJSON(w http.ResponseWriter, r *http.Request, v any) bool
  ```

- [ ] **Step 1: 写失败测试**

`internal/auth/auth_test.go` 追加：

```go
func TestSessionStore(t *testing.T) {
	ss := NewSessionStore(50 * time.Millisecond)
	tok := ss.Create()
	if tok == "" {
		t.Fatal("empty token")
	}
	if !ss.Validate(tok) {
		t.Fatal("fresh token must validate")
	}
	if ss.Validate("bogus") {
		t.Error("bogus token must not validate")
	}
	time.Sleep(60 * time.Millisecond)
	if ss.Validate(tok) {
		t.Error("expired token must not validate")
	}
	tok2 := ss.Create()
	ss.Delete(tok2)
	if ss.Validate(tok2) {
		t.Error("deleted token must not validate")
	}
}

func TestRequireSession(t *testing.T) {
	ss := NewSessionStore(time.Hour)
	tok := ss.Create()
	inner := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { io.WriteString(w, "ok") })
	h := RequireSession(ss)(inner)

	// 无 cookie → 401
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/api/me", nil))
	if rec.Code != 401 {
		t.Errorf("no cookie: %d", rec.Code)
	}
	// 有 cookie → 200
	req := httptest.NewRequest(http.MethodGet, "/api/me", nil)
	req.AddCookie(&http.Cookie{Name: SessionCookie, Value: tok})
	rec = httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	if rec.Code != 200 {
		t.Errorf("with cookie: %d", rec.Code)
	}
}
```

`internal/admin/admin_test.go`：

```go
package admin

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"pengapi/internal/auth"
	"pengapi/internal/relay/provider"
	"pengapi/internal/store"

	"github.com/go-chi/chi/v5"
)

type adminEnv struct {
	st     *store.Store
	srv    *httptest.Server
	cookie *http.Cookie // 已登录的 session cookie
}

func newAdminEnv(t *testing.T) *adminEnv {
	t.Helper()
	st, err := store.Open(":memory:")
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	t.Cleanup(func() { st.Close() })
	ss := auth.NewSessionStore(time.Hour)
	h := New(st, ss, "testpass", provider.NewRegistry(
		provider.NewOpenAI(&http.Client{}, 10*time.Second, 5*time.Second)))
	r := chi.NewRouter()
	h.RegisterRoutes(r)
	srv := httptest.NewServer(r)
	t.Cleanup(srv.Close)
	env := &adminEnv{st: st, srv: srv}
	env.cookie = env.login(t, "testpass")
	return env
}

func (e *adminEnv) login(t *testing.T, password string) *http.Cookie {
	t.Helper()
	req, _ := http.NewRequest(http.MethodPost, e.srv.URL+"/api/login", strings.NewReader(`{"password":"`+password+`"}`))
	req.Header.Set("Content-Type", "application/json")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("login: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != 200 {
		t.Fatalf("login status = %d", resp.StatusCode)
	}
	for _, c := range resp.Cookies() {
		if c.Name == auth.SessionCookie {
			return c
		}
	}
	t.Fatal("no session cookie set")
	return nil
}

// 带 session 的管理 API 请求
func (e *adminEnv) call(t *testing.T, method, path string, body string) *http.Response {
	t.Helper()
	var rdr io.Reader
	if body != "" {
		rdr = strings.NewReader(body)
	}
	req, _ := http.NewRequest(method, e.srv.URL+path, rdr)
	req.Header.Set("Content-Type", "application/json")
	if e.cookie != nil {
		req.AddCookie(e.cookie)
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("%s %s: %v", method, path, err)
	}
	return resp
}

func readJSON(t *testing.T, resp *http.Response) map[string]any {
	t.Helper()
	defer resp.Body.Close()
	var v map[string]any
	if err := json.NewDecoder(resp.Body).Decode(&v); err != nil {
		t.Fatalf("decode: %v", err)
	}
	return v
}

func TestLoginFlow(t *testing.T) {
	st, _ := store.Open(":memory:")
	defer st.Close()
	ss := auth.NewSessionStore(time.Hour)
	h := New(st, ss, "testpass", provider.NewRegistry())
	r := chi.NewRouter()
	h.RegisterRoutes(r)
	srv := httptest.NewServer(r)
	defer srv.Close()

	// 错误密码 → 401
	req, _ := http.NewRequest(http.MethodPost, srv.URL+"/api/login", strings.NewReader(`{"password":"wrong"}`))
	req.Header.Set("Content-Type", "application/json")
	resp, _ := http.DefaultClient.Do(req)
	if resp.StatusCode != 401 {
		t.Errorf("wrong password: %d", resp.StatusCode)
	}
	resp.Body.Close()

	// 未登录访问受保护接口 → 401
	resp, _ = http.Get(srv.URL + "/api/me")
	if resp.StatusCode != 401 {
		t.Errorf("unauth me: %d", resp.StatusCode)
	}
	resp.Body.Close()

	// 正确密码 → 200 + cookie；me → 200；logout 后 me → 401
	env := &adminEnv{st: st, srv: srv}
	env.cookie = env.login(t, "testpass")
	resp = env.call(t, http.MethodGet, "/api/me", "")
	if resp.StatusCode != 200 {
		t.Errorf("me: %d", resp.StatusCode)
	}
	resp.Body.Close()
	resp = env.call(t, http.MethodPost, "/api/logout", "")
	resp.Body.Close()
	resp = env.call(t, http.MethodGet, "/api/me", "")
	if resp.StatusCode != 401 {
		t.Errorf("after logout: %d", resp.StatusCode)
	}
	resp.Body.Close()
}
```

- [ ] **Step 2: 运行测试确认失败**

Run: `go test ./internal/auth/ ./internal/admin/`
Expected: FAIL（SessionStore、admin 包不存在）

- [ ] **Step 3: 实现 `internal/auth/session.go`**

```go
package auth

import (
	"crypto/rand"
	"encoding/hex"
	"net/http"
	"sync"
	"time"
)

const SessionCookie = "peng_session"

type SessionStore struct {
	mu       sync.Mutex
	sessions map[string]time.Time
	ttl      time.Duration
}

func NewSessionStore(ttl time.Duration) *SessionStore {
	return &SessionStore{sessions: map[string]time.Time{}, ttl: ttl}
}

func (s *SessionStore) Create() string {
	b := make([]byte, 32)
	rand.Read(b)
	tok := hex.EncodeToString(b)
	s.mu.Lock()
	s.gcLocked(time.Now())
	s.sessions[tok] = time.Now().Add(s.ttl)
	s.mu.Unlock()
	return tok
}

func (s *SessionStore) Validate(token string) bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	exp, ok := s.sessions[token]
	if !ok || time.Now().After(exp) {
		delete(s.sessions, token)
		return false
	}
	s.sessions[token] = time.Now().Add(s.ttl) // 滑动续期
	return true
}

func (s *SessionStore) Delete(token string) {
	s.mu.Lock()
	delete(s.sessions, token)
	s.mu.Unlock()
}

func (s *SessionStore) gcLocked(now time.Time) {
	for k, exp := range s.sessions {
		if now.After(exp) {
			delete(s.sessions, k)
		}
	}
}

func RequireSession(ss *SessionStore) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			c, err := r.Cookie(SessionCookie)
			if err != nil || !ss.Validate(c.Value) {
				w.Header().Set("Content-Type", "application/json")
				w.WriteHeader(http.StatusUnauthorized)
				w.Write([]byte(`{"error":"unauthorized"}`))
				return
			}
			next.ServeHTTP(w, r)
		})
	}
}
```

- [ ] **Step 4: 实现 `internal/admin/handler.go` 和 `internal/admin/auth.go`**

`handler.go`：

```go
package admin

import (
	"encoding/json"
	"io"
	"net/http"

	"pengapi/internal/auth"
	"pengapi/internal/relay/provider"
	"pengapi/internal/store"

	"github.com/go-chi/chi/v5"
)

type Handler struct {
	store         *store.Store
	sessions      *auth.SessionStore
	adminPassword string
	providers     *provider.Registry
}

func New(st *store.Store, ss *auth.SessionStore, adminPassword string, reg *provider.Registry) *Handler {
	return &Handler{store: st, sessions: ss, adminPassword: adminPassword, providers: reg}
}

func (h *Handler) RegisterRoutes(r chi.Router) {
	r.Route("/api", func(r chi.Router) {
		r.Post("/login", h.login)
		r.Group(func(r chi.Router) {
			r.Use(auth.RequireSession(h.sessions))
			r.Post("/logout", h.logout)
			r.Get("/me", h.me)
			// Task 13-17 在此追加 models/tokens/channels/logs 路由
		})
	})
}

func writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	json.NewEncoder(w).Encode(v)
}

func writeErr(w http.ResponseWriter, status int, msg string) {
	writeJSON(w, status, map[string]string{"error": msg})
}

func decodeJSON(w http.ResponseWriter, r *http.Request, v any) bool {
	if err := json.NewDecoder(io.LimitReader(r.Body, 4<<20)).Decode(v); err != nil {
		writeErr(w, http.StatusBadRequest, "invalid json: "+err.Error())
		return false
	}
	return true
}
```

`auth.go`：

```go
package admin

import (
	"crypto/subtle"
	"net/http"

	"pengapi/internal/auth"
)

func (h *Handler) login(w http.ResponseWriter, r *http.Request) {
	var body struct {
		Password string `json:"password"`
	}
	if !decodeJSON(w, r, &body) {
		return
	}
	if subtle.ConstantTimeCompare([]byte(body.Password), []byte(h.adminPassword)) != 1 {
		writeErr(w, http.StatusUnauthorized, "invalid password")
		return
	}
	tok := h.sessions.Create()
	http.SetCookie(w, &http.Cookie{
		Name:     auth.SessionCookie,
		Value:    tok,
		Path:     "/",
		HttpOnly: true,
		SameSite: http.SameSiteLaxMode,
	})
	writeJSON(w, http.StatusOK, map[string]bool{"ok": true})
}

func (h *Handler) logout(w http.ResponseWriter, r *http.Request) {
	if c, err := r.Cookie(auth.SessionCookie); err == nil {
		h.sessions.Delete(c.Value)
	}
	http.SetCookie(w, &http.Cookie{Name: auth.SessionCookie, Value: "", Path: "/", MaxAge: -1})
	writeJSON(w, http.StatusOK, map[string]bool{"ok": true})
}

func (h *Handler) me(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, http.StatusOK, map[string]bool{"ok": true})
}
```

- [ ] **Step 5: 运行测试确认通过**

Run: `go test ./internal/auth/ ./internal/admin/`
Expected: PASS（先执行 `go get github.com/go-chi/chi/v5@latest` 装好依赖）

- [ ] **Step 6: Commit**

```bash
git add -A
git commit -m "feat(admin): session store, login/logout/me, handler skeleton"
```

---

### Task 13: 管理 API——模型与 token

**Files:**
- Create: `internal/admin/models.go`
- Create: `internal/admin/tokens.go`
- Modify: `internal/admin/handler.go`（RegisterRoutes 加路由）
- Modify: `internal/admin/admin_test.go`（追加测试）

**Interfaces:**
- Consumes: `store.CreateModel/UpdateModel/DeleteModel/ListModels`（Task 4）、`store.CreateToken/...`（Task 3）、admin Handler 骨架（Task 12）
- Produces:
  ```
  GET    /api/models         → {"data": []store.Model}
  POST   /api/models         {name, aliases, context_length, max_output_tokens} → 201 Model
  PUT    /api/models/{id}    同 POST → 200 Model
  DELETE /api/models/{id}    → {"ok":true}
  GET    /api/tokens         → {"data": [行内 token 打码为 "sk-****末4位"]}
  POST   /api/tokens         {name} → 201 完整 token（仅此一次）
  PUT    /api/tokens/{id}    {name, enabled} → {"ok":true}
  DELETE /api/tokens/{id}    → {"ok":true}
  ```

- [ ] **Step 1: 写失败测试（追加到 `admin_test.go`）**

```go
func TestModelsAPI(t *testing.T) {
	env := newAdminEnv(t)

	// 创建
	resp := env.call(t, http.MethodPost, "/api/models",
		`{"name":"glm-5.3","aliases":["GLM5.3"],"context_length":131072}`)
	v := readJSON(t, resp)
	if resp.StatusCode != 201 {
		t.Fatalf("create: %d %v", resp.StatusCode, v)
	}
	id := int64(v["id"].(float64))

	// 重名 → 400
	resp = env.call(t, http.MethodPost, "/api/models", `{"name":"glm-5.3","aliases":[]}`)
	readJSON(t, resp)
	if resp.StatusCode != 400 {
		t.Errorf("duplicate name: %d", resp.StatusCode)
	}

	// 列表
	resp = env.call(t, http.MethodGet, "/api/models", "")
	v = readJSON(t, resp)
	data := v["data"].([]any)
	if len(data) != 1 || data[0].(map[string]any)["name"] != "glm-5.3" {
		t.Fatalf("list: %v", v)
	}
	aliases := data[0].(map[string]any)["aliases"].([]any)
	if len(aliases) != 1 || aliases[0] != "GLM5.3" {
		t.Errorf("aliases: %v", aliases)
	}

	// 更新（别名整体替换）
	resp = env.call(t, http.MethodPut, "/api/models/"+itoa(id),
		`{"name":"glm-5.3","aliases":["glm53"],"max_output_tokens":8192}`)
	if resp.StatusCode != 200 {
		t.Fatalf("update: %d", resp.StatusCode)
	}
	readJSON(t, resp)

	// 删除
	resp = env.call(t, http.MethodDelete, "/api/models/"+itoa(id), "")
	if resp.StatusCode != 200 {
		t.Fatalf("delete: %d", resp.StatusCode)
	}
	readJSON(t, resp)
}

func TestTokensAPI(t *testing.T) {
	env := newAdminEnv(t)

	// 创建 → 完整 token 仅此一次
	resp := env.call(t, http.MethodPost, "/api/tokens", `{"name":"cline"}`)
	v := readJSON(t, resp)
	if resp.StatusCode != 201 {
		t.Fatalf("create: %d %v", resp.StatusCode, v)
	}
	full := v["token"].(string)
	if !strings.HasPrefix(full, "sk-") {
		t.Fatalf("token: %v", v)
	}
	id := int64(v["id"].(float64))

	// 列表里必须打码
	resp = env.call(t, http.MethodGet, "/api/tokens", "")
	v = readJSON(t, resp)
	masked := v["data"].([]any)[0].(map[string]any)["token"].(string)
	if masked == full || !strings.HasPrefix(masked, "sk-****") {
		t.Errorf("token must be masked in list: %q", masked)
	}

	// 更新
	resp = env.call(t, http.MethodPut, "/api/tokens/"+itoa(id), `{"name":"cline2","enabled":false}`)
	if resp.StatusCode != 200 {
		t.Fatalf("update: %d", resp.StatusCode)
	}
	readJSON(t, resp)

	// 删除
	resp = env.call(t, http.MethodDelete, "/api/tokens/"+itoa(id), "")
	if resp.StatusCode != 200 {
		t.Fatalf("delete: %d", resp.StatusCode)
	}
	readJSON(t, resp)
}

func itoa(v int64) string { return strconv.FormatInt(v, 10) }
```

- [ ] **Step 2: 运行测试确认失败**

Run: `go test ./internal/admin/`
Expected: FAIL（models.go/tokens.go 不存在；注意 import 加 `"strconv"`）

- [ ] **Step 3: 实现**

`internal/admin/models.go`：

```go
package admin

import (
	"net/http"
	"strconv"
	"strings"

	"github.com/go-chi/chi/v5"
)

type modelInput struct {
	Name            string   `json:"name"`
	Aliases         []string `json:"aliases"`
	ContextLength   *int64   `json:"context_length"`
	MaxOutputTokens *int64   `json:"max_output_tokens"`
}

func (h *Handler) listModels(w http.ResponseWriter, r *http.Request) {
	models, err := h.store.ListModels(r.Context())
	if err != nil {
		writeErr(w, http.StatusInternalServerError, "internal error")
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"data": models})
}

func (h *Handler) createModel(w http.ResponseWriter, r *http.Request) {
	var in modelInput
	if !decodeJSON(w, r, &in) {
		return
	}
	in.Name = strings.TrimSpace(in.Name)
	if in.Name == "" {
		writeErr(w, http.StatusBadRequest, "name is required")
		return
	}
	m, err := h.store.CreateModel(r.Context(), in.Name, in.Aliases, in.ContextLength, in.MaxOutputTokens)
	if err != nil {
		if strings.Contains(err.Error(), "UNIQUE") {
			writeErr(w, http.StatusBadRequest, "name or alias already exists")
			return
		}
		writeErr(w, http.StatusInternalServerError, "internal error")
		return
	}
	writeJSON(w, http.StatusCreated, m)
}

func (h *Handler) updateModel(w http.ResponseWriter, r *http.Request) {
	id, err := strconv.ParseInt(chi.URLParam(r, "id"), 10, 64)
	if err != nil {
		writeErr(w, http.StatusBadRequest, "invalid id")
		return
	}
	var in modelInput
	if !decodeJSON(w, r, &in) {
		return
	}
	if strings.TrimSpace(in.Name) == "" {
		writeErr(w, http.StatusBadRequest, "name is required")
		return
	}
	if err := h.store.UpdateModel(r.Context(), id, in.Name, in.Aliases, in.ContextLength, in.MaxOutputTokens); err != nil {
		if strings.Contains(err.Error(), "UNIQUE") {
			writeErr(w, http.StatusBadRequest, "name or alias already exists")
			return
		}
		writeErr(w, http.StatusInternalServerError, "internal error")
		return
	}
	writeJSON(w, http.StatusOK, map[string]bool{"ok": true})
}

func (h *Handler) deleteModel(w http.ResponseWriter, r *http.Request) {
	id, err := strconv.ParseInt(chi.URLParam(r, "id"), 10, 64)
	if err != nil {
		writeErr(w, http.StatusBadRequest, "invalid id")
		return
	}
	if err := h.store.DeleteModel(r.Context(), id); err != nil {
		writeErr(w, http.StatusInternalServerError, "internal error")
		return
	}
	writeJSON(w, http.StatusOK, map[string]bool{"ok": true})
}
```

`internal/admin/tokens.go`：

```go
package admin

import (
	"net/http"
	"strconv"

	"pengapi/internal/store"

	"github.com/go-chi/chi/v5"
)

type tokenRow struct {
	ID        int64  `json:"id"`
	Name      string `json:"name"`
	Token     string `json:"token"` // 列表中打码
	Enabled   bool   `json:"enabled"`
	CreatedAt string `json:"created_at"`
}

func maskToken(t store.Token) tokenRow {
	masked := t.Token
	if len(masked) > 7 {
		masked = "sk-****" + masked[len(masked)-4:]
	}
	return tokenRow{ID: t.ID, Name: t.Name, Token: masked, Enabled: t.Enabled,
		CreatedAt: t.CreatedAt.Format("2006-01-02 15:04:05")}
}

func (h *Handler) listTokens(w http.ResponseWriter, r *http.Request) {
	tokens, err := h.store.ListTokens(r.Context())
	if err != nil {
		writeErr(w, http.StatusInternalServerError, "internal error")
		return
	}
	rows := make([]tokenRow, 0, len(tokens))
	for _, t := range tokens {
		rows = append(rows, maskToken(t))
	}
	writeJSON(w, http.StatusOK, map[string]any{"data": rows})
}

func (h *Handler) createToken(w http.ResponseWriter, r *http.Request) {
	var in struct {
		Name string `json:"name"`
	}
	if !decodeJSON(w, r, &in) {
		return
	}
	if in.Name == "" {
		writeErr(w, http.StatusBadRequest, "name is required")
		return
	}
	t, err := h.store.CreateToken(r.Context(), in.Name)
	if err != nil {
		writeErr(w, http.StatusInternalServerError, "internal error")
		return
	}
	writeJSON(w, http.StatusCreated, t) // 完整 token 仅此一次返回
}

func (h *Handler) updateToken(w http.ResponseWriter, r *http.Request) {
	id, err := strconv.ParseInt(chi.URLParam(r, "id"), 10, 64)
	if err != nil {
		writeErr(w, http.StatusBadRequest, "invalid id")
		return
	}
	var in struct {
		Name    string `json:"name"`
		Enabled bool   `json:"enabled"`
	}
	if !decodeJSON(w, r, &in) {
		return
	}
	if err := h.store.UpdateToken(r.Context(), id, in.Name, in.Enabled); err != nil {
		writeErr(w, http.StatusInternalServerError, "internal error")
		return
	}
	writeJSON(w, http.StatusOK, map[string]bool{"ok": true})
}

func (h *Handler) deleteToken(w http.ResponseWriter, r *http.Request) {
	id, err := strconv.ParseInt(chi.URLParam(r, "id"), 10, 64)
	if err != nil {
		writeErr(w, http.StatusBadRequest, "invalid id")
		return
	}
	if err := h.store.DeleteToken(r.Context(), id); err != nil {
		writeErr(w, http.StatusInternalServerError, "internal error")
		return
	}
	writeJSON(w, http.StatusOK, map[string]bool{"ok": true})
}
```

`handler.go` RegisterRoutes 的 session 组内追加：

```go
			r.Route("/models", func(r chi.Router) {
				r.Get("/", h.listModels)
				r.Post("/", h.createModel)
				r.Put("/{id}", h.updateModel)
				r.Delete("/{id}", h.deleteModel)
			})
			r.Route("/tokens", func(r chi.Router) {
				r.Get("/", h.listTokens)
				r.Post("/", h.createToken)
				r.Put("/{id}", h.updateToken)
				r.Delete("/{id}", h.deleteToken)
			})
```

- [ ] **Step 4: 运行测试确认通过**

Run: `go test ./internal/admin/`
Expected: PASS

- [ ] **Step 5: Commit**

```bash
git add -A
git commit -m "feat(admin): models and tokens CRUD APIs"
```

---

### Task 14: 管理 API——渠道 CRUD、启停、重置

**Files:**
- Create: `internal/admin/channels.go`
- Modify: `internal/admin/handler.go`（RegisterRoutes 加路由）
- Modify: `internal/admin/admin_test.go`（追加测试）

**Interfaces:**
- Consumes: `store.CreateChannel/...`（Task 5）、admin Handler（Task 12）
- Produces:
  ```
  GET    /api/channels                 → {"data": []store.Channel}（api_key 完整返回，自用单管理员）
  POST   /api/channels                 {name, type?, base_url, api_key, priority, test_model,
                                        models: [{model_id, upstream_model}]} → 201 Channel
  PUT    /api/channels/{id}            同 POST（整体更新，保留失败状态）→ {"ok":true}
  DELETE /api/channels/{id}            → {"ok":true}
  POST   /api/channels/{id}/toggle     {enabled} → {"ok":true}
  POST   /api/channels/{id}/reset      → {"ok":true}
  ```

- [ ] **Step 1: 写失败测试（追加到 `admin_test.go`）**

```go
func TestChannelsAPI(t *testing.T) {
	env := newAdminEnv(t)

	// 先建模型供绑定
	resp := env.call(t, http.MethodPost, "/api/models", `{"name":"glm-5.3","aliases":[]}`)
	mv := readJSON(t, resp)
	modelID := int64(mv["id"].(float64))

	// 创建渠道（含绑定）
	resp = env.call(t, http.MethodPost, "/api/channels", `{
		"name":"zhipu","base_url":"https://api.bigmodel.cn/coding/paas/v4","api_key":"k1",
		"priority":10,"test_model":"glm-5.3-free",
		"models":[{"model_id":`+itoa(modelID)+`,"upstream_model":"glm-5.3-free"}]}`)
	v := readJSON(t, resp)
	if resp.StatusCode != 201 {
		t.Fatalf("create: %d %v", resp.StatusCode, v)
	}
	chID := int64(v["id"].(float64))

	// 列表：含绑定与状态字段
	resp = env.call(t, http.MethodGet, "/api/channels", "")
	v = readJSON(t, resp)
	ch := v["data"].([]any)[0].(map[string]any)
	if ch["name"] != "zhipu" || ch["priority"].(float64) != 10 || ch["enabled"] != true {
		t.Fatalf("list: %v", ch)
	}
	bindings := ch["models"].([]any)
	if len(bindings) != 1 || bindings[0].(map[string]any)["upstream_model"] != "glm-5.3-free" {
		t.Errorf("bindings: %v", bindings)
	}

	// 缺必填字段 → 400
	resp = env.call(t, http.MethodPost, "/api/channels", `{"name":"x"}`)
	readJSON(t, resp)
	if resp.StatusCode != 400 {
		t.Errorf("missing fields: %d", resp.StatusCode)
	}

	// 更新
	resp = env.call(t, http.MethodPut, "/api/channels/"+itoa(chID), `{
		"name":"zhipu2","base_url":"https://example.com/v1","api_key":"k2","priority":5,
		"enabled":true,"test_model":"m","models":[{"model_id":`+itoa(modelID)+`,"upstream_model":""}]}`)
	if resp.StatusCode != 200 {
		t.Fatalf("update: %d", resp.StatusCode)
	}
	readJSON(t, resp)

	// toggle 禁用
	resp = env.call(t, http.MethodPost, "/api/channels/"+itoa(chID)+"/toggle", `{"enabled":false}`)
	readJSON(t, resp)
	if resp.StatusCode != 200 {
		t.Fatalf("toggle: %d", resp.StatusCode)
	}
	got, _ := env.st.GetChannel(context.Background(), chID)
	if got.Enabled {
		t.Error("channel should be disabled")
	}

	// reset 清失败状态
	env.st.RecordFailure(context.Background(), chID, 1) // threshold=1 → 必禁用
	resp = env.call(t, http.MethodPost, "/api/channels/"+itoa(chID)+"/reset", "")
	readJSON(t, resp)
	got, _ = env.st.GetChannel(context.Background(), chID)
	if got.AutoDisabled || got.ConsecutiveFailures != 0 {
		t.Errorf("reset failed: %+v", got)
	}

	// 删除
	resp = env.call(t, http.MethodDelete, "/api/channels/"+itoa(chID), "")
	if resp.StatusCode != 200 {
		t.Fatalf("delete: %d", resp.StatusCode)
	}
	readJSON(t, resp)
}
```

- [ ] **Step 2: 运行测试确认失败**

Run: `go test ./internal/admin/ -run TestChannelsAPI`
Expected: FAIL（channels.go 不存在）

- [ ] **Step 3: 实现 `internal/admin/channels.go`**

```go
package admin

import (
	"net/http"
	"strconv"
	"strings"

	"pengapi/internal/store"

	"github.com/go-chi/chi/v5"
)

type channelInput struct {
	Name      string `json:"name"`
	Type      string `json:"type"`
	BaseURL   string `json:"base_url"`
	APIKey    string `json:"api_key"`
	Priority  int    `json:"priority"`
	Enabled   *bool  `json:"enabled"`
	TestModel string `json:"test_model"`
	Models    []struct {
		ModelID       int64  `json:"model_id"`
		UpstreamModel string `json:"upstream_model"`
	} `json:"models"`
}

func (in channelInput) validate() string {
	if strings.TrimSpace(in.Name) == "" {
		return "name is required"
	}
	if strings.TrimSpace(in.BaseURL) == "" {
		return "base_url is required"
	}
	if strings.TrimSpace(in.APIKey) == "" {
		return "api_key is required"
	}
	return ""
}

func (h *Handler) listChannels(w http.ResponseWriter, r *http.Request) {
	channels, err := h.store.ListChannels(r.Context())
	if err != nil {
		writeErr(w, http.StatusInternalServerError, "internal error")
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"data": channels})
}

func (h *Handler) createChannel(w http.ResponseWriter, r *http.Request) {
	var in channelInput
	if !decodeJSON(w, r, &in) {
		return
	}
	if msg := in.validate(); msg != "" {
		writeErr(w, http.StatusBadRequest, msg)
		return
	}
	ch := store.Channel{
		Name: in.Name, Type: in.Type, BaseURL: in.BaseURL, APIKey: in.APIKey,
		Priority: in.Priority, TestModel: in.TestModel,
	}
	for _, b := range in.Models {
		ch.Models = append(ch.Models, store.ChannelModelBinding{ModelID: b.ModelID, UpstreamModel: b.UpstreamModel})
	}
	created, err := h.store.CreateChannel(r.Context(), ch)
	if err != nil {
		writeErr(w, http.StatusInternalServerError, "internal error: "+err.Error())
		return
	}
	writeJSON(w, http.StatusCreated, created)
}

func (h *Handler) updateChannel(w http.ResponseWriter, r *http.Request) {
	id, err := strconv.ParseInt(chi.URLParam(r, "id"), 10, 64)
	if err != nil {
		writeErr(w, http.StatusBadRequest, "invalid id")
		return
	}
	existing, err := h.store.GetChannel(r.Context(), id)
	if err != nil {
		writeErr(w, http.StatusInternalServerError, "internal error")
		return
	}
	if existing == nil {
		writeErr(w, http.StatusNotFound, "channel not found")
		return
	}
	var in channelInput
	if !decodeJSON(w, r, &in) {
		return
	}
	if msg := in.validate(); msg != "" {
		writeErr(w, http.StatusBadRequest, msg)
		return
	}
	enabled := existing.Enabled
	if in.Enabled != nil {
		enabled = *in.Enabled
	}
	ch := store.Channel{
		ID: id, Name: in.Name, Type: in.Type, BaseURL: in.BaseURL, APIKey: in.APIKey,
		Priority: in.Priority, Enabled: enabled, TestModel: in.TestModel,
	}
	for _, b := range in.Models {
		ch.Models = append(ch.Models, store.ChannelModelBinding{ModelID: b.ModelID, UpstreamModel: b.UpstreamModel})
	}
	if err := h.store.UpdateChannel(r.Context(), ch); err != nil {
		writeErr(w, http.StatusInternalServerError, "internal error: "+err.Error())
		return
	}
	writeJSON(w, http.StatusOK, map[string]bool{"ok": true})
}

func (h *Handler) deleteChannel(w http.ResponseWriter, r *http.Request) {
	id, err := strconv.ParseInt(chi.URLParam(r, "id"), 10, 64)
	if err != nil {
		writeErr(w, http.StatusBadRequest, "invalid id")
		return
	}
	if err := h.store.DeleteChannel(r.Context(), id); err != nil {
		writeErr(w, http.StatusInternalServerError, "internal error")
		return
	}
	writeJSON(w, http.StatusOK, map[string]bool{"ok": true})
}

func (h *Handler) toggleChannel(w http.ResponseWriter, r *http.Request) {
	id, err := strconv.ParseInt(chi.URLParam(r, "id"), 10, 64)
	if err != nil {
		writeErr(w, http.StatusBadRequest, "invalid id")
		return
	}
	var in struct {
		Enabled bool `json:"enabled"`
	}
	if !decodeJSON(w, r, &in) {
		return
	}
	if err := h.store.SetChannelEnabled(r.Context(), id, in.Enabled); err != nil {
		writeErr(w, http.StatusInternalServerError, "internal error")
		return
	}
	writeJSON(w, http.StatusOK, map[string]bool{"ok": true})
}

func (h *Handler) resetChannel(w http.ResponseWriter, r *http.Request) {
	id, err := strconv.ParseInt(chi.URLParam(r, "id"), 10, 64)
	if err != nil {
		writeErr(w, http.StatusBadRequest, "invalid id")
		return
	}
	if err := h.store.ResetChannelState(r.Context(), id); err != nil {
		writeErr(w, http.StatusInternalServerError, "internal error")
		return
	}
	writeJSON(w, http.StatusOK, map[string]bool{"ok": true})
}
```

`handler.go` RegisterRoutes 的 session 组内追加：

```go
			r.Route("/channels", func(r chi.Router) {
				r.Get("/", h.listChannels)
				r.Post("/", h.createChannel)
				r.Put("/{id}", h.updateChannel)
				r.Delete("/{id}", h.deleteChannel)
				r.Post("/{id}/toggle", h.toggleChannel)
				r.Post("/{id}/reset", h.resetChannel)
			})
```

- [ ] **Step 4: 运行测试确认通过**

Run: `go test ./internal/admin/`
Expected: PASS

- [ ] **Step 5: Commit**

```bash
git add -A
git commit -m "feat(admin): channel CRUD, toggle, reset"
```

---

### Task 15: 管理 API——拉取上游模型并批量绑定

**Files:**
- Modify: `internal/admin/channels.go`（追加 fetchModels、bindModels 两个 handler）
- Modify: `internal/admin/handler.go`（RegisterRoutes 加路由）
- Modify: `internal/admin/admin_test.go`（追加测试）

**Interfaces:**
- Consumes: `provider.Registry.For`、`Provider.ListModels`（Task 7）、`store.ResolveModel`（Task 4）、`store.BindModels`（Task 5）
- Produces:
  ```
  POST /api/channels/{id}/fetch-models
    → {"data":[{"upstream_id":"glm-5.3","context_length":131072|null,
                "max_output_tokens":null,"suggested_model_id":1|null,"suggested_name":"glm-5.3"}]}
    预匹配规则：ResolveModel(upstream_id) 命中 → suggested_model_id；
    未命中 → suggested_model_id=null、suggested_name=upstream_id（前端默认「新建」）
    上游失败 → 502 {"error":"..."}
  POST /api/channels/{id}/models
    {"bindings":[{"upstream_model":"...","model_id":1|null,"new_model_name":"",
                  "context_length":null,"max_output_tokens":null}]} → {"ok":true}
  ```

- [ ] **Step 1: 写失败测试（追加到 `admin_test.go`）**

```go
func TestFetchModelsAndBind(t *testing.T) {
	// 假上游 /models：一个能匹配已有模型，一个不能
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/models" {
			io.WriteString(w, `{"object":"list","data":[
				{"id":"GLM5.3","object":"model"},
				{"id":"new-model-x","object":"model","context_length":64000}
			]}`)
			return
		}
		w.WriteHeader(404)
	}))
	defer upstream.Close()

	env := newAdminEnv(t)
	resp := env.call(t, http.MethodPost, "/api/models", `{"name":"glm-5.3","aliases":["GLM5.3"]}`)
	mv := readJSON(t, resp)
	modelID := int64(mv["id"].(float64))

	resp = env.call(t, http.MethodPost, "/api/channels",
		`{"name":"up","base_url":"`+upstream.URL+`","api_key":"k","priority":1}`)
	cv := readJSON(t, resp)
	chID := int64(cv["id"].(float64))

	// fetch-models：GLM5.3 应预匹配到已有模型，new-model-x 建议新建
	resp = env.call(t, http.MethodPost, "/api/channels/"+itoa(chID)+"/fetch-models", "")
	v := readJSON(t, resp)
	if resp.StatusCode != 200 {
		t.Fatalf("fetch-models: %d %v", resp.StatusCode, v)
	}
	data := v["data"].([]any)
	if len(data) != 2 {
		t.Fatalf("expected 2 upstream models: %v", data)
	}
	first := data[0].(map[string]any)
	if first["upstream_id"] != "GLM5.3" || first["suggested_model_id"].(float64) != float64(modelID) {
		t.Errorf("pre-match failed: %v", first)
	}
	second := data[1].(map[string]any)
	if second["suggested_model_id"] != nil || second["suggested_name"] != "new-model-x" {
		t.Errorf("new model suggestion wrong: %v", second)
	}

	// 批量绑定：一个绑已有，一个新建
	resp = env.call(t, http.MethodPost, "/api/channels/"+itoa(chID)+"/models", `{"bindings":[
		{"upstream_model":"GLM5.3","model_id":`+itoa(modelID)+`},
		{"upstream_model":"new-model-x","new_model_name":"new-model-x","context_length":64000}
	]}`)
	if resp.StatusCode != 200 {
		t.Fatalf("bind: %d", resp.StatusCode)
	}
	readJSON(t, resp)

	got, _ := env.st.GetChannel(context.Background(), chID)
	if len(got.Models) != 2 {
		t.Fatalf("bindings: %+v", got.Models)
	}
	// 新建的模型带参数、且自身为别名
	m, err := env.st.ResolveModel(context.Background(), "NEW-MODEL-X")
	if err != nil || m == nil || m.ContextLength == nil || *m.ContextLength != 64000 {
		t.Errorf("auto-created model: %v %+v", err, m)
	}

	// 渠道不存在 → 404
	resp = env.call(t, http.MethodPost, "/api/channels/999/fetch-models", "")
	readJSON(t, resp)
	if resp.StatusCode != 404 {
		t.Errorf("missing channel: %d", resp.StatusCode)
	}
}
```

- [ ] **Step 2: 运行测试确认失败**

Run: `go test ./internal/admin/ -run TestFetchModelsAndBind`
Expected: FAIL（路由不存在 → 404/405）

- [ ] **Step 3: 实现（追加到 `internal/admin/channels.go`）**

```go
type fetchModelsItem struct {
	UpstreamID       string `json:"upstream_id"`
	ContextLength    *int64 `json:"context_length"`
	MaxOutputTokens  *int64 `json:"max_output_tokens"`
	SuggestedModelID *int64 `json:"suggested_model_id"`
	SuggestedName    string `json:"suggested_name"`
}

func (h *Handler) fetchModels(w http.ResponseWriter, r *http.Request) {
	id, err := strconv.ParseInt(chi.URLParam(r, "id"), 10, 64)
	if err != nil {
		writeErr(w, http.StatusBadRequest, "invalid id")
		return
	}
	ch, err := h.store.GetChannel(r.Context(), id)
	if err != nil {
		writeErr(w, http.StatusInternalServerError, "internal error")
		return
	}
	if ch == nil {
		writeErr(w, http.StatusNotFound, "channel not found")
		return
	}
	p, ok := h.providers.For(ch.Type)
	if !ok {
		writeErr(w, http.StatusBadRequest, "unknown channel type: "+ch.Type)
		return
	}
	models, err := p.ListModels(r.Context(), *ch)
	if err != nil {
		writeErr(w, http.StatusBadGateway, "fetch upstream models: "+err.Error())
		return
	}
	items := make([]fetchModelsItem, 0, len(models))
	for _, m := range models {
		item := fetchModelsItem{
			UpstreamID: m.ID, ContextLength: m.ContextLength,
			MaxOutputTokens: m.MaxOutputTokens, SuggestedName: m.ID,
		}
		if existing, err := h.store.ResolveModel(r.Context(), m.ID); err == nil && existing != nil {
			item.SuggestedModelID = &existing.ID
			item.SuggestedName = existing.Name
		}
		items = append(items, item)
	}
	writeJSON(w, http.StatusOK, map[string]any{"data": items})
}

func (h *Handler) bindModels(w http.ResponseWriter, r *http.Request) {
	id, err := strconv.ParseInt(chi.URLParam(r, "id"), 10, 64)
	if err != nil {
		writeErr(w, http.StatusBadRequest, "invalid id")
		return
	}
	ch, err := h.store.GetChannel(r.Context(), id)
	if err != nil {
		writeErr(w, http.StatusInternalServerError, "internal error")
		return
	}
	if ch == nil {
		writeErr(w, http.StatusNotFound, "channel not found")
		return
	}
	var in struct {
		Bindings []store.BindingInput `json:"bindings"`
	}
	if !decodeJSON(w, r, &in) {
		return
	}
	if len(in.Bindings) == 0 {
		writeErr(w, http.StatusBadRequest, "bindings is empty")
		return
	}
	for _, b := range in.Bindings {
		if b.UpstreamModel == "" {
			writeErr(w, http.StatusBadRequest, "upstream_model is required")
			return
		}
	}
	if err := h.store.BindModels(r.Context(), id, in.Bindings); err != nil {
		writeErr(w, http.StatusInternalServerError, "internal error: "+err.Error())
		return
	}
	writeJSON(w, http.StatusOK, map[string]bool{"ok": true})
}
```

`handler.go` RegisterRoutes 的 channels 组内追加：

```go
				r.Post("/{id}/fetch-models", h.fetchModels)
				r.Post("/{id}/models", h.bindModels)
```

- [ ] **Step 4: 运行测试确认通过**

Run: `go test ./internal/admin/`
Expected: PASS

- [ ] **Step 5: Commit**

```bash
git add -A
git commit -m "feat(admin): fetch upstream models with pre-match suggestions, batch bind"
```

---

### Task 16: 管理 API——渠道连通性测试

**Files:**
- Modify: `internal/admin/channels.go`（追加 testChannel handler）
- Modify: `internal/admin/handler.go`（RegisterRoutes 加路由）
- Modify: `internal/admin/admin_test.go`（追加测试）

**Interfaces:**
- Consumes: `Provider.Chat`（Task 7）、`store.GetChannel`
- Produces:
  ```
  POST /api/channels/{id}/test
    → {"success":true,"latency_ms":123,"http_status":200,
       "error":"","request_body":"...","response_body":"..."}
  规则：用渠道 test_model 发 {"model":<test_model>,"messages":[{"role":"user","content":"ping"}],"max_tokens":1,"stream":false}
  test_model 未配置 → 400；不写 request_logs，不影响失败计数
  ```

- [ ] **Step 1: 写失败测试（追加到 `admin_test.go`）**

```go
func TestChannelConnectivityTest(t *testing.T) {
	var gotBody string
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		b, _ := io.ReadAll(r.Body)
		gotBody = string(b)
		io.WriteString(w, `{"choices":[{"message":{"content":"pong"}}]}`)
	}))
	defer upstream.Close()

	env := newAdminEnv(t)
	resp := env.call(t, http.MethodPost, "/api/channels",
		`{"name":"up","base_url":"`+upstream.URL+`","api_key":"k","priority":1,"test_model":"cheap-model"}`)
	cv := readJSON(t, resp)
	chID := int64(cv["id"].(float64))

	resp = env.call(t, http.MethodPost, "/api/channels/"+itoa(chID)+"/test", "")
	v := readJSON(t, resp)
	if resp.StatusCode != 200 {
		t.Fatalf("test: %d %v", resp.StatusCode, v)
	}
	if v["success"] != true || v["http_status"].(float64) != 200 {
		t.Errorf("test result: %v", v)
	}
	if !strings.Contains(v["request_body"].(string), "cheap-model") ||
		!strings.Contains(v["request_body"].(string), `"max_tokens":1`) {
		t.Errorf("ping request body: %v", v["request_body"])
	}
	if !strings.Contains(v["response_body"].(string), "pong") {
		t.Errorf("response body: %v", v["response_body"])
	}
	if !strings.Contains(gotBody, "cheap-model") {
		t.Errorf("upstream got: %s", gotBody)
	}

	// 不写日志、不计数
	_, total, _ := env.st.ListLogs(context.Background(), store.LogFilter{Page: 1, Size: 10})
	if total != 0 {
		t.Error("ping must not write request_logs")
	}
	got, _ := env.st.GetChannel(context.Background(), chID)
	if got.ConsecutiveFailures != 0 {
		t.Error("ping must not touch failure counters")
	}

	// 未配置 test_model → 400
	resp = env.call(t, http.MethodPost, "/api/channels",
		`{"name":"up2","base_url":"`+upstream.URL+`","api_key":"k"}`)
	cv2 := readJSON(t, resp)
	resp = env.call(t, http.MethodPost, "/api/channels/"+itoa(int64(cv2["id"].(float64)))+"/test", "")
	readJSON(t, resp)
	if resp.StatusCode != 400 {
		t.Errorf("missing test_model: %d", resp.StatusCode)
	}
}
```

- [ ] **Step 2: 运行测试确认失败**

Run: `go test ./internal/admin/ -run TestChannelConnectivityTest`
Expected: FAIL（路由不存在）

- [ ] **Step 3: 实现（追加到 `internal/admin/channels.go`）**

```go
func (h *Handler) testChannel(w http.ResponseWriter, r *http.Request) {
	id, err := strconv.ParseInt(chi.URLParam(r, "id"), 10, 64)
	if err != nil {
		writeErr(w, http.StatusBadRequest, "invalid id")
		return
	}
	ch, err := h.store.GetChannel(r.Context(), id)
	if err != nil {
		writeErr(w, http.StatusInternalServerError, "internal error")
		return
	}
	if ch == nil {
		writeErr(w, http.StatusNotFound, "channel not found")
		return
	}
	if ch.TestModel == "" {
		writeErr(w, http.StatusBadRequest, "test_model not configured: pick a cheap bound model first")
		return
	}
	p, ok := h.providers.For(ch.Type)
	if !ok {
		writeErr(w, http.StatusBadRequest, "unknown channel type: "+ch.Type)
		return
	}
	pingBody, _ := json.Marshal(map[string]any{
		"model":      ch.TestModel,
		"messages":   []map[string]string{{"role": "user", "content": "ping"}},
		"max_tokens": 1,
		"stream":     false,
	})
	start := time.Now()
	res := p.Chat(r.Context(), *ch, provider.ChatRequest{
		Model: ch.TestModel, Body: pingBody, Stream: false,
	})
	out := map[string]any{
		"latency_ms":   time.Since(start).Milliseconds(),
		"request_body": string(pingBody),
	}
	switch {
	case res.Err != nil:
		out["success"] = false
		out["error"] = res.Err.Error()
	case res.HTTPStatus >= 200 && res.HTTPStatus < 300:
		out["success"] = true
		out["http_status"] = res.HTTPStatus
		out["response_body"] = string(res.Body)
	default:
		out["success"] = false
		out["http_status"] = res.HTTPStatus
		out["response_body"] = string(res.Body)
		out["error"] = fmt.Sprintf("upstream returned %d", res.HTTPStatus)
	}
	writeJSON(w, http.StatusOK, out)
}
```

`handler.go` RegisterRoutes 的 channels 组内追加：

```go
				r.Post("/{id}/test", h.testChannel)
```

`channels.go` 需要新增 import：`"encoding/json"`、`"fmt"`、`"time"`、`"pengapi/internal/relay/provider"`。

- [ ] **Step 4: 运行测试确认通过**

Run: `go test ./internal/admin/`
Expected: PASS

- [ ] **Step 5: Commit**

```bash
git add -A
git commit -m "feat(admin): channel connectivity ping via configured test model"
```

---

### Task 17: 管理 API——日志查询

**Files:**
- Create: `internal/admin/logs.go`
- Modify: `internal/admin/handler.go`（RegisterRoutes 加路由）
- Modify: `internal/admin/admin_test.go`（追加测试）

**Interfaces:**
- Consumes: `store.ListLogs/GetLog/GetLogGroup`（Task 6）
- Produces:
  ```
  GET /api/logs?page=1&size=20&model=&status=&token_id=&channel_id=
    → {"data":[]LogEntry,"total":N,"page":P,"size":S}
  GET /api/logs/{id}
    → {"data":LogEntry,"attempts":[]LogEntry}（同 request_id 全部尝试，attempt 升序）
  ```

- [ ] **Step 1: 写失败测试（追加到 `admin_test.go`）**

```go
func TestLogsAPI(t *testing.T) {
	env := newAdminEnv(t)
	ctx := context.Background()

	// 造两条日志：同一 request_id 的两次尝试
	env.st.InsertLog(ctx, store.LogEntry{
		RequestID: "req-a", Attempt: 1, ModelRequested: "m1", ModelCanonical: "m1",
		ChannelName: "c1", Status: "failed", Error: "boom", RequestBody: "{}", ResponseBody: "err",
	})
	secondID, _ := env.st.InsertLog(ctx, store.LogEntry{
		RequestID: "req-a", Attempt: 2, ModelRequested: "m1", ModelCanonical: "m1",
		ChannelName: "c2", Status: "success", RequestBody: "{}", ResponseBody: "ok",
	})

	// 列表 + 分页字段
	resp := env.call(t, http.MethodGet, "/api/logs?page=1&size=10", "")
	v := readJSON(t, resp)
	if resp.StatusCode != 200 || v["total"].(float64) != 2 {
		t.Fatalf("list: %d %v", resp.StatusCode, v)
	}

	// 状态筛选
	resp = env.call(t, http.MethodGet, "/api/logs?status=success", "")
	v = readJSON(t, resp)
	if v["total"].(float64) != 1 {
		t.Errorf("status filter: %v", v["total"])
	}

	// 详情 + 降级链路
	resp = env.call(t, http.MethodGet, "/api/logs/"+itoa(secondID), "")
	v = readJSON(t, resp)
	if resp.StatusCode != 200 {
		t.Fatalf("detail: %d", resp.StatusCode)
	}
	attempts := v["attempts"].([]any)
	if len(attempts) != 2 || attempts[0].(map[string]any)["attempt"].(float64) != 1 {
		t.Errorf("attempts group: %v", attempts)
	}

	// 不存在 → 404
	resp = env.call(t, http.MethodGet, "/api/logs/999", "")
	readJSON(t, resp)
	if resp.StatusCode != 404 {
		t.Errorf("missing log: %d", resp.StatusCode)
	}
}
```

- [ ] **Step 2: 运行测试确认失败**

Run: `go test ./internal/admin/ -run TestLogsAPI`
Expected: FAIL

- [ ] **Step 3: 实现 `internal/admin/logs.go`**

```go
package admin

import (
	"net/http"
	"strconv"

	"pengapi/internal/store"

	"github.com/go-chi/chi/v5"
)

func (h *Handler) listLogs(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()
	f := store.LogFilter{
		Model:  q.Get("model"),
		Status: q.Get("status"),
	}
	f.Page, _ = strconv.Atoi(q.Get("page"))
	f.Size, _ = strconv.Atoi(q.Get("size"))
	if v := q.Get("token_id"); v != "" {
		if id, err := strconv.ParseInt(v, 10, 64); err == nil {
			f.TokenID = &id
		}
	}
	if v := q.Get("channel_id"); v != "" {
		if id, err := strconv.ParseInt(v, 10, 64); err == nil {
			f.ChannelID = &id
		}
	}
	logs, total, err := h.store.ListLogs(r.Context(), f)
	if err != nil {
		writeErr(w, http.StatusInternalServerError, "internal error")
		return
	}
	if f.Page < 1 {
		f.Page = 1
	}
	if f.Size < 1 || f.Size > 200 {
		f.Size = 20
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"data": logs, "total": total, "page": f.Page, "size": f.Size,
	})
}

func (h *Handler) getLog(w http.ResponseWriter, r *http.Request) {
	id, err := strconv.ParseInt(chi.URLParam(r, "id"), 10, 64)
	if err != nil {
		writeErr(w, http.StatusBadRequest, "invalid id")
		return
	}
	entry, err := h.store.GetLog(r.Context(), id)
	if err != nil {
		writeErr(w, http.StatusInternalServerError, "internal error")
		return
	}
	if entry == nil {
		writeErr(w, http.StatusNotFound, "log not found")
		return
	}
	attempts, err := h.store.GetLogGroup(r.Context(), entry.RequestID)
	if err != nil {
		writeErr(w, http.StatusInternalServerError, "internal error")
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"data": entry, "attempts": attempts})
}
```

`handler.go` RegisterRoutes 的 session 组内追加：

```go
			r.Get("/logs", h.listLogs)
			r.Get("/logs/{id}", h.getLog)
```

- [ ] **Step 4: 运行测试确认通过**

Run: `go test ./internal/admin/`
Expected: PASS

- [ ] **Step 5: Commit**

```bash
git add -A
git commit -m "feat(admin): log list with filters and detail with attempt group"
```

---

### Task 18: Web 管理 SPA（Vue3 嵌入）

**Files:**
- Create: `web/vendor/vue.global.prod.js`（curl 下载）
- Create: `web/index.html`
- Create: `web/app.js`
- Create: `web/style.css`
- Create: `web/embed.go`

**Interfaces:**
- Consumes: Task 12-17 全部 `/api/*` 端点
- Produces:
  ```go
  package web

  // 注册静态路由：GET /（index.html）、/app.js、/style.css、/vendor/*
  func RegisterRoutes(r chi.Router)
  ```

- [ ] **Step 1: 下载 Vue3 生产构建（vendor，不走 CDN）**

```bash
mkdir -p web/vendor
curl -L -o web/vendor/vue.global.prod.js https://unpkg.com/vue@3.5.13/dist/vue.global.prod.js
ls -la web/vendor/vue.global.prod.js  # 应约 150KB+
head -c 100 web/vendor/vue.global.prod.js  # 应以 JS 代码开头，不是 HTML 错误页
```

若网络不通，从 https://unpkg.com/vue@3.5.13/dist/vue.global.prod.js 手动下载放入（必须是有 compiler 的 global 构建，因为模板在 DOM 里）。

- [ ] **Step 2: 写 `web/index.html`**

```html
<!DOCTYPE html>
<html lang="zh-CN">
<head>
<meta charset="utf-8">
<meta name="viewport" content="width=device-width, initial-scale=1">
<title>peng_api 管理</title>
<link rel="stylesheet" href="/style.css">
</head>
<body>
<div id="app" v-cloak>
  <!-- 登录 -->
  <div v-if="!authed" class="login-wrap">
    <div class="card login-card">
      <h2>peng_api</h2>
      <input type="password" v-model="password" placeholder="管理密码" @keyup.enter="login" autofocus>
      <button class="primary block" @click="login">登录</button>
      <p class="err" v-if="loginErr">{{ loginErr }}</p>
    </div>
  </div>

  <div v-else>
    <nav class="nav">
      <span class="brand">peng_api</span>
      <a v-for="t in tabs" :key="t.key" :class="{on: tab===t.key}" @click="switchTab(t.key)">{{ t.label }}</a>
      <a class="right" @click="logout">退出</a>
    </nav>
    <p class="err global-err" v-if="err">{{ err }}</p>

    <!-- ============ 渠道 ============ -->
    <section v-if="tab==='channels'">
      <div class="bar">
        <h3>渠道</h3>
        <button class="primary" @click="openChannelForm(null)">新建渠道</button>
      </div>
      <table>
        <thead><tr>
          <th>ID</th><th>名称</th><th>地址</th><th>优先级</th><th>模型数</th><th>状态</th><th>连续失败</th><th>操作</th>
        </tr></thead>
        <tbody>
          <tr v-for="c in channels" :key="c.id">
            <td>{{ c.id }}</td>
            <td>{{ c.name }}</td>
            <td class="mono wrap">{{ c.base_url }}</td>
            <td>{{ c.priority }}</td>
            <td>{{ c.models.length }}</td>
            <td>
              <span v-if="!c.enabled" class="tag gray">已停用</span>
              <span v-else-if="c.auto_disabled" class="tag red" :title="'冷却至 ' + fmtTime(c.disabled_until)">自动禁用</span>
              <span v-else class="tag green">正常</span>
            </td>
            <td>{{ c.consecutive_failures }}</td>
            <td class="ops">
              <a @click="openChannelForm(c)">编辑</a>
              <a @click="fetchModels(c)">获取模型</a>
              <a @click="testChannel(c)">测试</a>
              <a v-if="c.auto_disabled" @click="resetChannel(c)">重置</a>
              <a @click="toggleChannel(c)">{{ c.enabled ? '停用' : '启用' }}</a>
              <a class="danger" @click="delChannel(c)">删除</a>
            </td>
          </tr>
          <tr v-if="!channels.length"><td colspan="8" class="empty">暂无渠道，点击右上角新建</td></tr>
        </tbody>
      </table>
    </section>

    <!-- ============ 模型与别名 ============ -->
    <section v-if="tab==='models'">
      <div class="bar">
        <h3>模型与别名</h3>
        <button class="primary" @click="openModelForm(null)">新建模型</button>
      </div>
      <table>
        <thead><tr>
          <th>ID</th><th>标准名</th><th>别名</th><th>上下文长度</th><th>最大输出</th><th>渠道数</th><th>操作</th>
        </tr></thead>
        <tbody>
          <tr v-for="m in models" :key="m.id">
            <td>{{ m.id }}</td>
            <td class="mono">{{ m.name }}</td>
            <td class="mono wrap">{{ (m.aliases||[]).join(', ') || '-' }}</td>
            <td>{{ m.context_length ?? '-' }}</td>
            <td>{{ m.max_output_tokens ?? '-' }}</td>
            <td>{{ m.bound_channels }}</td>
            <td class="ops">
              <a @click="openModelForm(m)">编辑</a>
              <a class="danger" @click="delModel(m)">删除</a>
            </td>
          </tr>
          <tr v-if="!models.length"><td colspan="7" class="empty">暂无模型</td></tr>
        </tbody>
      </table>
    </section>

    <!-- ============ Token ============ -->
    <section v-if="tab==='tokens'">
      <div class="bar">
        <h3>API Token</h3>
        <div>
          <input v-model="newTokenName" placeholder="名称（如 cline）">
          <button class="primary" @click="createToken">新建 Token</button>
        </div>
      </div>
      <table>
        <thead><tr><th>ID</th><th>名称</th><th>Token</th><th>状态</th><th>创建时间</th><th>操作</th></tr></thead>
        <tbody>
          <tr v-for="t in tokens" :key="t.id">
            <td>{{ t.id }}</td>
            <td>{{ t.name }}</td>
            <td class="mono">{{ t.token }}</td>
            <td><span class="tag" :class="t.enabled ? 'green' : 'gray'">{{ t.enabled ? '启用' : '禁用' }}</span></td>
            <td>{{ fmtTime(t.created_at) }}</td>
            <td class="ops">
              <a @click="toggleToken(t)">{{ t.enabled ? '禁用' : '启用' }}</a>
              <a class="danger" @click="delToken(t)">删除</a>
            </td>
          </tr>
          <tr v-if="!tokens.length"><td colspan="6" class="empty">暂无 token</td></tr>
        </tbody>
      </table>
    </section>

    <!-- ============ 日志 ============ -->
    <section v-if="tab==='logs'">
      <div class="bar">
        <h3>请求日志</h3>
        <div class="filters">
          <input v-model="logFilter.model" placeholder="模型（模糊）">
          <select v-model="logFilter.status">
            <option value="">全部状态</option>
            <option value="success">success</option>
            <option value="failed">failed</option>
          </select>
          <select v-model="logFilter.token_id">
            <option value="">全部 token</option>
            <option v-for="t in tokens" :key="t.id" :value="String(t.id)">{{ t.name }}</option>
          </select>
          <select v-model="logFilter.channel_id">
            <option value="">全部渠道</option>
            <option v-for="c in channels" :key="c.id" :value="String(c.id)">{{ c.name }}</option>
          </select>
          <button class="primary" @click="searchLogs">查询</button>
        </div>
      </div>
      <table>
        <thead><tr>
          <th>时间</th><th>模型</th><th>渠道</th><th>Token</th><th>状态</th><th>HTTP</th><th>耗时</th><th>tokens</th><th>操作</th>
        </tr></thead>
        <tbody>
          <tr v-for="l in logs.data" :key="l.id">
            <td class="nowrap">{{ fmtTime(l.created_at) }}</td>
            <td class="mono">{{ l.model_requested }}<template v-if="l.model_canonical && l.model_canonical !== l.model_requested"> → {{ l.model_canonical }}</template></td>
            <td>{{ l.channel_name || '-' }}<span class="dim">#{{ l.attempt }}</span></td>
            <td>{{ l.token_name || '-' }}</td>
            <td><span class="tag" :class="l.status==='success' ? 'green' : 'red'">{{ l.status }}</span></td>
            <td>{{ l.http_status ?? '-' }}</td>
            <td>{{ l.latency_ms }}ms</td>
            <td>{{ l.prompt_tokens != null ? (l.prompt_tokens + '+' + l.completion_tokens) : '-' }}</td>
            <td class="ops"><a @click="openLogDetail(l)">详情</a></td>
          </tr>
          <tr v-if="!logs.data.length"><td colspan="9" class="empty">暂无日志</td></tr>
        </tbody>
      </table>
      <div class="pager">
        <button :disabled="logs.page<=1" @click="pageLogs(-1)">上一页</button>
        <span>第 {{ logs.page }} 页 / 共 {{ logs.total }} 条</span>
        <button :disabled="logs.page>=logPages" @click="pageLogs(1)">下一页</button>
      </div>
    </section>

    <!-- ============ 渠道编辑弹窗 ============ -->
    <div class="mask" v-if="chForm" @click.self="chForm=null">
      <div class="card modal">
        <h3>{{ chForm.id ? '编辑渠道' : '新建渠道' }}</h3>
        <label>名称<input v-model="chForm.name"></label>
        <label>上游地址（base_url）<input v-model="chForm.base_url" placeholder="https://api.example.com/v1"></label>
        <label>API Key<input v-model="chForm.api_key"></label>
        <label>优先级（越大越优先）<input type="number" v-model.number="chForm.priority"></label>
        <label>测试模型（连通性测试用，选便宜的）
          <select v-model="chForm.test_model">
            <option value="">-- 未配置 --</option>
            <option v-for="opt in testModelOptions" :key="opt" :value="opt">{{ opt }}</option>
          </select>
        </label>
        <label class="row"><input type="checkbox" v-model="chForm.enabled"> 启用</label>
        <h4>模型绑定</h4>
        <table>
          <thead><tr><th>标准模型</th><th>上游模型名（空=用标准名）</th><th></th></tr></thead>
          <tbody>
            <tr v-for="(b, i) in chForm.models" :key="i">
              <td>
                <select v-model.number="b.model_id">
                  <option v-for="m in models" :key="m.id" :value="m.id">{{ m.name }}</option>
                </select>
              </td>
              <td><input v-model="b.upstream_model" placeholder="留空则用标准名"></td>
              <td><a class="danger" @click="chForm.models.splice(i,1)">移除</a></td>
            </tr>
          </tbody>
        </table>
        <button @click="addBinding">添加绑定</button>
        <div class="actions">
          <button class="primary" @click="saveChannel">保存</button>
          <button @click="chForm=null">取消</button>
        </div>
      </div>
    </div>

    <!-- ============ 获取模型弹窗 ============ -->
    <div class="mask" v-if="fetchDlg" @click.self="fetchDlg=null">
      <div class="card modal wide">
        <h3>获取到的上游模型</h3>
        <p class="dim">勾选要绑定的模型，并为每个选择对应的标准模型（或新建）。</p>
        <table>
          <thead><tr><th></th><th>上游模型</th><th>上下文</th><th>绑定到</th></tr></thead>
          <tbody>
            <tr v-for="r in fetchDlg.rows" :key="r.upstream_id">
              <td><input type="checkbox" v-model="r.checked"></td>
              <td class="mono">{{ r.upstream_id }}</td>
              <td>{{ r.context_length ?? '-' }}</td>
              <td>
                <select v-model="r.target">
                  <option value="__new__">＋新建：{{ r.suggested_name }}</option>
                  <option v-for="m in models" :key="m.id" :value="m.id">{{ m.name }}</option>
                </select>
              </td>
            </tr>
          </tbody>
        </table>
        <div class="actions">
          <button class="primary" @click="saveFetch">绑定选中项</button>
          <button @click="fetchDlg=null">取消</button>
        </div>
      </div>
    </div>

    <!-- ============ 测试结果弹窗 ============ -->
    <div class="mask" v-if="testResult" @click.self="testResult=null">
      <div class="card modal wide">
        <h3>连通性测试：{{ testResult.channel }}</h3>
        <p v-if="testResult.loading">测试中…</p>
        <template v-else>
          <p>
            <span class="tag" :class="testResult.result.success ? 'green' : 'red'">
              {{ testResult.result.success ? '成功' : '失败' }}
            </span>
            耗时 {{ testResult.result.latency_ms }}ms
            <template v-if="testResult.result.http_status">HTTP {{ testResult.result.http_status }}</template>
          </p>
          <p class="err" v-if="testResult.result.error">{{ testResult.result.error }}</p>
          <h4>请求</h4><pre>{{ pretty(testResult.result.request_body) }}</pre>
          <h4>响应</h4><pre>{{ pretty(testResult.result.response_body || '') }}</pre>
        </template>
        <div class="actions"><button @click="testResult=null">关闭</button></div>
      </div>
    </div>

    <!-- ============ 模型编辑弹窗 ============ -->
    <div class="mask" v-if="modelForm" @click.self="modelForm=null">
      <div class="card modal">
        <h3>{{ modelForm.id ? '编辑模型' : '新建模型' }}</h3>
        <label>标准名<input v-model="modelForm.name" placeholder="如 glm-5.3"></label>
        <label>别名（每行一个，匹配大小写不敏感）
          <textarea v-model="modelForm.aliasesText" rows="4" placeholder="GLM-5.3&#10;glm5.3"></textarea>
        </label>
        <label>上下文长度（可空）<input type="number" v-model.number="modelForm.context_length"></label>
        <label>最大输出 tokens（可空）<input type="number" v-model.number="modelForm.max_output_tokens"></label>
        <div class="actions">
          <button class="primary" @click="saveModel">保存</button>
          <button @click="modelForm=null">取消</button>
        </div>
      </div>
    </div>

    <!-- ============ 新 token 弹窗 ============ -->
    <div class="mask" v-if="createdToken" @click.self="createdToken=null">
      <div class="card modal">
        <h3>Token 已创建</h3>
        <p class="err">这是唯一一次完整显示，请立即复制保存：</p>
        <pre class="token-full">{{ createdToken.token }}</pre>
        <div class="actions"><button class="primary" @click="copyToken">复制</button><button @click="createdToken=null">关闭</button></div>
      </div>
    </div>

    <!-- ============ 日志详情弹窗 ============ -->
    <div class="mask" v-if="logDetail" @click.self="logDetail=null">
      <div class="card modal wide">
        <h3>请求详情 <span class="dim mono">{{ logDetail.data.request_id }}</span></h3>
        <div class="attempt-tabs">
          <button v-for="a in logDetail.attempts" :key="a.id"
                  :class="{on: logDetail.sel===a.id}" @click="logDetail.sel=a.id">
            第{{ a.attempt }}次 {{ a.channel_name }}
            <span :class="a.status==='success'?'green-text':'red-text'">{{ a.status }}</span>
          </button>
        </div>
        <template v-if="selAttempt">
          <p class="dim">
            HTTP {{ selAttempt.http_status ?? '-' }} · {{ selAttempt.latency_ms }}ms ·
            {{ selAttempt.stream ? '流式' : '非流式' }} · {{ fmtTime(selAttempt.created_at) }}
          </p>
          <p class="err" v-if="selAttempt.error">{{ selAttempt.error }}</p>
          <h4>请求体</h4><pre>{{ pretty(selAttempt.request_body) }}</pre>
          <h4>响应体</h4><pre>{{ pretty(selAttempt.response_body) }}</pre>
        </template>
        <div class="actions"><button @click="logDetail=null">关闭</button></div>
      </div>
    </div>
  </div>
</div>
<script src="/vendor/vue.global.prod.js"></script>
<script src="/app.js"></script>
</body>
</html>
```

- [ ] **Step 3: 写 `web/app.js`**

```js
const { createApp } = Vue;

async function api(path, opts = {}) {
  const r = await fetch('/api' + path, {
    method: opts.method || 'GET',
    headers: { 'Content-Type': 'application/json' },
    body: opts.body !== undefined ? JSON.stringify(opts.body) : undefined,
  });
  let data = {};
  try { data = await r.json(); } catch (e) { /* 空响应 */ }
  if (!r.ok) {
    const err = new Error(data.error || ('HTTP ' + r.status));
    err.status = r.status;
    throw err;
  }
  return data;
}

createApp({
  data() {
    return {
      authed: false,
      password: '',
      loginErr: '',
      err: '',
      tab: 'channels',
      tabs: [
        { key: 'channels', label: '渠道' },
        { key: 'models', label: '模型与别名' },
        { key: 'tokens', label: 'Token' },
        { key: 'logs', label: '日志' },
      ],
      channels: [],
      models: [],
      tokens: [],
      logs: { data: [], total: 0, page: 1, size: 20 },
      logFilter: { model: '', status: '', token_id: '', channel_id: '' },
      chForm: null,
      fetchDlg: null,
      testResult: null,
      modelForm: null,
      newTokenName: '',
      createdToken: null,
      logDetail: null,
    };
  },
  computed: {
    testModelOptions() {
      if (!this.chForm) return [];
      return this.chForm.models
        .map(b => {
          const m = this.models.find(x => x.id === b.model_id);
          return b.upstream_model || (m ? m.name : '');
        })
        .filter(Boolean);
    },
    logPages() {
      return Math.max(1, Math.ceil(this.logs.total / this.logs.size));
    },
    selAttempt() {
      if (!this.logDetail) return null;
      return this.logDetail.attempts.find(a => a.id === this.logDetail.sel) || this.logDetail.data;
    },
  },
  methods: {
    async guard(fn) {
      try {
        await fn();
      } catch (e) {
        if (e.status === 401) { this.authed = false; return; }
        this.err = e.message;
        setTimeout(() => { this.err = ''; }, 5000);
      }
    },
    async login() {
      this.loginErr = '';
      try {
        await api('/login', { method: 'POST', body: { password: this.password } });
        this.authed = true;
        this.password = '';
        await this.loadAll();
      } catch (e) {
        this.loginErr = '登录失败：' + e.message;
      }
    },
    async logout() {
      await this.guard(async () => { await api('/logout', { method: 'POST' }); });
      this.authed = false;
    },
    switchTab(k) {
      this.tab = k;
      if (k === 'logs') this.loadLogs();
    },
    async loadAll() {
      await this.guard(async () => {
        const [ch, m, tk] = await Promise.all([
          api('/channels'), api('/models'), api('/tokens'),
        ]);
        this.channels = ch.data || [];
        this.models = m.data || [];
        this.tokens = tk.data || [];
        await this.loadLogs();
      });
    },
    async loadChannels() { await this.guard(async () => { this.channels = (await api('/channels')).data || []; }); },
    async loadModels() { await this.guard(async () => { this.models = (await api('/models')).data || []; }); },
    async loadTokens() { await this.guard(async () => { this.tokens = (await api('/tokens')).data || []; }); },
    async loadLogs() {
      await this.guard(async () => {
        const q = new URLSearchParams({ page: this.logs.page, size: this.logs.size });
        if (this.logFilter.model) q.set('model', this.logFilter.model);
        if (this.logFilter.status) q.set('status', this.logFilter.status);
        if (this.logFilter.token_id) q.set('token_id', this.logFilter.token_id);
        if (this.logFilter.channel_id) q.set('channel_id', this.logFilter.channel_id);
        const v = await api('/logs?' + q);
        this.logs.data = v.data || [];
        this.logs.total = v.total;
      });
    },
    searchLogs() { this.logs.page = 1; this.loadLogs(); },
    pageLogs(d) { this.logs.page += d; this.loadLogs(); },

    // ---- 渠道 ----
    openChannelForm(c) {
      if (c) {
        this.chForm = {
          id: c.id, name: c.name, base_url: c.base_url, api_key: c.api_key,
          priority: c.priority, enabled: c.enabled, test_model: c.test_model,
          models: (c.models || []).map(b => ({ model_id: b.model_id, upstream_model: b.upstream_model })),
        };
      } else {
        this.chForm = { name: '', base_url: '', api_key: '', priority: 0, enabled: true, test_model: '', models: [] };
      }
    },
    addBinding() {
      this.chForm.models.push({ model_id: this.models.length ? this.models[0].id : 0, upstream_model: '' });
    },
    async saveChannel() {
      await this.guard(async () => {
        const f = this.chForm;
        const body = {
          name: f.name, base_url: f.base_url, api_key: f.api_key,
          priority: f.priority, enabled: f.enabled, test_model: f.test_model,
          models: f.models.filter(b => b.model_id),
        };
        if (f.id) await api('/channels/' + f.id, { method: 'PUT', body });
        else await api('/channels', { method: 'POST', body });
        this.chForm = null;
        await this.loadChannels();
      });
    },
    async delChannel(c) {
      if (!confirm('删除渠道「' + c.name + '」？')) return;
      await this.guard(async () => {
        await api('/channels/' + c.id, { method: 'DELETE' });
        await this.loadChannels();
      });
    },
    async toggleChannel(c) {
      await this.guard(async () => {
        await api('/channels/' + c.id + '/toggle', { method: 'POST', body: { enabled: !c.enabled } });
        await this.loadChannels();
      });
    },
    async resetChannel(c) {
      await this.guard(async () => {
        await api('/channels/' + c.id + '/reset', { method: 'POST' });
        await this.loadChannels();
      });
    },
    async fetchModels(c) {
      await this.guard(async () => {
        const v = await api('/channels/' + c.id + '/fetch-models', { method: 'POST' });
        this.fetchDlg = {
          channelID: c.id,
          rows: (v.data || []).map(r => ({
            checked: true,
            upstream_id: r.upstream_id,
            context_length: r.context_length,
            max_output_tokens: r.max_output_tokens,
            suggested_name: r.suggested_name,
            target: r.suggested_model_id || '__new__',
          })),
        };
      });
    },
    async saveFetch() {
      await this.guard(async () => {
        const bindings = this.fetchDlg.rows.filter(r => r.checked).map(r => {
          const b = { upstream_model: r.upstream_id, context_length: r.context_length, max_output_tokens: r.max_output_tokens };
          if (r.target === '__new__') b.new_model_name = r.suggested_name;
          else b.model_id = r.target;
          return b;
        });
        if (!bindings.length) { this.fetchDlg = null; return; }
        await api('/channels/' + this.fetchDlg.channelID + '/models', { method: 'POST', body: { bindings } });
        this.fetchDlg = null;
        await Promise.all([this.loadChannels(), this.loadModels()]);
      });
    },
    async testChannel(c) {
      this.testResult = { loading: true, channel: c.name, result: null };
      await this.guard(async () => {
        const v = await api('/channels/' + c.id + '/test', { method: 'POST' });
        this.testResult = { loading: false, channel: c.name, result: v };
      });
      if (this.testResult && this.testResult.loading) this.testResult = null;
    },

    // ---- 模型 ----
    openModelForm(m) {
      if (m) {
        this.modelForm = {
          id: m.id, name: m.name, aliasesText: (m.aliases || []).join('\n'),
          context_length: m.context_length, max_output_tokens: m.max_output_tokens,
        };
      } else {
        this.modelForm = { name: '', aliasesText: '', context_length: null, max_output_tokens: null };
      }
    },
    async saveModel() {
      await this.guard(async () => {
        const f = this.modelForm;
        const body = {
          name: f.name,
          aliases: f.aliasesText.split('\n').map(s => s.trim()).filter(Boolean),
          context_length: f.context_length || null,
          max_output_tokens: f.max_output_tokens || null,
        };
        if (f.id) await api('/models/' + f.id, { method: 'PUT', body });
        else await api('/models', { method: 'POST', body });
        this.modelForm = null;
        await this.loadModels();
      });
    },
    async delModel(m) {
      if (!confirm('删除模型「' + m.name + '」？相关渠道绑定会一并移除。')) return;
      await this.guard(async () => {
        await api('/models/' + m.id, { method: 'DELETE' });
        await Promise.all([this.loadModels(), this.loadChannels()]);
      });
    },

    // ---- Token ----
    async createToken() {
      if (!this.newTokenName.trim()) return;
      await this.guard(async () => {
        const v = await api('/tokens', { method: 'POST', body: { name: this.newTokenName.trim() } });
        this.createdToken = v;
        this.newTokenName = '';
        await this.loadTokens();
      });
    },
    async toggleToken(t) {
      await this.guard(async () => {
        await api('/tokens/' + t.id, { method: 'PUT', body: { name: t.name, enabled: !t.enabled } });
        await this.loadTokens();
      });
    },
    async delToken(t) {
      if (!confirm('删除 token「' + t.name + '」？使用它的客户端会立即失效。')) return;
      await this.guard(async () => {
        await api('/tokens/' + t.id, { method: 'DELETE' });
        await this.loadTokens();
      });
    },
    copyToken() {
      navigator.clipboard && navigator.clipboard.writeText(this.createdToken.token);
    },

    // ---- 日志 ----
    async openLogDetail(l) {
      await this.guard(async () => {
        const v = await api('/logs/' + l.id);
        this.logDetail = { data: v.data, attempts: v.attempts || [v.data], sel: v.data.id };
      });
    },

    // ---- 工具 ----
    fmtTime(s) {
      if (!s) return '-';
      const d = new Date(s);
      return isNaN(d) ? s : d.toLocaleString();
    },
    pretty(s) {
      if (!s) return '(空)';
      try { return JSON.stringify(JSON.parse(s), null, 2); } catch (e) { return s; }
    },
  },
  async mounted() {
    try {
      await api('/me');
      this.authed = true;
      await this.loadAll();
    } catch (e) { /* 未登录，停在登录页 */ }
  },
}).mount('#app');
```

- [ ] **Step 4: 写 `web/style.css`**

```css
* { box-sizing: border-box; }
body { margin: 0; font-family: -apple-system, "Segoe UI", "PingFang SC", "Microsoft YaHei", sans-serif; background: #f5f6f8; color: #222; }
[v-cloak] { display: none; }
.mono { font-family: ui-monospace, Consolas, monospace; font-size: 12px; }
.wrap { word-break: break-all; max-width: 260px; }
.nowrap { white-space: nowrap; }
.dim { color: #888; font-size: 12px; }
.err { color: #c0392b; }
.global-err { margin: 8px 16px; }

.login-wrap { display: flex; align-items: center; justify-content: center; min-height: 100vh; }
.login-card { width: 320px; }
.login-card input { width: 100%; margin-bottom: 12px; }

.card { background: #fff; border-radius: 8px; padding: 20px; box-shadow: 0 1px 4px rgba(0,0,0,.08); }

.nav { display: flex; align-items: center; gap: 18px; background: #1f2937; color: #cbd5e1; padding: 0 20px; height: 48px; }
.nav .brand { font-weight: 700; color: #fff; margin-right: 12px; }
.nav a { cursor: pointer; padding: 4px 6px; }
.nav a.on { color: #fff; border-bottom: 2px solid #3b82f6; }
.nav a.right { margin-left: auto; }

section { padding: 16px 20px; }
.bar { display: flex; justify-content: space-between; align-items: center; margin-bottom: 12px; flex-wrap: wrap; gap: 8px; }
.bar h3 { margin: 0; }
.filters { display: flex; gap: 8px; flex-wrap: wrap; }

table { width: 100%; border-collapse: collapse; background: #fff; border-radius: 8px; overflow: hidden; box-shadow: 0 1px 3px rgba(0,0,0,.06); }
th, td { text-align: left; padding: 8px 10px; border-bottom: 1px solid #eee; font-size: 14px; vertical-align: top; }
th { background: #fafafa; color: #555; font-weight: 600; }
.empty { text-align: center; color: #999; padding: 24px; }

.ops a { color: #2563eb; cursor: pointer; margin-right: 10px; white-space: nowrap; }
.ops a.danger, a.danger { color: #dc2626; cursor: pointer; }

.tag { display: inline-block; padding: 1px 8px; border-radius: 10px; font-size: 12px; }
.tag.green { background: #dcfce7; color: #15803d; }
.tag.red { background: #fee2e2; color: #b91c1c; }
.tag.gray { background: #e5e7eb; color: #4b5563; }
.green-text { color: #15803d; } .red-text { color: #b91c1c; }

input, select, textarea { padding: 6px 8px; border: 1px solid #d1d5db; border-radius: 6px; font-size: 14px; }
button { padding: 6px 14px; border: 1px solid #d1d5db; border-radius: 6px; background: #fff; cursor: pointer; font-size: 14px; }
button.primary { background: #2563eb; border-color: #2563eb; color: #fff; }
button.block { width: 100%; }
button:disabled { opacity: .5; cursor: not-allowed; }

.mask { position: fixed; inset: 0; background: rgba(0,0,0,.4); display: flex; align-items: flex-start; justify-content: center; padding: 40px 16px; z-index: 10; overflow-y: auto; }
.modal { width: 560px; max-height: 85vh; overflow-y: auto; }
.modal.wide { width: 860px; }
.modal label { display: block; margin-bottom: 10px; font-size: 13px; color: #555; }
.modal label input, .modal label select, .modal label textarea { display: block; width: 100%; margin-top: 4px; }
.modal label.row input { display: inline; width: auto; }
.modal h4 { margin: 14px 0 6px; }
.actions { margin-top: 16px; display: flex; gap: 10px; }
pre { background: #0f172a; color: #e2e8f0; padding: 12px; border-radius: 6px; overflow: auto; max-height: 320px; font-size: 12px; white-space: pre-wrap; word-break: break-all; }
.token-full { font-size: 14px; user-select: all; }
.pager { margin-top: 12px; display: flex; gap: 12px; align-items: center; }
.attempt-tabs { display: flex; gap: 8px; margin-bottom: 10px; flex-wrap: wrap; }
.attempt-tabs button.on { border-color: #2563eb; color: #2563eb; }
```

- [ ] **Step 5: 写 `web/embed.go` 并验证静态路由**

```go
package web

import (
	"embed"
	"net/http"

	"github.com/go-chi/chi/v5"
)

//go:embed index.html app.js style.css vendor
var content embed.FS

var indexHTML, _ = content.ReadFile("index.html")

func RegisterRoutes(r chi.Router) {
	assets := http.FileServer(http.FS(content))
	r.Get("/", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		w.Write(indexHTML)
	})
	r.Get("/app.js", assets.ServeHTTP)
	r.Get("/style.css", assets.ServeHTTP)
	r.Get("/vendor/*", assets.ServeHTTP)
}
```

验证（此时 main.go 还是 Task 1 的占位，先用临时测试验证）——写 `web/embed_test.go`：

```go
package web

import (
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/go-chi/chi/v5"
)

func TestStaticRoutes(t *testing.T) {
	r := chi.NewRouter()
	RegisterRoutes(r)
	srv := httptest.NewServer(r)
	defer srv.Close()

	for _, path := range []string{"/", "/app.js", "/style.css", "/vendor/vue.global.prod.js"} {
		resp, err := http.Get(srv.URL + path)
		if err != nil {
			t.Fatalf("GET %s: %v", path, err)
		}
		body, _ := io.ReadAll(resp.Body)
		resp.Body.Close()
		if resp.StatusCode != 200 || len(body) == 0 {
			t.Errorf("GET %s: status=%d len=%d", path, resp.StatusCode, len(body))
		}
		if path == "/" && !strings.Contains(string(body), `id="app"`) {
			t.Error("index.html should contain #app")
		}
	}
}
```

- [ ] **Step 6: 运行测试确认通过**

Run: `go test ./web/ && go build ./...`
Expected: PASS

- [ ] **Step 7: Commit**

```bash
git add -A
git commit -m "feat(web): embedded vue SPA with channels/models/tokens/logs tabs"
```

---

### Task 19: main 装配、日志清理、优雅退出、端到端集成测试

**Files:**
- Modify: `main.go`（替换 Task 1 占位）
- Create: `main_test.go`

**Interfaces:**
- Consumes: 前面所有任务
- Produces:
  ```go
  // main 包
  func buildHandler(cfg config.Config, st *store.Store, engine *relay.Engine, adminH *admin.Handler) http.Handler
  func main() // config.Parse → store.Open → 日志清理（启动+24h ticker）→ provider/engine/admin 装配 → server + 信号优雅退出
  ```

- [ ] **Step 1: 写端到端测试 `main_test.go`**

```go
package main

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"pengapi/internal/admin"
	"pengapi/internal/auth"
	"pengapi/internal/config"
	"pengapi/internal/relay"
	"pengapi/internal/relay/provider"
	"pengapi/internal/store"
)

func TestEndToEnd(t *testing.T) {
	// 假上游
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		io.WriteString(w, `{"id":"c1","choices":[{"message":{"role":"assistant","content":"pong"}}],"usage":{"prompt_tokens":5,"completion_tokens":1}}`)
	}))
	defer upstream.Close()

	st, err := store.Open(":memory:")
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	defer st.Close()

	cfg := config.Config{
		Addr: ":0", AdminPassword: "pw", LogRetentionDays: 30, FailThreshold: 2,
		RequestTimeout: 10 * time.Second, ConnectTimeout: 2 * time.Second,
		StreamFirstByteTimeout: 2 * time.Second,
	}
	reg := provider.NewRegistry(provider.NewOpenAI(&http.Client{}, cfg.RequestTimeout, cfg.StreamFirstByteTimeout))
	engine := relay.NewEngine(st, reg, cfg.FailThreshold)
	sessions := auth.NewSessionStore(time.Hour)
	adminH := admin.New(st, sessions, cfg.AdminPassword, reg)

	srv := httptest.NewServer(buildHandler(cfg, st, engine, adminH))
	defer srv.Close()
	ctx := context.Background()

	// 数据准备
	cl := int64(131072)
	m, _ := st.CreateModel(ctx, "glm-5.3", []string{"GLM5.3"}, &cl, nil)
	ch, _ := st.CreateChannel(ctx, store.Channel{
		Name: "up", Type: "openai", BaseURL: upstream.URL, APIKey: "k", Priority: 1,
	})
	st.BindModels(ctx, ch.ID, []store.BindingInput{{UpstreamModel: "", ModelID: &m.ID}})
	tok, _ := st.CreateToken(ctx, "e2e")

	// 1. 客户端：POST /v1/chat/completions（用别名）
	req, _ := http.NewRequest(http.MethodPost, srv.URL+"/v1/chat/completions",
		strings.NewReader(`{"model":"GLM5.3","messages":[{"role":"user","content":"ping"}]}`))
	req.Header.Set("Authorization", "Bearer "+tok.Token)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("chat: %v", err)
	}
	body := readAll(t, resp)
	if resp.StatusCode != 200 || !strings.Contains(body, "pong") {
		t.Fatalf("chat: %d %s", resp.StatusCode, body)
	}

	// 2. 客户端：GET /v1/models（带 context_length）
	req, _ = http.NewRequest(http.MethodGet, srv.URL+"/v1/models", nil)
	req.Header.Set("Authorization", "Bearer "+tok.Token)
	resp, _ = http.DefaultClient.Do(req)
	body = readAll(t, resp)
	if resp.StatusCode != 200 || !strings.Contains(body, `"glm-5.3"`) || !strings.Contains(body, `"context_length":131072`) {
		t.Fatalf("models: %d %s", resp.StatusCode, body)
	}

	// 3. 管理端：登录 → 查日志
	lreq, _ := http.NewRequest(http.MethodPost, srv.URL+"/api/login", strings.NewReader(`{"password":"pw"}`))
	lreq.Header.Set("Content-Type", "application/json")
	lresp, _ := http.DefaultClient.Do(lreq)
	var cookie *http.Cookie
	for _, c := range lresp.Cookies() {
		if c.Name == auth.SessionCookie {
			cookie = c
		}
	}
	lresp.Body.Close()
	if cookie == nil {
		t.Fatal("no session cookie")
	}
	req, _ = http.NewRequest(http.MethodGet, srv.URL+"/api/logs", nil)
	req.AddCookie(cookie)
	resp, _ = http.DefaultClient.Do(req)
	var logsPage struct {
		Total int `json:"total"`
	}
	json.NewDecoder(resp.Body).Decode(&logsPage)
	resp.Body.Close()
	if logsPage.Total != 1 {
		t.Fatalf("expected 1 log, got %d", logsPage.Total)
	}

	// 4. SPA 静态资源
	resp, _ = http.Get(srv.URL + "/")
	body = readAll(t, resp)
	if resp.StatusCode != 200 || !strings.Contains(body, `id="app"`) {
		t.Fatalf("index: %d", resp.StatusCode)
	}
	resp, _ = http.Get(srv.URL + "/vendor/vue.global.prod.js")
	resp.Body.Close()
	if resp.StatusCode != 200 {
		t.Fatalf("vue vendor: %d", resp.StatusCode)
	}
}

func readAll(t *testing.T, resp *http.Response) string {
	t.Helper()
	defer resp.Body.Close()
	b, _ := io.ReadAll(resp.Body)
	return string(b)
}
```

- [ ] **Step 2: 运行测试确认失败**

Run: `go test .`
Expected: FAIL（buildHandler 未定义）

- [ ] **Step 3: 实现 `main.go`（全量替换占位）**

```go
package main

import (
	"context"
	"log"
	"net"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"

	"pengapi/internal/admin"
	"pengapi/internal/auth"
	"pengapi/internal/config"
	"pengapi/internal/relay"
	"pengapi/internal/relay/provider"
	"pengapi/internal/store"
	"pengapi/web"

	"github.com/go-chi/chi/v5"
	"github.com/go-chi/chi/v5/middleware"
)

func buildHandler(cfg config.Config, st *store.Store, engine *relay.Engine, adminH *admin.Handler) http.Handler {
	r := chi.NewRouter()
	r.Use(middleware.Recoverer) // 请求路径 panic 兜底 → 500
	r.Route("/v1", func(r chi.Router) {
		r.Use(auth.Bearer(st))
		r.Post("/chat/completions", engine.ChatCompletions)
		r.Get("/models", engine.Models)
	})
	adminH.RegisterRoutes(r)
	web.RegisterRoutes(r)
	return r
}

func main() {
	cfg, err := config.Parse(os.Args[1:])
	if err != nil {
		log.Fatal("config: ", err)
	}
	st, err := store.Open(cfg.DBPath)
	if err != nil {
		log.Fatal("store: ", err)
	}
	defer st.Close()

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	// 日志清理：启动时一次 + 每 24h
	cleanLogs := func() {
		cutoff := time.Now().UTC().AddDate(0, 0, -cfg.LogRetentionDays)
		cctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
		defer cancel()
		if n, err := st.DeleteLogsBefore(cctx, cutoff); err != nil {
			log.Printf("log cleanup: %v", err)
		} else if n > 0 {
			log.Printf("log cleanup: deleted %d rows", n)
		}
	}
	cleanLogs()
	go func() {
		ticker := time.NewTicker(24 * time.Hour)
		defer ticker.Stop()
		for {
			select {
			case <-ticker.C:
				cleanLogs()
			case <-ctx.Done():
				return
			}
		}
	}()

	// 上游 http.Client：连接超时走 Transport；整体/首字节超时由 provider 控制
	// 不设 Client.Timeout（会杀死流式响应）
	upstreamClient := &http.Client{Transport: &http.Transport{
		DialContext:           (&net.Dialer{Timeout: cfg.ConnectTimeout}).DialContext,
		MaxIdleConns:          100,
		IdleConnTimeout:       90 * time.Second,
		TLSHandshakeTimeout:   10 * time.Second,
		ResponseHeaderTimeout: 0,
	}}
	providers := provider.NewRegistry(
		provider.NewOpenAI(upstreamClient, cfg.RequestTimeout, cfg.StreamFirstByteTimeout),
	)
	engine := relay.NewEngine(st, providers, cfg.FailThreshold)
	sessions := auth.NewSessionStore(7 * 24 * time.Hour)
	adminH := admin.New(st, sessions, cfg.AdminPassword, providers)

	srv := &http.Server{
		Addr:              cfg.Addr,
		Handler:           buildHandler(cfg, st, engine, adminH),
		ReadHeaderTimeout: 30 * time.Second,
	}
	go func() {
		log.Printf("peng_api listening on %s (db: %s)", cfg.Addr, cfg.DBPath)
		if err := srv.ListenAndServe(); err != nil && err != http.ErrServerClosed {
			log.Fatal(err)
		}
	}()
	<-ctx.Done()
	log.Println("shutting down...")
	sctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	srv.Shutdown(sctx)
}
```

- [ ] **Step 4: 运行全部测试 + 构建**

Run: `go test ./... -race && go build -o peng_api . && go vet ./...`
Expected: 全部 PASS，生成二进制，vet 无输出

- [ ] **Step 5: 手动冒烟（真实二进制）**

```bash
PENG_ADMIN_PASSWORD=test123 ./peng_api -addr :18080 -db /tmp/peng_smoke.db &
sleep 1
# 登录拿 cookie
curl -s -c /tmp/ck -X POST localhost:18080/api/login -H 'Content-Type: application/json' -d '{"password":"test123"}'
# 建模型
curl -s -b /tmp/ck -X POST localhost:18080/api/models -H 'Content-Type: application/json' -d '{"name":"test-model","aliases":["TEST-MODEL"]}'
# 建 token
curl -s -b /tmp/ck -X POST localhost:18080/api/tokens -H 'Content-Type: application/json' -d '{"name":"smoke"}'
# 无渠道时请求 → 503 no available channel
curl -s -X POST localhost:18080/v1/chat/completions -H "Authorization: Bearer <上面返回的token>" -H 'Content-Type: application/json' -d '{"model":"TEST-MODEL","messages":[]}'
# 首页
curl -s localhost:18080/ | head -5
kill %1
```

Expected: 各步输出符合预期；最后用浏览器打开 http://localhost:18080 过一遍四页签（此步可交人工）。

- [ ] **Step 6: Commit**

```bash
git add -A
git commit -m "feat: wire up server, log cleanup, graceful shutdown, e2e test"
```

---

### Task 20: README 与 AGENTS.md

**Files:**
- Create: `README.md`
- Create: `AGENTS.md`

**Interfaces:**
- Consumes: 全部（文档总结现状）
- Produces: 无代码接口

- [ ] **Step 1: 写 `README.md`**

```markdown
# peng_api

自用的 LLM API 分发网关。多个上游渠道统一成 OpenAI 格式出口，按优先级分发，失败自动降级，完整记录请求日志。

## 构建

go build -o peng_api .

（纯 Go，无 CGO；Windows/macOS/Linux 均可。）

## 运行

./peng_api -admin-password <管理密码>

| 参数 | 默认 | 说明 |
|---|---|---|
| -addr | :8080 | 监听地址 |
| -db | ./peng.db | SQLite 文件路径 |
| -admin-password | （必填） | 管理密码，也可用环境变量 PENG_ADMIN_PASSWORD |
| -log-retention-days | 30 | 日志保留天数 |
| -fail-threshold | 3 | 连续失败多少次后自动禁用渠道 |
| -request-timeout | 300 | 非流式上游整体超时（秒） |
| -connect-timeout | 10 | 上游连接超时（秒） |
| -stream-first-byte-timeout | 60 | 流式首字节超时（秒） |

## 快速上手

1. 启动后用浏览器打开 http://localhost:8080 ，输入管理密码登录
2. 「模型与别名」页：新建标准模型（如 glm-5.3），把各渠道里的不同命名填成别名（GLM-5.3、glm5.3…）
3. 「渠道」页：新建渠道（地址、key、优先级），绑定模型（可用「获取模型」自动拉取）；给渠道选一个便宜的测试模型后可用「测试」按钮验证连通性
4. 「Token」页：创建客户端 token
5. 客户端（任意 OpenAI 兼容工具）配置：base_url = http://localhost:8080/v1 ，api_key = 上一步的 token
6. 「日志」页查看每次请求的完整报文与降级链路

## 行为要点

- 客户端只需用 OpenAI 格式请求 /v1/chat/completions；模型名可写标准名或任意别名（大小写不敏感）
- 同模型多渠道按优先级从大到小尝试；网络错误/超时/5xx/429 自动降级下一渠道；4xx 原样透传（请求本身的问题）
- 渠道连续失败 N 次自动禁用，冷却 5 分钟起指数退避（封顶 1 小时）后自动恢复；管理页可手动重置
- 流式（SSE）请求逐行透传，日志里存拼接后的完整响应
- GET /v1/models 返回带 context_length 的模型列表（Kimi 风格扩展字段）
```

- [ ] **Step 2: 写 `AGENTS.md`**

```markdown
# AGENTS.md — peng_api

面向 AI agent 的项目说明。**修改代码后必须同步更新本文件**（以及 `docs/superpowers/specs/` 里的设计文档，若涉及设计变更）。

## 项目是什么

自用 LLM API 分发网关：多上游渠道 → 统一 OpenAI 格式 `/v1` 出口。Go 单二进制 + SQLite。详细设计见 `docs/superpowers/specs/2026-09-20-peng-api-design.md`。

## 常用命令

- 构建：`go build -o peng_api .`
- 测试：`go test ./...`（转发路径改动后加 `-race` 跑一遍）
- 运行：`PENG_ADMIN_PASSWORD=xxx ./peng_api`
- 静态检查：`go vet ./...`

## 目录结构

- `main.go` — 装配入口（config→store→provider→engine→admin→web→server）
- `internal/config` — flag/env 配置解析
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
```

- [ ] **Step 3: 全量验证并提交**

Run: `go test ./... -race && go build -o peng_api . && go vet ./...`
Expected: 全绿

```bash
git add -A
git commit -m "docs: README and AGENTS.md"
```

---

## Self-Review 记录（计划作者填写）

- **Spec 覆盖**：对照 spec 逐节——§1 范围（Task 1-20 全部在做清单内）、§2 技术栈（Task 1/2/12/18）、§3 架构与 Provider 抽象（Task 7、19）、§4 表结构（Task 2）、§5.1 转发主流程（Task 10）、§5.2 SSE（Task 8/11）、§5.3 失败计数与恢复（Task 5/10）、§5.4 日志（Task 6/10/11/19）、§5.5 /v1/models（Task 11）、§5.6 拉取模型（Task 15）、§5.7 连通性测试（Task 16）、§6.1-6.3 接口（Task 9-17、1）、§7 Web（Task 18，含补充的 Token 页签）、§8 错误处理（Task 10/12/19）、§9 测试（各任务内嵌 + Task 19 e2e）、§10 交付物（Task 20）。
- **已知偏差**（已在计划内注明）：SPA 增加第四个「Token」页签（spec §7 未列但为管理 token 的必经入口）。
- **执行顺序提示**：Task 4 的 `TestListModelsWithEnabledChannels` 依赖 Task 5 的函数，两任务连做或先注释该测试。
