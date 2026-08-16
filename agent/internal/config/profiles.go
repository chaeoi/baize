package config

import (
	"fmt"
	"regexp"
	"time"
)

type robotProfile struct {
	Motor MotorConfig
	BMS   BMSConfig
}

var robotProfiles = map[string]robotProfile{
	"2m_v0.1.2": build2MProfile(),
}

var profileNamePattern = regexp.MustCompile(`^[A-Za-z0-9._-]{1,64}$`)

func profileForModel(name string) (robotProfile, error) {
	if !profileNamePattern.MatchString(name) {
		return robotProfile{}, fmt.Errorf("invalid agent.robot_model %q", name)
	}
	profile, ok := robotProfiles[name]
	if !ok {
		return robotProfile{}, fmt.Errorf("robot model %q is not supported by this Agent release", name)
	}
	return profile, nil
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
	add := func(id int, canInterface, mode string, virtual bool) {
		key := fmt.Sprintf("motor_id_%02d", id)
		definitions[key] = MotorDefinition{Brand: "drive-unit", Model: fmt.Sprintf("model-%02d", id), CANInterface: canInterface, ControlMode: mode, VirtualJoint: virtual}
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
		add(id, canInterface, mode, id == 2 || id == 3)
	}
	for id := 21; id <= 32; id++ {
		canInterface := "can3"
		if id >= 27 {
			canInterface = "can4"
		}
		add(id, canInterface, "PT", id == 25 || id == 26 || id == 31 || id == 32)
	}
	return robotProfile{
		Motor: MotorConfig{Enabled: true, Topic: "/motor/q2w_upper_motor_joint_state", MessageType: "sensor_msgs/msg/JointState", ROSSetup: []string{"/opt/ros/humble/setup.bash"}, ROSEnvironment: map[string]string{"ROS_LOCALHOST_ONLY": "1"}, ROSUser: "ubuntu", ReadTimeout: Duration(5e9), FastSampleRateHz: 500, FastBufferSeconds: 10, FastBatchInterval: Duration(2 * time.Second), JointLabels: labels, Definitions: definitions},
		BMS:   BMSConfig{Enabled: true, Protocol: "sensor_msgs_battery_state", ROSTopic: "/bms_can/battery_data", ROSMessageType: "sensor_msgs/msg/BatteryState", ROSSetup: []string{"/opt/ros/humble/setup.bash"}, ROSEnvironment: map[string]string{"ROS_LOCALHOST_ONLY": "1"}, ROSUser: "ubuntu", ReadTimeout: Duration(5e9), Specification: BatterySpecification{PackModel: "XINXIANGYANG", SeriesCells: 24}},
	}
}
