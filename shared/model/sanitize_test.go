package model

import (
	"encoding/json"
	"math"
	"testing"
)

func TestSanitizeFiniteMakesTelemetryJSONSafe(t *testing.T) {
	telemetry := &Telemetry{
		System: &SystemMetrics{CPUUsagePercent: math.NaN()},
		GPUs:   []GPUMetrics{{PowerWatts: math.Inf(1)}},
		BMS:    &BMSMetrics{Voltage: math.Inf(-1)},
		Motors: &MotorSnapshot{Motors: []MotorState{{TorqueNm: math.NaN()}}},
	}
	if count := SanitizeFinite(telemetry); count != 4 {
		t.Fatalf("sanitized %d fields, want 4", count)
	}
	if _, err := json.Marshal(telemetry); err != nil {
		t.Fatalf("telemetry should marshal after sanitizing: %v", err)
	}
}
