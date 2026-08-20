package collector

import (
	_ "embed"
	"encoding/json"
	"fmt"
	"time"

	"baize/agent/internal/config"
	"baize/shared/model"
)

//go:embed ros2_subscriber.py
var ros2SubscriberScript string

func rosSubscriberCommand(setup []string, environment map[string]string, user, topic, messageType string) (string, error) {
	if topic == "" || messageType == "" {
		return "", fmt.Errorf("ROS2 subscriber topic and message type are required")
	}
	arguments := "python3 -u -c " + shellQuote(ros2SubscriberScript) +
		" --topic " + shellQuote(topic) + " --message-type " + shellQuote(messageType)
	return rosCommand(setup, environment, user, arguments)
}

type jointStateJSONMessage struct {
	Type     string    `json:"type"`
	StampNS  int64     `json:"stamp_ns"`
	Name     []string  `json:"name"`
	Position []float64 `json:"position"`
	Velocity []float64 `json:"velocity"`
	Effort   []float64 `json:"effort"`
}

func parseJointStateJSON(data []byte, labels map[string]string, definitions map[string]config.MotorDefinition) ([]model.MotorState, time.Time, error) {
	var message jointStateJSONMessage
	if err := json.Unmarshal(data, &message); err != nil {
		return nil, time.Time{}, fmt.Errorf("decode JointState JSON: %w", err)
	}
	if message.Type != "" && message.Type != "motor" {
		return nil, time.Time{}, fmt.Errorf("unexpected ROS2 subscriber message type %q", message.Type)
	}
	motors, err := motorStates(message.Name, message.Position, message.Velocity, message.Effort, labels, definitions)
	if err != nil {
		return nil, time.Time{}, err
	}
	stamp := time.Time{}
	if message.StampNS > 0 {
		stamp = time.Unix(0, message.StampNS).UTC()
	}
	return motors, stamp, nil
}
