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

func TestExternalRobotProfileSelectsTransportAndCANDecode(t *testing.T) {
	root := t.TempDir()
	profileDir := filepath.Join(root, "profiles")
	if err := os.MkdirAll(profileDir, 0o750); err != nil {
		t.Fatal(err)
	}
	profile := []byte(`motor:
  enabled: true
  source: can_query
  can_interface: can2
  read_timeout: 2s
  can_queries:
    - name: hip
      motor_id: hip
      request_id: 0x101
      response_id: 0x201
      request_data: [1, 2]
      fields:
        - {name: torque_nm, offset: 0, length: 2, encoding: int, endian: big, scale: 0.01}
bms:
  enabled: true
  source: ros2_topic
  protocol: sensor_msgs_battery_state
  can_interface: can0
  timeout: 3s
  query_interval: 2s
  ros_topic: /battery/state
  ros_message_type: sensor_msgs/msg/BatteryState
`)
	if err := os.WriteFile(filepath.Join(profileDir, "custom_v1.yml"), profile, 0o640); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(root, "config.yml")
	data := []byte(`agent:
  uuid: 7fd34256-bf3a-4cf6-8da0-fbce40f34d11
  robot_code: TEST
  robot_model: custom_v1
  profile_dir: ` + profileDir + `
  dashboard_url: http://dashboard:8080
  token: long-enough-agent-token
`)
	if err := os.WriteFile(path, data, 0o600); err != nil {
		t.Fatal(err)
	}
	cfg, err := Load(path)
	if err != nil {
		t.Fatal(err)
	}
	if cfg.Motor.Source != "can_query" || cfg.Motor.CANInterface != "can2" || cfg.Motor.CANQueries[0].RequestID != 0x101 {
		t.Fatalf("external motor transport was not loaded: %+v", cfg.Motor)
	}
	if cfg.BMS.Source != "ros2_topic" || cfg.BMS.ROSTopic != "/battery/state" {
		t.Fatalf("external BMS transport was not loaded: %+v", cfg.BMS)
	}
}
