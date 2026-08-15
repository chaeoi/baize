package config

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"time"

	"gopkg.in/yaml.v3"
)

type robotProfile struct {
	Motor MotorConfig `yaml:"motor"`
	BMS   BMSConfig   `yaml:"bms"`
}

var robotProfiles = map[string]robotProfile{
	"2m_v0.1.2": build2MProfile(),
}

var profileNamePattern = regexp.MustCompile(`^[A-Za-z0-9._-]{1,64}$`)

func loadRobotProfile(configPath, name, configuredDir string) (robotProfile, error) {
	if !profileNamePattern.MatchString(name) {
		return robotProfile{}, fmt.Errorf("invalid agent.robot_model %q", name)
	}
	base, builtIn := robotProfiles[name]
	directories := []string{}
	if env := strings.TrimSpace(os.Getenv("BAIZE_PROFILE_DIR")); env != "" {
		directories = append(directories, env)
	}
	if configuredDir != "" {
		if !filepath.IsAbs(configuredDir) {
			return robotProfile{}, errors.New("agent.profile_dir must be absolute")
		}
		directories = append(directories, configuredDir)
	} else {
		directories = append(directories, filepath.Join(filepath.Dir(configPath), "profiles"), "/opt/baize/agent/profiles", "agent/profiles", "profiles")
	}
	for _, directory := range directories {
		path := filepath.Join(directory, name+".yml")
		data, err := os.ReadFile(path)
		if errors.Is(err, os.ErrNotExist) {
			continue
		}
		if err != nil {
			return robotProfile{}, fmt.Errorf("read robot profile %s: %w", path, err)
		}
		profile := base
		decoder := yaml.NewDecoder(strings.NewReader(string(data)))
		decoder.KnownFields(true)
		if err := decoder.Decode(&profile); err != nil {
			return robotProfile{}, fmt.Errorf("parse robot profile %s: %w", path, err)
		}
		return profile, nil
	}
	if !builtIn {
		return robotProfile{}, fmt.Errorf("robot profile %q was not found", name)
	}
	return base, nil
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
			Enabled: true, Source: "ros2_topic", Topic: "/motor/joint_states", MessageType: "sensor_msgs/msg/JointState",
			ROSSetup:    []string{"/opt/ros/humble/setup.bash", "/opt/baize/agent/ros/setup.bash"},
			ReadTimeout: Duration(3 * time.Second), JointLabels: labels, Definitions: definitions,
		},
		BMS: BMSConfig{
			Enabled: true, Source: "can_query", Protocol: "yy-bcu14h-mos-24s100a", CANInterface: "can5",
			Timeout: Duration(5 * time.Second), QueryInterval: Duration(2 * time.Second),
			ROSTopic:        "/bms_can/battery_data",
			ROSMessageType:  "sensor_msgs/msg/BatteryState",
			ROSSetup:        []string{"/opt/ros/humble/setup.bash", "/opt/baize/agent/ros/setup.bash"},
			PublishInterval: Duration(2 * time.Second), PublishTimeout: Duration(4 * time.Second),
			Specification: BatterySpecification{PackModel: "YY-BCU14H-MOS-24S100A", SeriesCells: 24},
			CANQueries:    defaultBMSQueries(),
		},
	}
}

func defaultBMSQueries() []CANQuery {
	return []CANQuery{
		{Name: "pack", RequestID: 0x0400ff80, ResponseID: 0x04028001, Fields: []CANField{{Name: "voltage", Offset: 0, Length: 2, Encoding: "uint", Endian: "big", Scale: .1}, {Name: "current", Offset: 2, Length: 2, Encoding: "uint", Endian: "big", Scale: .1, Bias: -3000}, {Name: "soc_percent", Offset: 4, Length: 2, Encoding: "uint", Endian: "big", Scale: .1}}},
		{Name: "status", RequestID: 0x0400ff80, ResponseID: 0x04078001, Fields: []CANField{{Name: "power_supply_status", Offset: 0, Length: 1, Encoding: "enum"}}},
		{Name: "power", RequestID: 0x0400ff80, ResponseID: 0x04038001, Fields: []CANField{{Name: "power_watts", Offset: 0, Length: 2, Encoding: "int", Endian: "big", Scale: 1}, {Name: "total_energy_wh", Offset: 2, Length: 2, Encoding: "uint", Endian: "big", Scale: 1}, {Name: "mos_celsius", Offset: 4, Length: 1, Encoding: "uint", Scale: 1, Bias: -40}, {Name: "board_celsius", Offset: 5, Length: 1, Encoding: "uint", Scale: 1, Bias: -40}, {Name: "heater_celsius", Offset: 6, Length: 1, Encoding: "uint", Scale: 1, Bias: -40}}},
		{Name: "cell", RequestID: 0x0400ff80, ResponseID: 0x04048001, Fields: []CANField{{Name: "max_cell_voltage", Offset: 0, Length: 2, Encoding: "uint", Endian: "big", Scale: .001}, {Name: "min_cell_voltage", Offset: 3, Length: 2, Encoding: "uint", Endian: "big", Scale: .001}, {Name: "cell_voltage_delta", Offset: 6, Length: 2, Encoding: "uint", Endian: "big", Scale: .001}}},
		{Name: "temperature", RequestID: 0x0400ff80, ResponseID: 0x04058001, Fields: []CANField{{Name: "max_cell_temperature", Offset: 0, Length: 1, Encoding: "uint", Scale: 1, Bias: -40}, {Name: "min_cell_temperature", Offset: 2, Length: 1, Encoding: "uint", Scale: 1, Bias: -40}, {Name: "cell_temperature_delta", Offset: 4, Length: 1, Encoding: "uint", Scale: 1}}},
		{Name: "pack_info", RequestID: 0x0400ff80, ResponseID: 0x04088001, Fields: []CANField{{Name: "cell_count", Offset: 0, Length: 1, Encoding: "uint", Scale: 1}, {Name: "temperature_count", Offset: 1, Length: 1, Encoding: "uint", Scale: 1}, {Name: "remaining_capacity_ah", Offset: 2, Length: 4, Encoding: "uint", Endian: "big", Scale: .001}, {Name: "cycle_count", Offset: 6, Length: 2, Encoding: "uint", Endian: "big", Scale: 1}}},
		{Name: "fault", RequestID: 0x0400ff80, ResponseID: 0x04098001, Fields: []CANField{{Name: "faults", Offset: 0, Length: 8, Encoding: "bits"}}},
		{Name: "health", RequestID: 0x0400ff80, ResponseID: 0x040d8001, Fields: []CANField{{Name: "soh_percent", Offset: 3, Length: 2, Encoding: "uint", Endian: "big", Scale: 1}}},
	}
}
