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
	Mappings    []string  `json:"mappings"`    // 绑定的标准名列表，供列表展示
	MappingIDs  []int64   `json:"mapping_ids"` // 绑定的标准名 id，供模型侧编辑弹窗回显
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
		ids, names, err := s.modelBindings(ctx, out[i].ID)
		if err != nil {
			return nil, err
		}
		out[i].MappingIDs = ids
		out[i].Mappings = names
	}
	return out, nil
}

// modelBindings 模型实体被哪些标准名绑定，一次查询同时给出 id（编辑回显）与 name（列表展示），按标准名排序
func (s *Store) modelBindings(ctx context.Context, modelID int64) ([]int64, []string, error) {
	rows, err := s.db.QueryContext(ctx, `
		SELECT mp.id, mp.name FROM model_mapping_models mm
		JOIN model_mappings mp ON mp.id = mm.mapping_id
		WHERE mm.model_id=? ORDER BY mp.name`, modelID)
	if err != nil {
		return nil, nil, err
	}
	defer rows.Close()
	ids := []int64{}
	names := []string{}
	for rows.Next() {
		var id int64
		var name string
		if err := rows.Scan(&id, &name); err != nil {
			return nil, nil, err
		}
		ids = append(ids, id)
		names = append(names, name)
	}
	return ids, names, rows.Err()
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
	ids, names, err := s.modelBindings(ctx, id)
	if err != nil {
		return nil, err
	}
	m.MappingIDs = ids
	m.Mappings = names
	return &m, nil
}

// UpdateModel 原子更新模型名与（可选）标准名绑定。
// mappingIDs == nil 表示不动绑定（兼容只改名的调用）；非 nil（含空切片）表示全量替换该模型与标准名的绑定。
// 模型不存在返回 ErrNotFound；mapping_id 不存在时由外键报错，整个事务回滚，不留半成品。
func (s *Store) UpdateModel(ctx context.Context, id int64, name string, mappingIDs *[]int64) error {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	res, err := tx.ExecContext(ctx, `UPDATE models SET name=? WHERE id=?`, name, id)
	if err != nil {
		return err
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return ErrNotFound
	}
	if mappingIDs != nil {
		if _, err := tx.ExecContext(ctx, `DELETE FROM model_mapping_models WHERE model_id=?`, id); err != nil {
			return err
		}
		seen := make(map[int64]bool, len(*mappingIDs))
		for _, mpID := range *mappingIDs {
			if seen[mpID] {
				continue
			}
			seen[mpID] = true
			if _, err := tx.ExecContext(ctx,
				`INSERT INTO model_mapping_models (mapping_id, model_id) VALUES (?, ?)`, mpID, id); err != nil {
				return err
			}
		}
	}
	return tx.Commit()
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
