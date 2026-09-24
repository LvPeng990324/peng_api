package store

import (
	"context"
	"database/sql"
	"strings"
	"time"
)

// Model 渠道下的模型实体（上游真实模型名），请求时替换 body 的 model 字段就用它。
// 可用性 = 所属渠道 enabled，查询时动态推导，不落库。
type Model struct {
	ID          int64     `json:"id"`
	ChannelID   int64     `json:"channel_id"`
	ChannelName string    `json:"channel_name"`
	Name        string    `json:"name"`
	Mappings    []string  `json:"mappings"` // 绑定的标准名列表
	CreatedAt   time.Time `json:"created_at"`
}

// CreateModel 创建模型实体；同渠道下重名时返回已存在的实体（幂等，供 fetch 流程批量添加）
func (s *Store) CreateModel(ctx context.Context, channelID int64, name string) (*Model, error) {
	now := time.Now().UTC()
	res, err := s.db.ExecContext(ctx,
		`INSERT INTO models (channel_id, name, created_at) VALUES (?, ?, ?)`,
		channelID, name, now)
	if err != nil {
		if isUniqueConstraintErr(err) {
			return s.modelByName(ctx, channelID, name)
		}
		return nil, err
	}
	id, _ := res.LastInsertId()
	return &Model{ID: id, ChannelID: channelID, Name: name, CreatedAt: now}, nil
}

func (s *Store) modelByName(ctx context.Context, channelID int64, name string) (*Model, error) {
	var m Model
	err := s.db.QueryRowContext(ctx,
		`SELECT id, channel_id, name, created_at FROM models WHERE channel_id=? AND name=?`,
		channelID, name).Scan(&m.ID, &m.ChannelID, &m.Name, &m.CreatedAt)
	if err != nil {
		return nil, err
	}
	return &m, nil
}

func (s *Store) ListModels(ctx context.Context) ([]Model, error) {
	return s.queryModels(ctx, `
		SELECT m.id, m.channel_id, m.name, c.name, m.created_at
		FROM models m JOIN channels c ON c.id = m.channel_id
		ORDER BY m.id`)
}

// ModelsOfChannel 某渠道下的全部模型实体
func (s *Store) ModelsOfChannel(ctx context.Context, channelID int64) ([]Model, error) {
	return s.queryModels(ctx, `
		SELECT m.id, m.channel_id, m.name, c.name, m.created_at
		FROM models m JOIN channels c ON c.id = m.channel_id
		WHERE m.channel_id=?
		ORDER BY m.id`, channelID)
}

func (s *Store) queryModels(ctx context.Context, query string, args ...any) ([]Model, error) {
	rows, err := s.db.QueryContext(ctx, query, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []Model{}
	for rows.Next() {
		var m Model
		if err := rows.Scan(&m.ID, &m.ChannelID, &m.Name, &m.ChannelName, &m.CreatedAt); err != nil {
			return nil, err
		}
		out = append(out, m)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	for i := range out {
		mappings, err := s.mappingsOfModel(ctx, out[i].ID)
		if err != nil {
			return nil, err
		}
		out[i].Mappings = mappings
	}
	return out, nil
}

// mappingsOfModel 模型实体被哪些标准名绑定
func (s *Store) mappingsOfModel(ctx context.Context, modelID int64) ([]string, error) {
	rows, err := s.db.QueryContext(ctx, `
		SELECT mp.name FROM model_mapping_models mm
		JOIN model_mappings mp ON mp.id = mm.mapping_id
		WHERE mm.model_id=? ORDER BY mp.name`, modelID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []string{}
	for rows.Next() {
		var name string
		if err := rows.Scan(&name); err != nil {
			return nil, err
		}
		out = append(out, name)
	}
	return out, rows.Err()
}

func (s *Store) GetModel(ctx context.Context, id int64) (*Model, error) {
	var m Model
	err := s.db.QueryRowContext(ctx, `
		SELECT m.id, m.channel_id, m.name, c.name, m.created_at
		FROM models m JOIN channels c ON c.id = m.channel_id WHERE m.id=?`, id).
		Scan(&m.ID, &m.ChannelID, &m.Name, &m.ChannelName, &m.CreatedAt)
	if err == sql.ErrNoRows {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	mappings, err := s.mappingsOfModel(ctx, id)
	if err != nil {
		return nil, err
	}
	m.Mappings = mappings
	return &m, nil
}

// UpdateModel 模型实体改名（同渠道重名会撞 UNIQUE 约束，由调用方翻译为 400）
func (s *Store) UpdateModel(ctx context.Context, id int64, name string) error {
	_, err := s.db.ExecContext(ctx, `UPDATE models SET name=? WHERE id=?`, name, id)
	return err
}

func (s *Store) DeleteModel(ctx context.Context, id int64) error {
	_, err := s.db.ExecContext(ctx, `DELETE FROM models WHERE id=?`, id)
	return err // 映射绑定由外键 ON DELETE CASCADE 清理
}

// isUniqueConstraintErr 只认 UNIQUE 冲突；FOREIGN KEY 等约束错误必须漏给调用方
func isUniqueConstraintErr(err error) bool {
	return strings.Contains(err.Error(), "UNIQUE constraint failed")
}

func nullI64(v sql.NullInt64) *int64 {
	if !v.Valid {
		return nil
	}
	return &v.Int64
}
