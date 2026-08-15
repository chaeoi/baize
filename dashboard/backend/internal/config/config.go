package config

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"time"
)

const DefaultAdminPassword = "Baize@Admin1"

type Duration time.Duration

func (d Duration) Value() time.Duration { return time.Duration(d) }

type Config struct {
	Dashboard DashboardConfig
}

type DashboardConfig struct {
	AdminUser              string
	AdminPassword          string
	PasswordChangeRequired bool
	Listen                 string
	DataDir                string
	HistoryDataDir         string
	HistoryRetention       Duration
	HistorySampleInterval  Duration
	FrontendDir            string
	CookieSecure           bool
}

// Default is compiled into the Dashboard image. Runtime paths are the stable
// container contract; Docker port mapping controls the public listen port.
func Default() Config {
	return Config{Dashboard: DashboardConfig{
		AdminUser:              "admin",
		AdminPassword:          DefaultAdminPassword,
		PasswordChangeRequired: true,
		Listen:                 ":8080",
		DataDir:                "/data/control",
		HistoryDataDir:         "/data/history",
		HistoryRetention:       Duration(90 * 24 * time.Hour),
		HistorySampleInterval:  Duration(time.Minute),
		FrontendDir:            "/opt/baize/dashboard/frontend",
	}}
}

// Runtime applies only deployment-safe environment overrides. There is no
// YAML configuration file; secrets and mutable settings live in control.db.
func Runtime() (Config, error) {
	cfg := Default()
	if value := strings.TrimSpace(os.Getenv("BAIZE_LISTEN")); value != "" {
		cfg.Dashboard.Listen = value
	}
	if value := strings.TrimSpace(os.Getenv("BAIZE_DATA_DIR")); value != "" {
		cfg.Dashboard.DataDir = value
	}
	if value := strings.TrimSpace(os.Getenv("BAIZE_HISTORY_DATA_DIR")); value != "" {
		cfg.Dashboard.HistoryDataDir = value
	}
	if value := strings.TrimSpace(os.Getenv("BAIZE_FRONTEND_DIR")); value != "" {
		cfg.Dashboard.FrontendDir = value
	}
	if value := strings.TrimSpace(os.Getenv("BAIZE_COOKIE_SECURE")); value != "" {
		cfg.Dashboard.CookieSecure = value == "1" || strings.EqualFold(value, "true")
	}
	if err := cfg.Validate(); err != nil {
		return cfg, err
	}
	return cfg, nil
}

func (c Config) Validate() error {
	d := c.Dashboard
	if strings.TrimSpace(d.AdminUser) == "" || strings.ContainsAny(d.AdminUser, "\r\n\t ") {
		return errors.New("dashboard.admin_user must be a non-empty username without whitespace")
	}
	if strings.TrimSpace(d.Listen) == "" {
		return errors.New("dashboard.listen must not be empty")
	}
	if !filepath.IsAbs(d.DataDir) {
		return errors.New("dashboard.data_dir must be an absolute path")
	}
	if !filepath.IsAbs(d.HistoryDataDir) {
		return errors.New("dashboard.history_data_dir must be an absolute path")
	}
	if filepath.Clean(d.DataDir) == filepath.Clean(d.HistoryDataDir) {
		return errors.New("dashboard.data_dir and dashboard.history_data_dir must be different directories")
	}
	if d.HistoryRetention.Value() < 24*time.Hour {
		return errors.New("dashboard.history_retention must be at least 24h")
	}
	if d.HistorySampleInterval.Value() < 10*time.Second {
		return errors.New("dashboard.history_sample_interval must be at least 10s")
	}
	if !filepath.IsAbs(d.FrontendDir) {
		return errors.New("dashboard.frontend_dir must be an absolute path")
	}
	return nil
}
