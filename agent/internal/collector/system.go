package collector

import (
	"bufio"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"sort"
	"strconv"
	"strings"
	"sync"
	"syscall"

	"xuanjian/shared/model"
)

type cpuTimes struct {
	total uint64
	idle  uint64
}

type SystemCollector struct {
	mu       sync.Mutex
	previous cpuTimes
	hasPrev  bool
	model    string
}

func NewSystemCollector() *SystemCollector {
	return &SystemCollector{model: readCPUModel("/proc/cpuinfo")}
}

func (c *SystemCollector) Collect(diskPaths []string) (model.SystemMetrics, error) {
	metrics := model.SystemMetrics{CPUModel: c.model, CPUCores: runtime.NumCPU()}
	current, err := readCPUTimes("/proc/stat")
	if err != nil {
		return metrics, err
	}
	c.mu.Lock()
	if c.hasPrev && current.total > c.previous.total {
		totalDelta := current.total - c.previous.total
		idleDelta := current.idle - c.previous.idle
		metrics.CPUUsagePercent = clampPercent(100 * (1 - float64(idleDelta)/float64(totalDelta)))
	}
	c.previous, c.hasPrev = current, true
	c.mu.Unlock()

	metrics.Load1, metrics.Load5, metrics.Load15, _ = readLoadAverage("/proc/loadavg")
	mem, err := readKeyValues("/proc/meminfo")
	if err != nil {
		return metrics, err
	}
	metrics.MemoryTotalBytes = mem["MemTotal"] * 1024
	available := mem["MemAvailable"] * 1024
	if available > metrics.MemoryTotalBytes {
		available = metrics.MemoryTotalBytes
	}
	metrics.MemoryUsedBytes = metrics.MemoryTotalBytes - available
	metrics.SwapTotalBytes = mem["SwapTotal"] * 1024
	swapFree := mem["SwapFree"] * 1024
	if swapFree <= metrics.SwapTotalBytes {
		metrics.SwapUsedBytes = metrics.SwapTotalBytes - swapFree
	}
	metrics.UptimeSeconds, _ = readFirstFloat("/proc/uptime")

	for _, path := range diskPaths {
		disk, diskErr := readDisk(path)
		if diskErr == nil {
			metrics.Disks = append(metrics.Disks, disk)
		}
	}
	metrics.Temperatures = readTemperatures()
	return metrics, nil
}

func readCPUTimes(path string) (cpuTimes, error) {
	file, err := os.Open(path)
	if err != nil {
		return cpuTimes{}, err
	}
	defer file.Close()
	line, err := bufio.NewReader(file).ReadString('\n')
	if err != nil {
		return cpuTimes{}, err
	}
	fields := strings.Fields(line)
	if len(fields) < 5 || fields[0] != "cpu" {
		return cpuTimes{}, errors.New("unexpected /proc/stat cpu line")
	}
	var values []uint64
	for _, field := range fields[1:] {
		value, parseErr := strconv.ParseUint(field, 10, 64)
		if parseErr != nil {
			return cpuTimes{}, parseErr
		}
		values = append(values, value)
	}
	result := cpuTimes{}
	for _, value := range values {
		result.total += value
	}
	result.idle = values[3]
	if len(values) > 4 {
		result.idle += values[4]
	}
	return result, nil
}

func readCPUModel(path string) string {
	file, err := os.Open(path)
	if err != nil {
		return ""
	}
	defer file.Close()
	scanner := bufio.NewScanner(file)
	for scanner.Scan() {
		parts := strings.SplitN(scanner.Text(), ":", 2)
		if len(parts) != 2 {
			continue
		}
		key := strings.TrimSpace(parts[0])
		if key == "model name" || key == "Model" || key == "Hardware" {
			return strings.TrimSpace(parts[1])
		}
	}
	return ""
}

func readLoadAverage(path string) (float64, float64, float64, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return 0, 0, 0, err
	}
	fields := strings.Fields(string(data))
	if len(fields) < 3 {
		return 0, 0, 0, errors.New("unexpected loadavg format")
	}
	one, err1 := strconv.ParseFloat(fields[0], 64)
	five, err5 := strconv.ParseFloat(fields[1], 64)
	fifteen, err15 := strconv.ParseFloat(fields[2], 64)
	return one, five, fifteen, errors.Join(err1, err5, err15)
}

func readKeyValues(path string) (map[string]uint64, error) {
	file, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer file.Close()
	values := make(map[string]uint64)
	scanner := bufio.NewScanner(file)
	for scanner.Scan() {
		fields := strings.Fields(scanner.Text())
		if len(fields) < 2 {
			continue
		}
		value, parseErr := strconv.ParseUint(fields[1], 10, 64)
		if parseErr == nil {
			values[strings.TrimSuffix(fields[0], ":")] = value
		}
	}
	return values, scanner.Err()
}

func readFirstFloat(path string) (float64, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return 0, err
	}
	fields := strings.Fields(string(data))
	if len(fields) == 0 {
		return 0, errors.New("empty numeric file")
	}
	return strconv.ParseFloat(fields[0], 64)
}

func readDisk(path string) (model.DiskMetrics, error) {
	var stat syscall.Statfs_t
	if err := syscall.Statfs(path, &stat); err != nil {
		return model.DiskMetrics{}, err
	}
	total := stat.Blocks * uint64(stat.Bsize)
	available := stat.Bavail * uint64(stat.Bsize)
	return model.DiskMetrics{Path: path, TotalBytes: total, UsedBytes: total - available}, nil
}

func readTemperatures() []model.Temperature {
	seen := make(map[string]bool)
	var values []model.Temperature
	thermalPaths, _ := filepath.Glob("/sys/class/thermal/thermal_zone*/temp")
	for _, path := range thermalPaths {
		nameBytes, _ := os.ReadFile(filepath.Join(filepath.Dir(path), "type"))
		name := strings.TrimSpace(string(nameBytes))
		if name == "" {
			name = filepath.Base(filepath.Dir(path))
		}
		if value, ok := readTemperatureValue(path); ok {
			key := fmt.Sprintf("%s:%.2f", name, value)
			if !seen[key] {
				values = append(values, model.Temperature{Name: name, Celsius: value})
				seen[key] = true
			}
		}
	}
	hwmonPaths, _ := filepath.Glob("/sys/class/hwmon/hwmon*/temp*_input")
	for _, path := range hwmonPaths {
		dir := filepath.Dir(path)
		chipBytes, _ := os.ReadFile(filepath.Join(dir, "name"))
		chip := strings.TrimSpace(string(chipBytes))
		stem := strings.TrimSuffix(filepath.Base(path), "_input")
		labelBytes, _ := os.ReadFile(filepath.Join(dir, stem+"_label"))
		label := strings.TrimSpace(string(labelBytes))
		name := strings.Trim(strings.Join([]string{chip, label}, " "), " ")
		if name == "" {
			name = filepath.Base(dir) + " " + stem
		}
		if value, ok := readTemperatureValue(path); ok {
			key := fmt.Sprintf("%s:%.2f", name, value)
			if !seen[key] {
				values = append(values, model.Temperature{Name: name, Celsius: value})
				seen[key] = true
			}
		}
	}
	sort.Slice(values, func(i, j int) bool { return values[i].Name < values[j].Name })
	return values
}

func readTemperatureValue(path string) (float64, bool) {
	value, err := readFirstFloat(path)
	if err != nil {
		return 0, false
	}
	if value > 1000 || value < -1000 {
		value /= 1000
	}
	if value < -100 || value > 250 {
		return 0, false
	}
	return value, true
}

func clampPercent(value float64) float64 {
	if value < 0 {
		return 0
	}
	if value > 100 {
		return 100
	}
	return value
}
