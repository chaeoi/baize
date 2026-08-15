//go:build linux

package collector

import (
	"encoding/binary"
	"testing"
	"time"

	"baize/agent/internal/config"
)

func TestParseSupportedBMSFrames(t *testing.T) {
	cfg := config.Default().BMS
	cfg.Enabled = true
	cfg.Timeout = config.Duration(time.Second)
	c := NewBMSCollector(cfg)
	frame := make([]byte, 16)
	binary.LittleEndian.PutUint32(frame[0:4], uint32(0x04028001)|canEffFlag)
	frame[4] = 8
	copy(frame[8:], []byte{0x02, 0x00, 0x75, 0x30, 0x03, 0x52, 0, 0})
	c.consumeFrame(frame)
	got, err := c.Snapshot()
	if err != nil {
		t.Fatal(err)
	}
	if got.Voltage != 51.2 || got.Current != 0 || got.SOCPercent != 85 {
		t.Fatalf("unexpected BMS data: %+v", got)
	}
}

func TestParseSupportedBMSPackInfo(t *testing.T) {
	cfg := config.Default().BMS
	cfg.Enabled = true
	cfg.Timeout = config.Duration(time.Second)
	c := NewBMSCollector(cfg)
	frame := make([]byte, 16)
	binary.LittleEndian.PutUint32(frame[0:4], uint32(0x04088001)|canEffFlag)
	frame[4] = 8
	copy(frame[8:], []byte{24, 8, 0x00, 0x01, 0x09, 0xa0, 0x00, 0x7b})
	c.consumeFrame(frame)
	got, err := c.Snapshot()
	if err != nil {
		t.Fatal(err)
	}
	if got.CellCount != 24 || got.TemperatureCount != 8 || got.RemainingCapacityAh != 68 || got.CycleCount != 123 {
		t.Fatalf("unexpected extended BMS data: %+v", got)
	}
}

func TestParseSupportedBMSTemperatureStats(t *testing.T) {
	cfg := config.Default().BMS
	cfg.Enabled = true
	cfg.Timeout = config.Duration(time.Second)
	c := NewBMSCollector(cfg)
	frame := make([]byte, 16)
	binary.LittleEndian.PutUint32(frame[0:4], uint32(0x04058001)|canEffFlag)
	frame[4] = 8
	copy(frame[8:], []byte{72, 3, 65, 7, 7, 0, 0, 0})
	c.consumeFrame(frame)
	got, err := c.Snapshot()
	if err != nil {
		t.Fatal(err)
	}
	if got.MaxCellTemperature != 32 || got.MinCellTemperature != 25 || got.CellTemperatureDelta != 7 || got.Temperature != 32 {
		t.Fatalf("unexpected temperature stats: %+v", got)
	}
}
