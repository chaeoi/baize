//go:build linux

package collector

import (
	"context"
	"encoding/binary"
	"fmt"
	"syscall"
	"time"

	"baize/agent/internal/config"
	"baize/shared/model"
)

func collectMotorCAN(ctx context.Context, cfg config.MotorConfig) (model.MotorSnapshot, error) {
	snapshot := model.MotorSnapshot{Enabled: true, Source: "can_query", TopicOnline: false, PerMotorOnlineSupported: true, TemperatureSupported: false}
	fd, err := openCAN(cfg.CANInterface)
	if err != nil {
		return snapshot, err
	}
	defer syscall.Close(fd)
	deadline := time.Now().Add(cfg.ReadTimeout.Value())
	results := make(map[string]model.MotorState)
	seen := make(map[int]struct{})
	for _, query := range cfg.CANQueries {
		if err := sendMotorQuery(fd, query); err != nil {
			return snapshot, err
		}
	}
	buffer := make([]byte, 16)
	for time.Now().Before(deadline) && len(seen) < len(cfg.CANQueries) {
		select {
		case <-ctx.Done():
			return snapshot, ctx.Err()
		default:
		}
		ready, err := canReadable(fd, 100*time.Millisecond)
		if err != nil {
			return snapshot, err
		}
		if !ready {
			continue
		}
		n, err := syscall.Read(fd, buffer)
		if err != nil {
			return snapshot, err
		}
		if n != 16 {
			continue
		}
		id := binary.LittleEndian.Uint32(buffer[0:4]) & canEffMask
		length := int(buffer[4])
		if length > 8 {
			length = 8
		}
		data := buffer[8 : 8+length]
		for queryIndex, query := range cfg.CANQueries {
			if id&0x00ffffff != query.ResponseID&0x00ffffff {
				continue
			}
			motorID := query.MotorID
			if motorID == "" {
				motorID = query.Name
			}
			motor := results[motorID]
			motor.ID = motorID
			motor.Label = cfg.JointLabels[motorID]
			if definition, ok := cfg.Definitions[motorID]; ok {
				motor.Brand, motor.Model, motor.CANInterface, motor.ControlMode, motor.VirtualJoint = definition.Brand, definition.Model, definition.CANInterface, definition.ControlMode, definition.VirtualJoint
			}
			for _, field := range query.Fields {
				if field.Offset < 0 || field.Length <= 0 || field.Offset+field.Length > len(data) {
					continue
				}
				raw, ok := decodeCANNumber(data[field.Offset:field.Offset+field.Length], field.Encoding, field.Endian)
				if !ok {
					continue
				}
				scale := field.Scale
				if scale == 0 {
					scale = 1
				}
				value := raw*scale + field.Bias
				switch field.Name {
				case "position_rad":
					motor.PositionRad = value
				case "velocity_rad_per_sec":
					motor.VelocityRadPerSec = value
				case "velocity_rps":
					motor.VelocityRPS = value
				case "torque_nm":
					motor.TorqueNm = value
				}
			}
			results[motorID] = motor
			seen[queryIndex] = struct{}{}
		}
	}
	if len(results) == 0 {
		return snapshot, fmt.Errorf("no motor CAN responses received")
	}
	added := make(map[string]struct{})
	for _, query := range cfg.CANQueries {
		motorID := query.MotorID
		if motorID == "" {
			motorID = query.Name
		}
		if _, ok := added[motorID]; ok {
			continue
		}
		if motor, ok := results[motorID]; ok {
			snapshot.Motors = append(snapshot.Motors, motor)
			added[motorID] = struct{}{}
		}
	}
	snapshot.TopicOnline, snapshot.SampledAt = true, time.Now().UTC()
	return snapshot, nil
}

func sendMotorQuery(fd int, query config.CANQuery) error {
	frame := make([]byte, 16)
	binary.LittleEndian.PutUint32(frame[0:4], query.RequestID|canEffFlag)
	length := len(query.RequestData)
	if length > 8 {
		length = 8
	}
	frame[4] = byte(length)
	copy(frame[8:], query.RequestData[:length])
	if _, err := syscall.Write(fd, frame); err != nil {
		return fmt.Errorf("motor query %s write: %w", query.Name, err)
	}
	return nil
}
