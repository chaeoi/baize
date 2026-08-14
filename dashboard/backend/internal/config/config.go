package config

import (
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"

	"gopkg.in/yaml.v3"
)

type Config struct {
	Dashboard DashboardConfig `yaml:"dashboard"`
}

type DashboardConfig struct {
	AgentToken    string `yaml:"agent_token"`
	AdminUser     string `yaml:"admin_user"`
	AdminPassword string `yaml:"admin_password"`
	JWTSecret     string `yaml:"jwt_secret"`
	Listen        string `yaml:"listen"`
	DataDir       string `yaml:"data_dir"`
	FrontendDir   string `yaml:"frontend_dir"`
}

func Default() Config {
	return Config{Dashboard: DashboardConfig{
		AdminUser:   "admin",
		Listen:      ":8080",
		DataDir:     "/data",
		FrontendDir: "/opt/baize/dashboard/frontend",
	}}
}

func Load(path string) (Config, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return Config{}, err
	}
	cfg, err := decode(data)
	if err != nil {
		return cfg, err
	}
	if err := cfg.Validate(); err != nil {
		return cfg, err
	}
	return cfg, nil
}

type GeneratedValues struct {
	Created       bool
	AdminUser     bool
	AgentToken    string
	AdminPassword string
	JWTSecret     string
}

func LoadOrCreate(path string) (Config, GeneratedValues, error) {
	data, err := os.ReadFile(path)
	created := errors.Is(err, os.ErrNotExist)
	if err != nil && !created {
		return Config{}, GeneratedValues{}, err
	}
	cfg := Default()
	if !created {
		cfg, err = decode(data)
		if err != nil {
			return cfg, GeneratedValues{}, err
		}
	}
	generated := GeneratedValues{Created: created}
	if strings.TrimSpace(cfg.Dashboard.AdminUser) == "" {
		cfg.Dashboard.AdminUser = "admin"
		generated.AdminUser = true
	}
	if cfg.Dashboard.AgentToken == "" {
		cfg.Dashboard.AgentToken, err = randomSecret(32)
		if err != nil {
			return cfg, generated, fmt.Errorf("generate agent token: %w", err)
		}
		generated.AgentToken = cfg.Dashboard.AgentToken
	}
	if cfg.Dashboard.AdminPassword == "" {
		cfg.Dashboard.AdminPassword, err = randomSecret(24)
		if err != nil {
			return cfg, generated, fmt.Errorf("generate admin password: %w", err)
		}
		generated.AdminPassword = cfg.Dashboard.AdminPassword
	}
	if cfg.Dashboard.JWTSecret == "" {
		cfg.Dashboard.JWTSecret, err = randomSecret(32)
		if err != nil {
			return cfg, generated, fmt.Errorf("generate JWT secret: %w", err)
		}
		generated.JWTSecret = cfg.Dashboard.JWTSecret
	}
	if err := cfg.Validate(); err != nil {
		return cfg, generated, err
	}
	if created || generated.AdminUser || generated.AgentToken != "" || generated.AdminPassword != "" || generated.JWTSecret != "" || missingDefaults(data) {
		if err := write(path, cfg); err != nil {
			return cfg, generated, fmt.Errorf("write generated config: %w", err)
		}
	}
	return cfg, generated, nil
}

func missingDefaults(data []byte) bool {
	var raw struct {
		Dashboard map[string]yaml.Node `yaml:"dashboard"`
	}
	if err := yaml.Unmarshal(data, &raw); err != nil {
		return false
	}
	for _, field := range []string{"admin_user", "listen", "data_dir", "frontend_dir"} {
		if _, ok := raw.Dashboard[field]; !ok {
			return true
		}
	}
	return false
}

func decode(data []byte) (Config, error) {
	cfg := Default()
	decoder := yaml.NewDecoder(strings.NewReader(string(data)))
	decoder.KnownFields(true)
	if err := decoder.Decode(&cfg); err != nil {
		if errors.Is(err, io.EOF) {
			return cfg, nil
		}
		return cfg, fmt.Errorf("parse config: %w", err)
	}
	return cfg, nil
}

func write(path string, cfg Config) error {
	data, err := yaml.Marshal(cfg)
	if err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o750); err != nil {
		return err
	}
	temporary, err := os.CreateTemp(filepath.Dir(path), ".config-*.yml")
	if err != nil {
		return err
	}
	temporaryPath := temporary.Name()
	defer os.Remove(temporaryPath)
	if err := temporary.Chmod(0o600); err != nil {
		temporary.Close()
		return err
	}
	if _, err := temporary.Write(data); err != nil {
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

func randomSecret(size int) (string, error) {
	data := make([]byte, size)
	if _, err := rand.Read(data); err != nil {
		return "", err
	}
	return hex.EncodeToString(data), nil
}

func (c Config) Validate() error {
	d := c.Dashboard
	if len(d.AgentToken) < 12 {
		return errors.New("dashboard.agent_token must contain at least 12 characters")
	}
	if strings.TrimSpace(d.AdminUser) == "" || strings.ContainsAny(d.AdminUser, "\r\n\t ") {
		return errors.New("dashboard.admin_user must be a non-empty username without whitespace")
	}
	if len(d.AdminPassword) < 12 {
		return errors.New("dashboard.admin_password must contain at least 12 characters")
	}
	if len(d.JWTSecret) < 32 {
		return errors.New("dashboard.jwt_secret must contain at least 32 characters")
	}
	if strings.TrimSpace(d.Listen) == "" {
		return errors.New("dashboard.listen must not be empty")
	}
	if !filepath.IsAbs(d.DataDir) {
		return errors.New("dashboard.data_dir must be an absolute path")
	}
	if !filepath.IsAbs(d.FrontendDir) {
		return errors.New("dashboard.frontend_dir must be an absolute path")
	}
	return nil
}
