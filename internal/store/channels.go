package store

import (
	"context"
	"database/sql"
	"time"
)

// Channel 上游渠道：只提供连接信息与优先级，是模型实体的容器
type Channel struct {
	ID        int64     `json:"id"`
	Name      string    `json:"name"`
	Type      string    `json:"type"`
	BaseURL   string    `json:"base_url"`
	APIKey    string    `json:"api_key"`
	Priority  int       `json:"priority"`
	Enabled   bool      `json:"enabled"`
	TestModel string    `json:"test_model"`
	CreatedAt time.Time `json:"created_at"`
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
	return &ch, nil
}

func (s *Store) UpdateChannel(ctx context.Context, ch Channel) error {
	if ch.Type == "" {
		ch.Type = "openai"
	}
	_, err := s.db.ExecContext(ctx, `
		UPDATE channels SET name=?, type=?, base_url=?, api_key=?, priority=?, enabled=?, test_model=?
		WHERE id=?`,
		ch.Name, ch.Type, ch.BaseURL, ch.APIKey, ch.Priority, ch.Enabled, ch.TestModel, ch.ID)
	return err
}

func (s *Store) DeleteChannel(ctx context.Context, id int64) error {
	_, err := s.db.ExecContext(ctx, `DELETE FROM channels WHERE id=?`, id)
	return err // 渠道下的模型实体由外键 ON DELETE CASCADE 清理
}

func (s *Store) GetChannel(ctx context.Context, id int64) (*Channel, error) {
	const q = `SELECT id, name, type, base_url, api_key, priority, enabled, test_model, created_at
		FROM channels WHERE id=?`
	var c Channel
	err := s.db.QueryRowContext(ctx, q, id).Scan(&c.ID, &c.Name, &c.Type, &c.BaseURL, &c.APIKey,
		&c.Priority, &c.Enabled, &c.TestModel, &c.CreatedAt)
	if err == sql.ErrNoRows {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	return &c, nil
}

func (s *Store) ListChannels(ctx context.Context) ([]Channel, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT id, name, type, base_url, api_key, priority, enabled,
		test_model, created_at FROM channels ORDER BY id`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []Channel{}
	for rows.Next() {
		var c Channel
		if err := rows.Scan(&c.ID, &c.Name, &c.Type, &c.BaseURL, &c.APIKey, &c.Priority,
			&c.Enabled, &c.TestModel, &c.CreatedAt); err != nil {
			return nil, err
		}
		out = append(out, c)
	}
	return out, rows.Err()
}

func (s *Store) SetChannelEnabled(ctx context.Context, id int64, enabled bool) error {
	_, err := s.db.ExecContext(ctx, `UPDATE channels SET enabled=? WHERE id=?`, enabled, id)
	return err
}

// Candidate 一次上游尝试：渠道 + 该渠道下命中的模型实体名
type Candidate struct {
	Channel   Channel
	ModelName string
}

// SelectModels 展开映射绑定的模型实体：仅启用渠道，按渠道优先级 DESC、模型 id ASC 排序
func (s *Store) SelectModels(ctx context.Context, mappingID int64) ([]Candidate, error) {
	rows, err := s.db.QueryContext(ctx, `
		SELECT c.id, c.name, c.type, c.base_url, c.api_key, c.priority, c.enabled, c.test_model, c.created_at,
		       m.name
		FROM model_mapping_models mm
		JOIN models m ON m.id = mm.model_id
		JOIN channels c ON c.id = m.channel_id
		WHERE mm.mapping_id=? AND c.enabled=1
		ORDER BY c.priority DESC, m.id ASC`, mappingID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []Candidate{}
	for rows.Next() {
		var c Candidate
		if err := rows.Scan(&c.Channel.ID, &c.Channel.Name, &c.Channel.Type, &c.Channel.BaseURL,
			&c.Channel.APIKey, &c.Channel.Priority, &c.Channel.Enabled, &c.Channel.TestModel,
			&c.Channel.CreatedAt, &c.ModelName); err != nil {
			return nil, err
		}
		out = append(out, c)
	}
	return out, rows.Err()
}
