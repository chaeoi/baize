package config

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"time"

	"gopkg.in/yaml.v3"
)

const (
	AdminUsername        = "admin"
	DefaultAdminPassword = "123456"
)

type Duration time.Duration

func (d Duration) Value() time.Duration { return time.Duration(d) }

func (d *Duration) UnmarshalYAML(value *yaml.Node) error {
	if value.Kind != yaml.ScalarNode || value.Tag != "!!str" {
		return errors.New("duration must be a string such as \"1m\" or \"2160h\"")
	}
	parsed, err := time.ParseDuration(value.Value)
	if err != nil {
		return err
	}
	*d = Duration(parsed)
	return nil
}

type Config struct {
	Dashboard DashboardConfig
}

type DashboardConfig struct {
	Listen                string
	DataDir               string
	HistoryDataDir        string
	HistoryRetention      Duration
	HistorySampleInterval Duration
	FrontendDir           string
	CookieSecure          bool
}

// Default is compiled into the Dashboard image and used to seed the persistent
// configuration file on first startup.
func Default() Config {
	return Config{Dashboard: DashboardConfig{
		Listen:                ":8080",
		DataDir:               "/dashboard/data/control",
		HistoryDataDir:        "/dashboard/data/history",
		HistoryRetention:      Duration(90 * 24 * time.Hour),
		HistorySampleInterval: Duration(time.Minute),
		FrontendDir:           "/opt/baize/dashboard/frontend",
	}}
}

type fileDashboardConfig struct {
	// Deprecated authentication fields remain recognized so older persistent
	// config files continue to load. Authentication is fixed to the built-in
	// admin account and these values are ignored.
	AdminUser              *string   `yaml:"admin_user"`
	AdminPassword          *string   `yaml:"admin_password"`
	PasswordChangeRequired *bool     `yaml:"password_change_required"`
	Listen                 *string   `yaml:"listen"`
	DataDir                *string   `yaml:"data_dir"`
	HistoryDataDir         *string   `yaml:"history_data_dir"`
	HistoryRetention       *Duration `yaml:"history_retention"`
	HistorySampleInterval  *Duration `yaml:"history_sample_interval"`
	FrontendDir            *string   `yaml:"frontend_dir"`
	CookieSecure           *bool     `yaml:"cookie_secure"`
}

type fileConfig struct {
	Dashboard *fileDashboardConfig `yaml:"dashboard"`
}

// Load applies an explicit deployment configuration. Agent and JWT secrets
// intentionally remain in control.db and are never accepted from YAML.
func Load(path string) (Config, error) {
	cfg := Default()
	input, err := os.Open(path)
	if err != nil {
		return cfg, err
	}
	defer input.Close()
	var file fileConfig
	decoder := yaml.NewDecoder(input)
	decoder.KnownFields(true)
	if err := decoder.Decode(&file); err != nil {
		return cfg, err
	}
	if file.Dashboard == nil {
		return cfg, errors.New("dashboard configuration is required")
	}
	d := file.Dashboard
	if d.Listen != nil {
		cfg.Dashboard.Listen = *d.Listen
	}
	if d.DataDir != nil {
		cfg.Dashboard.DataDir = *d.DataDir
	}
	if d.HistoryDataDir != nil {
		cfg.Dashboard.HistoryDataDir = *d.HistoryDataDir
	}
	if d.HistoryRetention != nil {
		cfg.Dashboard.HistoryRetention = *d.HistoryRetention
	}
	if d.HistorySampleInterval != nil {
		cfg.Dashboard.HistorySampleInterval = *d.HistorySampleInterval
	}
	if d.FrontendDir != nil {
		cfg.Dashboard.FrontendDir = *d.FrontendDir
	}
	if d.CookieSecure != nil {
		cfg.Dashboard.CookieSecure = *d.CookieSecure
	}
	if err := cfg.Validate(); err != nil {
		return cfg, err
	}
	return cfg, nil
}

func (c Config) Validate() error {
	d := c.Dashboard
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
