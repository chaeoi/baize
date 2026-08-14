//go:build linux

package collector

import (
	"context"
	"encoding/binary"
	"errors"
	"fmt"
	"net"
	"sync"
	"syscall"
	"time"
	"unsafe"

	"baize/agent/internal/config"
	"baize/shared/model"
)

const (
	canRaw        = 1
	canEffFlag    = uint32(0x80000000)
	canEffMask    = uint32(0x1fffffff)
	bmsRequestID  = uint32(0x0400ff80)
	bmsTotalID    = uint32(0x04028001)
	bmsStatusID   = uint32(0x04078001)
	bmsPowerID    = uint32(0x04038001)
	bmsCellStatID = uint32(0x04048001)
	bmsTempStatID = uint32(0x04058001)
	bmsMOSStateID = uint32(0x04068001)
	bmsPackInfoID = uint32(0x04088001)
	bmsFaultID    = uint32(0x04098001)
	bmsSOHID      = uint32(0x040d8001)
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
	frame := make([]byte, 16)
	binary.LittleEndian.PutUint32(frame[0:4], bmsRequestID|canEffFlag)
	frame[4] = 8
	if _, err := syscall.Write(fd, frame); err != nil {
		c.setError(fmt.Errorf("BMS query write: %w", err))
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
	switch id & 0x00ffffff {
	case bmsTotalID & 0x00ffffff:
		if len(data) >= 6 {
			c.metrics.Voltage = float64(binary.BigEndian.Uint16(data[0:2])) * 0.1
			c.metrics.Current = float64(int32(binary.BigEndian.Uint16(data[2:4]))-30000) * 0.1
			c.metrics.SOCPercent = float64(binary.BigEndian.Uint16(data[4:6])) * 0.1
			updated = true
		}
	case bmsPowerID & 0x00ffffff:
		if len(data) >= 7 {
			c.metrics.PowerWatts = float64(int16(binary.BigEndian.Uint16(data[0:2])))
			c.metrics.TotalEnergyWh = float64(binary.BigEndian.Uint16(data[2:4]))
			c.metrics.MOSCelsius = float64(data[4]) - 40
			c.metrics.BoardCelsius = float64(data[5]) - 40
			c.metrics.HeaterCelsius = float64(data[6]) - 40
			c.metrics.Temperature = c.metrics.MOSCelsius
			updated = true
		}
	case bmsCellStatID & 0x00ffffff:
		if len(data) >= 8 {
			c.metrics.MaxCellVoltage = float64(binary.BigEndian.Uint16(data[0:2])) / 1000
			c.metrics.MinCellVoltage = float64(binary.BigEndian.Uint16(data[3:5])) / 1000
			c.metrics.CellVoltageDelta = float64(binary.BigEndian.Uint16(data[6:8])) / 1000
			updated = true
		}
	case bmsTempStatID & 0x00ffffff:
		if len(data) >= 5 {
			c.metrics.MaxCellTemperature = float64(data[0]) - 40
			c.metrics.MinCellTemperature = float64(data[2]) - 40
			c.metrics.CellTemperatureDelta = float64(data[4])
			c.metrics.Temperature = c.metrics.MaxCellTemperature
			updated = true
		}
	case bmsMOSStateID & 0x00ffffff:
		if len(data) >= 5 {
			updated = true
		}
	case bmsStatusID & 0x00ffffff:
		if len(data) >= 1 {
			c.metrics.PowerSupplyStatus = powerStatus(data[0])
			updated = true
		}
	case bmsPackInfoID & 0x00ffffff:
		if len(data) >= 8 {
			c.metrics.CellCount = int(data[0])
			c.metrics.TemperatureCount = int(data[1])
			c.metrics.RemainingCapacityAh = float64(binary.BigEndian.Uint32(data[2:6])) / 1000
			c.metrics.CycleCount = int(binary.BigEndian.Uint16(data[6:8]))
			updated = true
		}
	case bmsFaultID & 0x00ffffff:
		if len(data) >= 8 {
			c.metrics.Faults = bmsFaults(data)
			updated = true
		}
	case bmsSOHID & 0x00ffffff:
		if len(data) >= 5 {
			raw := binary.BigEndian.Uint16(data[3:5])
			c.metrics.SOHPercent = float64(raw)
			if raw > 100 {
				c.metrics.SOHPercent /= 10
			}
			updated = true
		}
	}
	if updated {
		c.metrics.LastFrameAt = time.Now().UTC()
		c.metrics.Online = true
		c.lastErr = nil
	}
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

func bmsFaults(data []byte) []string {
	names := [][]string{
		{"cell_overvoltage_l1", "cell_overvoltage_l2", "cell_undervoltage_l1", "cell_undervoltage_l2", "pack_overvoltage_l1", "pack_overvoltage_l2", "pack_undervoltage_l1", "pack_undervoltage_l2"},
		{"charge_overtemp_l1", "charge_overtemp_l2", "charge_undertemp_l1", "charge_undertemp_l2", "discharge_overtemp_l1", "discharge_overtemp_l2", "discharge_undertemp_l1", "discharge_undertemp_l2"},
		{"charge_overcurrent_l1", "charge_overcurrent_l2", "discharge_overcurrent_l1", "discharge_overcurrent_l2", "soc_high_l1", "soc_high_l2", "soc_low_l1", "soc_low_l2"},
		{"cell_delta_high_l1", "cell_delta_high_l2", "temp_delta_high_l1", "temp_delta_high_l2", "mos_overtemp_l1", "mos_overtemp_l2", "board_overtemp_l1", "board_overtemp_l2"},
		{"charge_mos_overtemp", "discharge_mos_overtemp", "charge_mos_sensor_fault", "discharge_mos_sensor_fault", "charge_mos_stuck", "discharge_mos_stuck", "charge_mos_open", "discharge_mos_open"},
		{"afe_fault", "cell_acquisition_offline", "cell_temp_sensor_fault", "eeprom_fault", "rtc_fault", "precharge_failed", "vehicle_communication_fault", "internal_communication_fault"},
		{"current_module_fault", "pack_voltage_module_fault", "short_circuit", "low_voltage_charge_inhibit", "external_mos_shutdown", "charger_removed", "thermal_runaway", "heater_fault"},
		{"balance_communication_fault", "balance_condition_not_met", "reserved", "reserved", "reserved", "reserved", "reserved", "reserved"},
	}
	var result []string
	for byteIndex := 0; byteIndex < len(data) && byteIndex < len(names); byteIndex++ {
		for bit := 0; bit < 8; bit++ {
			if data[byteIndex]&(1<<bit) != 0 && names[byteIndex][bit] != "reserved" {
				result = append(result, names[byteIndex][bit])
			}
		}
	}
	return result
}
