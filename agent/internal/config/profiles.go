package config

import "baize/shared/robotmodel"

type robotProfile struct {
	Motor MotorConfig
	BMS   BMSConfig
}

func SupportedRobotModels() []string {
	models, err := robotmodel.Names()
	if err != nil {
		return nil
	}
	return models
}

func profileForModel(name string) (robotProfile, error) {
	profile, err := robotmodel.Select(name)
	if err != nil {
		return robotProfile{}, err
	}
	motor := MotorConfig{
		Enabled:           profile.Motor.Enabled,
		Topic:             profile.Motor.Topic,
		MessageType:       profile.Motor.MessageType,
		ROSSetup:          append([]string(nil), profile.Motor.ROSSetup...),
		ROSEnvironment:    cloneEnvironment(profile.Motor.ROSEnvironment),
		ROSUser:           profile.Motor.ROSUser,
		ReadTimeout:       Duration(profile.Motor.ReadTimeout.Value()),
		FastSampleRateHz:  profile.Motor.FastSampleRateHz,
		FastBufferSeconds: profile.Motor.FastBufferSeconds,
		FastBatchInterval: Duration(profile.Motor.FastBatchInterval.Value()),
		JointLabels:       make(map[string]string, len(profile.Motor.Joints)),
		Definitions:       make(map[string]MotorDefinition, len(profile.Motor.Joints)),
	}
	for id, joint := range profile.Motor.Joints {
		motor.JointLabels[id] = joint.Label
		motor.Definitions[id] = MotorDefinition{
			Brand:        joint.Brand,
			Model:        joint.Model,
			CANInterface: joint.CANInterface,
			ControlMode:  joint.ControlMode,
			VirtualJoint: joint.VirtualJoint,
		}
	}
	bms := BMSConfig{
		Enabled:        profile.BMS.Enabled,
		Protocol:       profile.BMS.Protocol,
		ROSTopic:       profile.BMS.Topic,
		ROSMessageType: profile.BMS.MessageType,
		ROSSetup:       append([]string(nil), profile.BMS.ROSSetup...),
		ROSEnvironment: cloneEnvironment(profile.BMS.ROSEnvironment),
		ROSUser:        profile.BMS.ROSUser,
		ReadTimeout:    Duration(profile.BMS.ReadTimeout.Value()),
	}
	return robotProfile{Motor: motor, BMS: bms}, nil
}

func cloneEnvironment(environment map[string]string) map[string]string {
	clone := make(map[string]string, len(environment))
	for name, value := range environment {
		clone[name] = value
	}
	return clone
}
