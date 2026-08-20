package collector

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"math"
	"os/exec"
	"strconv"
	"strings"
	"time"

	"baize/agent/internal/config"
	"baize/shared/model"
	"gopkg.in/yaml.v3"
)

type diagnosticArrayMessage struct {
	Status []diagnosticStatusMessage `yaml:"status"`
}

type diagnosticStatusMessage struct {
	Name       string               `yaml:"name"`
	Message    string               `yaml:"message"`
	HardwareID string               `yaml:"hardware_id"`
	Values     []diagnosticKeyValue `yaml:"values"`
}

type diagnosticKeyValue struct {
	Key   string `yaml:"key"`
	Value string `yaml:"value"`
}

type BMSCollector struct {
	config config.BMSConfig
}

func NewBMSCollector(cfg config.BMSConfig) *BMSCollector {
	return &BMSCollector{config: cfg}
}

// Collect reads batcan's single DiagnosticArray topic. The bridge owns all
// CAN access and query behavior; the Agent only subscribes to the published
// ROS2 data.
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
	return decodeDiagnosticArray(output, metrics)
}

func decodeDiagnosticArray(output []byte, metrics model.BMSMetrics) (model.BMSMetrics, error) {
	var message diagnosticArrayMessage
	if err := yaml.Unmarshal(output, &message); err != nil {
		return metrics, fmt.Errorf("decode DiagnosticArray YAML: %w", err)
	}
	if len(message.Status) == 0 {
		return metrics, errors.New("BMS DiagnosticArray contains no status entries")
	}

	metrics.Online = true
	metrics.Present = true
	metrics.LastFrameAt = time.Now().UTC()
	metrics.Metrics = make(map[string]float64)
	var temperatures []float64
	for _, status := range message.Status {
		values := make(map[string]string, len(status.Values))
		for _, item := range status.Values {
			values[item.Key] = strings.TrimSpace(item.Value)
		}
		if strings.HasSuffix(status.Name, "/summary") {
			if profile := values["profile"]; profile != "" {
				metrics.Profile = profile
			}
			readMetric(values, "voltage", &metrics.Voltage)
			readMetric(values, "current", &metrics.Current)
			readMetric(values, "temperature", &metrics.Temperature)
			if value, ok := numericValue(values["percentage"]); ok {
				metrics.SOCPercent = normalizeSOC(value)
			}
			if value, ok := numericValue(values["power_supply_status"]); ok {
				metrics.PowerSupplyStatus = powerStatus(uint8(value))
			}
		}
		for key, raw := range values {
			value, ok := numericValue(raw)
			if !ok || math.IsNaN(value) || math.IsInf(value, 0) {
				continue
			}
			metrics.Metrics[statusMetricKey(status.Name, key)] = value
		}
		for key, raw := range values {
			value, ok := numericValue(raw)
			if !ok || math.IsNaN(value) || math.IsInf(value, 0) {
				continue
			}
			switch {
			case strings.HasPrefix(key, "cell_voltage."):
				setIndexedMetric(&metrics.CellVoltages, key, value)
			case strings.HasPrefix(key, "cell_temperature."):
				setIndexedMetric(&metrics.CellTemperatures, key, value)
				temperatures = append(temperatures, value)
			}
		}
	}
	if metrics.Profile == "" {
		metrics.Profile = metrics.Protocol
	}
	if metrics.PowerWatts == 0 && (metrics.Voltage != 0 || metrics.Current != 0) {
		metrics.PowerWatts = metrics.Voltage * metrics.Current
	}
	if metrics.Temperature == 0 && len(temperatures) > 0 {
		metrics.Temperature = maxValue(temperatures)
	}
	return metrics, nil
}

func readMetric(values map[string]string, key string, target *float64) {
	if value, ok := numericValue(values[key]); ok {
		*target = value
	}
}

func numericValue(value string) (float64, bool) {
	if value == "" {
		return 0, false
	}
	parsed, err := strconv.ParseFloat(strings.TrimSpace(value), 64)
	return parsed, err == nil
}

func normalizeSOC(value float64) float64 {
	if value >= 0 && value <= 1 {
		return value * 100
	}
	return value
}

func setIndexedMetric(target *[]float64, key string, value float64) {
	parts := strings.Split(key, ".")
	if len(parts) != 2 {
		return
	}
	index, err := strconv.Atoi(parts[1])
	if err != nil || index < 1 || index > 1024 {
		return
	}
	values := *target
	if len(values) < index {
		values = append(values, make([]float64, index-len(values))...)
	}
	values[index-1] = value
	*target = values
}

func statusMetricKey(status, key string) string {
	const prefix = "batcan/"
	status = strings.TrimPrefix(status, prefix)
	return status + "." + key
}

func maxValue(values []float64) float64 {
	if len(values) == 0 {
		return 0
	}
	result := values[0]
	for _, value := range values[1:] {
		if value > result {
			result = value
		}
	}
	return result
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
