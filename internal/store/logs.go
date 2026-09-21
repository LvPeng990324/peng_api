package store

import (
	"context"
	"database/sql"
	"strings"
	"time"
)

type LogEntry struct {
	ID               int64     `json:"id"`
	RequestID        string    `json:"request_id"`
	Attempt          int       `json:"attempt"`
	TokenID          *int64    `json:"token_id"`
	TokenName        string    `json:"token_name"`
	ModelRequested   string    `json:"model_requested"`
	ModelCanonical   string    `json:"model_canonical"`
	ChannelID        *int64    `json:"channel_id"`
	ChannelName      string    `json:"channel_name"`
	Stream           bool      `json:"stream"`
	Status           string    `json:"status"`
	HTTPStatus       *int      `json:"http_status"`
	Error            string    `json:"error"`
	RequestBody      string    `json:"request_body"`
	ResponseBody     string    `json:"response_body"`
	PromptTokens         *int      `json:"prompt_tokens"`
	CompletionTokens     *int      `json:"completion_tokens"`
	PromptCacheHitTokens *int      `json:"prompt_cache_hit_tokens"`
	LatencyMS        int64     `json:"latency_ms"`
	CreatedAt        time.Time `json:"created_at"`
}

func (s *Store) InsertLog(ctx context.Context, e LogEntry) (int64, error) {
	if e.CreatedAt.IsZero() {
		e.CreatedAt = time.Now().UTC()
	}
	res, err := s.db.ExecContext(ctx, `
		INSERT INTO request_logs
		(request_id, attempt, token_id, token_name, model_requested, model_canonical,
		 channel_id, channel_name, stream, status, http_status, error,
		 request_body, response_body, prompt_tokens, completion_tokens, prompt_cache_hit_tokens, latency_ms, created_at)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		e.RequestID, e.Attempt, e.TokenID, e.TokenName, e.ModelRequested, e.ModelCanonical,
		e.ChannelID, e.ChannelName, e.Stream, e.Status, e.HTTPStatus, e.Error,
		e.RequestBody, e.ResponseBody, e.PromptTokens, e.CompletionTokens, e.PromptCacheHitTokens, e.LatencyMS, e.CreatedAt)
	if err != nil {
		return 0, err
	}
	return res.LastInsertId()
}

type LogFilter struct {
	Model     string
	Status    string
	TokenID   *int64
	ChannelID *int64
	Page      int
	Size      int
}

func (f LogFilter) where() (string, []any) {
	var conds []string
	var args []any
	if f.Model != "" {
		like := "%" + f.Model + "%"
		conds = append(conds, "(model_requested LIKE ? OR model_canonical LIKE ?)")
		args = append(args, like, like)
	}
	if f.Status != "" {
		conds = append(conds, "status = ?")
		args = append(args, f.Status)
	}
	if f.TokenID != nil {
		conds = append(conds, "token_id = ?")
		args = append(args, *f.TokenID)
	}
	if f.ChannelID != nil {
		conds = append(conds, "channel_id = ?")
		args = append(args, *f.ChannelID)
	}
	if len(conds) == 0 {
		return "", nil
	}
	return " WHERE " + strings.Join(conds, " AND "), args
}

func (s *Store) ListLogs(ctx context.Context, f LogFilter) ([]LogEntry, int, error) {
	if f.Page < 1 {
		f.Page = 1
	}
	if f.Size < 1 || f.Size > 200 {
		f.Size = 20
	}
	where, args := f.where()
	var total int
	if err := s.db.QueryRowContext(ctx,
		`SELECT COUNT(*) FROM request_logs`+where, args...).Scan(&total); err != nil {
		return nil, 0, err
	}
	q := `SELECT id, request_id, attempt, token_id, token_name, model_requested, model_canonical,
		channel_id, channel_name, stream, status, http_status, error, request_body, response_body,
		prompt_tokens, completion_tokens, prompt_cache_hit_tokens, latency_ms, created_at
		FROM request_logs` + where + ` ORDER BY id DESC LIMIT ? OFFSET ?`
	rows, err := s.db.QueryContext(ctx, q, append(args, f.Size, (f.Page-1)*f.Size)...)
	if err != nil {
		return nil, 0, err
	}
	defer rows.Close()
	out := []LogEntry{}
	for rows.Next() {
		e, err := scanLog(rows)
		if err != nil {
			return nil, 0, err
		}
		out = append(out, *e)
	}
	return out, total, rows.Err()
}

func (s *Store) GetLog(ctx context.Context, id int64) (*LogEntry, error) {
	row := s.db.QueryRowContext(ctx, `
		SELECT id, request_id, attempt, token_id, token_name, model_requested, model_canonical,
		       channel_id, channel_name, stream, status, http_status, error, request_body, response_body,
		       prompt_tokens, completion_tokens, prompt_cache_hit_tokens, latency_ms, created_at
		FROM request_logs WHERE id=?`, id)
	e, err := scanLog(row)
	if err == sql.ErrNoRows {
		return nil, nil
	}
	return e, err
}

func (s *Store) GetLogGroup(ctx context.Context, requestID string) ([]LogEntry, error) {
	rows, err := s.db.QueryContext(ctx, `
		SELECT id, request_id, attempt, token_id, token_name, model_requested, model_canonical,
		       channel_id, channel_name, stream, status, http_status, error, request_body, response_body,
		       prompt_tokens, completion_tokens, prompt_cache_hit_tokens, latency_ms, created_at
		FROM request_logs WHERE request_id=? ORDER BY attempt ASC`, requestID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []LogEntry{}
	for rows.Next() {
		e, err := scanLog(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, *e)
	}
	return out, rows.Err()
}

func (s *Store) DeleteLogsBefore(ctx context.Context, cutoff time.Time) (int64, error) {
	res, err := s.db.ExecContext(ctx, `DELETE FROM request_logs WHERE created_at < ?`, cutoff.UTC())
	if err != nil {
		return 0, err
	}
	return res.RowsAffected()
}

type logScanner interface {
	Scan(dest ...any) error
}

func scanLog(row logScanner) (*LogEntry, error) {
	var e LogEntry
	var tokenID, channelID sql.NullInt64
	var httpStatus, promptTokens, completionTokens, cacheHit sql.NullInt64
	err := row.Scan(&e.ID, &e.RequestID, &e.Attempt, &tokenID, &e.TokenName,
		&e.ModelRequested, &e.ModelCanonical, &channelID, &e.ChannelName, &e.Stream,
		&e.Status, &httpStatus, &e.Error, &e.RequestBody, &e.ResponseBody,
		&promptTokens, &completionTokens, &cacheHit, &e.LatencyMS, &e.CreatedAt)
	if err != nil {
		return nil, err
	}
	if tokenID.Valid {
		e.TokenID = &tokenID.Int64
	}
	if channelID.Valid {
		e.ChannelID = &channelID.Int64
	}
	if httpStatus.Valid {
		v := int(httpStatus.Int64)
		e.HTTPStatus = &v
	}
	if promptTokens.Valid {
		v := int(promptTokens.Int64)
		e.PromptTokens = &v
	}
	if completionTokens.Valid {
		v := int(completionTokens.Int64)
		e.CompletionTokens = &v
	}
	if cacheHit.Valid {
		v := int(cacheHit.Int64)
		e.PromptCacheHitTokens = &v
	}
	return &e, nil
}
