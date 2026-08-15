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
	ProfileDir     string   `json:"profile_dir" yaml:"profile_dir"`
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
	Enabled      bool                       `json:"enabled" yaml:"enabled"`
	Source       string                     `json:"source" yaml:"source"`
	CANInterface string                     `json:"can_interface" yaml:"can_interface"`
	CANQueries   []CANQuery                 `json:"can_queries" yaml:"can_queries"`
	Topic        string                     `json:"topic" yaml:"topic"`
	MessageType  string                     `json:"message_type" yaml:"message_type"`
	ROSSetup     []string                   `json:"ros_setup" yaml:"ros_setup"`
	ReadTimeout  Duration                   `json:"read_timeout" yaml:"read_timeout"`
	JointLabels  map[string]string          `json:"joint_labels" yaml:"joint_labels"`
	Definitions  map[string]MotorDefinition `json:"definitions" yaml:"definitions"`
}

type MotorDefinition struct {
	Brand        string `json:"brand" yaml:"brand"`
	Model        string `json:"model" yaml:"model"`
	CANInterface string `json:"can_interface" yaml:"can_interface"`
	ControlMode  string `json:"control_mode" yaml:"control_mode"`
	VirtualJoint bool   `json:"virtual_joint" yaml:"virtual_joint"`
}

type BMSConfig struct {
	Enabled         bool                 `json:"enabled" yaml:"enabled"`
	Source          string               `json:"source" yaml:"source"`
	Protocol        string               `json:"protocol" yaml:"protocol"`
	CANInterface    string               `json:"can_interface" yaml:"can_interface"`
	Timeout         Duration             `json:"timeout" yaml:"timeout"`
	QueryInterval   Duration             `json:"query_interval" yaml:"query_interval"`
	PublishROS2     bool                 `json:"publish_ros2" yaml:"publish_ros2"`
	ROSTopic        string               `json:"ros_topic" yaml:"ros_topic"`
	ROSMessageType  string               `json:"ros_message_type" yaml:"ros_message_type"`
	ROSSetup        []string             `json:"ros_setup" yaml:"ros_setup"`
	PublishInterval Duration             `json:"publish_interval" yaml:"publish_interval"`
	PublishTimeout  Duration             `json:"publish_timeout" yaml:"publish_timeout"`
	Specification   BatterySpecification `json:"specification" yaml:"specification"`
	CANQueries      []CANQuery           `json:"can_queries" yaml:"can_queries"`
}

type CANQuery struct {
	Name        string     `json:"name" yaml:"name"`
	MotorID     string     `json:"motor_id" yaml:"motor_id"`
	RequestID   uint32     `json:"request_id" yaml:"request_id"`
	ResponseID  uint32     `json:"response_id" yaml:"response_id"`
	RequestData []byte     `json:"request_data,omitempty" yaml:"request_data,omitempty"`
	Fields      []CANField `json:"fields" yaml:"fields"`
}

type CANField struct {
	Name     string   `json:"name" yaml:"name"`
	Offset   int      `json:"offset" yaml:"offset"`
	Length   int      `json:"length" yaml:"length"`
	Encoding string   `json:"encoding" yaml:"encoding"`
	Endian   string   `json:"endian" yaml:"endian"`
	Scale    float64  `json:"scale" yaml:"scale"`
	Bias     float64  `json:"bias" yaml:"bias"`
	BitNames []string `json:"bit_names,omitempty" yaml:"bit_names,omitempty"`
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
			ROSSetup:    []string{"/opt/ros/humble/setup.bash", "/opt/baize/agent/ros/setup.bash"},
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
			ROSMessageType:  "sensor_msgs/msg/BatteryState",
			ROSSetup:        []string{"/opt/ros/humble/setup.bash", "/opt/baize/agent/ros/setup.bash"},
			PublishInterval: Duration(2 * time.Second),
			PublishTimeout:  Duration(4 * time.Second),
			CANQueries:      defaultBMSQueries(),
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
			ProfileDir string `yaml:"profile_dir"`
		} `yaml:"agent"`
	}
	if err := yaml.Unmarshal(data, &selector); err != nil {
		return Config{}, fmt.Errorf("parse robot model: %w", err)
	}
	cfg := Default()
	profile, err := loadRobotProfile(path, selector.Agent.RobotModel, selector.Agent.ProfileDir)
	if err != nil {
		return cfg, err
	}
	cfg.Motor = profile.Motor
	cfg.BMS = profile.BMS
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
	if c.Agent.ProfileDir != "" && !filepath.IsAbs(c.Agent.ProfileDir) {
		return errors.New("agent.profile_dir must be absolute")
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
		if c.Motor.Source == "" {
			c.Motor.Source = "ros2_topic"
		}
		if c.Motor.Source != "ros2_topic" && c.Motor.Source != "can_query" {
			return errors.New("motor.source must be ros2_topic or can_query")
		}
		if c.Motor.Source == "ros2_topic" && (!topicPattern.MatchString(c.Motor.Topic) || !messagePattern.MatchString(c.Motor.MessageType)) {
			return errors.New("motor topic or message_type is invalid")
		}
		if c.Motor.Source == "can_query" && (!ifPattern.MatchString(c.Motor.CANInterface) || len(c.Motor.CANQueries) == 0) {
			return errors.New("motor CAN source requires can_interface and can_queries")
		}
		if c.Motor.Source == "can_query" {
			if err := validateCANQueries("motor", c.Motor.CANQueries); err != nil {
				return err
			}
		}
		if c.Motor.ReadTimeout.Value() <= 0 {
			return errors.New("motor.read_timeout must be positive")
		}
	}
	if c.BMS.Enabled {
		if c.BMS.Source == "" {
			c.BMS.Source = "can_query"
		}
		if c.BMS.Source != "can_query" && c.BMS.Source != "ros2_topic" {
			return errors.New("bms.source must be can_query or ros2_topic")
		}
		c.BMS.Protocol = strings.ToLower(c.BMS.Protocol)
		if c.BMS.Protocol == "" {
			return errors.New("bms.protocol must not be empty")
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
		if c.BMS.Source == "ros2_topic" && (!topicPattern.MatchString(c.BMS.ROSTopic) || !messagePattern.MatchString(c.BMS.ROSMessageType)) {
			return errors.New("BMS ROS2 source requires a valid ros_topic and ros_message_type")
		}
		if c.BMS.Source == "can_query" && len(c.BMS.CANQueries) == 0 {
			return errors.New("bms.can_queries must define at least one query")
		}
		if c.BMS.Source == "can_query" {
			if err := validateCANQueries("bms", c.BMS.CANQueries); err != nil {
				return err
			}
		}
	}
	if c.Update.Enabled && c.Update.CheckInterval.Value() < 10*time.Second {
		return errors.New("update.check_interval must be at least 10s")
	}
	return nil
}

func validateCANQueries(component string, queries []CANQuery) error {
	for index, query := range queries {
		if strings.TrimSpace(query.Name) == "" || query.RequestID == 0 || query.ResponseID == 0 || query.RequestID > 0x1fffffff || query.ResponseID > 0x1fffffff {
			return fmt.Errorf("%s.can_queries[%d] has invalid name or CAN id", component, index)
		}
		if len(query.RequestData) > 8 {
			return fmt.Errorf("%s.can_queries[%d].request_data exceeds 8 bytes", component, index)
		}
		for fieldIndex, field := range query.Fields {
			if strings.TrimSpace(field.Name) == "" || field.Offset < 0 || field.Length <= 0 || field.Offset+field.Length > 8 {
				return fmt.Errorf("%s.can_queries[%d].fields[%d] is outside the CAN payload", component, index, fieldIndex)
			}
			if field.Encoding != "uint" && field.Encoding != "int" && field.Encoding != "enum" && field.Encoding != "bits" {
				return fmt.Errorf("%s.can_queries[%d].fields[%d] has unsupported encoding", component, index, fieldIndex)
			}
			if field.Encoding != "bits" && field.Length != 1 && field.Length != 2 && field.Length != 4 && field.Length != 8 {
				return fmt.Errorf("%s.can_queries[%d].fields[%d] length must be 1, 2, 4 or 8", component, index, fieldIndex)
			}
			if field.Endian != "" && field.Endian != "big" && field.Endian != "little" {
				return fmt.Errorf("%s.can_queries[%d].fields[%d] endian must be big or little", component, index, fieldIndex)
			}
		}
	}
	return nil
}
