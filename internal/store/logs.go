package store

import (
	"context"
	"database/sql"
	"strings"
	"time"
)

type LogEntry struct {
	ID                   int64     `json:"id"`
	RequestID            string    `json:"request_id"`
	Attempt              int       `json:"attempt"`
	TokenID              *int64    `json:"token_id"`
	TokenName            string    `json:"token_name"`
	Agent                string    `json:"agent"`
	ModelRequested       string    `json:"model_requested"`
	ModelCanonical       string    `json:"model_canonical"`
	ModelActual          string    `json:"model_actual"`
	ChannelID            *int64    `json:"channel_id"`
	ChannelName          string    `json:"channel_name"`
	Stream               bool      `json:"stream"`
	Status               string    `json:"status"`
	HTTPStatus           *int      `json:"http_status"`
	Error                string    `json:"error"`
	RequestBody          string    `json:"request_body,omitempty"`
	ResponseBody         string    `json:"response_body,omitempty"`
	PromptTokens         *int      `json:"prompt_tokens"`
	CompletionTokens     *int      `json:"completion_tokens"`
	PromptCacheHitTokens *int      `json:"prompt_cache_hit_tokens"`
	LatencyMS            int64     `json:"latency_ms"`
	FirstByteMS          int64     `json:"first_byte_ms"`
	CreatedAt            time.Time `json:"created_at"`

	// 仅内存行使用，不进 logColumns、不落库：
	// live_key 是进行中尝试的稳定标识（request_id:attempt），live_completion_tokens 是流式近似已输出 token。
	LiveKey              string `json:"live_key,omitempty"`
	LiveCompletionTokens *int   `json:"live_completion_tokens,omitempty"`
}

// logColumns 是 request_logs 的唯一列顺序定义：INSERT 列清单、SELECT 列清单、
// 写入值与 Scan 目标全部由它派生，新增列只需改这里以及 logValues/logTargets。
var logColumns = []string{
	"id", "request_id", "attempt", "token_id", "token_name", "agent",
	"model_requested", "model_canonical", "model_actual", "channel_id", "channel_name",
	"stream", "status", "http_status", "error", "request_body", "response_body",
	"prompt_tokens", "completion_tokens", "prompt_cache_hit_tokens", "latency_ms",
	"first_byte_ms", "created_at",
}

var logBodyColumns = map[string]bool{"request_body": true, "response_body": true}

const (
	defaultLogPerPage = 20
	maxLogPerPage     = 200
)

// 日志状态：一次尝试从 processing 起，收口到 success / failed。
// interrupted 不是独立状态，统一记为 failed + error 说明原因。
const (
	LogStatusProcessing = "processing"
	LogStatusSuccess    = "success"
	LogStatusFailed     = "failed"
)

func visibleLogColumns(withBody bool) []string {
	cols := make([]string, 0, len(logColumns))
	for _, c := range logColumns {
		if !withBody && logBodyColumns[c] {
			continue
		}
		cols = append(cols, c)
	}
	return cols
}

func logSelectColumns(withBody bool) string {
	return strings.Join(visibleLogColumns(withBody), ", ")
}

func placeholders(n int) string {
	return strings.TrimSuffix(strings.Repeat("?, ", n), ", ")
}

// logValues 必须与 logColumns[1:] 一一对应（首列 id 为自增，不写入）。
func (e LogEntry) logValues() []any {
	return []any{
		e.RequestID, e.Attempt, e.TokenID, e.TokenName, e.Agent,
		e.ModelRequested, e.ModelCanonical, e.ModelActual, e.ChannelID, e.ChannelName,
		e.Stream, e.Status, e.HTTPStatus, e.Error, e.RequestBody, e.ResponseBody,
		e.PromptTokens, e.CompletionTokens, e.PromptCacheHitTokens, e.LatencyMS,
		e.FirstByteMS, e.CreatedAt,
	}
}

func (s *Store) InsertLog(ctx context.Context, e LogEntry) (int64, error) {
	if e.CreatedAt.IsZero() {
		e.CreatedAt = time.Now().UTC()
	}
	cols := logColumns[1:]
	q := `INSERT INTO request_logs (` + strings.Join(cols, ", ") +
		`) VALUES (` + placeholders(len(cols)) + `)`
	res, err := s.db.ExecContext(ctx, q, e.logValues()...)
	if err != nil {
		return 0, err
	}
	return res.LastInsertId()
}

type LogFilter struct {
	Model     string
	Status    string
	Agent     string
	TokenID   *int64
	ChannelID *int64
	StartTime *time.Time
	EndTime   *time.Time
	Page      int
	Size      int
}

// Normalize 归一化分页参数；store 与 admin 共用，避免两处各写一份阈值。
func (f *LogFilter) Normalize() {
	if f.Page < 1 {
		f.Page = 1
	}
	if f.Size < 1 || f.Size > maxLogPerPage {
		f.Size = defaultLogPerPage
	}
}

func (f LogFilter) where() (string, []any) {
	var conds []string
	var args []any
	if f.Model != "" {
		like := "%" + f.Model + "%"
		conds = append(conds, "(model_requested LIKE ? OR model_canonical LIKE ? OR model_actual LIKE ?)")
		args = append(args, like, like, like)
	}
	if f.Status != "" {
		conds = append(conds, "status = ?")
		args = append(args, f.Status)
	}
	if f.Agent != "" {
		conds = append(conds, "agent LIKE ?")
		args = append(args, "%"+f.Agent+"%")
	}
	if f.TokenID != nil {
		conds = append(conds, "token_id = ?")
		args = append(args, *f.TokenID)
	}
	if f.ChannelID != nil {
		conds = append(conds, "channel_id = ?")
		args = append(args, *f.ChannelID)
	}
	if f.StartTime != nil {
		conds = append(conds, "created_at >= ?")
		args = append(args, *f.StartTime)
	}
	if f.EndTime != nil {
		conds = append(conds, "created_at <= ?")
		args = append(args, *f.EndTime)
	}
	if len(conds) == 0 {
		return "", nil
	}
	return " WHERE " + strings.Join(conds, " AND "), args
}

func (s *Store) ListLogs(ctx context.Context, f LogFilter) ([]LogEntry, int, error) {
	f.Normalize()
	where, args := f.where()
	var total int
	if err := s.db.QueryRowContext(ctx,
		`SELECT COUNT(*) FROM request_logs`+where, args...).Scan(&total); err != nil {
		return nil, 0, err
	}
	q := `SELECT ` + logSelectColumns(false) + ` FROM request_logs` + where + ` ORDER BY id DESC LIMIT ? OFFSET ?`
	rows, err := s.db.QueryContext(ctx, q, append(args, f.Size, (f.Page-1)*f.Size)...)
	if err != nil {
		return nil, 0, err
	}
	defer rows.Close()
	out := []LogEntry{}
	for rows.Next() {
		e, err := scanLog(rows, false)
		if err != nil {
			return nil, 0, err
		}
		out = append(out, *e)
	}
	return out, total, rows.Err()
}

func (s *Store) GetLog(ctx context.Context, id int64) (*LogEntry, error) {
	row := s.db.QueryRowContext(ctx,
		`SELECT `+logSelectColumns(true)+` FROM request_logs WHERE id=?`, id)
	e, err := scanLog(row, true)
	if err == sql.ErrNoRows {
		return nil, nil
	}
	return e, err
}

func (s *Store) GetLogGroup(ctx context.Context, requestID string) ([]LogEntry, error) {
	rows, err := s.db.QueryContext(ctx,
		`SELECT `+logSelectColumns(true)+` FROM request_logs WHERE request_id=? ORDER BY attempt ASC`, requestID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []LogEntry{}
	for rows.Next() {
		e, err := scanLog(rows, true)
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

type logNulls struct {
	tokenID, channelID, httpStatus, promptTokens, completionTokens, cacheHit sql.NullInt64
}

// logTargets 是列名 → Scan 目标；与 logColumns 同名同序，避免列顺序靠手工对齐。
func logTargets(e *LogEntry, n *logNulls) map[string]any {
	return map[string]any{
		"id":                      &e.ID,
		"request_id":              &e.RequestID,
		"attempt":                 &e.Attempt,
		"token_id":                &n.tokenID,
		"token_name":              &e.TokenName,
		"agent":                   &e.Agent,
		"model_requested":         &e.ModelRequested,
		"model_canonical":         &e.ModelCanonical,
		"model_actual":            &e.ModelActual,
		"channel_id":              &n.channelID,
		"channel_name":            &e.ChannelName,
		"stream":                  &e.Stream,
		"status":                  &e.Status,
		"http_status":             &n.httpStatus,
		"error":                   &e.Error,
		"request_body":            &e.RequestBody,
		"response_body":           &e.ResponseBody,
		"prompt_tokens":           &n.promptTokens,
		"completion_tokens":       &n.completionTokens,
		"prompt_cache_hit_tokens": &n.cacheHit,
		"latency_ms":              &e.LatencyMS,
		"first_byte_ms":           &e.FirstByteMS,
		"created_at":              &e.CreatedAt,
	}
}

// scanLog 是唯一的日志扫描器：按 withBody 选列，并从 logTargets 取对应目标。
func scanLog(row logScanner, withBody bool) (*LogEntry, error) {
	var e LogEntry
	var n logNulls
	targets := logTargets(&e, &n)
	cols := visibleLogColumns(withBody)
	dest := make([]any, len(cols))
	for i, c := range cols {
		dest[i] = targets[c]
	}
	if err := row.Scan(dest...); err != nil {
		return nil, err
	}
	e.TokenID = nullI64(n.tokenID)
	e.ChannelID = nullI64(n.channelID)
	e.HTTPStatus = nullInt(n.httpStatus)
	e.PromptTokens = nullInt(n.promptTokens)
	e.CompletionTokens = nullInt(n.completionTokens)
	e.PromptCacheHitTokens = nullInt(n.cacheHit)
	return &e, nil
}
