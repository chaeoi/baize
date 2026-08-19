package robotmodel

import (
	_ "embed"
	"errors"
	"fmt"
	"io"
	"regexp"
	"sort"
	"strings"
	"sync"
	"time"

	"gopkg.in/yaml.v3"
)

//go:embed models.yml
var embeddedModels string

type Duration time.Duration

func (d Duration) Value() time.Duration { return time.Duration(d) }

func (d *Duration) UnmarshalYAML(node *yaml.Node) error {
	if node.Kind != yaml.ScalarNode || node.Tag != "!!str" {
		return errors.New("duration must be a string such as \"2s\" or \"5m\"")
	}
	value, err := time.ParseDuration(node.Value)
	if err != nil {
		return err
	}
	*d = Duration(value)
	return nil
}

type Profile struct {
	Model ModelConfig `yaml:"model"`
	Motor MotorConfig `yaml:"motor"`
	BMS   BMSConfig   `yaml:"bms"`
}

type ModelConfig struct {
	ID string `yaml:"id"`
}

type MotorConfig struct {
	Enabled           bool                   `yaml:"enabled"`
	Topic             string                 `yaml:"topic"`
	MessageType       string                 `yaml:"message_type"`
	ROSSetup          []string               `yaml:"ros_setup"`
	ROSEnvironment    map[string]string      `yaml:"ros_environment"`
	ROSUser           string                 `yaml:"ros_user"`
	ReadTimeout       Duration               `yaml:"read_timeout"`
	FastSampleRateHz  float64                `yaml:"fast_sample_rate_hz"`
	FastBufferSeconds int                    `yaml:"fast_buffer_seconds"`
	FastBatchInterval Duration               `yaml:"fast_batch_interval"`
	Joints            map[string]JointConfig `yaml:"joints"`
}

type JointConfig struct {
	Label        string `yaml:"label"`
	Brand        string `yaml:"brand"`
	Model        string `yaml:"model"`
	CANInterface string `yaml:"can_interface"`
	ControlMode  string `yaml:"control_mode"`
	VirtualJoint bool   `yaml:"virtual_joint"`
}

type BMSConfig struct {
	Enabled        bool              `yaml:"enabled"`
	Protocol       string            `yaml:"protocol"`
	Topic          string            `yaml:"topic"`
	MessageType    string            `yaml:"message_type"`
	ROSSetup       []string          `yaml:"ros_setup"`
	ROSEnvironment map[string]string `yaml:"ros_environment"`
	ROSUser        string            `yaml:"ros_user"`
	ReadTimeout    Duration          `yaml:"read_timeout"`
}

var (
	modelIDPattern   = regexp.MustCompile(`^[A-Za-z0-9._-]{1,64}$`)
	topicPattern     = regexp.MustCompile(`^/[A-Za-z0-9_/]{1,255}$`)
	messagePattern   = regexp.MustCompile(`^[A-Za-z0-9_]+/msg/[A-Za-z0-9_]+$`)
	envNamePattern   = regexp.MustCompile(`^[A-Z_][A-Z0-9_]{0,63}$`)
	userNamePattern  = regexp.MustCompile(`^[a-z_][a-z0-9_-]{0,31}$`)
	jointNamePattern = regexp.MustCompile(`^[A-Za-z0-9_.-]{1,64}$`)
)

var profileCache struct {
	sync.Once
	profiles map[string]Profile
	err      error
}

func Names() ([]string, error) {
	profiles, err := all()
	if err != nil {
		return nil, err
	}
	names := make([]string, 0, len(profiles))
	for name := range profiles {
		names = append(names, name)
	}
	sort.Strings(names)
	return names, nil
}

func Select(name string) (Profile, error) {
	profiles, err := all()
	if err != nil {
		return Profile{}, err
	}
	profile, ok := profiles[name]
	if !ok {
		return Profile{}, fmt.Errorf("robot model %q is not supported by this release", name)
	}
	return profile, nil
}

func Validate() error {
	_, err := all()
	return err
}

func all() (map[string]Profile, error) {
	profileCache.Do(func() {
		profileCache.profiles, profileCache.err = parseEmbedded()
	})
	return profileCache.profiles, profileCache.err
}

func parseEmbedded() (map[string]Profile, error) {
	decoder := yaml.NewDecoder(strings.NewReader(embeddedModels))
	decoder.KnownFields(true)
	profiles := make(map[string]Profile)
	for document := 1; ; document++ {
		var profile Profile
		err := decoder.Decode(&profile)
		if errors.Is(err, io.EOF) {
			break
		}
		if err != nil {
			return nil, fmt.Errorf("parse embedded robot model document %d: %w", document, err)
		}
		if err := validate(profile); err != nil {
			return nil, fmt.Errorf("validate embedded robot model document %d: %w", document, err)
		}
		if _, exists := profiles[profile.Model.ID]; exists {
			return nil, fmt.Errorf("duplicate embedded robot model %q", profile.Model.ID)
		}
		profiles[profile.Model.ID] = profile
	}
	if len(profiles) == 0 {
		return nil, errors.New("embedded robot model catalogue is empty")
	}
	return profiles, nil
}

func validate(profile Profile) error {
	if !modelIDPattern.MatchString(profile.Model.ID) {
		return fmt.Errorf("model.id %q is invalid", profile.Model.ID)
	}
	if profile.Motor.Enabled {
		if err := validateMotor(profile.Motor); err != nil {
			return err
		}
	}
	if profile.BMS.Enabled {
		if err := validateBMS(profile.BMS); err != nil {
			return err
		}
	}
	return nil
}

func validateMotor(motor MotorConfig) error {
	if !topicPattern.MatchString(motor.Topic) || !messagePattern.MatchString(motor.MessageType) {
		return errors.New("motor topic or message_type is invalid")
	}
	if motor.ReadTimeout.Value() <= 0 {
		return errors.New("motor.read_timeout must be positive")
	}
	if motor.FastSampleRateHz < 0 || motor.FastSampleRateHz > 500 {
		return errors.New("motor.fast_sample_rate_hz must be between 0 and 500")
	}
	if motor.FastSampleRateHz > 0 && (motor.FastBufferSeconds < 1 || motor.FastBufferSeconds > 60) {
		return errors.New("motor.fast_buffer_seconds must be between 1 and 60 when enabled")
	}
	if motor.FastSampleRateHz > 0 && (motor.FastBatchInterval.Value() < time.Second || motor.FastBatchInterval.Value() > time.Minute) {
		return errors.New("motor.fast_batch_interval must be between 1s and 1m when enabled")
	}
	if err := validateROS(motor.ROSSetup, motor.ROSEnvironment, motor.ROSUser); err != nil {
		return fmt.Errorf("motor: %w", err)
	}
	for name := range motor.Joints {
		if !jointNamePattern.MatchString(name) {
			return fmt.Errorf("motor.joints contains invalid name %q", name)
		}
	}
	return nil
}

func validateBMS(bms BMSConfig) error {
	if bms.Protocol == "" || !topicPattern.MatchString(bms.Topic) || !messagePattern.MatchString(bms.MessageType) {
		return errors.New("bms protocol, topic, or message_type is invalid")
	}
	if bms.ReadTimeout.Value() <= 0 {
		return errors.New("bms.read_timeout must be positive")
	}
	if err := validateROS(bms.ROSSetup, bms.ROSEnvironment, bms.ROSUser); err != nil {
		return fmt.Errorf("bms: %w", err)
	}
	return nil
}

func validateROS(setup []string, environment map[string]string, user string) error {
	for _, path := range setup {
		if len(path) == 0 || path[0] != '/' {
			return fmt.Errorf("ros_setup path %q must be absolute", path)
		}
	}
	for name, value := range environment {
		if !envNamePattern.MatchString(name) || containsControl(value) {
			return fmt.Errorf("invalid ROS environment variable %q", name)
		}
	}
	if user != "" && !userNamePattern.MatchString(user) {
		return fmt.Errorf("invalid ros_user %q", user)
	}
	return nil
}

func containsControl(value string) bool {
	for _, character := range value {
		if character == 0 || character == '\n' || character == '\r' {
			return true
		}
	}
	return false
}
