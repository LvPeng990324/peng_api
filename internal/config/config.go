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
		Addr:                   envOr("PENG_ADDR", ":8080"),
		DBPath:                 envOr("PENG_DB", "./peng.db"),
		AdminPassword:          envOr("PENG_ADMIN_PASSWORD", ""),
		LogRetentionDays:       envOrInt("PENG_LOG_RETENTION_DAYS", 30),
		RequestTimeout:         time.Duration(envOrInt("PENG_REQUEST_TIMEOUT", 300)) * time.Second,
		ConnectTimeout:         time.Duration(envOrInt("PENG_CONNECT_TIMEOUT", 10)) * time.Second,
		StreamFirstByteTimeout: time.Duration(envOrInt("PENG_STREAM_FIRST_BYTE_TIMEOUT", 60)) * time.Second,
	}
	if *addr != "" {
		cfg.Addr = *addr
	}
	if *dbPath != "" {
		cfg.DBPath = *dbPath
	}
	if *password != "" {
		cfg.AdminPassword = *password
	}
	if *retention != 0 {
		cfg.LogRetentionDays = *retention
	}
	if *reqTimeout != 0 {
		cfg.RequestTimeout = time.Duration(*reqTimeout) * time.Second
	}
	if *connTimeout != 0 {
		cfg.ConnectTimeout = time.Duration(*connTimeout) * time.Second
	}
	if *firstByte != 0 {
		cfg.StreamFirstByteTimeout = time.Duration(*firstByte) * time.Second
	}
	if cfg.AdminPassword == "" {
		return Config{}, fmt.Errorf("admin password required: use -admin-password, PENG_ADMIN_PASSWORD or .env")
	}
	return cfg, nil
}

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

func envOrInt(key string, def int) int {
	if v := os.Getenv(key); v != "" {
		if n, err := strconv.Atoi(v); err == nil {
			return n
		}
	}
	return def
}
