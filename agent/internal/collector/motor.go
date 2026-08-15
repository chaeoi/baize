package collector

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"regexp"
	"sort"
	"strings"
	"time"

	"baize/agent/internal/config"
	"baize/shared/model"
	"gopkg.in/yaml.v3"
)

type jointStateMessage struct {
	Name     []string  `yaml:"name"`
	Position []float64 `yaml:"position"`
	Velocity []float64 `yaml:"velocity"`
	Effort   []float64 `yaml:"effort"`
}

type MotorCollector struct {
	config config.MotorConfig
}

var rosEnvironmentNamePattern = regexp.MustCompile(`^[A-Z_][A-Z0-9_]{0,63}$`)
var rosUserNamePattern = regexp.MustCompile(`^[a-z_][a-z0-9_-]{0,31}$`)

func NewMotorCollector(cfg config.MotorConfig) *MotorCollector {
	return &MotorCollector{config: cfg}
}

func (c *MotorCollector) Collect(ctx context.Context) (model.MotorSnapshot, error) {
	snapshot := model.MotorSnapshot{
		Enabled:                 true,
		Source:                  "ros2_joint_state",
		Topic:                   c.config.Topic,
		PerMotorOnlineSupported: false,
		TemperatureSupported:    false,
	}
	readCtx, cancel := context.WithTimeout(ctx, c.config.ReadTimeout.Value())
	defer cancel()
	command, err := rosCommand(c.config.ROSSetup, c.config.ROSEnvironment, c.config.ROSUser,
		"ros2 topic echo --no-daemon --once "+shellQuote(c.config.Topic)+" "+shellQuote(c.config.MessageType))
	if err != nil {
		return snapshot, err
	}
	cmd := exec.CommandContext(readCtx, "/bin/bash", "-lc", command)
	var stderr bytes.Buffer
	cmd.Stderr = &stderr
	output, err := cmd.Output()
	if err != nil {
		if errors.Is(readCtx.Err(), context.DeadlineExceeded) {
			return snapshot, fmt.Errorf("topic read timed out after %s", c.config.ReadTimeout.Value())
		}
		message := strings.TrimSpace(stderr.String())
		if message == "" {
			message = err.Error()
		}
		return snapshot, fmt.Errorf("ros2 topic read: %s", message)
	}
	motors, err := parseJointState(output, c.config.JointLabels, c.config.Definitions)
	if err != nil {
		return snapshot, err
	}
	snapshot.TopicOnline = true
	snapshot.SampledAt = time.Now().UTC()
	snapshot.Motors = motors
	return snapshot, nil
}

func parseJointState(data []byte, labels map[string]string, definitions map[string]config.MotorDefinition) ([]model.MotorState, error) {
	decoder := yaml.NewDecoder(bytes.NewReader(data))
	var message jointStateMessage
	if err := decoder.Decode(&message); err != nil {
		return nil, fmt.Errorf("decode JointState YAML: %w", err)
	}
	count := len(message.Name)
	if count == 0 {
		count = max(len(message.Position), len(message.Velocity), len(message.Effort))
		message.Name = make([]string, count)
		for i := range message.Name {
			message.Name[i] = fmt.Sprintf("motor_id_%02d", i+1)
		}
	}
	if len(message.Position) != count || len(message.Velocity) != count || len(message.Effort) != count {
		return nil, fmt.Errorf("JointState array size mismatch: name=%d position=%d velocity=%d effort=%d",
			count, len(message.Position), len(message.Velocity), len(message.Effort))
	}
	result := make([]model.MotorState, count)
	for i := 0; i < count; i++ {
		definition := definitions[message.Name[i]]
		result[i] = model.MotorState{
			ID:                message.Name[i],
			Label:             labels[message.Name[i]],
			PositionRad:       message.Position[i],
			VelocityRadPerSec: message.Velocity[i],
			TorqueNm:          message.Effort[i],
			Brand:             definition.Brand,
			Model:             definition.Model,
			CANInterface:      definition.CANInterface,
			ControlMode:       definition.ControlMode,
			VirtualJoint:      definition.VirtualJoint,
		}
	}
	return result, nil
}

func rosCommand(setupFiles []string, environment map[string]string, user, finalCommand string) (string, error) {
	parts := make([]string, 0, len(setupFiles)+len(environment)+1)
	for _, path := range setupFiles {
		if !strings.HasPrefix(path, "/") || strings.ContainsAny(path, "\x00\n\r") {
			return "", fmt.Errorf("invalid ROS setup path %q", path)
		}
		parts = append(parts, "source "+shellQuote(path))
	}
	names := make([]string, 0, len(environment))
	for name := range environment {
		names = append(names, name)
	}
	sort.Strings(names)
	for _, name := range names {
		value := environment[name]
		if !rosEnvironmentNamePattern.MatchString(name) || strings.ContainsAny(value, "\x00\n\r") {
			return "", fmt.Errorf("invalid ROS environment variable %q", name)
		}
		parts = append(parts, "export "+name+"="+shellQuote(value))
	}
	parts = append(parts, "exec "+finalCommand)
	command := strings.Join(parts, " && ")
	return wrapROSCommand(command, user, os.Geteuid())
}

func wrapROSCommand(command, user string, euid int) (string, error) {
	if user == "" || euid != 0 {
		return command, nil
	}
	if !rosUserNamePattern.MatchString(user) {
		return "", fmt.Errorf("invalid ROS user %q", user)
	}
	// setpriv execs the lowered-privilege shell in place. This keeps the ROS
	// client in CommandContext's process tree, so a read timeout kills it too.
	return "exec /usr/bin/setpriv --reset-env --reuid=" + shellQuote(user) + " --regid=" + shellQuote(user) + " --init-groups -- /bin/bash -lc " + shellQuote(command), nil
}

func shellQuote(value string) string {
	return "'" + strings.ReplaceAll(value, "'", "'\"'\"'") + "'"
}

func max(values ...int) int {
	result := 0
	for _, value := range values {
		if value > result {
			result = value
		}
	}
	return result
}
