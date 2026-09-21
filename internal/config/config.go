package config

import (
	"flag"
	"fmt"
	"os"
	"time"
)

type Config struct {
	Addr                   string
	DBPath                 string
	AdminPassword          string
	LogRetentionDays       int
	FailThreshold          int
	RequestTimeout         time.Duration
	ConnectTimeout         time.Duration
	StreamFirstByteTimeout time.Duration
}

func Parse(args []string) (Config, error) {
	fs := flag.NewFlagSet("peng_api", flag.ContinueOnError)
	var (
		addr        = fs.String("addr", ":8080", "listen address")
		dbPath      = fs.String("db", "./peng.db", "sqlite file path")
		password    = fs.String("admin-password", "", "admin password (or PENG_ADMIN_PASSWORD)")
		retention   = fs.Int("log-retention-days", 30, "days to keep request logs")
		threshold   = fs.Int("fail-threshold", 3, "consecutive failures before auto-disable")
		reqTimeout  = fs.Int("request-timeout", 300, "non-stream upstream timeout in seconds")
		connTimeout = fs.Int("connect-timeout", 10, "upstream connect timeout in seconds")
		firstByte   = fs.Int("stream-first-byte-timeout", 60, "stream first-byte timeout in seconds")
	)
	if err := fs.Parse(args); err != nil {
		return Config{}, err
	}
	cfg := Config{
		Addr:                   *addr,
		DBPath:                 *dbPath,
		AdminPassword:          *password,
		LogRetentionDays:       *retention,
		FailThreshold:          *threshold,
		RequestTimeout:         time.Duration(*reqTimeout) * time.Second,
		ConnectTimeout:         time.Duration(*connTimeout) * time.Second,
		StreamFirstByteTimeout: time.Duration(*firstByte) * time.Second,
	}
	if cfg.AdminPassword == "" {
		cfg.AdminPassword = os.Getenv("PENG_ADMIN_PASSWORD")
	}
	if cfg.AdminPassword == "" {
		return Config{}, fmt.Errorf("admin password required: use -admin-password or PENG_ADMIN_PASSWORD")
	}
	return cfg, nil
}
