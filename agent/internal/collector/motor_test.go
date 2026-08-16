package collector

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"baize/agent/internal/config"
)

func TestParseJointState(t *testing.T) {
	data := []byte(`header:
  stamp:
    sec: 1
    nanosec: 2
name:
- motor_id_01
- motor_id_02
position:
- 1.25
- -2.5
velocity:
- 3.0
- 4.0
effort:
- 5.0
- 6.0
---
`)
	motors, err := parseJointState(data, map[string]string{"motor_id_01": "waist_1"}, nil)
	if err != nil {
		t.Fatal(err)
	}
	if len(motors) != 2 || motors[0].Label != "waist_1" || motors[1].TorqueNm != 6 {
		t.Fatalf("unexpected motors: %+v", motors)
	}
}

func TestParseJointStateRejectsMismatchedArrays(t *testing.T) {
	_, err := parseJointState([]byte("name: [a, b]\nposition: [1]\nvelocity: [1, 2]\neffort: [1, 2]\n"), nil, nil)
	if err == nil {
		t.Fatal("expected array mismatch error")
	}
}

func TestMotorCollectorReadsSimulatedROS2Topic(t *testing.T) {
	root := t.TempDir()
	binDir := filepath.Join(root, "bin")
	if err := os.MkdirAll(binDir, 0o750); err != nil {
		t.Fatal(err)
	}
	ros2 := []byte("#!/bin/sh\nprintf '%s\\n' 'name: [hip]' 'position: [1.5]' 'velocity: [2.5]' 'effort: [3.5]'\n")
	if err := os.WriteFile(filepath.Join(binDir, "ros2"), ros2, 0o750); err != nil {
		t.Fatal(err)
	}
	setup := filepath.Join(root, "setup.bash")
	if err := os.WriteFile(setup, []byte("export PATH='"+binDir+"':$PATH\n"), 0o640); err != nil {
		t.Fatal(err)
	}
	collector := NewMotorCollector(config.MotorConfig{Enabled: true, Topic: "/motor/joint_states", MessageType: "sensor_msgs/msg/JointState", ROSSetup: []string{setup}, ReadTimeout: config.Duration(2 * time.Second)})
	snapshot, err := collector.Collect(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if !snapshot.TopicOnline || len(snapshot.Motors) != 1 || snapshot.Motors[0].VelocityRadPerSec != 2.5 || snapshot.Motors[0].TorqueNm != 3.5 {
		t.Fatalf("unexpected simulated ROS2 snapshot: %+v", snapshot)
	}
}

func TestMotorCollectorStreamsFastSamples(t *testing.T) {
	root := t.TempDir()
	binDir := filepath.Join(root, "bin")
	if err := os.MkdirAll(binDir, 0o750); err != nil {
		t.Fatal(err)
	}
	ros2 := []byte("#!/bin/sh\nprintf '%s\\n' 'name: [hip]' 'position: [1.5]' 'velocity: [2.5]' 'effort: [3.5]' '---'\nsleep 0.06\nprintf '%s\\n' 'name: [hip]' 'position: [1.6]' 'velocity: [2.6]' 'effort: [3.6]' '---'\nsleep 1\n")
	if err := os.WriteFile(filepath.Join(binDir, "ros2"), ros2, 0o750); err != nil {
		t.Fatal(err)
	}
	setup := filepath.Join(root, "setup.bash")
	if err := os.WriteFile(setup, []byte("export PATH='"+binDir+"':$PATH\n"), 0o640); err != nil {
		t.Fatal(err)
	}
	collector := NewMotorCollector(config.MotorConfig{Enabled: true, Topic: "/motor/joint_states", MessageType: "sensor_msgs/msg/JointState", ROSSetup: []string{setup}, ReadTimeout: config.Duration(time.Second), FastSampleRateHz: 20, FastBufferSeconds: 5})
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	first, err := collector.Collect(ctx)
	if err != nil || !first.TopicOnline || len(first.Motors) != 1 {
		t.Fatalf("unexpected first stream snapshot: %+v err=%v", first, err)
	}
	time.Sleep(100 * time.Millisecond)
	second, err := collector.Collect(ctx)
	if err != nil || len(second.Samples) == 0 || second.Samples[0].Motors[0].TorqueNm != 3.6 {
		t.Fatalf("unexpected fast samples: %+v err=%v", second.Samples, err)
	}
}

func TestROSCommandExportsEnvironment(t *testing.T) {
	command, err := rosCommand(nil, map[string]string{"ROS_LOCALHOST_ONLY": "1"}, "", "ros2 topic echo --once '/motor/state'")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(command, "export ROS_LOCALHOST_ONLY='1'") {
		t.Fatalf("ROS environment was not exported: %s", command)
	}
}

func TestROSCommandDropsRootToProfileUser(t *testing.T) {
	command, err := wrapROSCommand("exec ros2 topic echo --once '/motor/state'", "ubuntu", 0)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(command, "/usr/bin/setpriv --reset-env") || !strings.Contains(command, "--reuid='ubuntu'") {
		t.Fatalf("ROS user transition was not configured: %s", command)
	}
}
