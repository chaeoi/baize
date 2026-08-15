package config

import (
	"encoding/json"
	"errors"
	"fmt"
	"net/url"
	"path/filepath"
	"regexp"
	"strings"
	"time"
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
	Enabled        bool                       `json:"enabled" yaml:"enabled"`
	Topic          string                     `json:"topic" yaml:"topic"`
	MessageType    string                     `json:"message_type" yaml:"message_type"`
	ROSSetup       []string                   `json:"ros_setup" yaml:"ros_setup"`
	ROSEnvironment map[string]string          `json:"ros_environment" yaml:"ros_environment"`
	ROSUser        string                     `json:"ros_user" yaml:"ros_user"`
	ReadTimeout    Duration                   `json:"read_timeout" yaml:"read_timeout"`
	JointLabels    map[string]string          `json:"joint_labels" yaml:"joint_labels"`
	Definitions    map[string]MotorDefinition `json:"definitions" yaml:"definitions"`
}

type MotorDefinition struct {
	Brand        string `json:"brand" yaml:"brand"`
	Model        string `json:"model" yaml:"model"`
	CANInterface string `json:"can_interface" yaml:"can_interface"`
	ControlMode  string `json:"control_mode" yaml:"control_mode"`
	VirtualJoint bool   `json:"virtual_joint" yaml:"virtual_joint"`
}

type BMSConfig struct {
	Enabled        bool                 `json:"enabled" yaml:"enabled"`
	Protocol       string               `json:"protocol" yaml:"protocol"`
	ROSTopic       string               `json:"ros_topic" yaml:"ros_topic"`
	ROSMessageType string               `json:"ros_message_type" yaml:"ros_message_type"`
	ROSSetup       []string             `json:"ros_setup" yaml:"ros_setup"`
	ROSEnvironment map[string]string    `json:"ros_environment" yaml:"ros_environment"`
	ROSUser        string               `json:"ros_user" yaml:"ros_user"`
	ReadTimeout    Duration             `json:"read_timeout" yaml:"read_timeout"`
	Specification  BatterySpecification `json:"specification" yaml:"specification"`
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
			// Resolve through PATH so distributions such as Jetson can provide
			// nvidia-smi from /usr/sbin instead of /usr/bin.
			Command: "nvidia-smi",
			Timeout: Duration(3 * time.Second),
		},
		Motor: MotorConfig{
			Topic:       "/motor/joint_states",
			MessageType: "sensor_msgs/msg/JointState",
			ROSSetup:    []string{"/opt/ros/humble/setup.bash"},
			ReadTimeout: Duration(3 * time.Second),
			JointLabels: map[string]string{},
			Definitions: make(map[string]MotorDefinition),
		},
		BMS: BMSConfig{
			Protocol:       "sensor_msgs_battery_state",
			ROSTopic:       "/bms_can/battery_data",
			ROSMessageType: "sensor_msgs/msg/BatteryState",
			ROSSetup:       []string{"/opt/ros/humble/setup.bash"},
			ReadTimeout:    Duration(3 * time.Second),
		},
		Update: UpdateConfig{
			Enabled:       true,
			Automatic:     true,
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
	if agent.ReportInterval.Value() == 0 {
		agent.ReportInterval = cfg.Agent.ReportInterval
	}
	if agent.HTTPTimeout.Value() == 0 {
		agent.HTTPTimeout = cfg.Agent.HTTPTimeout
	}
	profile, err := profileForModel(agent.RobotModel)
	if err != nil {
		return cfg, err
	}
	cfg.Agent = agent
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
		if c.Motor.ReadTimeout.Value() <= 0 {
			return errors.New("motor.read_timeout must be positive")
		}
	}
	if c.BMS.Enabled {
		if err := validateROSEnvironment("bms", c.BMS.ROSEnvironment); err != nil {
			return err
		}
		if err := validateROSUser("bms", c.BMS.ROSUser); err != nil {
			return err
		}
		c.BMS.Protocol = strings.ToLower(c.BMS.Protocol)
		if c.BMS.Protocol == "" {
			return errors.New("bms.protocol must not be empty")
		}
		if !topicPattern.MatchString(c.BMS.ROSTopic) || !messagePattern.MatchString(c.BMS.ROSMessageType) {
			return errors.New("BMS ROS2 topic or message_type is invalid")
		}
		if c.BMS.ReadTimeout.Value() <= 0 {
			return errors.New("bms.read_timeout must be positive")
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
