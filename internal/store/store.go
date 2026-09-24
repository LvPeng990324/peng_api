package store

import (
	"database/sql"

	_ "modernc.org/sqlite"
)

type Store struct{ db *sql.DB }

// 三级结构：channels 1─n models（模型实体）n─n model_mappings（标准名）
// 模型可用性不落库，查询时按渠道 enabled 动态推导
const schema = `
CREATE TABLE IF NOT EXISTS channels (
  id INTEGER PRIMARY KEY AUTOINCREMENT,
  name TEXT NOT NULL,
  type TEXT NOT NULL DEFAULT 'openai',
  base_url TEXT NOT NULL,
  api_key TEXT NOT NULL,
  priority INTEGER NOT NULL DEFAULT 0,
  enabled INTEGER NOT NULL DEFAULT 1,
  test_model TEXT NOT NULL DEFAULT '',
  created_at DATETIME DEFAULT CURRENT_TIMESTAMP
);
CREATE TABLE IF NOT EXISTS models (
  id INTEGER PRIMARY KEY AUTOINCREMENT,
  channel_id INTEGER NOT NULL REFERENCES channels(id) ON DELETE CASCADE,
  name TEXT NOT NULL,
  created_at DATETIME DEFAULT CURRENT_TIMESTAMP,
  UNIQUE(channel_id, name)
);
CREATE TABLE IF NOT EXISTS model_mappings (
  id INTEGER PRIMARY KEY AUTOINCREMENT,
  name TEXT NOT NULL UNIQUE,
  context_length INTEGER,
  max_output_tokens INTEGER,
  created_at DATETIME DEFAULT CURRENT_TIMESTAMP
);
CREATE TABLE IF NOT EXISTS model_mapping_models (
  mapping_id INTEGER NOT NULL REFERENCES model_mappings(id) ON DELETE CASCADE,
  model_id INTEGER NOT NULL REFERENCES models(id) ON DELETE CASCADE,
  PRIMARY KEY (mapping_id, model_id)
);
CREATE TABLE IF NOT EXISTS tokens (
  id INTEGER PRIMARY KEY AUTOINCREMENT,
  name TEXT NOT NULL,
  token TEXT NOT NULL UNIQUE,
  enabled INTEGER NOT NULL DEFAULT 1,
  created_at DATETIME DEFAULT CURRENT_TIMESTAMP
);
CREATE TABLE IF NOT EXISTS request_logs (
  id INTEGER PRIMARY KEY AUTOINCREMENT,
  request_id TEXT NOT NULL,
  attempt INTEGER NOT NULL,
  token_id INTEGER,
  token_name TEXT NOT NULL DEFAULT '',
  model_requested TEXT NOT NULL,
  model_canonical TEXT NOT NULL DEFAULT '',
  channel_id INTEGER,
  channel_name TEXT NOT NULL DEFAULT '',
  stream INTEGER NOT NULL DEFAULT 0,
  status TEXT NOT NULL,
  http_status INTEGER,
  error TEXT NOT NULL DEFAULT '',
  request_body TEXT NOT NULL DEFAULT '',
  response_body TEXT NOT NULL DEFAULT '',
  prompt_tokens INTEGER,
  completion_tokens INTEGER,
  prompt_cache_hit_tokens INTEGER,
  latency_ms INTEGER NOT NULL DEFAULT 0,
  created_at DATETIME DEFAULT CURRENT_TIMESTAMP
);
CREATE INDEX IF NOT EXISTS idx_logs_created ON request_logs(created_at DESC);
CREATE INDEX IF NOT EXISTS idx_logs_request ON request_logs(request_id);
`

func Open(path string) (*Store, error) {
	db, err := sql.Open("sqlite", path)
	if err != nil {
		return nil, err
	}
	// 单连接：SQLite 单写者；同时保证 :memory: 测试库不被连接池分裂
	db.SetMaxOpenConns(1)
	for _, pragma := range []string{
		"PRAGMA journal_mode=WAL",
		"PRAGMA foreign_keys=ON",
		"PRAGMA busy_timeout=5000",
	} {
		if _, err := db.Exec(pragma); err != nil {
			db.Close()
			return nil, err
		}
	}
	// 旧「渠道↔标准模型↔别名」结构检测：存在 model_aliases 表即视为 legacy，
	// 整体 DROP 重建（tokens/request_logs 保留，日志里的 channel_id 本就是无约束引用）
	legacy, err := hasTable(db, "model_aliases")
	if err != nil {
		db.Close()
		return nil, err
	}
	if legacy {
		for _, stmt := range []string{
			`DROP TABLE IF EXISTS model_aliases`,
			`DROP TABLE IF EXISTS channel_models`,
			`DROP TABLE IF EXISTS models`,
			`DROP TABLE IF EXISTS channels`,
		} {
			if _, err := db.Exec(stmt); err != nil {
				db.Close()
				return nil, err
			}
		}
	}
	if _, err := db.Exec(schema); err != nil {
		db.Close()
		return nil, err
	}
	// 旧库补列：CREATE TABLE IF NOT EXISTS 不会更新已存在的表
	if err := ensureColumn(db, "request_logs", "prompt_cache_hit_tokens", "INTEGER"); err != nil {
		db.Close()
		return nil, err
	}
	return &Store{db: db}, nil
}

func hasTable(db *sql.DB, name string) (bool, error) {
	var got string
	err := db.QueryRow(`SELECT name FROM sqlite_master WHERE type='table' AND name=?`, name).Scan(&got)
	if err == sql.ErrNoRows {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	return true, nil
}

// ensureColumn 为已存在的表补列（列已存在时不动）
func ensureColumn(db *sql.DB, table, column, ddl string) error {
	rows, err := db.Query(`SELECT name FROM pragma_table_info('` + table + `')`)
	if err != nil {
		return err
	}
	defer rows.Close()
	for rows.Next() {
		var name string
		if err := rows.Scan(&name); err != nil {
			return err
		}
		if name == column {
			return nil
		}
	}
	if err := rows.Err(); err != nil {
		return err
	}
	_, err = db.Exec(`ALTER TABLE ` + table + ` ADD COLUMN ` + column + ` ` + ddl)
	return err
}

func (s *Store) Close() error { return s.db.Close() }
