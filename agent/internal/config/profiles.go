package config

import (
	"fmt"
	"time"
)

type robotProfile struct {
	Motor MotorConfig
	BMS   BMSConfig
}

var robotProfiles = map[string]robotProfile{
	"2m_v0.1.2": build2MProfile(),
}

func applyRobotProfile(cfg *Config, name string) error {
	profile, ok := robotProfiles[name]
	if !ok {
		return fmt.Errorf("unknown agent.robot_model %q", name)
	}
	cfg.Motor = profile.Motor
	cfg.BMS = profile.BMS
	return nil
}

func build2MProfile() robotProfile {
	labels := map[string]string{
		"motor_id_01": "waist_1", "motor_id_02": "waist_2_virtual", "motor_id_03": "waist_3_virtual",
		"motor_id_04": "left_arm_1", "motor_id_05": "left_arm_2", "motor_id_06": "left_arm_3",
		"motor_id_07": "left_arm_4", "motor_id_08": "left_arm_5", "motor_id_09": "left_arm_6", "motor_id_10": "left_arm_7",
		"motor_id_11": "right_arm_1", "motor_id_12": "right_arm_2", "motor_id_13": "right_arm_3",
		"motor_id_14": "right_arm_4", "motor_id_15": "right_arm_5", "motor_id_16": "right_arm_6", "motor_id_17": "right_arm_7",
		"motor_id_18": "head_1", "motor_id_19": "head_2", "motor_id_20": "head_3",
		"motor_id_21": "left_leg_1", "motor_id_22": "left_leg_2", "motor_id_23": "left_leg_3", "motor_id_24": "left_leg_4",
		"motor_id_25": "left_ankle_virtual_1", "motor_id_26": "left_ankle_virtual_2",
		"motor_id_27": "right_leg_1", "motor_id_28": "right_leg_2", "motor_id_29": "right_leg_3", "motor_id_30": "right_leg_4",
		"motor_id_31": "right_ankle_virtual_1", "motor_id_32": "right_ankle_virtual_2",
	}
	definitions := make(map[string]MotorDefinition, 32)
	add := func(id int, brand, motorModel, canInterface, mode string, virtual bool) {
		key := fmt.Sprintf("motor_id_%02d", id)
		definitions[key] = MotorDefinition{
			Brand: brand, Model: motorModel, CANInterface: canInterface,
			ControlMode: mode, VirtualJoint: virtual,
		}
	}
	for id := 1; id <= 20; id++ {
		canInterface := "can0"
		if id >= 4 && id <= 10 {
			canInterface = "can1"
		} else if id >= 11 && id <= 17 {
			canInterface = "can2"
		}
		mode := "CSP"
		if id <= 3 {
			mode = "PT"
		}
		add(id, "drive-unit", fmt.Sprintf("model-%02d", id), canInterface, mode, id == 2 || id == 3)
	}
	for id := 21; id <= 32; id++ {
		canInterface := "can3"
		if id >= 27 {
			canInterface = "can4"
		}
		virtual := id == 25 || id == 26 || id == 31 || id == 32
		add(id, "drive-unit", fmt.Sprintf("model-%02d", id), canInterface, "PT", virtual)
	}
	return robotProfile{
		Motor: MotorConfig{
			Enabled: true, Topic: "/motor/joint_states", MessageType: "sensor_msgs/msg/JointState",
			ROSSetup:    []string{"/opt/ros/humble/setup.bash", "/opt/xuanjian/agent/ros/setup.bash"},
			ReadTimeout: Duration(3 * time.Second), JointLabels: labels, Definitions: definitions,
		},
		BMS: BMSConfig{
			Enabled: true, Protocol: "yy-bcu14h-mos-24s100a", CANInterface: "can5",
			Timeout: Duration(5 * time.Second), QueryInterval: Duration(2 * time.Second),
			ROSTopic:        "/bms_can/battery_data",
			ROSSetup:        []string{"/opt/ros/humble/setup.bash", "/opt/xuanjian/agent/ros/setup.bash"},
			PublishInterval: Duration(2 * time.Second), PublishTimeout: Duration(4 * time.Second),
			Specification: BatterySpecification{PackModel: "YY-BCU14H-MOS-24S100A", SeriesCells: 24},
		},
	}
}
