package store

import (
	"context"
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
	if got2.ConsecutiveFailures != 1 {
		t.Errorf("RecordFailure should have incremented: %+v", got2)
	}
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
