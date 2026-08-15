package config

import (
	"os"
	"path/filepath"
	"testing"
)

func TestLoad(t *testing.T) {
	path := filepath.Join(t.TempDir(), "config.yml")
	data := []byte(`dashboard:
  agent_token: agent-token-long-enough
  admin_user: admin
  admin_password: admin-password-long-enough
  jwt_secret: jwt-secret-long-enough-for-tests
  listen: :9090
  data_dir: /var/lib/baize
  frontend_dir: /opt/baize/dashboard/frontend
`)
	if err := os.WriteFile(path, data, 0o600); err != nil {
		t.Fatal(err)
	}
	cfg, err := Load(path)
	if err != nil {
		t.Fatal(err)
	}
	if cfg.Dashboard.Listen != ":9090" || cfg.Dashboard.DataDir != "/var/lib/baize" {
		t.Fatalf("unexpected config: %+v", cfg.Dashboard)
	}
}

func TestLoadRejectsUnknownField(t *testing.T) {
	path := filepath.Join(t.TempDir(), "config.yml")
	data := []byte(`dashboard:
  agent_token: agent-token-long-enough
  admin_user: admin
  admin_password: admin-password-long-enough
  jwt_secret: jwt-secret-long-enough-for-tests
  unknown: value
`)
	if err := os.WriteFile(path, data, 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := Load(path); err == nil {
		t.Fatal("expected unknown field error")
	}
}

func TestLoadOrCreateGeneratesMissingValues(t *testing.T) {
	path := filepath.Join(t.TempDir(), "config.yml")
	cfg, generated, err := LoadOrCreate(path)
	if err != nil {
		t.Fatal(err)
	}
	if !generated.Created || generated.AgentToken == "" || !generated.AdminPassword || generated.JWTSecret == "" {
		t.Fatalf("expected generated values: %+v", generated)
	}
	if cfg.Dashboard.AdminUser != "admin" {
		t.Fatalf("unexpected default admin user: %q", cfg.Dashboard.AdminUser)
	}
	if cfg.Dashboard.AdminPassword != DefaultAdminPassword || !cfg.Dashboard.PasswordChangeRequired {
		t.Fatalf("unexpected bootstrap credentials: password=%q force_change=%v", cfg.Dashboard.AdminPassword, cfg.Dashboard.PasswordChangeRequired)
	}
	mode, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	if mode.Mode().Perm() != 0o600 {
		t.Fatalf("config mode = %o, want 600", mode.Mode().Perm())
	}
	loaded, generatedAgain, err := LoadOrCreate(path)
	if err != nil {
		t.Fatal(err)
	}
	if generatedAgain.AgentToken != "" || loaded.Dashboard.AgentToken != cfg.Dashboard.AgentToken || loaded.Dashboard.AdminPassword != cfg.Dashboard.AdminPassword {
		t.Fatal("generated config was not reused")
	}
}
