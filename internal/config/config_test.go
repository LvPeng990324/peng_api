package config

import (
	"os"
	"path/filepath"
	"testing"
	"time"
)

func chdir(t *testing.T, dir string) {
	t.Helper()
	old, err := os.Getwd()
	if err != nil {
		t.Fatalf("Getwd: %v", err)
	}
	if err := os.Chdir(dir); err != nil {
		t.Fatalf("Chdir: %v", err)
	}
	t.Cleanup(func() { os.Chdir(old) })
}

func unsetenv(t *testing.T, keys ...string) {
	t.Helper()
	type saved struct {
		v   string
		ok  bool
	}
	old := make(map[string]saved, len(keys))
	for _, k := range keys {
		v, ok := os.LookupEnv(k)
		old[k] = saved{v, ok}
		os.Unsetenv(k)
	}
	t.Cleanup(func() {
		for k, s := range old {
			if s.ok {
				os.Setenv(k, s.v)
			} else {
				os.Unsetenv(k)
			}
		}
	})
}

func writeDotEnv(t *testing.T, dir, content string) {
	t.Helper()
	if err := os.WriteFile(filepath.Join(dir, ".env"), []byte(content), 0o600); err != nil {
		t.Fatalf("WriteFile: %v", err)
	}
}

func TestParseFromDotEnv(t *testing.T) {
	unsetenv(t, "PENG_ADMIN_PASSWORD", "PENG_ADDR", "PENG_DB", "PENG_LOG_RETENTION_DAYS",
		"PENG_FAIL_THRESHOLD", "PENG_REQUEST_TIMEOUT", "PENG_CONNECT_TIMEOUT", "PENG_STREAM_FIRST_BYTE_TIMEOUT")
	dir := t.TempDir()
	writeDotEnv(t, dir, `
# comment line
PENG_ADMIN_PASSWORD=filepass
PENG_ADDR=:9091

PENG_LOG_RETENTION_DAYS=7
`)
	chdir(t, dir)
	cfg, err := Parse(nil)
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}
	if cfg.AdminPassword != "filepass" {
		t.Errorf("AdminPassword = %q", cfg.AdminPassword)
	}
	if cfg.Addr != ":9091" {
		t.Errorf("Addr = %q", cfg.Addr)
	}
	if cfg.LogRetentionDays != 7 {
		t.Errorf("LogRetentionDays = %d", cfg.LogRetentionDays)
	}
	if cfg.DBPath != "./peng.db" {
		t.Errorf("DBPath = %q (should keep default)", cfg.DBPath)
	}
}

func TestEnvOverridesDotEnv(t *testing.T) {
	unsetenv(t, "PENG_ADDR", "PENG_DB", "PENG_LOG_RETENTION_DAYS",
		"PENG_FAIL_THRESHOLD", "PENG_REQUEST_TIMEOUT", "PENG_CONNECT_TIMEOUT", "PENG_STREAM_FIRST_BYTE_TIMEOUT")
	t.Setenv("PENG_ADMIN_PASSWORD", "realpass")
	dir := t.TempDir()
	writeDotEnv(t, dir, "PENG_ADMIN_PASSWORD=filepass\n")
	chdir(t, dir)
	cfg, err := Parse(nil)
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}
	if cfg.AdminPassword != "realpass" {
		t.Errorf("AdminPassword = %q, want real env to win", cfg.AdminPassword)
	}
}

func TestFlagOverridesDotEnv(t *testing.T) {
	unsetenv(t, "PENG_ADMIN_PASSWORD", "PENG_ADDR", "PENG_DB", "PENG_LOG_RETENTION_DAYS",
		"PENG_FAIL_THRESHOLD", "PENG_REQUEST_TIMEOUT", "PENG_CONNECT_TIMEOUT", "PENG_STREAM_FIRST_BYTE_TIMEOUT")
	dir := t.TempDir()
	writeDotEnv(t, dir, "PENG_ADMIN_PASSWORD=filepass\nPENG_ADDR=:9091\n")
	chdir(t, dir)
	cfg, err := Parse([]string{"-admin-password", "flagpass", "-addr", ":9092"})
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}
	if cfg.AdminPassword != "flagpass" || cfg.Addr != ":9092" {
		t.Errorf("unexpected: %+v", cfg)
	}
}

func TestParseMissingDotEnvIsOK(t *testing.T) {
	unsetenv(t, "PENG_ADMIN_PASSWORD")
	chdir(t, t.TempDir())
	if _, err := Parse([]string{"-admin-password", "x"}); err != nil {
		t.Fatalf("missing .env should be silently ignored: %v", err)
	}
}

func TestParseDefaults(t *testing.T) {
	cfg, err := Parse([]string{"-admin-password", "secret"})
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}
	if cfg.Addr != ":8080" || cfg.DBPath != "./peng.db" {
		t.Errorf("unexpected defaults: %+v", cfg)
	}
	if cfg.LogRetentionDays != 30 || cfg.FailThreshold != 3 {
		t.Errorf("unexpected numeric defaults: %+v", cfg)
	}
	if cfg.RequestTimeout != 300*time.Second || cfg.ConnectTimeout != 10*time.Second || cfg.StreamFirstByteTimeout != 60*time.Second {
		t.Errorf("unexpected timeouts: %+v", cfg)
	}
}

func TestParseMissingPassword(t *testing.T) {
	t.Setenv("PENG_ADMIN_PASSWORD", "")
	if _, err := Parse([]string{}); err == nil {
		t.Fatal("expected error when admin password missing")
	}
}

func TestParsePasswordFromEnv(t *testing.T) {
	t.Setenv("PENG_ADMIN_PASSWORD", "envpass")
	cfg, err := Parse(nil)
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}
	if cfg.AdminPassword != "envpass" {
		t.Errorf("AdminPassword = %q", cfg.AdminPassword)
	}
}

func TestParseFlagOverridesEnv(t *testing.T) {
	t.Setenv("PENG_ADMIN_PASSWORD", "envpass")
	cfg, err := Parse([]string{"-admin-password", "flagpass", "-addr", ":9090", "-request-timeout", "60"})
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}
	if cfg.AdminPassword != "flagpass" || cfg.Addr != ":9090" || cfg.RequestTimeout != 60*time.Second {
		t.Errorf("unexpected: %+v", cfg)
	}
}
