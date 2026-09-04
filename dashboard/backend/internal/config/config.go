package config

import (
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
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
	AgentToken            string
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
		AgentToken:            "",
		Listen:                ":8080",
		DataDir:               "/dashboard/data/control",
		HistoryDataDir:        "/dashboard/data/history",
		HistoryRetention:      Duration(90 * 24 * time.Hour),
		HistorySampleInterval: Duration(2 * time.Second),
		FrontendDir:           "/opt/baize/dashboard/frontend",
	}}
}

type fileDashboardConfig struct {
	AgentToken            *string   `yaml:"agent_token"`
	Listen                *string   `yaml:"listen"`
	DataDir               *string   `yaml:"data_dir"`
	HistoryDataDir        *string   `yaml:"history_data_dir"`
	HistoryRetention      *Duration `yaml:"history_retention"`
	HistorySampleInterval *Duration `yaml:"history_sample_interval"`
	FrontendDir           *string   `yaml:"frontend_dir"`
	CookieSecure          *bool     `yaml:"cookie_secure"`
}

type fileConfig struct {
	Dashboard *fileDashboardConfig `yaml:"dashboard"`
}

// Load applies an explicit deployment configuration. JWT secrets remain in
// control.db; the Agent token may be supplied by configuration or generated
// into control.db when omitted.
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
	if d.AgentToken != nil {
		cfg.Dashboard.AgentToken = *d.AgentToken
	}
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
	if strings.IndexAny(d.AgentToken, " \t\r\n") >= 0 {
		return errors.New("dashboard.agent_token must not contain whitespace")
	}
	if d.AgentToken != "" && len(d.AgentToken) < 12 {
		return errors.New("dashboard.agent_token must contain at least 12 characters")
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
	if d.HistorySampleInterval.Value() < 2*time.Second {
		return errors.New("dashboard.history_sample_interval must be at least 2s")
	}
	if !filepath.IsAbs(d.FrontendDir) {
		return errors.New("dashboard.frontend_dir must be an absolute path")
	}
	return nil
}

// NewAgentToken returns a random token suitable for the Dashboard-Agent
// bearer authentication header.
func NewAgentToken() (string, error) {
	data := make([]byte, 32)
	if _, err := rand.Read(data); err != nil {
		return "", fmt.Errorf("generate agent token: %w", err)
	}
	return hex.EncodeToString(data), nil
}

// WriteAgentToken updates the dashboard.agent_token scalar in an existing
// YAML file without changing any other configuration values.
func WriteAgentToken(path, token string) error {
	input, err := os.ReadFile(path)
	if err != nil {
		return err
	}
	var document yaml.Node
	if err := yaml.Unmarshal(input, &document); err != nil {
		return err
	}
	if len(document.Content) == 0 || document.Content[0].Kind != yaml.MappingNode {
		return errors.New("dashboard configuration must be a YAML mapping")
	}
	dashboard, ok := mappingValue(document.Content[0], "dashboard")
	if !ok || dashboard.Kind != yaml.MappingNode {
		return errors.New("dashboard configuration is required")
	}
	if value, ok := mappingValue(dashboard, "agent_token"); ok {
		value.Kind = yaml.ScalarNode
		value.Tag = "!!str"
		value.Value = token
		if value.Style == 0 {
			value.Style = yaml.DoubleQuotedStyle
		}
	} else {
		dashboard.Content = append(dashboard.Content,
			&yaml.Node{Kind: yaml.ScalarNode, Tag: "!!str", Value: "agent_token"},
			&yaml.Node{Kind: yaml.ScalarNode, Tag: "!!str", Value: token, Style: yaml.DoubleQuotedStyle},
		)
	}
	info, err := os.Stat(path)
	if err != nil {
		return err
	}
	temporary, err := os.CreateTemp(filepath.Dir(path), ".config.yaml.*")
	if err != nil {
		return err
	}
	temporaryPath := temporary.Name()
	defer os.Remove(temporaryPath)
	if err := temporary.Chmod(info.Mode().Perm()); err != nil {
		temporary.Close()
		return err
	}
	encoder := yaml.NewEncoder(temporary)
	encoder.SetIndent(2)
	if err := encoder.Encode(&document); err != nil {
		encoder.Close()
		temporary.Close()
		return err
	}
	if err := encoder.Close(); err != nil {
		temporary.Close()
		return err
	}
	if err := temporary.Sync(); err != nil {
		temporary.Close()
		return err
	}
	if err := temporary.Close(); err != nil {
		return err
	}
	return os.Rename(temporaryPath, path)
}

func mappingValue(mapping *yaml.Node, key string) (*yaml.Node, bool) {
	for index := 0; index+1 < len(mapping.Content); index += 2 {
		if mapping.Content[index].Value == key {
			return mapping.Content[index+1], true
		}
	}
	return nil, false
}
