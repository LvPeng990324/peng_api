package store

import (
	"context"
	"database/sql"
	"errors"
	"time"
)

// ErrNotFound 更新对象不存在（由 RowsAffected=0 判定）
var ErrNotFound = errors.New("store: not found")

// BoundModel 映射绑定的一个模型实体（附渠道名，供管理端展示）
type BoundModel struct {
	ID          int64  `json:"id"`
	Name        string `json:"name"`
	ChannelName string `json:"channel_name"`
}

// Mapping 模型映射（标准名）：请求入口，携带上下文参数，须绑定 ≥1 个模型实体才可被请求到。
// 请求按 name 大小写敏感精确匹配。
type Mapping struct {
	ID              int64        `json:"id"`
	Name            string       `json:"name"`
	ContextLength   *int64       `json:"context_length"`
	MaxOutputTokens *int64       `json:"max_output_tokens"`
	BoundModels     []BoundModel `json:"bound_models"`
	CreatedAt       time.Time    `json:"created_at"`
}

func (s *Store) CreateMapping(ctx context.Context, name string, contextLength, maxOutputTokens *int64, modelIDs []int64) (*Mapping, error) {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return nil, err
	}
	defer tx.Rollback()
	now := time.Now().UTC()
	res, err := tx.ExecContext(ctx,
		`INSERT INTO model_mappings (name, context_length, max_output_tokens, created_at) VALUES (?, ?, ?, ?)`,
		name, contextLength, maxOutputTokens, now)
	if err != nil {
		return nil, err
	}
	id, _ := res.LastInsertId()
	if err := replaceMappingBindings(ctx, tx, id, modelIDs); err != nil {
		return nil, err
	}
	if err := tx.Commit(); err != nil {
		return nil, err
	}
	bound, err := s.boundModelsOf(ctx, id)
	if err != nil {
		return nil, err
	}
	return &Mapping{ID: id, Name: name, ContextLength: contextLength, MaxOutputTokens: maxOutputTokens,
		BoundModels: bound, CreatedAt: now}, nil
}

// UpdateMapping 全量更新字段与绑定（model_ids 整体替换）；映射不存在返回 ErrNotFound
func (s *Store) UpdateMapping(ctx context.Context, id int64, name string, contextLength, maxOutputTokens *int64, modelIDs []int64) error {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	res, err := tx.ExecContext(ctx,
		`UPDATE model_mappings SET name=?, context_length=?, max_output_tokens=? WHERE id=?`,
		name, contextLength, maxOutputTokens, id)
	if err != nil {
		return err
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return ErrNotFound
	}
	if err := replaceMappingBindings(ctx, tx, id, modelIDs); err != nil {
		return err
	}
	return tx.Commit()
}

// replaceMappingBindings 全量替换绑定；调用方传来的重复 id 去重后插入
func replaceMappingBindings(ctx context.Context, tx *sql.Tx, mappingID int64, modelIDs []int64) error {
	if _, err := tx.ExecContext(ctx, `DELETE FROM model_mapping_models WHERE mapping_id=?`, mappingID); err != nil {
		return err
	}
	seen := make(map[int64]bool, len(modelIDs))
	for _, mid := range modelIDs {
		if seen[mid] {
			continue
		}
		seen[mid] = true
		if _, err := tx.ExecContext(ctx,
			`INSERT INTO model_mapping_models (mapping_id, model_id) VALUES (?, ?)`, mappingID, mid); err != nil {
			return err
		}
	}
	return nil
}

func (s *Store) DeleteMapping(ctx context.Context, id int64) error {
	_, err := s.db.ExecContext(ctx, `DELETE FROM model_mappings WHERE id=?`, id)
	return err // 绑定关系由外键 ON DELETE CASCADE 清理
}

func (s *Store) ListMappings(ctx context.Context) ([]Mapping, error) {
	rows, err := s.db.QueryContext(ctx,
		`SELECT id, name, context_length, max_output_tokens, created_at FROM model_mappings ORDER BY id`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []Mapping
	for rows.Next() {
		var m Mapping
		var cl, mo sql.NullInt64
		if err := rows.Scan(&m.ID, &m.Name, &cl, &mo, &m.CreatedAt); err != nil {
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
		bound, err := s.boundModelsOf(ctx, out[i].ID)
		if err != nil {
			return nil, err
		}
		out[i].BoundModels = bound
	}
	return out, nil
}

func (s *Store) boundModelsOf(ctx context.Context, mappingID int64) ([]BoundModel, error) {
	rows, err := s.db.QueryContext(ctx, `
		SELECT m.id, m.name, c.name
		FROM model_mapping_models mm
		JOIN models m ON m.id = mm.model_id
		JOIN channels c ON c.id = m.channel_id
		WHERE mm.mapping_id=? ORDER BY m.id`, mappingID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []BoundModel{}
	for rows.Next() {
		var b BoundModel
		if err := rows.Scan(&b.ID, &b.Name, &b.ChannelName); err != nil {
			return nil, err
		}
		out = append(out, b)
	}
	return out, rows.Err()
}

// ResolveMapping 按标准名精确匹配（大小写敏感），未命中返回 nil
func (s *Store) ResolveMapping(ctx context.Context, name string) (*Mapping, error) {
	var m Mapping
	var cl, mo sql.NullInt64
	err := s.db.QueryRowContext(ctx,
		`SELECT id, name, context_length, max_output_tokens, created_at FROM model_mappings WHERE name=?`, name).
		Scan(&m.ID, &m.Name, &cl, &mo, &m.CreatedAt)
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

// ListMappingsWithEnabledModels 至少绑定了一个「启用渠道下模型实体」的映射，供 /v1/models
func (s *Store) ListMappingsWithEnabledModels(ctx context.Context) ([]Mapping, error) {
	rows, err := s.db.QueryContext(ctx, `
		SELECT DISTINCT mp.id, mp.name, mp.context_length, mp.max_output_tokens, mp.created_at
		FROM model_mappings mp
		JOIN model_mapping_models mm ON mm.mapping_id = mp.id
		JOIN models m ON m.id = mm.model_id
		JOIN channels c ON c.id = m.channel_id
		WHERE c.enabled = 1
		ORDER BY mp.name`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []Mapping{}
	for rows.Next() {
		var m Mapping
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
