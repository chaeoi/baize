package collector

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"os/exec"
	"strings"
	"time"

	"gopkg.in/yaml.v3"
	"xuanjian/agent/internal/config"
	"xuanjian/shared/model"
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
	command, err := rosCommand(c.config.ROSSetup,
		"ros2 topic echo --once "+shellQuote(c.config.Topic)+" "+shellQuote(c.config.MessageType))
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
			ID:           message.Name[i],
			Label:        labels[message.Name[i]],
			PositionRad:  message.Position[i],
			VelocityRPS:  message.Velocity[i],
			TorqueNm:     message.Effort[i],
			Brand:        definition.Brand,
			Model:        definition.Model,
			CANInterface: definition.CANInterface,
			ControlMode:  definition.ControlMode,
			VirtualJoint: definition.VirtualJoint,
		}
	}
	return result, nil
}

func rosCommand(setupFiles []string, finalCommand string) (string, error) {
	parts := make([]string, 0, len(setupFiles)+1)
	for _, path := range setupFiles {
		if !strings.HasPrefix(path, "/") || strings.ContainsAny(path, "\x00\n\r") {
			return "", fmt.Errorf("invalid ROS setup path %q", path)
		}
		parts = append(parts, "source "+shellQuote(path))
	}
	parts = append(parts, "exec "+finalCommand)
	return strings.Join(parts, " && "), nil
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
