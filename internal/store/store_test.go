package store

import (
	"context"
	"database/sql"
	"errors"
	"strings"
	"testing"
	"time"
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
	for _, table := range []string{"channels", "models", "model_mappings", "model_mapping_models", "tokens", "request_logs"} {
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

// 旧库（渠道↔标准模型↔别名结构）打开后四张表 DROP 重建，tokens/request_logs 保留
func TestOpenDropsLegacySchema(t *testing.T) {
	path := t.TempDir() + "/legacy.db"
	db, err := sql.Open("sqlite", path)
	if err != nil {
		t.Fatal(err)
	}
	legacy := `
	CREATE TABLE models (id INTEGER PRIMARY KEY AUTOINCREMENT, name TEXT NOT NULL UNIQUE,
	  context_length INTEGER, max_output_tokens INTEGER, created_at DATETIME DEFAULT CURRENT_TIMESTAMP);
	CREATE TABLE model_aliases (id INTEGER PRIMARY KEY AUTOINCREMENT, alias TEXT NOT NULL UNIQUE,
	  model_id INTEGER NOT NULL REFERENCES models(id) ON DELETE CASCADE);
	CREATE TABLE channels (id INTEGER PRIMARY KEY AUTOINCREMENT, name TEXT NOT NULL,
	  type TEXT NOT NULL DEFAULT 'openai', base_url TEXT NOT NULL, api_key TEXT NOT NULL,
	  priority INTEGER NOT NULL DEFAULT 0, enabled INTEGER NOT NULL DEFAULT 1,
	  test_model TEXT NOT NULL DEFAULT '', auto_disabled INTEGER NOT NULL DEFAULT 0,
	  consecutive_failures INTEGER NOT NULL DEFAULT 0, disabled_until DATETIME,
	  created_at DATETIME DEFAULT CURRENT_TIMESTAMP);
	CREATE TABLE channel_models (channel_id INTEGER NOT NULL REFERENCES channels(id) ON DELETE CASCADE,
	  model_id INTEGER NOT NULL REFERENCES models(id) ON DELETE CASCADE,
	  upstream_model TEXT NOT NULL DEFAULT '', PRIMARY KEY (channel_id, model_id));
	CREATE TABLE tokens (id INTEGER PRIMARY KEY AUTOINCREMENT, name TEXT NOT NULL,
	  token TEXT NOT NULL UNIQUE, enabled INTEGER NOT NULL DEFAULT 1,
	  created_at DATETIME DEFAULT CURRENT_TIMESTAMP);
	CREATE TABLE request_logs (id INTEGER PRIMARY KEY AUTOINCREMENT, request_id TEXT NOT NULL,
	  attempt INTEGER NOT NULL, token_id INTEGER, token_name TEXT NOT NULL DEFAULT '',
	  model_requested TEXT NOT NULL, model_canonical TEXT NOT NULL DEFAULT '',
	  channel_id INTEGER, channel_name TEXT NOT NULL DEFAULT '', stream INTEGER NOT NULL DEFAULT 0,
	  status TEXT NOT NULL, http_status INTEGER, error TEXT NOT NULL DEFAULT '',
	  request_body TEXT NOT NULL DEFAULT '', response_body TEXT NOT NULL DEFAULT '',
	  prompt_tokens INTEGER, completion_tokens INTEGER, latency_ms INTEGER NOT NULL DEFAULT 0,
	  created_at DATETIME DEFAULT CURRENT_TIMESTAMP);`
	if _, err := db.Exec(legacy); err != nil {
		t.Fatalf("legacy ddl: %v", err)
	}
	if _, err := db.Exec(`INSERT INTO channels (name, base_url, api_key) VALUES ('old', 'http://x', 'k')`); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`INSERT INTO models (name) VALUES ('glm-5.3')`); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`INSERT INTO model_aliases (alias, model_id) VALUES ('GLM5.3', 1)`); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`INSERT INTO channel_models (channel_id, model_id, upstream_model) VALUES (1, 1, 'glm-free')`); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`INSERT INTO tokens (name, token) VALUES ('keepme', 'sk-keep')`); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`INSERT INTO request_logs (request_id, attempt, model_requested, status) VALUES ('r1', 1, 'glm-5.3', 'success')`); err != nil {
		t.Fatal(err)
	}
	db.Close()

	s, err := Open(path)
	if err != nil {
		t.Fatalf("Open legacy: %v", err)
	}
	defer s.Close()
	ctx := context.Background()

	// legacy 表已删，新结构就位
	if exists, _ := hasTable(s.db, "model_aliases"); exists {
		t.Error("model_aliases must be dropped")
	}
	if exists, _ := hasTable(s.db, "channel_models"); exists {
		t.Error("channel_models must be dropped")
	}
	channels, err := s.ListChannels(ctx)
	if err != nil || len(channels) != 0 {
		t.Fatalf("channels must be rebuilt empty: %v %+v", err, channels)
	}
	models, err := s.ListModels(ctx)
	if err != nil || len(models) != 0 {
		t.Fatalf("models must be rebuilt empty: %v %+v", err, models)
	}
	// tokens / request_logs 保留
	toks, err := s.ListTokens(ctx)
	if err != nil || len(toks) != 1 || toks[0].Name != "keepme" {
		t.Fatalf("tokens must survive: %v %+v", err, toks)
	}
	logs, total, err := s.ListLogs(ctx, LogFilter{Page: 1, Size: 10})
	if err != nil || total != 1 || logs[0].RequestID != "r1" {
		t.Fatalf("request_logs must survive: %v total=%d", err, total)
	}
	// 重建后能正常走新流程
	ch, err := s.CreateChannel(ctx, Channel{Name: "new", Type: "openai", BaseURL: "http://y", APIKey: "k"})
	if err != nil {
		t.Fatalf("CreateChannel after migration: %v", err)
	}
	if _, err := s.CreateModel(ctx, ch.ID, "glm-5.3"); err != nil {
		t.Fatalf("CreateModel after migration: %v", err)
	}
}

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

func TestChannelCRUD(t *testing.T) {
	s := openTest(t)
	ctx := context.Background()

	ch, err := s.CreateChannel(ctx, Channel{
		Name: "zhipu", Type: "openai", BaseURL: "https://api.bigmodel.cn/coding/paas/v4",
		APIKey: "key1", Priority: 10, TestModel: "glm-5.3-free",
	})
	if err != nil {
		t.Fatalf("CreateChannel: %v", err)
	}

	got, err := s.GetChannel(ctx, ch.ID)
	if err != nil || got == nil {
		t.Fatalf("GetChannel: %v", err)
	}
	if got.Name != "zhipu" || got.Priority != 10 || !got.Enabled || got.TestModel != "glm-5.3-free" {
		t.Errorf("unexpected channel: %+v", got)
	}

	if err := s.UpdateChannel(ctx, Channel{
		ID: ch.ID, Name: "zhipu2", Type: "openai", BaseURL: got.BaseURL, APIKey: "key2",
		Priority: 5, Enabled: true, TestModel: "m2",
	}); err != nil {
		t.Fatalf("UpdateChannel: %v", err)
	}
	got3, _ := s.GetChannel(ctx, ch.ID)
	if got3.Name != "zhipu2" || got3.APIKey != "key2" || got3.Priority != 5 {
		t.Errorf("update lost fields: %+v", got3)
	}

	// 渠道禁用只改 enabled（不再有健康状态列）
	if err := s.SetChannelEnabled(ctx, ch.ID, false); err != nil {
		t.Fatalf("SetChannelEnabled: %v", err)
	}
	got4, _ := s.GetChannel(ctx, ch.ID)
	if got4.Enabled {
		t.Error("channel should be disabled")
	}

	if err := s.DeleteChannel(ctx, ch.ID); err != nil {
		t.Fatalf("DeleteChannel: %v", err)
	}
	got5, _ := s.GetChannel(ctx, ch.ID)
	if got5 != nil {
		t.Error("channel should be deleted")
	}
}

func TestChannelDeleteCascadesModels(t *testing.T) {
	s := openTest(t)
	ctx := context.Background()
	ch, _ := s.CreateChannel(ctx, Channel{Name: "c", Type: "openai", BaseURL: "http://x", APIKey: "k"})
	m, _ := s.CreateModel(ctx, ch.ID, "glm-5.3")
	mp, _ := s.CreateMapping(ctx, "glm-5.3", nil, nil, []int64{m.ID})

	if err := s.DeleteChannel(ctx, ch.ID); err != nil {
		t.Fatalf("DeleteChannel: %v", err)
	}
	got, _ := s.GetModel(ctx, m.ID)
	if got != nil {
		t.Error("channel delete must cascade to its models")
	}
	// 绑定随之清空，映射本身还在
	mp2, _ := s.ResolveMapping(ctx, "glm-5.3")
	if mp2 == nil || mp2.ID != mp.ID {
		t.Fatalf("mapping should survive: %+v", mp2)
	}
	cands, _ := s.SelectModels(ctx, mp.ID)
	if len(cands) != 0 {
		t.Errorf("bindings must be cascade-deleted: %+v", cands)
	}
}

func TestModelCRUD(t *testing.T) {
	s := openTest(t)
	ctx := context.Background()
	ch, _ := s.CreateChannel(ctx, Channel{Name: "zhipu", Type: "openai", BaseURL: "http://x", APIKey: "k"})

	m, err := s.CreateModel(ctx, ch.ID, "glm-5.3-free")
	if err != nil {
		t.Fatalf("CreateModel: %v", err)
	}
	// 同渠道重名 → 幂等返回已存在实体
	again, err := s.CreateModel(ctx, ch.ID, "glm-5.3-free")
	if err != nil || again.ID != m.ID {
		t.Fatalf("CreateModel must be idempotent per channel: %v %+v", err, again)
	}
	// 不同渠道允许同名
	ch2, _ := s.CreateChannel(ctx, Channel{Name: "other", Type: "openai", BaseURL: "http://y", APIKey: "k"})
	if _, err := s.CreateModel(ctx, ch2.ID, "glm-5.3-free"); err != nil {
		t.Fatalf("same name on another channel must be allowed: %v", err)
	}
	// 渠道不存在 → FK 错误
	if _, err := s.CreateModel(ctx, 9999, "m"); err == nil {
		t.Fatal("model on missing channel must fail (FK)")
	}

	// 绑定标准名后列表带出渠道名 + 映射名
	mp, _ := s.CreateMapping(ctx, "glm-5.3", nil, nil, []int64{m.ID})
	list, err := s.ListModels(ctx)
	if err != nil || len(list) != 2 {
		t.Fatalf("ListModels: %v len=%d", err, len(list))
	}
	if list[0].ChannelName != "zhipu" || len(list[0].Mappings) != 1 || list[0].Mappings[0] != "glm-5.3" {
		t.Errorf("unexpected model row: %+v", list[0])
	}

	// ModelsOfChannel 只含本渠道
	ofCh, err := s.ModelsOfChannel(ctx, ch.ID)
	if err != nil || len(ofCh) != 1 || ofCh[0].ID != m.ID {
		t.Fatalf("ModelsOfChannel: %v %+v", err, ofCh)
	}

	got, err := s.GetModel(ctx, m.ID)
	if err != nil || got == nil || got.Name != "glm-5.3-free" || got.ChannelName != "zhipu" {
		t.Fatalf("GetModel: %v %+v", err, got)
	}

	// 改名
	if err := s.UpdateModel(ctx, m.ID, "glm-5.3"); err != nil {
		t.Fatalf("UpdateModel: %v", err)
	}
	got2, _ := s.GetModel(ctx, m.ID)
	if got2.Name != "glm-5.3" {
		t.Errorf("rename failed: %+v", got2)
	}

	// 删除 → 绑定级联清理
	if err := s.DeleteModel(ctx, m.ID); err != nil {
		t.Fatalf("DeleteModel: %v", err)
	}
	got3, _ := s.GetModel(ctx, m.ID)
	if got3 != nil {
		t.Error("model should be deleted")
	}
	bound, _ := s.boundModelsOf(ctx, mp.ID)
	if len(bound) != 0 {
		t.Errorf("bindings must cascade: %+v", bound)
	}
}

func TestSelectModelsOrderAndDisabledSkip(t *testing.T) {
	s := openTest(t)
	ctx := context.Background()

	low, _ := s.CreateChannel(ctx, Channel{Name: "low", Type: "openai", BaseURL: "http://low", APIKey: "k", Priority: 1})
	high, _ := s.CreateChannel(ctx, Channel{Name: "high", Type: "openai", BaseURL: "http://high", APIKey: "k", Priority: 10})
	off, _ := s.CreateChannel(ctx, Channel{Name: "off", Type: "openai", BaseURL: "http://off", APIKey: "k", Priority: 100})
	mLow, _ := s.CreateModel(ctx, low.ID, "glm-low")
	mHigh1, _ := s.CreateModel(ctx, high.ID, "glm-high-1")
	mHigh2, _ := s.CreateModel(ctx, high.ID, "glm-high-2")
	mOff, _ := s.CreateModel(ctx, off.ID, "glm-off")
	s.SetChannelEnabled(ctx, off.ID, false)

	mp, err := s.CreateMapping(ctx, "glm-5.3", nil, nil,
		[]int64{mLow.ID, mHigh1.ID, mHigh2.ID, mOff.ID})
	if err != nil {
		t.Fatalf("CreateMapping: %v", err)
	}

	cands, err := s.SelectModels(ctx, mp.ID)
	if err != nil || len(cands) != 3 {
		t.Fatalf("SelectModels: %v len=%d", err, len(cands))
	}
	// priority DESC → high 在前；同渠道按 model id ASC；禁用渠道剔除
	want := []string{"glm-high-1", "glm-high-2", "glm-low"}
	for i, w := range want {
		if cands[i].ModelName != w {
			t.Errorf("cand[%d] = %s, want %s", i, cands[i].ModelName, w)
		}
	}
	if cands[0].Channel.Name != "high" || cands[0].Channel.BaseURL != "http://high" {
		t.Errorf("channel info not carried: %+v", cands[0].Channel)
	}

	// 未绑定任何模型的映射 → 空
	empty, _ := s.CreateMapping(ctx, "empty-map", nil, nil, nil)
	cands, _ = s.SelectModels(ctx, empty.ID)
	if len(cands) != 0 {
		t.Errorf("unbound mapping must have no candidates: %+v", cands)
	}
}

func TestMappingCRUDAndResolve(t *testing.T) {
	s := openTest(t)
	ctx := context.Background()
	ch, _ := s.CreateChannel(ctx, Channel{Name: "c", Type: "openai", BaseURL: "http://x", APIKey: "k"})
	m1, _ := s.CreateModel(ctx, ch.ID, "glm-5.3-free")
	m2, _ := s.CreateModel(ctx, ch.ID, "glm-5.3-pro")

	mp, err := s.CreateMapping(ctx, "glm-5.3", ptrI64(131072), ptrI64(8192), []int64{m1.ID, m2.ID})
	if err != nil {
		t.Fatalf("CreateMapping: %v", err)
	}
	if mp.ContextLength == nil || *mp.ContextLength != 131072 || len(mp.BoundModels) != 2 {
		t.Errorf("unexpected mapping: %+v", mp)
	}
	if mp.BoundModels[0].ChannelName != "c" {
		t.Errorf("bound model should carry channel name: %+v", mp.BoundModels[0])
	}

	// 重名 → UNIQUE 冲突
	if _, err := s.CreateMapping(ctx, "glm-5.3", nil, nil, nil); err == nil {
		t.Fatal("duplicate mapping name must fail")
	}
	// 绑定不存在的模型 → FK 错误
	if _, err := s.CreateMapping(ctx, "bad", nil, nil, []int64{9999}); err == nil {
		t.Fatal("binding missing model must fail (FK)")
	}

	// 精确匹配（大小写敏感）
	got, err := s.ResolveMapping(ctx, "glm-5.3")
	if err != nil || got == nil || got.ID != mp.ID {
		t.Fatalf("resolve exact: %v %+v", err, got)
	}
	got, err = s.ResolveMapping(ctx, "GLM-5.3")
	if err != nil || got != nil {
		t.Fatalf("resolve must be case-sensitive: %v %+v", err, got)
	}
	got, err = s.ResolveMapping(ctx, "glm-5.3-free")
	if err != nil || got != nil {
		t.Fatalf("model entity name must not resolve as mapping: %v %+v", err, got)
	}

	// 列表带绑定
	list, err := s.ListMappings(ctx)
	if err != nil || len(list) != 1 || len(list[0].BoundModels) != 2 {
		t.Fatalf("ListMappings: %v %+v", err, list)
	}

	// 全量更新：改参数 + 整体替换绑定
	if err := s.UpdateMapping(ctx, mp.ID, "glm-5.3", ptrI64(200000), nil, []int64{m2.ID}); err != nil {
		t.Fatalf("UpdateMapping: %v", err)
	}
	list, _ = s.ListMappings(ctx)
	if len(list[0].BoundModels) != 1 || list[0].BoundModels[0].ID != m2.ID {
		t.Errorf("bindings not replaced: %+v", list[0].BoundModels)
	}
	if list[0].ContextLength == nil || *list[0].ContextLength != 200000 || list[0].MaxOutputTokens != nil {
		t.Errorf("params not updated: %+v", list[0])
	}
	// 重复的 model_ids 去重
	if err := s.UpdateMapping(ctx, mp.ID, "glm-5.3", nil, nil, []int64{m2.ID, m2.ID}); err != nil {
		t.Fatalf("UpdateMapping dup ids: %v", err)
	}
	// 不存在的映射 → ErrNotFound
	if err := s.UpdateMapping(ctx, 9999, "x", nil, nil, nil); !errors.Is(err, ErrNotFound) {
		t.Fatalf("expected ErrNotFound, got %v", err)
	}

	if err := s.DeleteMapping(ctx, mp.ID); err != nil {
		t.Fatalf("DeleteMapping: %v", err)
	}
	got, _ = s.ResolveMapping(ctx, "glm-5.3")
	if got != nil {
		t.Error("mapping should be deleted")
	}
}

func TestListMappingsWithEnabledModels(t *testing.T) {
	s := openTest(t)
	ctx := context.Background()
	ch, _ := s.CreateChannel(ctx, Channel{Name: "c", Type: "openai", BaseURL: "http://x", APIKey: "k"})
	m, _ := s.CreateModel(ctx, ch.ID, "glm-5.3-free")

	withModel, _ := s.CreateMapping(ctx, "has-model", ptrI64(1000), nil, []int64{m.ID})
	without, _ := s.CreateMapping(ctx, "no-model", nil, nil, nil)

	list, err := s.ListMappingsWithEnabledModels(ctx)
	if err != nil || len(list) != 1 || list[0].ID != withModel.ID {
		t.Fatalf("ListMappingsWithEnabledModels: %v %+v", err, list)
	}
	if list[0].ContextLength == nil || *list[0].ContextLength != 1000 {
		t.Errorf("params missing: %+v", list[0])
	}

	// 渠道禁用后映射不再出现
	if err := s.SetChannelEnabled(ctx, ch.ID, false); err != nil {
		t.Fatalf("SetChannelEnabled: %v", err)
	}
	list, _ = s.ListMappingsWithEnabledModels(ctx)
	if len(list) != 0 {
		t.Fatalf("mapping on disabled channel must be hidden: %+v (mapping %d)", list, without.ID)
	}
}

func ptrI64(v int64) *int64 { return &v }

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
	e2.CompletionTokens = new(int)
	*e2.CompletionTokens = 5
	e2.PromptCacheHitTokens = new(int)
	*e2.PromptCacheHitTokens = 8
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
	if rows[0].PromptCacheHitTokens == nil || *rows[0].PromptCacheHitTokens != 8 {
		t.Errorf("cache hit roundtrip: %+v", rows[0].PromptCacheHitTokens)
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
