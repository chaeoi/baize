package collector

import (
	"bytes"
	"context"
	"fmt"
	"os/exec"

	"baize/agent/internal/config"
	"baize/shared/model"
)

func PublishBMSROS2(ctx context.Context, cfg config.BMSConfig, metrics model.BMSMetrics) error {
	status := 0
	switch metrics.PowerSupplyStatus {
	case "charging":
		status = 1
	case "discharging":
		status = 2
	case "not_charging":
		status = 3
	}
	message := fmt.Sprintf(
		"{voltage: %.4f, current: %.4f, temperature: %.4f, percentage: %.6f, power_supply_status: %d, present: true}",
		metrics.Voltage, metrics.Current, metrics.Temperature, metrics.SOCPercent/100, status,
	)
	command, err := rosCommand(cfg.ROSSetup,
		"ros2 topic pub --once "+shellQuote(cfg.ROSTopic)+" sensor_msgs/msg/BatteryState "+shellQuote(message))
	if err != nil {
		return err
	}
	publishCtx, cancel := context.WithTimeout(ctx, cfg.PublishTimeout.Value())
	defer cancel()
	cmd := exec.CommandContext(publishCtx, "/bin/bash", "-lc", command)
	var stderr bytes.Buffer
	cmd.Stderr = &stderr
	if err := cmd.Run(); err != nil {
		if stderr.Len() > 0 {
			return fmt.Errorf("publish BMS ROS2 topic: %s", stderr.String())
		}
		return fmt.Errorf("publish BMS ROS2 topic: %w", err)
	}
	return nil
}
