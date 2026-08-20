package config

import (
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestBuildUsesBuiltInRobotProfile(t *testing.T) {
	cfg, err := Build(AgentConfig{
		UUID: "7fd34256-bf3a-4cf6-8da0-fbce40f34d11", RobotCode: "TEST",
		RobotModel: "2m_v0.1.2", DashboardURL: "http://dashboard:8080",
		Token: "long-enough-agent-token",
	})
	if err != nil {
		t.Fatal(err)
	}
	if cfg.Motor.Topic != "/motor/q2w_upper_motor_joint_state" || cfg.Motor.ROSUser != "ubuntu" || cfg.Motor.ROSEnvironment["ROS_LOCALHOST_ONLY"] != "1" || cfg.Motor.Definitions["motor_id_22"].Model != "model-22" || cfg.Motor.FastSampleRateHz != 500 || cfg.Motor.FastBufferSeconds != 10 || cfg.Motor.FastBatchInterval.Value() != 2*time.Second {
		t.Fatalf("unexpected built-in motor profile: %+v", cfg.Motor)
	}
	if cfg.BMS.Protocol != "batcan_diagnostic_array" || cfg.BMS.ROSTopic != "/batcan/data" || cfg.BMS.ROSMessageType != "diagnostic_msgs/msg/DiagnosticArray" || cfg.BMS.ReadTimeout.Value() <= 0 {
		t.Fatalf("unexpected built-in BMS profile: %+v", cfg.BMS)
	}
}

func TestBuildRejectsUnknownRobotModel(t *testing.T) {
	_, err := Build(AgentConfig{
		UUID: "7fd34256-bf3a-4cf6-8da0-fbce40f34d11", RobotCode: "TEST",
		RobotModel: "not_a_model", DashboardURL: "http://dashboard:8080",
		Token: "long-enough-agent-token",
	})
	if err == nil {
		t.Fatal("expected unsupported model error")
	}
}

func TestLoadUsesBuiltInProfile(t *testing.T) {
	path := filepath.Join(t.TempDir(), "config.yml")
	data := []byte(`model: 2m_v0.1.2
agent:
  uuid: 7fd34256-bf3a-4cf6-8da0-fbce40f34d11
  robot_code: TEST
  dashboard_url: https://dashboard.example.test
  token: long-enough-agent-token
  report_interval: 3s
system:
  enabled: true
  disk_paths: ["/"]
`)
	if err := os.WriteFile(path, data, 0o600); err != nil {
		t.Fatal(err)
	}
	cfg, err := Load(path)
	if err != nil {
		t.Fatal(err)
	}
	if cfg.Agent.ReportInterval.Value().Seconds() != 3 || cfg.Motor.Topic != "/motor/q2w_upper_motor_joint_state" || cfg.BMS.ROSTopic != "/batcan/data" {
		t.Fatalf("unexpected loaded config: %+v", cfg)
	}
}

func TestLoadRejectsExternalProfileOverrides(t *testing.T) {
	path := filepath.Join(t.TempDir(), "config.yml")
	data := []byte(`model: 2m_v0.1.2
agent:
  uuid: 7fd34256-bf3a-4cf6-8da0-fbce40f34d11
  robot_code: TEST
  dashboard_url: https://dashboard.example.test
  token: long-enough-agent-token
motor:
  topic: /not-allowed
`)
	if err := os.WriteFile(path, data, 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := Load(path); err == nil {
		t.Fatal("expected external motor profile to be rejected")
	}
}
