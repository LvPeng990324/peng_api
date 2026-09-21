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
