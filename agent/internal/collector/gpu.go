package collector

import (
	"context"
	"encoding/csv"
	"errors"
	"fmt"
	"os/exec"
	"strconv"
	"strings"
	"time"

	"echobot/shared/model"
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
	if err != nil {
		if errors.Is(err, exec.ErrNotFound) {
			return nil, ErrNoGPU
		}
		return nil, fmt.Errorf("nvidia-smi: %w", err)
	}
	reader := csv.NewReader(strings.NewReader(string(output)))
	records, err := reader.ReadAll()
	if err != nil {
		return nil, err
	}
	var result []model.GPUMetrics
	for _, record := range records {
		if len(record) < 7 {
			continue
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
		return nil, ErrNoGPU
	}
	return result, nil
}

func parseNVIDIAFloat(value string) (float64, error) {
	value = strings.TrimSpace(value)
	if value == "" || value == "N/A" || value == "[N/A]" {
		return 0, nil
	}
	return strconv.ParseFloat(value, 64)
}
