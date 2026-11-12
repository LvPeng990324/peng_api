package store

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"time"
)

// ErrNotFound 更新对象不存在（由 RowsAffected=0 判定）
var ErrNotFound = errors.New("store: not found")

// BoundModel 映射绑定的一个模型实体（附渠道名/优先级/启用状态，供管理端按真实调用顺序展示）
type BoundModel struct {
	ID              int64  `json:"id"`
	Name            string `json:"name"`
	ChannelName     string `json:"channel_name"`
	ChannelPriority int    `json:"channel_priority"`
	ChannelEnabled  bool   `json:"channel_enabled"`
}

// Mapping 模型映射（标准名）：请求入口，携带上下文参数，须绑定 ≥1 个模型实体才可被请求到。
// 请求按 name 大小写敏感精确匹配。
// ParameterOverrides 是强制覆盖的上游请求参数（如 temperature 固定为 1），转发时覆盖客户端同名字段。
type Mapping struct {
	ID                 int64          `json:"id"`
	Name               string         `json:"name"`
	ContextLength      *int64         `json:"context_length"`
	MaxOutputTokens    *int64         `json:"max_output_tokens"`
	ParameterOverrides map[string]any `json:"parameter_overrides"`
	BoundModels        []BoundModel   `json:"bound_models"`
	CreatedAt          time.Time      `json:"created_at"`
}

// encodeOverrides 序列化为 JSON 文本入库；空映射存 ''（扫描侧归一为 nil）
func encodeOverrides(overrides map[string]any) (string, error) {
	if len(overrides) == 0 {
		return "", nil
	}
	b, err := json.Marshal(overrides)
	if err != nil {
		return "", err
	}
	return string(b), nil
}

func decodeOverrides(text string) (map[string]any, error) {
	if text == "" {
		return nil, nil
	}
	var m map[string]any
	if err := json.Unmarshal([]byte(text), &m); err != nil {
		return nil, err
	}
	return m, nil
}

func (s *Store) CreateMapping(ctx context.Context, name string, contextLength, maxOutputTokens *int64, modelIDs []int64, overrides map[string]any) (*Mapping, error) {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return nil, err
	}
	defer tx.Rollback()
	now := time.Now().UTC()
	ovText, err := encodeOverrides(overrides)
	if err != nil {
		return nil, err
	}
	res, err := tx.ExecContext(ctx,
		`INSERT INTO model_mappings (name, context_length, max_output_tokens, parameter_overrides, created_at) VALUES (?, ?, ?, ?, ?)`,
		name, contextLength, maxOutputTokens, ovText, now)
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
		ParameterOverrides: overrides, BoundModels: bound, CreatedAt: now}, nil
}

// UpdateMapping 全量更新字段与绑定（model_ids 整体替换）；映射不存在返回 ErrNotFound
func (s *Store) UpdateMapping(ctx context.Context, id int64, name string, contextLength, maxOutputTokens *int64, modelIDs []int64, overrides map[string]any) error {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	ovText, err := encodeOverrides(overrides)
	if err != nil {
		return err
	}
	res, err := tx.ExecContext(ctx,
		`UPDATE model_mappings SET name=?, context_length=?, max_output_tokens=?, parameter_overrides=? WHERE id=?`,
		name, contextLength, maxOutputTokens, ovText, id)
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
		`SELECT id, name, context_length, max_output_tokens, parameter_overrides, created_at FROM model_mappings ORDER BY id`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []Mapping
	for rows.Next() {
		var m Mapping
		var cl, mo sql.NullInt64
		var ovText string
		if err := rows.Scan(&m.ID, &m.Name, &cl, &mo, &ovText, &m.CreatedAt); err != nil {
			return nil, err
		}
		m.ContextLength = nullI64(cl)
		m.MaxOutputTokens = nullI64(mo)
		if m.ParameterOverrides, err = decodeOverrides(ovText); err != nil {
			return nil, err
		}
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

// boundModelsOf 返回管理端展示用的绑定列表，顺序与 SelectModels 的真实调用顺序对齐：
// 启用渠道按优先级 DESC、模型 id ASC，禁用渠道的模型排在末尾。
// 注意：这里不能过滤禁用渠道——编辑映射时 model_ids 由该列表生成并整体替换，
// 过滤会导致用户在编辑页保存时误删禁用渠道下的绑定。
func (s *Store) boundModelsOf(ctx context.Context, mappingID int64) ([]BoundModel, error) {
	rows, err := s.db.QueryContext(ctx, `
		SELECT m.id, m.name, c.name, c.priority, c.enabled
		FROM model_mapping_models mm
		JOIN models m ON m.id = mm.model_id
		JOIN channels c ON c.id = m.channel_id
		WHERE mm.mapping_id=?
		ORDER BY c.enabled DESC, c.priority DESC, m.id ASC`, mappingID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []BoundModel{}
	for rows.Next() {
		var b BoundModel
		if err := rows.Scan(&b.ID, &b.Name, &b.ChannelName, &b.ChannelPriority, &b.ChannelEnabled); err != nil {
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
	var ovText string
	err := s.db.QueryRowContext(ctx,
		`SELECT id, name, context_length, max_output_tokens, parameter_overrides, created_at FROM model_mappings WHERE name=?`, name).
		Scan(&m.ID, &m.Name, &cl, &mo, &ovText, &m.CreatedAt)
	if err == sql.ErrNoRows {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	m.ContextLength = nullI64(cl)
	m.MaxOutputTokens = nullI64(mo)
	if m.ParameterOverrides, err = decodeOverrides(ovText); err != nil {
		return nil, err
	}
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
