package config

import (
	"os"
	"path/filepath"
	"testing"
)

func TestLoadDashboardConfig(t *testing.T) {
	path := filepath.Join(t.TempDir(), "config.yml")
	if err := os.WriteFile(path, []byte(`dashboard:
  listen: 127.0.0.1:5037
  agent_token: configured-agent-token
  data_dir: /data/control
  history_data_dir: /data/history
  history_retention: 2160h
  history_sample_interval: 1m
  cookie_secure: true
`), 0o600); err != nil {
		t.Fatal(err)
	}
	cfg, err := Load(path)
	if err != nil {
		t.Fatal(err)
	}
	if cfg.Dashboard.AgentToken != "configured-agent-token" || cfg.Dashboard.Listen != "127.0.0.1:5037" || cfg.Dashboard.DataDir != "/data/control" || cfg.Dashboard.HistoryDataDir != "/data/history" || !cfg.Dashboard.CookieSecure {
		t.Fatalf("unexpected config: %+v", cfg.Dashboard)
	}
}

func TestLoadRejectsRemovedAuthenticationFields(t *testing.T) {
	path := filepath.Join(t.TempDir(), "config.yaml")
	if err := os.WriteFile(path, []byte(`dashboard:
  admin_password: old-password
`), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := Load(path); err == nil {
		t.Fatal("removed authentication fields must be rejected")
	}
}

func TestDefaultAdminCredentials(t *testing.T) {
	cfg := Default()
	if AdminUsername != "admin" || DefaultAdminPassword != "123456" {
		t.Fatalf("unexpected bootstrap credentials: user=%q password=%q", AdminUsername, DefaultAdminPassword)
	}
	if cfg.Dashboard.AgentToken != "" {
		t.Fatalf("default agent token must be empty: %q", cfg.Dashboard.AgentToken)
	}
	if cfg.Dashboard.DataDir != "/dashboard/data/control" || cfg.Dashboard.HistoryDataDir != "/dashboard/data/history" {
		t.Fatalf("unexpected default data paths: %+v", cfg.Dashboard)
	}
}
