package collector

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"math"
	"os/exec"
	"strconv"
	"strings"
	"sync"
	"time"

	"baize/agent/internal/config"
	"baize/shared/model"
	"gopkg.in/yaml.v3"
)

type diagnosticArrayMessage struct {
	Status []diagnosticStatusMessage `yaml:"status" json:"status"`
}

type diagnosticStatusMessage struct {
	Name       string               `yaml:"name" json:"name"`
	Message    string               `yaml:"message" json:"message"`
	HardwareID string               `yaml:"hardware_id" json:"hardware_id"`
	Values     []diagnosticKeyValue `yaml:"values" json:"values"`
}

type diagnosticKeyValue struct {
	Key   string `yaml:"key" json:"key"`
	Value string `yaml:"value" json:"value"`
}

type BMSCollector struct {
	config     config.BMSConfig
	streamOnce sync.Once
	readyOnce  sync.Once
	ready      chan struct{}
	mu         sync.Mutex
	latest     model.BMSMetrics
	streamErr  error
}

func NewBMSCollector(cfg config.BMSConfig) *BMSCollector {
	return &BMSCollector{config: cfg, ready: make(chan struct{})}
}

// Collect reads batcan's single DiagnosticArray topic. The bridge owns all
// CAN access and query behavior; the Agent only subscribes to the published
// ROS2 data.
func (c *BMSCollector) Collect(ctx context.Context) (model.BMSMetrics, error) {
	c.streamOnce.Do(func() { go c.streamLoop(ctx) })
	wait := c.config.ReadTimeout.Value()
	if wait <= 0 {
		wait = 3 * time.Second
	}
	select {
	case <-c.ready:
	case <-ctx.Done():
		return c.emptyMetrics(), ctx.Err()
	case <-time.After(wait):
		c.mu.Lock()
		metrics, err := c.latest, c.streamErr
		c.mu.Unlock()
		if !metrics.Online && err == nil {
			err = fmt.Errorf("BMS topic did not publish within %s", wait)
		}
		return metrics, err
	}
	c.mu.Lock()
	metrics, err := c.latest, c.streamErr
	c.mu.Unlock()
	if !metrics.LastFrameAt.IsZero() && time.Since(metrics.LastFrameAt) > wait {
		metrics.Online = false
		metrics.Present = false
		if err == nil {
			err = fmt.Errorf("BMS topic has not published for %s", time.Since(metrics.LastFrameAt).Round(time.Second))
		}
	}
	return metrics, err
}

func (c *BMSCollector) emptyMetrics() model.BMSMetrics {
	return model.BMSMetrics{Enabled: true, Protocol: c.config.Protocol, Interface: c.config.ROSTopic}
}

func (c *BMSCollector) streamLoop(ctx context.Context) {
	for ctx.Err() == nil {
		if err := c.readStreamProcess(ctx); err != nil && ctx.Err() == nil {
			c.mu.Lock()
			c.streamErr = err
			c.mu.Unlock()
			select {
			case <-ctx.Done():
				return
			case <-time.After(time.Second):
			}
		}
	}
}

func (c *BMSCollector) readStreamProcess(ctx context.Context) error {
	command, err := rosSubscriberCommand(c.config.ROSSetup, c.config.ROSEnvironment, c.config.ROSUser, c.config.ROSTopic, c.config.ROSMessageType)
	if err != nil {
		return err
	}
	cmd := exec.CommandContext(ctx, "/bin/bash", "-lc", command)
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		return err
	}
	stderr, err := cmd.StderrPipe()
	if err != nil {
		return err
	}
	if err := cmd.Start(); err != nil {
		return err
	}
	stderrDone := make(chan struct{})
	go func() { _, _ = io.Copy(io.Discard, stderr); close(stderrDone) }()
	scanner := bufio.NewScanner(stdout)
	scanner.Buffer(make([]byte, 16*1024), 2*1024*1024)
	for scanner.Scan() {
		line := strings.TrimSpace(scanner.Text())
		if line == "" || !strings.HasPrefix(line, "{") {
			continue
		}
		metrics, err := decodeBMSMessage([]byte(line), c.emptyMetrics())
		if err != nil {
			c.mu.Lock()
			c.streamErr = err
			c.mu.Unlock()
			continue
		}
		c.mu.Lock()
		c.latest = metrics
		c.streamErr = nil
		c.mu.Unlock()
		c.readyOnce.Do(func() { close(c.ready) })
	}
	if err := scanner.Err(); err != nil {
		_ = cmd.Process.Kill()
		<-stderrDone
		_ = cmd.Wait()
		return err
	}
	<-stderrDone
	if err := cmd.Wait(); err != nil && ctx.Err() == nil {
		return fmt.Errorf("BMS ROS2 topic stream exited: %w", err)
	}
	return nil
}

type diagnosticJSONMessage struct {
	Type    string                    `json:"type"`
	StampNS int64                     `json:"stamp_ns"`
	Status  []diagnosticStatusMessage `json:"status"`
}

func decodeBMSMessage(output []byte, metrics model.BMSMetrics) (model.BMSMetrics, error) {
	var message diagnosticJSONMessage
	if err := json.Unmarshal(output, &message); err != nil {
		return metrics, fmt.Errorf("decode DiagnosticArray JSON: %w", err)
	}
	if message.Type != "" && message.Type != "bms" {
		return metrics, fmt.Errorf("unexpected ROS2 subscriber message type %q", message.Type)
	}
	metrics, err := decodeDiagnosticStatuses(message.Status, metrics)
	if message.StampNS > 0 {
		metrics.LastFrameAt = time.Unix(0, message.StampNS).UTC()
	}
	return metrics, err
}

func decodeDiagnosticArray(output []byte, metrics model.BMSMetrics) (model.BMSMetrics, error) {
	var message diagnosticArrayMessage
	if err := yaml.Unmarshal(output, &message); err != nil {
		return metrics, fmt.Errorf("decode DiagnosticArray YAML: %w", err)
	}
	return decodeDiagnosticStatuses(message.Status, metrics)
}

func decodeDiagnosticStatuses(statuses []diagnosticStatusMessage, metrics model.BMSMetrics) (model.BMSMetrics, error) {
	if len(statuses) == 0 {
		return metrics, errors.New("BMS DiagnosticArray contains no status entries")
	}

	metrics.Online = true
	metrics.LastFrameAt = time.Now().UTC()
	metrics.Metrics = make(map[string]float64)
	var temperatures []float64
	for _, status := range statuses {
		values := make(map[string]string, len(status.Values))
		for _, item := range status.Values {
			values[item.Key] = strings.TrimSpace(item.Value)
		}
		if strings.HasSuffix(status.Name, "/summary") {
			metrics.Present = status.Message == "BMS data received"
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
		if !strings.HasSuffix(status.Name, "/summary") && len(values) > 0 {
			metrics.Present = true
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
