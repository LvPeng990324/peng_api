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

func TestOpenAddsQuotaTypeToExistingChannels(t *testing.T) {
	path := t.TempDir() + "/existing.db"
	db, err := sql.Open("sqlite", path)
	if err != nil {
		t.Fatal(err)
	}
	_, err = db.Exec(`CREATE TABLE channels (
		id INTEGER PRIMARY KEY AUTOINCREMENT, name TEXT NOT NULL,
		type TEXT NOT NULL DEFAULT 'openai', base_url TEXT NOT NULL, api_key TEXT NOT NULL,
		priority INTEGER NOT NULL DEFAULT 0, enabled INTEGER NOT NULL DEFAULT 1,
		test_model TEXT NOT NULL DEFAULT '', remark TEXT NOT NULL DEFAULT '',
		created_at DATETIME DEFAULT CURRENT_TIMESTAMP)`)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`INSERT INTO channels (name, base_url, api_key) VALUES ('old', 'http://x', 'k')`); err != nil {
		t.Fatal(err)
	}
	if err := db.Close(); err != nil {
		t.Fatal(err)
	}

	s, err := Open(path)
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	defer s.Close()
	channels, err := s.ListChannels(context.Background())
	if err != nil || len(channels) != 1 || channels[0].QuotaType != "" {
		t.Fatalf("migrated channels: %v %+v", err, channels)
	}
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
	// 旧库无 model_actual 列：补列后历史日志应为空串且可正常读取
	if logs[0].ModelActual != "" {
		t.Errorf("migrated log should have empty model_actual: %q", logs[0].ModelActual)
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
		Name: "zhipu", Type: "openai", QuotaType: "openrouter", BaseURL: "https://api.bigmodel.cn/coding/paas/v4",
		APIKey: "key1", Priority: 10, TestModel: "glm-5.3-free",
	})
	if err != nil {
		t.Fatalf("CreateChannel: %v", err)
	}

	got, err := s.GetChannel(ctx, ch.ID)
	if err != nil || got == nil {
		t.Fatalf("GetChannel: %v", err)
	}
	if got.Name != "zhipu" || got.Priority != 10 || !got.Enabled || got.TestModel != "glm-5.3-free" || got.QuotaType != "openrouter" {
		t.Errorf("unexpected channel: %+v", got)
	}
	list, err := s.ListChannels(ctx)
	if err != nil || len(list) != 1 || list[0].QuotaType != "openrouter" {
		t.Errorf("ListChannels quota_type: %v %+v", err, list)
	}

	if err := s.UpdateChannel(ctx, Channel{
		ID: ch.ID, Name: "zhipu2", Type: "openai", QuotaType: "kimi", BaseURL: got.BaseURL, APIKey: "key2",
		Priority: 5, Enabled: true, TestModel: "m2",
	}); err != nil {
		t.Fatalf("UpdateChannel: %v", err)
	}
	got3, _ := s.GetChannel(ctx, ch.ID)
	if got3.Name != "zhipu2" || got3.APIKey != "key2" || got3.Priority != 5 || got3.QuotaType != "kimi" {
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
	mp, _ := s.CreateMapping(ctx, MappingInput{Name: "glm-5.3", ModelIDs: []int64{m.ID}})

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
	mp, _ := s.CreateMapping(ctx, MappingInput{Name: "glm-5.3", ModelIDs: []int64{m.ID}})
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
	if err := s.UpdateModel(ctx, m.ID, "glm-5.3", nil); err != nil {
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

	mp, err := s.CreateMapping(ctx, MappingInput{Name: "glm-5.3", ModelIDs: []int64{mLow.ID, mHigh1.ID, mHigh2.ID, mOff.ID}})

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
	empty, _ := s.CreateMapping(ctx, MappingInput{Name: "empty-map"})
	cands, _ = s.SelectModels(ctx, empty.ID)
	if len(cands) != 0 {
		t.Errorf("unbound mapping must have no candidates: %+v", cands)
	}
}

// 管理端「绑定模型」列表的顺序必须与请求进来后实际尝试的顺序一致；
// 同时不能丢掉禁用渠道下的绑定（编辑映射时 model_ids 来自该列表并全量替换）。
func TestBoundModelsFollowCallOrder(t *testing.T) {
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

	// 绑定顺序刻意与调用顺序相反
	mp, err := s.CreateMapping(ctx, MappingInput{Name: "glm-5.3", ModelIDs: []int64{mLow.ID, mOff.ID, mHigh2.ID, mHigh1.ID}})

	if err != nil {
		t.Fatalf("CreateMapping: %v", err)
	}

	cands, err := s.SelectModels(ctx, mp.ID)
	if err != nil {
		t.Fatalf("SelectModels: %v", err)
	}
	list, err := s.ListMappings(ctx)
	if err != nil {
		t.Fatalf("ListMappings: %v", err)
	}

	for _, bound := range [][]BoundModel{mp.BoundModels, list[0].BoundModels} {
		if len(bound) != 4 {
			t.Fatalf("禁用渠道的绑定必须保留，got %d: %+v", len(bound), bound)
		}
		// 前 len(cands) 项逐项对齐真实调用顺序
		for i, c := range cands {
			if bound[i].Name != c.ModelName || bound[i].ChannelName != c.Channel.Name {
				t.Errorf("bound[%d] = %s/%s, want %s/%s", i, bound[i].ChannelName, bound[i].Name, c.Channel.Name, c.ModelName)
			}
			if !bound[i].ChannelEnabled {
				t.Errorf("bound[%d] must come from an enabled channel", i)
			}
			if i > 0 && bound[i-1].ChannelPriority < bound[i].ChannelPriority {
				t.Errorf("bound[%d] priority out of order: %d then %d",
					i, bound[i-1].ChannelPriority, bound[i].ChannelPriority)
			}
		}
		// 禁用渠道排在末尾，且仍带着启用状态标记
		if last := bound[len(bound)-1]; last.Name != "glm-off" || last.ChannelEnabled || last.ChannelPriority != 100 {
			t.Errorf("disabled channel model should be last and marked disabled: %+v", last)
		}
	}
}

// 模型侧编辑绑定：改名与绑定替换在同一事务内，失败时整体回滚
func TestUpdateModelMappings(t *testing.T) {
	s := openTest(t)
	ctx := context.Background()

	ch, _ := s.CreateChannel(ctx, Channel{Name: "c", Type: "openai", BaseURL: "http://x", APIKey: "k"})
	m, _ := s.CreateModel(ctx, ch.ID, "glm-free")
	mpA, _ := s.CreateMapping(ctx, MappingInput{Name: "glm-5.3", ModelIDs: []int64{m.ID}})
	mpB, _ := s.CreateMapping(ctx, MappingInput{Name: "claude-sonnet"})

	// 只改名、mappingIDs=nil → 绑定不动
	if err := s.UpdateModel(ctx, m.ID, "glm-free-v2", nil); err != nil {
		t.Fatalf("rename: %v", err)
	}
	got, _ := s.GetModel(ctx, m.ID)
	if got.Name != "glm-free-v2" || len(got.MappingIDs) != 1 || got.MappingIDs[0] != mpA.ID {
		t.Fatalf("nil must keep bindings: %+v", got)
	}

	// 全量替换（重复 id 去重），列表与详情都要带上 id + 名字
	ids := []int64{mpB.ID, mpA.ID, mpB.ID}
	if err := s.UpdateModel(ctx, m.ID, "glm-free-v2", &ids); err != nil {
		t.Fatalf("replace bindings: %v", err)
	}
	got, _ = s.GetModel(ctx, m.ID)
	// modelBindings 按标准名排序：claude-sonnet < glm-5.3
	if len(got.MappingIDs) != 2 || got.MappingIDs[0] != mpB.ID || got.MappingIDs[1] != mpA.ID ||
		len(got.Mappings) != 2 || got.Mappings[0] != "claude-sonnet" {
		t.Errorf("bindings not replaced/deduped: %+v", got)
	}
	list, _ := s.ListModels(ctx)
	if len(list[0].MappingIDs) != 2 {
		t.Errorf("ListModels missing mapping_ids: %+v", list[0])
	}

	// 无效 mapping_id → 外键报错，改名与绑定都必须回滚
	bad := []int64{9999}
	if err := s.UpdateModel(ctx, m.ID, "SHOULD-NOT-APPLY", &bad); err == nil {
		t.Fatal("invalid mapping_id must fail")
	}
	got, _ = s.GetModel(ctx, m.ID)
	if got.Name != "glm-free-v2" || len(got.MappingIDs) != 2 {
		t.Errorf("failed update must roll back entirely: %+v", got)
	}

	// 空数组 → 清空绑定，映射页视角同步
	empty := []int64{}
	if err := s.UpdateModel(ctx, m.ID, "glm-free-v2", &empty); err != nil {
		t.Fatalf("clear: %v", err)
	}
	got, _ = s.GetModel(ctx, m.ID)
	if len(got.MappingIDs) != 0 || len(got.Mappings) != 0 {
		t.Errorf("empty slice must clear bindings: %+v", got)
	}
	cands, _ := s.SelectModels(ctx, mpA.ID)
	if len(cands) != 0 {
		t.Errorf("cleared binding must not be callable: %+v", cands)
	}

	// 模型不存在 → ErrNotFound
	if err := s.UpdateModel(ctx, 9999, "x", &empty); !errors.Is(err, ErrNotFound) {
		t.Errorf("missing model should return ErrNotFound, got %v", err)
	}
}

func TestMappingCRUDAndResolve(t *testing.T) {
	s := openTest(t)
	ctx := context.Background()
	ch, _ := s.CreateChannel(ctx, Channel{Name: "c", Type: "openai", BaseURL: "http://x", APIKey: "k"})
	m1, _ := s.CreateModel(ctx, ch.ID, "glm-5.3-free")
	m2, _ := s.CreateModel(ctx, ch.ID, "glm-5.3-pro")

	mp, err := s.CreateMapping(ctx, MappingInput{Name: "glm-5.3", ContextLength: ptrI64(131072), MaxOutputTokens: ptrI64(8192), ModelIDs: []int64{m1.ID, m2.ID}})
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
	if _, err := s.CreateMapping(ctx, MappingInput{Name: "glm-5.3"}); err == nil {
		t.Fatal("duplicate mapping name must fail")
	}
	// 绑定不存在的模型 → FK 错误
	if _, err := s.CreateMapping(ctx, MappingInput{Name: "bad", ModelIDs: []int64{9999}}); err == nil {
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
	if err := s.UpdateMapping(ctx, mp.ID, MappingInput{Name: "glm-5.3", ContextLength: ptrI64(200000), ModelIDs: []int64{m2.ID}}); err != nil {
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
	if err := s.UpdateMapping(ctx, mp.ID, MappingInput{Name: "glm-5.3", ModelIDs: []int64{m2.ID, m2.ID}}); err != nil {
		t.Fatalf("UpdateMapping dup ids: %v", err)
	}
	// 不存在的映射 → ErrNotFound
	if err := s.UpdateMapping(ctx, 9999, MappingInput{Name: "x"}); !errors.Is(err, ErrNotFound) {
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

	withModel, _ := s.CreateMapping(ctx, MappingInput{Name: "has-model", ContextLength: ptrI64(1000), ModelIDs: []int64{m.ID}})
	without, _ := s.CreateMapping(ctx, MappingInput{Name: "no-model"})

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
		Agent:          "kimi-code",
		ModelRequested: "GLM5.3", ModelCanonical: "glm-5.3", ModelActual: "glm-5.3-free",
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
	e2.ModelActual = "glm-5.3-paid" // 降级到另一个渠道的模型实体
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
	if rows[1].Agent != "kimi-code" {
		t.Errorf("agent roundtrip: %q", rows[1].Agent)
	}
	// 列表：每次尝试各自记录实际发往上游的模型实体名
	if rows[0].ModelActual != "glm-5.3-paid" || rows[1].ModelActual != "glm-5.3-free" {
		t.Errorf("model_actual roundtrip: %q / %q", rows[0].ModelActual, rows[1].ModelActual)
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
	_, total, _ = s.ListLogs(ctx, LogFilter{Model: "glm-5.3-paid", Page: 1, Size: 10})
	if total != 1 {
		t.Errorf("model filter should match actual: total=%d", total)
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
	if group[0].Agent != "kimi-code" {
		t.Errorf("agent in group: %q", group[0].Agent)
	}
	// 详情 / 归组也要带上实际模型名
	if got.ModelActual != "glm-5.3-free" {
		t.Errorf("model_actual in GetLog: %q", got.ModelActual)
	}
	if group[0].ModelActual != "glm-5.3-free" || group[1].ModelActual != "glm-5.3-paid" {
		t.Errorf("model_actual in group: %q / %q", group[0].ModelActual, group[1].ModelActual)
	}

	// agent 模糊筛选
	_, total, _ = s.ListLogs(ctx, LogFilter{Agent: "kimi", Page: 1, Size: 10})
	if total != 2 {
		t.Errorf("agent filter fuzzy: total=%d", total)
	}
	_, total, _ = s.ListLogs(ctx, LogFilter{Agent: "cline", Page: 1, Size: 10})
	if total != 0 {
		t.Errorf("agent filter mismatch: total=%d", total)
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

func TestMappingParameterOverrides(t *testing.T) {
	s := openTest(t)
	ctx := context.Background()
	ch, _ := s.CreateChannel(ctx, Channel{Name: "c", Type: "openai", BaseURL: "http://x", APIKey: "k"})
	m, _ := s.CreateModel(ctx, ch.ID, "glm-5.3-free")

	// 创建时不带覆盖 → nil
	mp, err := s.CreateMapping(ctx, MappingInput{Name: "plain", ModelIDs: []int64{m.ID}})
	if err != nil {
		t.Fatalf("CreateMapping: %v", err)
	}
	if mp.ParameterOverrides != nil {
		t.Errorf("overrides should be nil: %+v", mp.ParameterOverrides)
	}
	got, _ := s.ResolveMapping(ctx, "plain")
	if got.ParameterOverrides != nil {
		t.Errorf("resolve overrides should be nil: %+v", got.ParameterOverrides)
	}

	// 创建时带覆盖（数值/布尔/字符串混合）
	ov := map[string]any{"temperature": float64(1), "top_p": float64(0.9), "stream": false, "custom_tag": "abc"}
	mp, err = s.CreateMapping(ctx, MappingInput{Name: "forced", ModelIDs: []int64{m.ID}, ParameterOverrides: ov})
	if err != nil {
		t.Fatalf("CreateMapping with overrides: %v", err)
	}
	if mp.ParameterOverrides["temperature"] != float64(1) || mp.ParameterOverrides["stream"] != false {
		t.Errorf("create returns overrides: %+v", mp.ParameterOverrides)
	}

	// Resolve 往返
	got, err = s.ResolveMapping(ctx, "forced")
	if err != nil || got == nil {
		t.Fatalf("ResolveMapping: %v %+v", err, got)
	}
	if got.ParameterOverrides["temperature"] != float64(1) ||
		got.ParameterOverrides["top_p"] != float64(0.9) ||
		got.ParameterOverrides["stream"] != false ||
		got.ParameterOverrides["custom_tag"] != "abc" {
		t.Errorf("overrides roundtrip: %+v", got.ParameterOverrides)
	}

	// List 往返
	list, _ := s.ListMappings(ctx)
	if len(list) != 2 || list[1].ParameterOverrides["temperature"] != float64(1) {
		t.Errorf("ListMappings overrides: %+v", list)
	}

	// 更新：整体替换覆盖参数；传 nil 清空
	if err := s.UpdateMapping(ctx, mp.ID, MappingInput{Name: "forced", ModelIDs: []int64{m.ID}, ParameterOverrides: map[string]any{"max_tokens": float64(1024)}}); err != nil {
		t.Fatalf("UpdateMapping: %v", err)
	}
	got, _ = s.ResolveMapping(ctx, "forced")
	if len(got.ParameterOverrides) != 1 || got.ParameterOverrides["max_tokens"] != float64(1024) {
		t.Errorf("overrides replaced: %+v", got.ParameterOverrides)
	}
	if err := s.UpdateMapping(ctx, mp.ID, MappingInput{Name: "forced", ModelIDs: []int64{m.ID}}); err != nil {
		t.Fatalf("UpdateMapping clear: %v", err)
	}
	got, _ = s.ResolveMapping(ctx, "forced")
	if got.ParameterOverrides != nil {
		t.Errorf("overrides should be cleared: %+v", got.ParameterOverrides)
	}
}
