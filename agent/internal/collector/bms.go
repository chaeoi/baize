package collector

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"os/exec"
	"strings"
	"time"

	"baize/agent/internal/config"
	"baize/shared/model"
	"gopkg.in/yaml.v3"
)

type batteryStateMessage struct {
	Voltage           float64 `yaml:"voltage"`
	Current           float64 `yaml:"current"`
	Temperature       float64 `yaml:"temperature"`
	Percentage        float64 `yaml:"percentage"`
	PowerSupplyStatus uint8   `yaml:"power_supply_status"`
	Present           bool    `yaml:"present"`
}

type BMSCollector struct {
	config config.BMSConfig
}

func NewBMSCollector(cfg config.BMSConfig) *BMSCollector {
	return &BMSCollector{config: cfg}
}

// Collect reads the standard BatteryState topic once. The Agent never opens a
// CAN socket and never publishes or sends a command on behalf of the bridge.
func (c *BMSCollector) Collect(ctx context.Context) (model.BMSMetrics, error) {
	metrics := model.BMSMetrics{Enabled: true, Protocol: c.config.Protocol, Interface: c.config.ROSTopic}
	readCtx, cancel := context.WithTimeout(ctx, c.config.ReadTimeout.Value())
	defer cancel()
	command, err := rosCommand(c.config.ROSSetup, c.config.ROSEnvironment, c.config.ROSUser,
		"ros2 topic echo --no-daemon --once "+shellQuote(c.config.ROSTopic)+" "+shellQuote(c.config.ROSMessageType))
	if err != nil {
		return metrics, err
	}
	cmd := exec.CommandContext(readCtx, "/bin/bash", "-lc", command)
	var stderr bytes.Buffer
	cmd.Stderr = &stderr
	output, err := cmd.Output()
	if err != nil {
		if errors.Is(readCtx.Err(), context.DeadlineExceeded) {
			return metrics, fmt.Errorf("BMS topic read timed out after %s", c.config.ReadTimeout.Value())
		}
		message := strings.TrimSpace(stderr.String())
		if message == "" {
			message = err.Error()
		}
		return metrics, fmt.Errorf("BMS ROS2 topic read: %s", message)
	}
	return decodeBatteryState(output, metrics)
}

func decodeBatteryState(output []byte, metrics model.BMSMetrics) (model.BMSMetrics, error) {
	var message batteryStateMessage
	if err := yaml.Unmarshal(output, &message); err != nil {
		return metrics, fmt.Errorf("decode BatteryState YAML: %w", err)
	}
	percentage := message.Percentage
	if percentage >= 0 && percentage <= 1 {
		percentage *= 100
	}
	metrics.Voltage, metrics.Current, metrics.Temperature = message.Voltage, message.Current, message.Temperature
	metrics.SOCPercent = percentage
	metrics.PowerWatts = message.Voltage * message.Current
	metrics.PowerSupplyStatus = powerStatus(message.PowerSupplyStatus)
	metrics.Present = message.Present
	metrics.LastFrameAt = time.Now().UTC()
	metrics.Online = true
	return metrics, nil
}

func powerStatus(value uint8) string {
	switch value {
	case 1:
		return "charging"
	case 2:
		return "discharging"
	case 3:
		return "not_charging"
	case 4:
		return "full"
	default:
		return "unknown"
	}
}
