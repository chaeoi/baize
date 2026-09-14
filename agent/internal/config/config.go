package config

import (
	"encoding/json"
	"errors"
	"fmt"
	"net/url"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"time"

	"gopkg.in/yaml.v3"
)

type Duration time.Duration

func (d Duration) Value() time.Duration      { return time.Duration(d) }
func (d Duration) String() string            { return time.Duration(d).String() }
func (d Duration) MarshalYAML() (any, error) { return d.String(), nil }

func (d *Duration) UnmarshalJSON(data []byte) error {
	var value string
	if err := json.Unmarshal(data, &value); err != nil {
		return errors.New("duration must be a string such as \"2s\" or \"5m\"")
	}
	parsed, err := time.ParseDuration(value)
	if err != nil {
		return err
	}
	*d = Duration(parsed)
	return nil
}

func (d *Duration) UnmarshalYAML(value *yaml.Node) error {
	if value.Kind != yaml.ScalarNode || value.Tag != "!!str" {
		return errors.New("duration must be a string such as \"2s\" or \"5m\"")
	}
	parsed, err := time.ParseDuration(value.Value)
	if err != nil {
		return err
	}
	*d = Duration(parsed)
	return nil
}

type Config struct {
	Agent  AgentConfig  `json:"agent" yaml:"agent"`
	System SystemConfig `json:"system" yaml:"system"`
	GPU    GPUConfig    `json:"gpu" yaml:"gpu"`
	Motor  MotorConfig  `json:"motor" yaml:"motor"`
	BMS    BMSConfig    `json:"bms" yaml:"bms"`
	Update UpdateConfig `json:"update" yaml:"update"`
}

type AgentConfig struct {
	UUID             string   `json:"uuid" yaml:"uuid"`
	RobotCode        string   `json:"robot_code" yaml:"robot_code"`
	RobotModel       string   `json:"robot_model" yaml:"robot_model"`
	DashboardURL     string   `json:"dashboard_url" yaml:"dashboard_url"`
	Token            string   `json:"token" yaml:"token"`
	ReportInterval   Duration `json:"report_interval" yaml:"report_interval"`
	HTTPTimeout      Duration `json:"http_timeout" yaml:"http_timeout"`
	RawBatchInterval Duration `json:"raw_batch_interval" yaml:"raw_batch_interval"`
	RawCacheBytes    int64    `json:"raw_cache_bytes" yaml:"raw_cache_bytes"`
}

type SystemConfig struct {
	Enabled   bool     `json:"enabled" yaml:"enabled"`
	DiskPaths []string `json:"disk_paths" yaml:"disk_paths"`
}

type GPUConfig struct {
	Enabled bool     `json:"enabled" yaml:"enabled"`
	Command string   `json:"command" yaml:"command"`
	Timeout Duration `json:"timeout" yaml:"timeout"`
}

type MotorConfig struct {
	Enabled        bool              `json:"enabled" yaml:"enabled"`
	Topic          string            `json:"topic" yaml:"topic"`
	MessageType    string            `json:"message_type" yaml:"message_type"`
	ROSSetup       []string          `json:"ros_setup" yaml:"ros_setup"`
	ROSEnvironment map[string]string `json:"ros_environment" yaml:"ros_environment"`
	ROSUser        string            `json:"ros_user" yaml:"ros_user"`
}

type BMSConfig struct {
	Enabled        bool              `json:"enabled" yaml:"enabled"`
	ROSTopic       string            `json:"ros_topic" yaml:"ros_topic"`
	ROSMessageType string            `json:"ros_message_type" yaml:"ros_message_type"`
	ROSSetup       []string          `json:"ros_setup" yaml:"ros_setup"`
	ROSEnvironment map[string]string `json:"ros_environment" yaml:"ros_environment"`
	ROSUser        string            `json:"ros_user" yaml:"ros_user"`
}

type UpdateConfig struct {
	Enabled       bool     `json:"enabled" yaml:"enabled"`
	CheckInterval Duration `json:"check_interval" yaml:"check_interval"`
}

type fileUpdateConfig struct {
	Enabled       bool     `yaml:"enabled"`
	CheckInterval Duration `yaml:"check_interval"`
}

// fileConfig deliberately excludes motor and BMS sections. Robot capability is
// selected by the single top-level model field and compiled into the Agent
// release from the shared robot model catalogue.
type fileConfig struct {
	Model  string            `yaml:"model"`
	Agent  fileAgentConfig   `yaml:"agent"`
	System *SystemConfig     `yaml:"system"`
	GPU    *GPUConfig        `yaml:"gpu"`
	Update *fileUpdateConfig `yaml:"update"`
}

type fileAgentConfig struct {
	UUID             string   `yaml:"uuid"`
	RobotCode        string   `yaml:"robot_code"`
	DashboardURL     string   `yaml:"dashboard_url"`
	Token            string   `yaml:"token"`
	ReportInterval   Duration `yaml:"report_interval"`
	HTTPTimeout      Duration `yaml:"http_timeout"`
	RawBatchInterval Duration `yaml:"raw_batch_interval"`
	RawCacheBytes    int64    `yaml:"raw_cache_bytes"`
}

func Default() Config {
	return Config{
		Agent: AgentConfig{
			ReportInterval:   Duration(2 * time.Second),
			HTTPTimeout:      Duration(10 * time.Second),
			RawBatchInterval: Duration(2 * time.Second),
			RawCacheBytes:    256 << 20,
		},
		System: SystemConfig{Enabled: true, DiskPaths: []string{"/"}},
		GPU: GPUConfig{
			Enabled: true,
			// Resolve through PATH so distributions such as Jetson can provide
			// nvidia-smi from /usr/sbin instead of /usr/bin.
			Command: "nvidia-smi",
			Timeout: Duration(3 * time.Second),
		},
		Motor: MotorConfig{
			Topic:       "/motor/joint_states",
			MessageType: "sensor_msgs/msg/JointState",
			ROSSetup:    []string{"/opt/ros/humble/setup.bash"},
		},
		BMS: BMSConfig{
			ROSTopic:       "/batcan/data",
			ROSMessageType: "diagnostic_msgs/msg/DiagnosticArray",
			ROSSetup:       []string{"/opt/ros/humble/setup.bash"},
		},
		Update: UpdateConfig{
			Enabled:       true,
			CheckInterval: Duration(time.Minute),
		},
	}
}

var (
	uuidPattern     = regexp.MustCompile(`^[0-9a-fA-F]{8}-[0-9a-fA-F]{4}-[1-5][0-9a-fA-F]{3}-[89aAbB][0-9a-fA-F]{3}-[0-9a-fA-F]{12}$`)
	codePattern     = regexp.MustCompile(`^[A-Za-z0-9._-]{1,64}$`)
	topicPattern    = regexp.MustCompile(`^/[A-Za-z0-9_/]{1,255}$`)
	messagePattern  = regexp.MustCompile(`^[A-Za-z0-9_]+/msg/[A-Za-z0-9_]+$`)
	envNamePattern  = regexp.MustCompile(`^[A-Z_][A-Z0-9_]{0,63}$`)
	userNamePattern = regexp.MustCompile(`^[a-z_][a-z0-9_-]{0,31}$`)
)

func Build(agent AgentConfig) (Config, error) {
	cfg := Default()
	cfg.Agent = agent
	return build(cfg)
}

// Load reads deployment identity and generic collection settings. It rejects
// YAML fields for model-specific collectors so they cannot override a built-in
// robot profile.
func Load(path string) (Config, error) {
	input, err := os.Open(path)
	if err != nil {
		return Config{}, fmt.Errorf("open agent config: %w", err)
	}
	defer input.Close()
	var file fileConfig
	decoder := yaml.NewDecoder(input)
	decoder.KnownFields(true)
	if err := decoder.Decode(&file); err != nil {
		return Config{}, fmt.Errorf("parse agent config: %w", err)
	}
	cfg := Default()
	cfg.Agent = AgentConfig{
		UUID: file.Agent.UUID, RobotCode: file.Agent.RobotCode, RobotModel: file.Model,
		DashboardURL: file.Agent.DashboardURL, Token: file.Agent.Token,
		ReportInterval: file.Agent.ReportInterval, HTTPTimeout: file.Agent.HTTPTimeout,
		RawBatchInterval: file.Agent.RawBatchInterval, RawCacheBytes: file.Agent.RawCacheBytes,
	}
	if file.System != nil {
		cfg.System = *file.System
	}
	if file.GPU != nil {
		cfg.GPU = *file.GPU
	}
	if file.Update != nil {
		cfg.Update = UpdateConfig{Enabled: file.Update.Enabled, CheckInterval: file.Update.CheckInterval}
	}
	return build(cfg)
}

func Marshal(cfg Config) ([]byte, error) {
	return yaml.Marshal(fileConfig{
		Model: cfg.Agent.RobotModel,
		Agent: fileAgentConfig{UUID: cfg.Agent.UUID, RobotCode: cfg.Agent.RobotCode,
			DashboardURL: cfg.Agent.DashboardURL, Token: cfg.Agent.Token,
			ReportInterval: cfg.Agent.ReportInterval, HTTPTimeout: cfg.Agent.HTTPTimeout,
			RawBatchInterval: cfg.Agent.RawBatchInterval, RawCacheBytes: cfg.Agent.RawCacheBytes},
		System: &cfg.System, GPU: &cfg.GPU,
		Update: &fileUpdateConfig{Enabled: cfg.Update.Enabled, CheckInterval: cfg.Update.CheckInterval},
	})
}

func build(cfg Config) (Config, error) {
	defaults := Default()
	if cfg.Agent.ReportInterval.Value() == 0 {
		cfg.Agent.ReportInterval = defaults.Agent.ReportInterval
	}
	if cfg.Agent.HTTPTimeout.Value() == 0 {
		cfg.Agent.HTTPTimeout = defaults.Agent.HTTPTimeout
	}
	if cfg.Agent.RawBatchInterval.Value() == 0 {
		cfg.Agent.RawBatchInterval = defaults.Agent.RawBatchInterval
	}
	if cfg.Agent.RawCacheBytes == 0 {
		cfg.Agent.RawCacheBytes = defaults.Agent.RawCacheBytes
	}
	if cfg.System.DiskPaths == nil {
		cfg.System.DiskPaths = defaults.System.DiskPaths
	}
	if cfg.GPU.Command == "" {
		cfg.GPU.Command = defaults.GPU.Command
	}
	if cfg.GPU.Timeout.Value() == 0 {
		cfg.GPU.Timeout = defaults.GPU.Timeout
	}
	if cfg.Update.CheckInterval.Value() == 0 {
		cfg.Update.CheckInterval = defaults.Update.CheckInterval
	}
	profile, err := profileForModel(cfg.Agent.RobotModel)
	if err != nil {
		return cfg, err
	}
	cfg.Motor = profile.Motor
	cfg.BMS = profile.BMS
	if err := cfg.Validate(); err != nil {
		return cfg, err
	}
	return cfg, nil
}

func (c *Config) Validate() error {
	if !uuidPattern.MatchString(c.Agent.UUID) {
		return errors.New("agent.uuid must be a canonical UUID")
	}
	if !codePattern.MatchString(c.Agent.RobotCode) {
		return errors.New("agent.robot_code may contain only letters, numbers, dot, underscore and dash")
	}
	if strings.TrimSpace(c.Agent.RobotModel) == "" {
		return errors.New("agent.robot_model must not be empty")
	}
	dashboardURL, err := url.Parse(c.Agent.DashboardURL)
	if err != nil || dashboardURL.Host == "" || (dashboardURL.Scheme != "http" && dashboardURL.Scheme != "https") {
		return errors.New("agent.dashboard_url must be an http or https URL")
	}
	c.Agent.DashboardURL = strings.TrimRight(c.Agent.DashboardURL, "/")
	if len(c.Agent.Token) < 12 {
		return errors.New("agent.token must contain at least 12 characters")
	}
	if c.Agent.ReportInterval.Value() < time.Second {
		return errors.New("agent.report_interval must be at least 1s")
	}
	if c.Agent.HTTPTimeout.Value() <= 0 {
		return errors.New("agent.http_timeout must be positive")
	}
	if c.Agent.RawBatchInterval.Value() < 250*time.Millisecond {
		return errors.New("agent.raw_batch_interval must be at least 250ms")
	}
	if c.Agent.RawCacheBytes < 1<<20 {
		return errors.New("agent.raw_cache_bytes must be at least 1MiB")
	}
	for _, path := range c.System.DiskPaths {
		if !filepath.IsAbs(path) {
			return fmt.Errorf("system disk path %q is not absolute", path)
		}
	}
	if c.Motor.Enabled {
		if err := validateROSEnvironment("motor", c.Motor.ROSEnvironment); err != nil {
			return err
		}
		if err := validateROSUser("motor", c.Motor.ROSUser); err != nil {
			return err
		}
		if !topicPattern.MatchString(c.Motor.Topic) || !messagePattern.MatchString(c.Motor.MessageType) {
			return errors.New("motor topic or message_type is invalid")
		}
	}
	if c.BMS.Enabled {
		if err := validateROSEnvironment("bms", c.BMS.ROSEnvironment); err != nil {
			return err
		}
		if err := validateROSUser("bms", c.BMS.ROSUser); err != nil {
			return err
		}
		if !topicPattern.MatchString(c.BMS.ROSTopic) || !messagePattern.MatchString(c.BMS.ROSMessageType) {
			return errors.New("BMS ROS2 topic or message_type is invalid")
		}

	}
	if c.Update.Enabled && c.Update.CheckInterval.Value() < 10*time.Second {
		return errors.New("update.check_interval must be at least 10s")
	}
	return nil
}

func validateROSEnvironment(component string, environment map[string]string) error {
	for name, value := range environment {
		if !envNamePattern.MatchString(name) {
			return fmt.Errorf("%s.ros_environment contains invalid variable name %q", component, name)
		}
		if strings.ContainsAny(value, "\x00\n\r") {
			return fmt.Errorf("%s.ros_environment[%s] contains an invalid control character", component, name)
		}
	}
	return nil
}

func validateROSUser(component, user string) error {
	if user != "" && !userNamePattern.MatchString(user) {
		return fmt.Errorf("%s.ros_user is invalid", component)
	}
	return nil
}
