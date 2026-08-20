package collector

import (
	"math"
	"testing"

	"baize/shared/model"
)

func TestDecodeDiagnosticArray(t *testing.T) {
	metrics, err := decodeDiagnosticArray([]byte(`
status:
- name: batcan/jbd/summary
  message: BMS data received
  hardware_id: JBD-CANBUS
  values:
  - key: profile
    value: jbd
  - key: voltage
    value: '38.22'
  - key: current
    value: '-2.88'
  - key: percentage
    value: '0.01'
  - key: power_supply_status
    value: '2'
- name: batcan/jbd/cell_voltage_1_3
  values:
  - key: cell_voltage.1
    value: '3.801'
  - key: cell_voltage.2
    value: '3.802'
- name: batcan/jbd/temperatures_1_3
  values:
  - key: cell_temperature.1
    value: '31.5'
`), model.BMSMetrics{Enabled: true})
	if err != nil {
		t.Fatal(err)
	}
	if !metrics.Online || metrics.Profile != "jbd" || metrics.Voltage != 38.22 || metrics.Current != -2.88 || metrics.SOCPercent != 1 || metrics.PowerSupplyStatus != "discharging" || math.Abs(metrics.PowerWatts+110.0736) > 1e-9 {
		t.Fatalf("unexpected BMS data: %+v", metrics)
	}
	if len(metrics.CellVoltages) != 2 || math.Abs(metrics.CellVoltages[0]-3.801) > 1e-9 || len(metrics.CellTemperatures) != 1 || metrics.Temperature != 31.5 {
		t.Fatalf("unexpected cell data: %+v", metrics)
	}
}

func TestPowerStatus(t *testing.T) {
	if powerStatus(1) != "charging" || powerStatus(3) != "not_charging" || powerStatus(99) != "unknown" {
		t.Fatal("unexpected power status mapping")
	}
}

func TestDecodeDiagnosticArrayWithoutBatteryFrame(t *testing.T) {
	metrics, err := decodeDiagnosticArray([]byte(`
status:
- name: batcan/auto/summary
  message: No BMS data received
  hardware_id: BMS auto-detection
  values:
  - key: profile
    value: auto
`), model.BMSMetrics{Enabled: true})
	if err != nil {
		t.Fatal(err)
	}
	if !metrics.Online || metrics.Present {
		t.Fatalf("topic must be online while the battery is absent: %+v", metrics)
	}
}
