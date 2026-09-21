package config

import (
	"testing"
	"time"
)

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
