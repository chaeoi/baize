package collector

import (
	"math"
	"testing"

	"baize/shared/model"
)

func TestDecodeBatteryState(t *testing.T) {
	metrics, err := decodeBatteryState([]byte(`
voltage: 52.4
current: -10.5
temperature: 31.0
percentage: 0.9
power_supply_status: 2
present: true
`), model.BMSMetrics{Enabled: true})
	if err != nil {
		t.Fatal(err)
	}
	if !metrics.Online || metrics.Voltage != 52.4 || metrics.Current != -10.5 || metrics.SOCPercent != 90 || metrics.PowerSupplyStatus != "discharging" || math.Abs(metrics.PowerWatts+550.2) > 1e-9 {
		t.Fatalf("unexpected BMS data: %+v", metrics)
	}
}

func TestPowerStatus(t *testing.T) {
	if powerStatus(1) != "charging" || powerStatus(3) != "not_charging" || powerStatus(99) != "unknown" {
		t.Fatal("unexpected power status mapping")
	}
}
