//go:build linux

package collector

import (
	"bytes"
	"context"
	"encoding/binary"
	"errors"
	"fmt"
	"net"
	"os/exec"
	"strings"
	"sync"
	"syscall"
	"time"
	"unsafe"

	"baize/agent/internal/config"
	"baize/shared/model"
	"gopkg.in/yaml.v3"
)

const (
	canRaw     = 1
	canEffFlag = uint32(0x80000000)
	canEffMask = uint32(0x1fffffff)
)

type rawSockaddrCAN struct {
	Family  uint16
	Ifindex int32
	Addr    [8]byte
}

type BMSCollector struct {
	config  config.BMSConfig
	mu      sync.RWMutex
	metrics model.BMSMetrics
	lastErr error
}

func NewBMSCollector(cfg config.BMSConfig) *BMSCollector {
	return &BMSCollector{
		config: cfg,
		metrics: model.BMSMetrics{
			Enabled:   true,
			Protocol:  cfg.Protocol,
			Interface: cfg.CANInterface,
			Specification: model.BatterySpecification{
				Vendor: cfg.Specification.Vendor, PackModel: cfg.Specification.PackModel,
				Chemistry: cfg.Specification.Chemistry, NominalVoltage: cfg.Specification.NominalVoltage,
				CapacityAh: cfg.Specification.CapacityAh, SeriesCells: cfg.Specification.SeriesCells,
			},
		},
	}
}

func (c *BMSCollector) Run(ctx context.Context) {
	if c.config.Source == "ros2_topic" {
		c.readROSLoop(ctx)
		return
	}
	for {
		if ctx.Err() != nil {
			return
		}
		fd, err := openCAN(c.config.CANInterface)
		if err != nil {
			c.setError(err)
			if !waitContext(ctx, 5*time.Second) {
				return
			}
			continue
		}
		c.readLoop(ctx, fd)
		syscall.Close(fd)
		if !waitContext(ctx, time.Second) {
			return
		}
	}
}

type batteryStateMessage struct {
	Voltage           float64 `yaml:"voltage"`
	Current           float64 `yaml:"current"`
	Temperature       float64 `yaml:"temperature"`
	Percentage        float64 `yaml:"percentage"`
	PowerSupplyStatus uint8   `yaml:"power_supply_status"`
}

func (c *BMSCollector) readROSLoop(ctx context.Context) {
	ticker := time.NewTicker(c.config.QueryInterval.Value())
	defer ticker.Stop()
	for {
		readCtx, cancel := context.WithTimeout(ctx, c.config.Timeout.Value())
		command, err := rosCommand(c.config.ROSSetup, "ros2 topic echo --once "+shellQuote(c.config.ROSTopic)+" "+shellQuote(c.config.ROSMessageType))
		if err == nil {
			var stderr bytes.Buffer
			cmd := exec.CommandContext(readCtx, "/bin/bash", "-lc", command)
			cmd.Stderr = &stderr
			var output []byte
			output, err = cmd.Output()
			if err == nil {
				var message batteryStateMessage
				err = yaml.Unmarshal(output, &message)
				if err == nil {
					percentage := message.Percentage
					if percentage >= 0 && percentage <= 1 {
						percentage *= 100
					}
					c.mu.Lock()
					c.metrics.Voltage, c.metrics.Current, c.metrics.Temperature, c.metrics.SOCPercent = message.Voltage, message.Current, message.Temperature, percentage
					c.metrics.PowerWatts, c.metrics.PowerSupplyStatus = message.Voltage*message.Current, powerStatus(message.PowerSupplyStatus)
					c.metrics.LastFrameAt, c.metrics.Online, c.lastErr = time.Now().UTC(), true, nil
					c.mu.Unlock()
				}
			}
			if err != nil && strings.TrimSpace(stderr.String()) != "" {
				err = errors.New(strings.TrimSpace(stderr.String()))
			}
		}
		cancel()
		if err != nil {
			c.setError(fmt.Errorf("BMS ROS2 topic: %w", err))
		}
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
		}
	}
}

func (c *BMSCollector) Snapshot() (model.BMSMetrics, error) {
	c.mu.RLock()
	result := c.metrics
	err := c.lastErr
	c.mu.RUnlock()
	if result.LastFrameAt.IsZero() || time.Since(result.LastFrameAt) > c.config.Timeout.Value() {
		result.Online = false
	} else {
		result.Online = true
	}
	result.Faults = append([]string(nil), result.Faults...)
	return result, err
}

func (c *BMSCollector) MarkPublished(success bool) {
	c.mu.Lock()
	c.metrics.PublishedToROS2 = success
	c.mu.Unlock()
}

func (c *BMSCollector) readLoop(ctx context.Context, fd int) {
	queryTicker := time.NewTicker(c.config.QueryInterval.Value())
	defer queryTicker.Stop()
	c.sendQuery(fd)
	buffer := make([]byte, 16)
	for {
		select {
		case <-ctx.Done():
			return
		case <-queryTicker.C:
			c.sendQuery(fd)
		default:
		}
		ready, err := canReadable(fd, 200*time.Millisecond)
		if err != nil {
			c.setError(fmt.Errorf("CAN select: %w", err))
			return
		}
		if !ready {
			continue
		}
		n, err := syscall.Read(fd, buffer)
		if err != nil {
			if errors.Is(err, syscall.EINTR) || errors.Is(err, syscall.EAGAIN) {
				continue
			}
			c.setError(fmt.Errorf("CAN read: %w", err))
			return
		}
		if n == 16 {
			c.consumeFrame(buffer)
		}
	}
}

func (c *BMSCollector) sendQuery(fd int) {
	sent := make(map[uint32]struct{})
	for _, query := range c.config.CANQueries {
		if _, ok := sent[query.RequestID]; ok {
			continue
		}
		sent[query.RequestID] = struct{}{}
		frame := make([]byte, 16)
		binary.LittleEndian.PutUint32(frame[0:4], query.RequestID|canEffFlag)
		frame[4] = 8
		if _, err := syscall.Write(fd, frame); err != nil {
			c.setError(fmt.Errorf("BMS query %s write: %w", query.Name, err))
		}
	}
}

func (c *BMSCollector) consumeFrame(frame []byte) {
	rawID := binary.LittleEndian.Uint32(frame[0:4])
	if rawID&canEffFlag == 0 {
		return
	}
	id := rawID & canEffMask
	length := int(frame[4])
	if length > 8 {
		length = 8
	}
	data := frame[8 : 8+length]
	c.mu.Lock()
	defer c.mu.Unlock()
	updated := false
	for _, query := range c.config.CANQueries {
		if id&0x00ffffff != query.ResponseID&0x00ffffff {
			continue
		}
		for _, field := range query.Fields {
			if c.applyCANField(field, data) {
				updated = true
			}
		}
		break
	}
	if updated {
		c.metrics.LastFrameAt = time.Now().UTC()
		c.metrics.Online = true
		c.lastErr = nil
	}
}

func (c *BMSCollector) applyCANField(field config.CANField, data []byte) bool {
	if field.Offset < 0 || field.Length <= 0 || field.Offset+field.Length > len(data) {
		return false
	}
	segment := data[field.Offset : field.Offset+field.Length]
	if field.Name == "faults" && field.Encoding == "bits" {
		c.metrics.Faults = namedBits(segment, field.BitNames)
		return true
	}
	raw, ok := decodeCANNumber(segment, field.Encoding, field.Endian)
	if !ok {
		return false
	}
	scale := field.Scale
	if scale == 0 {
		scale = 1
	}
	value := raw*scale + field.Bias
	switch field.Name {
	case "voltage":
		c.metrics.Voltage = value
	case "current":
		c.metrics.Current = value
	case "soc_percent":
		c.metrics.SOCPercent = value
	case "power_supply_status":
		c.metrics.PowerSupplyStatus = powerStatus(byte(raw))
	case "power_watts":
		c.metrics.PowerWatts = value
	case "total_energy_wh":
		c.metrics.TotalEnergyWh = value
	case "mos_celsius":
		c.metrics.MOSCelsius, c.metrics.Temperature = value, value
	case "board_celsius":
		c.metrics.BoardCelsius = value
	case "heater_celsius":
		c.metrics.HeaterCelsius = value
	case "max_cell_voltage":
		c.metrics.MaxCellVoltage = value
	case "min_cell_voltage":
		c.metrics.MinCellVoltage = value
	case "cell_voltage_delta":
		c.metrics.CellVoltageDelta = value
	case "max_cell_temperature":
		c.metrics.MaxCellTemperature, c.metrics.Temperature = value, value
	case "min_cell_temperature":
		c.metrics.MinCellTemperature = value
	case "cell_temperature_delta":
		c.metrics.CellTemperatureDelta = value
	case "cell_count":
		c.metrics.CellCount = int(value)
	case "temperature_count":
		c.metrics.TemperatureCount = int(value)
	case "remaining_capacity_ah":
		c.metrics.RemainingCapacityAh = value
	case "cycle_count":
		c.metrics.CycleCount = int(value)
	case "soh_percent":
		c.metrics.SOHPercent = value
	default:
		return false
	}
	return true
}

func decodeCANNumber(data []byte, encoding, endian string) (float64, bool) {
	if len(data) != 1 && len(data) != 2 && len(data) != 4 && len(data) != 8 {
		return 0, false
	}
	var order binary.ByteOrder = binary.BigEndian
	if endian == "little" {
		order = binary.LittleEndian
	}
	var unsigned uint64
	switch len(data) {
	case 1:
		unsigned = uint64(data[0])
	case 2:
		unsigned = uint64(order.Uint16(data))
	case 4:
		unsigned = uint64(order.Uint32(data))
	case 8:
		unsigned = order.Uint64(data)
	}
	if encoding != "int" {
		return float64(unsigned), true
	}
	bits := uint(len(data) * 8)
	if unsigned&(uint64(1)<<(bits-1)) == 0 {
		return float64(unsigned), true
	}
	return float64(int64(unsigned | (^uint64(0) << bits))), true
}

func namedBits(data []byte, names []string) []string {
	result := make([]string, 0)
	for byteIndex, value := range data {
		for bit := 0; bit < 8; bit++ {
			if value&(1<<bit) == 0 {
				continue
			}
			index := byteIndex*8 + bit
			name := fmt.Sprintf("fault_bit_%d", index)
			if index < len(names) {
				if names[index] == "" || names[index] == "reserved" {
					continue
				}
				name = names[index]
			}
			result = append(result, name)
		}
	}
	return result
}

func (c *BMSCollector) setError(err error) {
	c.mu.Lock()
	c.lastErr = err
	c.mu.Unlock()
}

func openCAN(interfaceName string) (int, error) {
	iface, err := net.InterfaceByName(interfaceName)
	if err != nil {
		return -1, fmt.Errorf("CAN interface %s: %w", interfaceName, err)
	}
	fd, err := syscall.Socket(syscall.AF_CAN, syscall.SOCK_RAW|syscall.SOCK_CLOEXEC, canRaw)
	if err != nil {
		return -1, err
	}
	address := rawSockaddrCAN{Family: syscall.AF_CAN, Ifindex: int32(iface.Index)}
	_, _, errno := syscall.Syscall(syscall.SYS_BIND, uintptr(fd), uintptr(unsafe.Pointer(&address)), unsafe.Sizeof(address))
	if errno != 0 {
		syscall.Close(fd)
		return -1, errno
	}
	return fd, nil
}

func canReadable(fd int, timeout time.Duration) (bool, error) {
	var set syscall.FdSet
	index := fd / 64
	if index >= len(set.Bits) {
		return false, errors.New("CAN file descriptor exceeds select limit")
	}
	set.Bits[index] |= 1 << uint(fd%64)
	tv := syscall.NsecToTimeval(timeout.Nanoseconds())
	count, err := syscall.Select(fd+1, &set, nil, nil, &tv)
	return count > 0, err
}

func waitContext(ctx context.Context, duration time.Duration) bool {
	timer := time.NewTimer(duration)
	defer timer.Stop()
	select {
	case <-ctx.Done():
		return false
	case <-timer.C:
		return true
	}
}

func powerStatus(value byte) string {
	switch value {
	case 0:
		return "not_charging"
	case 1:
		return "charging"
	case 2:
		return "discharging"
	default:
		return "unknown"
	}
}
