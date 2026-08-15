package config

import "testing"

func TestRuntimeDefaults(t *testing.T) {
	cfg, err := Runtime()
	if err != nil {
		t.Fatal(err)
	}
	if cfg.Dashboard.Listen != ":8080" || cfg.Dashboard.DataDir != "/data/control" || cfg.Dashboard.HistoryDataDir != "/data/history" {
		t.Fatalf("unexpected config: %+v", cfg.Dashboard)
	}
}

func TestDefaultAdminRequiresPasswordChange(t *testing.T) {
	cfg := Default()
	if cfg.Dashboard.AdminPassword != DefaultAdminPassword || !cfg.Dashboard.PasswordChangeRequired {
		t.Fatalf("unexpected bootstrap credentials: %+v", cfg.Dashboard)
	}
}
