package config

import "testing"

func TestBuildUsesBuiltInRobotProfile(t *testing.T) {
	cfg, err := Build(AgentConfig{
		UUID: "7fd34256-bf3a-4cf6-8da0-fbce40f34d11", RobotCode: "TEST",
		RobotModel: "2m_v0.1.2", DashboardURL: "http://dashboard:8080",
		Token: "long-enough-agent-token",
	})
	if err != nil {
		t.Fatal(err)
	}
	if cfg.Motor.Topic != "/motor/q2w_upper_motor_joint_state" || cfg.Motor.ROSUser != "ubuntu" || cfg.Motor.ROSEnvironment["ROS_LOCALHOST_ONLY"] != "1" || cfg.Motor.Definitions["motor_id_22"].Model != "model-22" {
		t.Fatalf("unexpected built-in motor profile: %+v", cfg.Motor)
	}
	if cfg.BMS.Protocol != "sensor_msgs_battery_state" || cfg.BMS.ROSTopic != "/bms_can/battery_data" || cfg.BMS.ReadTimeout.Value() <= 0 {
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
