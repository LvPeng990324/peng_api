package store

import (
	"context"
	"crypto/rand"
	"database/sql"
	"encoding/hex"
	"time"
)

type Token struct {
	ID        int64     `json:"id"`
	Name      string    `json:"name"`
	Token     string    `json:"token"`
	Enabled   bool      `json:"enabled"`
	CreatedAt time.Time `json:"created_at"`
}

func newRandomKey() (string, error) {
	b := make([]byte, 24)
	if _, err := rand.Read(b); err != nil {
		return "", err
	}
	return "sk-" + hex.EncodeToString(b), nil
}

func (s *Store) CreateToken(ctx context.Context, name string) (*Token, error) {
	value, err := newRandomKey()
	if err != nil {
		return nil, err
	}
	now := time.Now().UTC()
	res, err := s.db.ExecContext(ctx,
		`INSERT INTO tokens (name, token, enabled, created_at) VALUES (?, ?, 1, ?)`,
		name, value, now)
	if err != nil {
		return nil, err
	}
	id, _ := res.LastInsertId()
	return &Token{ID: id, Name: name, Token: value, Enabled: true, CreatedAt: now}, nil
}

func (s *Store) ListTokens(ctx context.Context) ([]Token, error) {
	rows, err := s.db.QueryContext(ctx,
		`SELECT id, name, token, enabled, created_at FROM tokens ORDER BY id`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []Token
	for rows.Next() {
		var t Token
		if err := rows.Scan(&t.ID, &t.Name, &t.Token, &t.Enabled, &t.CreatedAt); err != nil {
			return nil, err
		}
		out = append(out, t)
	}
	return out, rows.Err()
}

func (s *Store) UpdateToken(ctx context.Context, id int64, name string, enabled bool) error {
	_, err := s.db.ExecContext(ctx,
		`UPDATE tokens SET name=?, enabled=? WHERE id=?`, name, enabled, id)
	return err
}

func (s *Store) DeleteToken(ctx context.Context, id int64) error {
	_, err := s.db.ExecContext(ctx, `DELETE FROM tokens WHERE id=?`, id)
	return err
}

func (s *Store) GetToken(ctx context.Context, id int64) (*Token, error) {
	var t Token
	err := s.db.QueryRowContext(ctx,
		`SELECT id, name, token, enabled, created_at FROM tokens WHERE id=?`,
		id).Scan(&t.ID, &t.Name, &t.Token, &t.Enabled, &t.CreatedAt)
	if err == sql.ErrNoRows {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	return &t, nil
}

func (s *Store) FindTokenByValue(ctx context.Context, value string) (*Token, error) {
	var t Token
	err := s.db.QueryRowContext(ctx,
		`SELECT id, name, token, enabled, created_at FROM tokens WHERE token=? AND enabled=1`,
		value).Scan(&t.ID, &t.Name, &t.Token, &t.Enabled, &t.CreatedAt)
	if err == sql.ErrNoRows {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	return &t, nil
}
