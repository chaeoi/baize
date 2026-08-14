package config

import (
	"os"
	"path/filepath"
	"testing"
)

func TestRobotProfileDefaultsAndOverrides(t *testing.T) {
	path := filepath.Join(t.TempDir(), "config.yml")
	data := []byte(`agent:
  uuid: 7fd34256-bf3a-4cf6-8da0-fbce40f34d11
  robot_code: TEST
  robot_model: 2m_v0.1.2
  dashboard_url: http://dashboard:8080
  token: long-enough-agent-token
motor:
  enabled: false
bms:
  publish_ros2: true
`)
	if err := os.WriteFile(path, data, 0o600); err != nil {
		t.Fatal(err)
	}
	cfg, err := Load(path)
	if err != nil {
		t.Fatal(err)
	}
	if cfg.Motor.Enabled || cfg.Motor.Definitions["motor_id_22"].Model != "model-22" {
		t.Fatalf("unexpected motor profile: %+v", cfg.Motor)
	}
	if cfg.BMS.Protocol != "yy-bcu14h-mos-24s100a" || cfg.BMS.CANInterface != "can5" || !cfg.BMS.PublishROS2 {
		t.Fatalf("unexpected BMS profile: %+v", cfg.BMS)
	}
}
