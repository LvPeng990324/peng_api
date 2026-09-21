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
