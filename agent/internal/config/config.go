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

func (d Duration) Value() time.Duration { return time.Duration(d) }

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

func (d *Duration) UnmarshalYAML(node *yaml.Node) error {
	if node.Kind != yaml.ScalarNode {
		return errors.New("duration must be a string such as 2s or 5m")
	}
	parsed, err := time.ParseDuration(node.Value)
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
	UUID           string   `json:"uuid" yaml:"uuid"`
	RobotCode      string   `json:"robot_code" yaml:"robot_code"`
	RobotModel     string   `json:"robot_model" yaml:"robot_model"`
	DashboardURL   string   `json:"dashboard_url" yaml:"dashboard_url"`
	Token          string   `json:"token" yaml:"token"`
	ReportInterval Duration `json:"report_interval" yaml:"report_interval"`
	HTTPTimeout    Duration `json:"http_timeout" yaml:"http_timeout"`
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
	Enabled     bool                       `json:"enabled" yaml:"enabled"`
	Topic       string                     `json:"topic" yaml:"topic"`
	MessageType string                     `json:"message_type" yaml:"message_type"`
	ROSSetup    []string                   `json:"ros_setup" yaml:"ros_setup"`
	ReadTimeout Duration                   `json:"read_timeout" yaml:"read_timeout"`
	JointLabels map[string]string          `json:"joint_labels" yaml:"joint_labels"`
	Definitions map[string]MotorDefinition `json:"-" yaml:"-"`
}

type MotorDefinition struct {
	Brand        string
	Model        string
	CANInterface string
	ControlMode  string
	VirtualJoint bool
}

type BMSConfig struct {
	Enabled         bool                 `json:"enabled" yaml:"enabled"`
	Protocol        string               `json:"protocol" yaml:"protocol"`
	CANInterface    string               `json:"can_interface" yaml:"can_interface"`
	Timeout         Duration             `json:"timeout" yaml:"timeout"`
	QueryInterval   Duration             `json:"query_interval" yaml:"query_interval"`
	PublishROS2     bool                 `json:"publish_ros2" yaml:"publish_ros2"`
	ROSTopic        string               `json:"ros_topic" yaml:"ros_topic"`
	ROSSetup        []string             `json:"ros_setup" yaml:"ros_setup"`
	PublishInterval Duration             `json:"publish_interval" yaml:"publish_interval"`
	PublishTimeout  Duration             `json:"publish_timeout" yaml:"publish_timeout"`
	Specification   BatterySpecification `json:"specification" yaml:"specification"`
}

type BatterySpecification struct {
	Vendor         string  `json:"vendor" yaml:"vendor"`
	PackModel      string  `json:"pack_model" yaml:"pack_model"`
	Chemistry      string  `json:"chemistry" yaml:"chemistry"`
	NominalVoltage float64 `json:"nominal_voltage" yaml:"nominal_voltage"`
	CapacityAh     float64 `json:"capacity_ah" yaml:"capacity_ah"`
	SeriesCells    int     `json:"series_cells" yaml:"series_cells"`
}

type UpdateConfig struct {
	Enabled       bool     `json:"enabled" yaml:"enabled"`
	Automatic     bool     `json:"automatic" yaml:"automatic"`
	CheckInterval Duration `json:"check_interval" yaml:"check_interval"`
}

func Default() Config {
	return Config{
		Agent: AgentConfig{
			ReportInterval: Duration(2 * time.Second),
			HTTPTimeout:    Duration(10 * time.Second),
		},
		System: SystemConfig{Enabled: true, DiskPaths: []string{"/"}},
		GPU: GPUConfig{
			Enabled: true,
			Command: "/usr/bin/nvidia-smi",
			Timeout: Duration(3 * time.Second),
		},
		Motor: MotorConfig{
			Topic:       "/motor/joint_states",
			MessageType: "sensor_msgs/msg/JointState",
			ROSSetup:    []string{"/opt/ros/humble/setup.bash", "/opt/xuanjian/agent/ros/setup.bash"},
			ReadTimeout: Duration(3 * time.Second),
			JointLabels: map[string]string{},
			Definitions: make(map[string]MotorDefinition),
		},
		BMS: BMSConfig{
			Protocol:        "yy-bcu14h-mos-24s100a",
			CANInterface:    "can5",
			Timeout:         Duration(5 * time.Second),
			QueryInterval:   Duration(2 * time.Second),
			ROSTopic:        "/bms_can/battery_data",
			ROSSetup:        []string{"/opt/ros/humble/setup.bash", "/opt/xuanjian/agent/ros/setup.bash"},
			PublishInterval: Duration(2 * time.Second),
			PublishTimeout:  Duration(4 * time.Second),
		},
		Update: UpdateConfig{
			Enabled:       true,
			Automatic:     true,
			CheckInterval: Duration(time.Minute),
		},
	}
}

var (
	uuidPattern    = regexp.MustCompile(`^[0-9a-fA-F]{8}-[0-9a-fA-F]{4}-[1-5][0-9a-fA-F]{3}-[89aAbB][0-9a-fA-F]{3}-[0-9a-fA-F]{12}$`)
	codePattern    = regexp.MustCompile(`^[A-Za-z0-9._-]{1,64}$`)
	ifPattern      = regexp.MustCompile(`^[A-Za-z0-9_.-]{1,32}$`)
	topicPattern   = regexp.MustCompile(`^/[A-Za-z0-9_/]{1,255}$`)
	messagePattern = regexp.MustCompile(`^[A-Za-z0-9_]+/msg/[A-Za-z0-9_]+$`)
)

func Load(path string) (Config, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return Config{}, err
	}
	var selector struct {
		Agent struct {
			RobotModel string `yaml:"robot_model"`
		} `yaml:"agent"`
	}
	if err := yaml.Unmarshal(data, &selector); err != nil {
		return Config{}, fmt.Errorf("parse robot model: %w", err)
	}
	cfg := Default()
	if err := applyRobotProfile(&cfg, selector.Agent.RobotModel); err != nil {
		return cfg, err
	}
	decoder := yaml.NewDecoder(strings.NewReader(string(data)))
	decoder.KnownFields(true)
	if err := decoder.Decode(&cfg); err != nil {
		return cfg, fmt.Errorf("parse config: %w", err)
	}
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
	if _, ok := robotProfiles[c.Agent.RobotModel]; !ok {
		return fmt.Errorf("unknown agent.robot_model %q", c.Agent.RobotModel)
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
	for _, path := range c.System.DiskPaths {
		if !filepath.IsAbs(path) {
			return fmt.Errorf("system disk path %q is not absolute", path)
		}
	}
	if c.Motor.Enabled {
		if !topicPattern.MatchString(c.Motor.Topic) || !messagePattern.MatchString(c.Motor.MessageType) {
			return errors.New("motor topic or message_type is invalid")
		}
		if c.Motor.ReadTimeout.Value() <= 0 {
			return errors.New("motor.read_timeout must be positive")
		}
	}
	if c.BMS.Enabled {
		c.BMS.Protocol = strings.ToLower(c.BMS.Protocol)
		if c.BMS.Protocol != "yy-bcu14h-mos-24s100a" {
			return errors.New("bms.protocol must be yy-bcu14h-mos-24s100a")
		}
		if !ifPattern.MatchString(c.BMS.CANInterface) {
			return errors.New("bms.can_interface is invalid")
		}
		if c.BMS.Timeout.Value() <= 0 || c.BMS.QueryInterval.Value() <= 0 {
			return errors.New("bms timeout and query_interval must be positive")
		}
		if c.BMS.PublishROS2 && !topicPattern.MatchString(c.BMS.ROSTopic) {
			return errors.New("bms.ros_topic is invalid")
		}
	}
	if c.Update.Enabled && c.Update.CheckInterval.Value() < 10*time.Second {
		return errors.New("update.check_interval must be at least 10s")
	}
	return nil
}
