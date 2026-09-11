package dashboard

import (
	"encoding/binary"
	"errors"
	"fmt"
	"math"
	"strconv"
	"strings"
	"time"

	"baize/shared/model"
	"baize/shared/rawstream"
	"baize/shared/robotmodel"
)

type rawRobotState struct {
	sequence                   uint64
	motorReceived, bmsReceived time.Time
	hostUpdated                bool
}

// ROS receipt times track topic health independently of simulated ROS clocks.
// Source timestamps and payload bytes remain available to the MCAP recorder.
func decodeRawBatch(batch rawstream.Batch, status *rawRobotState) (model.Telemetry, error) {
	if !uuidPattern.MatchString(batch.RobotUUID) || batch.RobotCode == "" {
		return model.Telemetry{}, errors.New("raw batch identity is empty")
	}
	telemetry := model.Telemetry{
		SchemaVersion: model.SchemaVersion,
		Robot:         model.Robot{UUID: batch.RobotUUID, Code: batch.RobotCode, Model: batch.RobotModel, Hostname: batch.Hostname, OS: batch.OS, Arch: batch.Arch},
		AgentVersion:  batch.AgentVersion,
	}
	var latestAt time.Time
	var latestMotors *model.MotorSnapshot
	status.hostUpdated = false
	for index := range batch.Records {
		record := &batch.Records[index]
		at := record.SourceTimestamp
		if at.IsZero() {
			at = record.ReceiveTimestamp
		}
		if record.ReceiveTimestamp.After(latestAt) {
			latestAt = record.ReceiveTimestamp
		}
		switch {
		case record.Kind == rawstream.KindHost:
			host, err := rawstream.DecodeHostPayload(record.Payload)
			if err != nil {
				telemetry.Errors = append(telemetry.Errors, model.ComponentError{Component: "host", Message: err.Error(), At: at})
				continue
			}
			status.hostUpdated = true
			telemetry.System, telemetry.GPUs = host.System, host.GPUs
			telemetry.Errors = append(telemetry.Errors, host.Errors...)
		case record.Kind == rawstream.KindROS && record.MessageType == "sensor_msgs/msg/JointState":
			motors, err := decodeJointState(record.Payload, record.Topic, at, batch.RobotModel)
			if err != nil {
				telemetry.Errors = append(telemetry.Errors, model.ComponentError{Component: "motor", Message: err.Error(), At: at})
				continue
			}
			if latestMotors == nil {
				latestMotors = motors
			} else {
				latestMotors.Motors = motors.Motors
				latestMotors.SampledAt = motors.SampledAt
				latestMotors.TopicOnline = true
				latestMotors.Samples = append(latestMotors.Samples, motors.Samples...)
			}
			record.SourceTimestamp = motors.SampledAt
			status.motorReceived = record.ReceiveTimestamp
		case record.Kind == rawstream.KindROS && record.MessageType == "diagnostic_msgs/msg/DiagnosticArray":
			bms, err := decodeDiagnosticArrayCDR(record.Payload, record.Topic, at)
			if err != nil {
				telemetry.Errors = append(telemetry.Errors, model.ComponentError{Component: "bms", Message: err.Error(), At: at})
				continue
			}
			telemetry.BMS = bms
			record.SourceTimestamp = bms.LastFrameAt
			status.bmsReceived = record.ReceiveTimestamp
		}
	}
	if latestAt.IsZero() {
		latestAt = time.Now().UTC()
	}
	telemetry.Motors = latestMotors
	if latestMotors != nil && len(latestMotors.Samples) > 1 {
		duration := latestMotors.Samples[len(latestMotors.Samples)-1].At.Sub(latestMotors.Samples[0].At).Seconds()
		if duration > 0 {
			latestMotors.SampleRateHz = float64(len(latestMotors.Samples)-1) / duration
		}
	}
	telemetry.CollectedAt = latestAt
	model.SanitizeFinite(&telemetry)
	return telemetry, nil
}

func mergeRawTelemetry(previous, current model.Telemetry, status rawRobotState) model.Telemetry {
	if !status.hostUpdated {
		current.System = previous.System
		current.GPUs = previous.GPUs
		for _, componentError := range previous.Errors {
			if componentError.Component == "system" || componentError.Component == "gpu" {
				current.Errors = append(current.Errors, componentError)
			}
		}
	}
	profile, _ := robotmodel.Select(current.Robot.Model)
	if current.BMS == nil {
		if previous.BMS != nil {
			bms := *previous.BMS
			current.BMS = &bms
		} else if profile.BMS.Enabled {
			current.BMS = &model.BMSMetrics{Enabled: true, Interface: profile.BMS.Topic, Protocol: profile.BMS.Protocol}
		}
		if current.BMS != nil && current.CollectedAt.Sub(status.bmsReceived) > profile.BMS.ReadTimeout.Value() {
			current.BMS.Online = false
			current.BMS.Present = false
		}
	}
	if current.Motors == nil {
		if previous.Motors != nil {
			motors := *previous.Motors
			motors.Samples = nil
			current.Motors = &motors
		} else if profile.Motor.Enabled {
			current.Motors = &model.MotorSnapshot{Enabled: true, Source: "ros2_joint_state", Topic: profile.Motor.Topic}
		}
		if current.Motors != nil && current.CollectedAt.Sub(status.motorReceived) > profile.Motor.ReadTimeout.Value() {
			current.Motors.TopicOnline = false
		}
	}
	return current
}

type cdrReader struct {
	data   []byte
	pos    int
	little bool
}

func newCDRReader(data []byte) (*cdrReader, error) {
	if len(data) < 4 {
		return nil, errors.New("serialized CDR payload is too short")
	}
	identifier := binary.BigEndian.Uint16(data[:2])
	var little bool
	switch identifier {
	case 1:
		little = true
	case 0:
		little = false
	default:
		return nil, fmt.Errorf("unsupported CDR encapsulation %#x", identifier)
	}
	return &cdrReader{data: data, pos: 4, little: little}, nil
}

func (r *cdrReader) align(alignment int) error {
	if alignment <= 1 {
		return nil
	}
	padding := (alignment - ((r.pos - 4) % alignment)) % alignment
	if padding > len(r.data)-r.pos {
		return errors.New("CDR alignment exceeds payload")
	}
	r.pos += padding
	return nil
}

func (r *cdrReader) bytes(size int) ([]byte, error) {
	if size < 0 || size > len(r.data)-r.pos {
		return nil, errors.New("CDR field exceeds payload")
	}
	value := r.data[r.pos : r.pos+size]
	r.pos += size
	return value, nil
}

func (r *cdrReader) uint32() (uint32, error) {
	if err := r.align(4); err != nil {
		return 0, err
	}
	data, err := r.bytes(4)
	if err != nil {
		return 0, err
	}
	if r.little {
		return binary.LittleEndian.Uint32(data), nil
	}
	return binary.BigEndian.Uint32(data), nil
}

func (r *cdrReader) int32() (int32, error) {
	value, err := r.uint32()
	return int32(value), err
}

func (r *cdrReader) uint64() (uint64, error) {
	if err := r.align(8); err != nil {
		return 0, err
	}
	data, err := r.bytes(8)
	if err != nil {
		return 0, err
	}
	if r.little {
		return binary.LittleEndian.Uint64(data), nil
	}
	return binary.BigEndian.Uint64(data), nil
}

func (r *cdrReader) float64() (float64, error) {
	value, err := r.uint64()
	return math.Float64frombits(value), err
}

func (r *cdrReader) string() (string, error) {
	size, err := r.uint32()
	if err != nil {
		return "", err
	}
	if size == 0 || size > 1<<20 {
		return "", errors.New("invalid CDR string size")
	}
	data, err := r.bytes(int(size))
	if err != nil {
		return "", err
	}
	if data[len(data)-1] != 0 {
		return "", errors.New("CDR string is not terminated")
	}
	return string(data[:len(data)-1]), nil
}

func (r *cdrReader) stringSequence() ([]string, error) {
	count, err := r.uint32()
	if err != nil {
		return nil, err
	}
	if count > 4096 {
		return nil, errors.New("CDR string sequence is too large")
	}
	values := make([]string, count)
	for index := range values {
		values[index], err = r.string()
		if err != nil {
			return nil, err
		}
	}
	return values, nil
}

func (r *cdrReader) floatSequence() ([]float64, error) {
	count, err := r.uint32()
	if err != nil {
		return nil, err
	}
	if count > 4096 {
		return nil, errors.New("CDR float sequence is too large")
	}
	values := make([]float64, count)
	for index := range values {
		values[index], err = r.float64()
		if err != nil {
			return nil, err
		}
	}
	return values, nil
}

func (r *cdrReader) header() (time.Time, error) {
	seconds, err := r.int32()
	if err != nil {
		return time.Time{}, err
	}
	nanoseconds, err := r.uint32()
	if err != nil {
		return time.Time{}, err
	}
	if _, err := r.string(); err != nil {
		return time.Time{}, err
	}
	if nanoseconds >= 1_000_000_000 {
		return time.Time{}, errors.New("invalid ROS timestamp")
	}
	if seconds == 0 && nanoseconds == 0 {
		return time.Time{}, nil
	}
	return time.Unix(int64(seconds), int64(nanoseconds)).UTC(), nil
}

func decodeJointState(payload []byte, topic string, at time.Time, robotModel string) (*model.MotorSnapshot, error) {
	r, err := newCDRReader(payload)
	if err != nil {
		return nil, err
	}
	if stamped, err := r.header(); err == nil && !stamped.IsZero() {
		at = stamped
	} else if err != nil {
		return nil, err
	}
	names, err := r.stringSequence()
	if err != nil {
		return nil, err
	}
	positions, err := r.floatSequence()
	if err != nil {
		return nil, err
	}
	velocities, err := r.floatSequence()
	if err != nil {
		return nil, err
	}
	efforts, err := r.floatSequence()
	if err != nil {
		return nil, err
	}
	if len(names) == 0 || len(names) != len(positions) || len(names) != len(velocities) || len(names) != len(efforts) {
		return nil, fmt.Errorf("JointState array size mismatch: name=%d position=%d velocity=%d effort=%d", len(names), len(positions), len(velocities), len(efforts))
	}
	snapshot := &model.MotorSnapshot{Enabled: true, Source: "ros2_joint_state", Topic: topic, TopicOnline: true, SampledAt: at, SampleRateHz: 0, Motors: make([]model.MotorState, len(names)), Samples: []model.MotorSample{{At: at, Motors: make([]model.MotorSampleState, len(names))}}}
	var joints map[string]robotmodel.JointConfig
	if profile, selectErr := robotmodel.Select(robotModel); selectErr == nil {
		joints = profile.Motor.Joints
	}
	for index, name := range names {
		definition := joints[name]
		snapshot.Motors[index] = model.MotorState{ID: name, Label: definition.Label, Brand: definition.Brand, Model: definition.Model, CANInterface: definition.CANInterface, ControlMode: definition.ControlMode, VirtualJoint: definition.VirtualJoint, PositionRad: positions[index], VelocityRadPerSec: velocities[index], TorqueNm: efforts[index]}
		snapshot.Samples[0].Motors[index] = model.MotorSampleState{ID: name, Label: definition.Label, PositionRad: positions[index], VelocityRadPerSec: velocities[index], TorqueNm: efforts[index]}
	}
	return snapshot, nil
}

type cdrDiagnosticStatus struct {
	name, message, hardwareID string
	values                    []cdrDiagnosticValue
}

type cdrDiagnosticValue struct{ key, value string }

func decodeDiagnosticArrayCDR(payload []byte, topic string, at time.Time) (*model.BMSMetrics, error) {
	r, err := newCDRReader(payload)
	if err != nil {
		return nil, err
	}
	if stamped, err := r.header(); err == nil && !stamped.IsZero() {
		at = stamped
	} else if err != nil {
		return nil, err
	}
	count, err := r.uint32()
	if err != nil || count > 4096 {
		return nil, errors.New("invalid DiagnosticArray status count")
	}
	statuses := make([]cdrDiagnosticStatus, count)
	for index := range statuses {
		if _, err := r.bytes(1); err != nil {
			return nil, err
		} // DiagnosticStatus.level
		if statuses[index].name, err = r.string(); err != nil {
			return nil, err
		}
		if statuses[index].message, err = r.string(); err != nil {
			return nil, err
		}
		if statuses[index].hardwareID, err = r.string(); err != nil {
			return nil, err
		}
		valueCount, readErr := r.uint32()
		if readErr != nil || valueCount > 4096 {
			return nil, errors.New("invalid DiagnosticStatus value count")
		}
		statuses[index].values = make([]cdrDiagnosticValue, valueCount)
		for valueIndex := range statuses[index].values {
			if statuses[index].values[valueIndex].key, err = r.string(); err != nil {
				return nil, err
			}
			if statuses[index].values[valueIndex].value, err = r.string(); err != nil {
				return nil, err
			}
		}
	}
	metrics := &model.BMSMetrics{Enabled: true, Protocol: "batcan_diagnostic_array", Interface: topic, Online: true, LastFrameAt: at, Metrics: make(map[string]float64)}
	var temperatures []float64
	for _, status := range statuses {
		values := make(map[string]string, len(status.values))
		for _, item := range status.values {
			values[item.key] = strings.TrimSpace(item.value)
		}
		if strings.HasSuffix(status.name, "/summary") {
			metrics.Present = status.message == "BMS data received"
			metrics.Profile = values["profile"]
			rawReadMetric(values, "voltage", &metrics.Voltage)
			rawReadMetric(values, "current", &metrics.Current)
			rawReadMetric(values, "temperature", &metrics.Temperature)
			if value, ok := numericValue(values["percentage"]); ok {
				metrics.SOCPercent = normalizeSOC(value)
			}
			if value, ok := numericValue(values["power_supply_status"]); ok {
				metrics.PowerSupplyStatus = powerStatus(uint8(value))
			}
		}
		if !strings.HasSuffix(status.name, "/summary") && len(values) > 0 {
			metrics.Present = true
		}
		for key, raw := range values {
			value, ok := numericValue(raw)
			if !ok || math.IsNaN(value) || math.IsInf(value, 0) {
				continue
			}
			metrics.Metrics[strings.TrimPrefix(status.name, "batcan/")+"."+key] = value
			switch {
			case strings.HasPrefix(key, "cell_voltage."):
				setIndexedMetric(&metrics.CellVoltages, key, value)
			case strings.HasPrefix(key, "cell_temperature."):
				setIndexedMetric(&metrics.CellTemperatures, key, value)
				temperatures = append(temperatures, value)
			}
		}
	}
	if metrics.Profile == "" {
		metrics.Profile = metrics.Protocol
	}
	if metrics.PowerWatts == 0 && (metrics.Voltage != 0 || metrics.Current != 0) {
		metrics.PowerWatts = metrics.Voltage * metrics.Current
	}
	if metrics.Temperature == 0 && len(temperatures) > 0 {
		metrics.Temperature = maxFloat(temperatures)
	}
	return metrics, nil
}

func rawReadMetric(values map[string]string, key string, target *float64) {
	if value, ok := numericValue(values[key]); ok {
		*target = value
	}
}

func numericValue(value string) (float64, bool) {
	parsed, err := strconv.ParseFloat(strings.TrimSpace(value), 64)
	return parsed, err == nil
}

func normalizeSOC(value float64) float64 {
	if value >= 0 && value <= 1 {
		return value * 100
	}
	return value
}

func setIndexedMetric(target *[]float64, key string, value float64) {
	parts := strings.Split(key, ".")
	if len(parts) != 2 {
		return
	}
	index, err := strconv.Atoi(parts[1])
	if err != nil || index < 1 || index > 1024 {
		return
	}
	values := *target
	if len(values) < index {
		values = append(values, make([]float64, index-len(values))...)
	}
	values[index-1] = value
	*target = values
}

func maxFloat(values []float64) float64 {
	var maximum float64
	for index, value := range values {
		if index == 0 || value > maximum {
			maximum = value
		}
	}
	return maximum
}

func powerStatus(value uint8) string {
	switch value {
	case 1:
		return "charging"
	case 2:
		return "discharging"
	case 3:
		return "not_charging"
	case 4:
		return "full"
	default:
		return "unknown"
	}
}
