package collector

import (
	"context"
	"encoding/csv"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"baize/shared/model"
)

var ErrNoGPU = errors.New("no NVIDIA GPU data available")

func CollectNVIDIAGPUs(command string, timeout time.Duration) ([]model.GPUMetrics, error) {
	ctx, cancel := context.WithTimeout(context.Background(), timeout)
	defer cancel()
	args := []string{
		"--query-gpu=index,name,utilization.gpu,memory.total,memory.used,temperature.gpu,power.draw",
		"--format=csv,noheader,nounits",
	}
	output, err := exec.CommandContext(ctx, command, args...).Output()
	var nvidiaMetrics []model.GPUMetrics
	var nvidiaValuesAvailable bool
	if err == nil {
		nvidiaMetrics, nvidiaValuesAvailable, err = parseNVIDIAOutput(output)
		if err != nil {
			return nil, err
		}
		if nvidiaValuesAvailable {
			return nvidiaMetrics, nil
		}
	}

	name := "NVIDIA Jetson GPU"
	if len(nvidiaMetrics) > 0 && nvidiaMetrics[0].Name != "" {
		name = nvidiaMetrics[0].Name
	}
	if jetsonMetrics, jetsonErr := collectJetsonGPU(name); jetsonErr == nil {
		return []model.GPUMetrics{jetsonMetrics}, nil
	}
	if err != nil {
		if errors.Is(err, exec.ErrNotFound) {
			return nil, ErrNoGPU
		}
		return nil, fmt.Errorf("nvidia-smi: %w", err)
	}
	if len(nvidiaMetrics) > 0 {
		return nvidiaMetrics, nil
	}
	return nil, ErrNoGPU
}

func parseNVIDIAOutput(output []byte) ([]model.GPUMetrics, bool, error) {
	reader := csv.NewReader(strings.NewReader(string(output)))
	records, err := reader.ReadAll()
	if err != nil {
		return nil, false, err
	}
	var result []model.GPUMetrics
	valuesAvailable := false
	for _, record := range records {
		if len(record) < 7 {
			continue
		}
		for _, value := range record[2:7] {
			valuesAvailable = valuesAvailable || nvidiaValueAvailable(value)
		}
		index, _ := strconv.Atoi(strings.TrimSpace(record[0]))
		utilization, _ := parseNVIDIAFloat(record[2])
		memoryTotal, _ := parseNVIDIAFloat(record[3])
		memoryUsed, _ := parseNVIDIAFloat(record[4])
		temperature, _ := parseNVIDIAFloat(record[5])
		power, _ := parseNVIDIAFloat(record[6])
		result = append(result, model.GPUMetrics{
			Index:              index,
			Name:               strings.TrimSpace(record[1]),
			UtilizationPercent: utilization,
			MemoryTotalBytes:   uint64(memoryTotal * 1024 * 1024),
			MemoryUsedBytes:    uint64(memoryUsed * 1024 * 1024),
			TemperatureCelsius: temperature,
			PowerWatts:         power,
		})
	}
	if len(result) == 0 {
		return nil, false, ErrNoGPU
	}
	return result, valuesAvailable, nil
}

func collectJetsonGPU(name string) (model.GPUMetrics, error) {
	metrics := model.GPUMetrics{Name: name}
	loadData, loadErr := os.ReadFile("/sys/devices/platform/bus@0/17000000.gpu/load")
	temperatureData, temperatureErr := readJetsonGPUTemperature()
	if loadErr != nil && temperatureErr != nil {
		return metrics, ErrNoGPU
	}
	if loadErr == nil {
		load, err := strconv.ParseFloat(strings.TrimSpace(string(loadData)), 64)
		if err != nil {
			return metrics, fmt.Errorf("parse Jetson GPU load: %w", err)
		}
		metrics.UtilizationPercent = clampPercent(load / 10)
	}
	if temperatureErr == nil {
		temperature, err := strconv.ParseFloat(strings.TrimSpace(string(temperatureData)), 64)
		if err != nil {
			return metrics, fmt.Errorf("parse Jetson GPU temperature: %w", err)
		}
		metrics.TemperatureCelsius = temperature / 1000
	}
	if memory, err := readKeyValues("/proc/meminfo"); err == nil {
		metrics.MemoryTotalBytes = memory["MemTotal"] * 1024
		available := memory["MemAvailable"] * 1024
		if available <= metrics.MemoryTotalBytes {
			metrics.MemoryUsedBytes = metrics.MemoryTotalBytes - available
		}
	}
	return metrics, nil
}

func readJetsonGPUTemperature() ([]byte, error) {
	typePaths, _ := filepath.Glob("/sys/class/thermal/thermal_zone*/type")
	for _, typePath := range typePaths {
		value, err := os.ReadFile(typePath)
		if err != nil || strings.TrimSpace(string(value)) != "gpu-thermal" {
			continue
		}
		return os.ReadFile(filepath.Join(filepath.Dir(typePath), "temp"))
	}
	return nil, ErrNoGPU
}

func nvidiaValueAvailable(value string) bool {
	value = strings.TrimSpace(value)
	return value != "" && value != "N/A" && value != "[N/A]"
}

func parseNVIDIAFloat(value string) (float64, error) {
	value = strings.TrimSpace(value)
	if value == "" || value == "N/A" || value == "[N/A]" {
		return 0, nil
	}
	return strconv.ParseFloat(value, 64)
}
