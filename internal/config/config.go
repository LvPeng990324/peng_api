package config

import (
	"flag"
	"fmt"
	"os"
	"strconv"
	"strings"
	"time"
)

type Config struct {
	Addr                   string
	DBPath                 string
	AdminPassword          string
	LogRetentionDays       int
	RequestTimeout         time.Duration
	ConnectTimeout         time.Duration
	StreamFirstByteTimeout time.Duration
}

// Parse 解析配置，优先级：命令行 flag > 环境变量 > .env 文件 > 内置默认值。
// 环境变量里的非法数值是配置错误，直接返回 error，不静默回落到默认值。
func Parse(args []string) (Config, error) {
	if err := loadDotEnv(".env"); err != nil {
		return Config{}, err
	}
	fs := flag.NewFlagSet("peng_api", flag.ContinueOnError)
	var (
		addr        = fs.String("addr", "", "listen address (or PENG_ADDR, default :8080)")
		dbPath      = fs.String("db", "", "sqlite file path (or PENG_DB, default ./peng.db)")
		password    = fs.String("admin-password", "", "admin password (or PENG_ADMIN_PASSWORD)")
		retention   = fs.Int("log-retention-days", 0, "days to keep request logs (or PENG_LOG_RETENTION_DAYS, default 30)")
		reqTimeout  = fs.Int("request-timeout", 0, "non-stream upstream timeout in seconds (or PENG_REQUEST_TIMEOUT, default 300)")
		connTimeout = fs.Int("connect-timeout", 0, "upstream connect timeout in seconds (or PENG_CONNECT_TIMEOUT, default 10)")
		firstByte   = fs.Int("stream-first-byte-timeout", 0, "stream first-byte timeout in seconds (or PENG_STREAM_FIRST_BYTE_TIMEOUT, default 60)")
	)
	if err := fs.Parse(args); err != nil {
		return Config{}, err
	}
	cfg := Config{
		Addr:          envOr("PENG_ADDR", ":8080"),
		DBPath:        envOr("PENG_DB", "./peng.db"),
		AdminPassword: envOr("PENG_ADMIN_PASSWORD", ""),
	}
	// 字符串项与数值项都按「env/默认 → flag 覆盖」处理，优先级规则只有一套。
	for _, item := range []struct {
		flag   string
		assign func(string)
	}{
		{*addr, func(v string) { cfg.Addr = v }},
		{*dbPath, func(v string) { cfg.DBPath = v }},
		{*password, func(v string) { cfg.AdminPassword = v }},
	} {
		if item.flag != "" {
			item.assign(item.flag)
		}
	}

	// 数值项：非法 env 值直接报错，flag 非空时覆盖。
	for _, item := range []struct {
		key    string
		def    int
		flag   int
		assign func(int)
	}{
		{"PENG_LOG_RETENTION_DAYS", 30, *retention, func(v int) { cfg.LogRetentionDays = v }},
		{"PENG_REQUEST_TIMEOUT", 300, *reqTimeout, func(v int) { cfg.RequestTimeout = seconds(v) }},
		{"PENG_CONNECT_TIMEOUT", 10, *connTimeout, func(v int) { cfg.ConnectTimeout = seconds(v) }},
		{"PENG_STREAM_FIRST_BYTE_TIMEOUT", 60, *firstByte, func(v int) { cfg.StreamFirstByteTimeout = seconds(v) }},
	} {
		v, err := envOrInt(item.key, item.def)
		if err != nil {
			return Config{}, err
		}
		if item.flag != 0 {
			v = item.flag
		}
		item.assign(v)
	}
	if cfg.AdminPassword == "" {
		return Config{}, fmt.Errorf("admin password required: use -admin-password, PENG_ADMIN_PASSWORD or .env")
	}
	return cfg, nil
}

func seconds(v int) time.Duration { return time.Duration(v) * time.Second }

// loadDotEnv 读取 .env 文件并把其中的键值写入环境变量；
// 已存在的环境变量不覆盖，文件不存在则静默跳过。
func loadDotEnv(path string) error {
	data, err := os.ReadFile(path)
	if err != nil {
		if os.IsNotExist(err) {
			return nil
		}
		return fmt.Errorf("read %s: %w", path, err)
	}
	for _, line := range strings.Split(string(data), "\n") {
		line = strings.TrimSpace(line)
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		key, value, ok := strings.Cut(line, "=")
		if !ok {
			continue
		}
		key = strings.TrimSpace(key)
		value = strings.Trim(strings.TrimSpace(value), `"'`)
		if key == "" {
			continue
		}
		if _, exists := os.LookupEnv(key); !exists {
			os.Setenv(key, value)
		}
	}
	return nil
}

func envOr(key, def string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return def
}

func envOrInt(key string, def int) (int, error) {
	v := os.Getenv(key)
	if v == "" {
		return def, nil
	}
	n, err := strconv.Atoi(v)
	if err != nil {
		return 0, fmt.Errorf("invalid %s=%q: must be an integer", key, v)
	}
	return n, nil
}
