package collector

import (
	"context"
	"encoding/binary"
	"errors"
	"fmt"
	"math"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"baize/agent/internal/config"
	"baize/shared/model"
)

func TestMotorStreamExpiresAndReportsSubscriberFailure(t *testing.T) {
	c := NewMotorCollector(config.MotorConfig{FastSampleRateHz: 500, FastBufferSeconds: 15, ReadTimeout: config.Duration(3 * time.Second)})
	c.streamOnce.Do(func() {})
	if err := c.consumeMotorValues([]string{"hip"}, []float64{1}, []float64{2}, []float64{3}, time.Now().Add(-time.Hour)); err != nil {
		t.Fatal(err)
	}
	// Fresh receipt must work even when a publisher uses a different clock.
	if snapshot, err := c.Collect(t.Context()); err != nil || !snapshot.TopicOnline {
		t.Fatalf("fresh receipt rejected: %+v %v", snapshot, err)
	}
	c.lastReceived = time.Now().Add(-time.Minute)
	if snapshot, err := c.Collect(t.Context()); err == nil || snapshot.TopicOnline {
		t.Fatalf("stale stream considered online: %+v %v", snapshot, err)
	}
	c.lastReceived = time.Now()
	c.streamErr = errors.New("subscriber exited")
	if snapshot, err := c.Collect(t.Context()); err == nil || snapshot.TopicOnline {
		t.Fatalf("failed stream considered online: %+v %v", snapshot, err)
	}
	if err := c.consumeMotorValues([]string{"hip"}, []float64{4}, []float64{5}, []float64{6}, time.Now()); err != nil {
		t.Fatal(err)
	}
	if snapshot, err := c.Collect(t.Context()); err != nil || !snapshot.TopicOnline {
		t.Fatalf("stream did not recover: %+v %v", snapshot, err)
	}
}

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

func TestParseJointStateJSON(t *testing.T) {
	motors, sampledAt, err := parseJointStateJSON([]byte(`{"type":"motor","stamp_ns":1700000000000000000,"name":["hip"],"position":[1.5],"velocity":[2.5],"effort":[3.5]}`), map[string]string{"hip": "left_hip"}, nil)
	if err != nil {
		t.Fatal(err)
	}
	if len(motors) != 1 || motors[0].Label != "left_hip" || motors[0].TorqueNm != 3.5 || sampledAt.IsZero() {
		t.Fatalf("unexpected JSON JointState: motors=%+v sampled_at=%v", motors, sampledAt)
	}
}

func TestMotorCollectorReadsSimulatedROS2Topic(t *testing.T) {
	root := t.TempDir()
	binDir := filepath.Join(root, "bin")
	if err := os.MkdirAll(binDir, 0o750); err != nil {
		t.Fatal(err)
	}
	writeFakeMotorSubscriber(t, filepath.Join(binDir, "baize-ros2-subscriber"), []motorTestFrame{{position: 1.5, velocity: 2.5, effort: 3.5}})
	t.Setenv("BAIZE_ROS2_SUBSCRIBER", filepath.Join(binDir, "baize-ros2-subscriber"))
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
	writeFakeMotorSubscriber(t, filepath.Join(binDir, "baize-ros2-subscriber"), []motorTestFrame{{position: 1.5, velocity: 2.5, effort: 3.5}, {position: 1.6, velocity: 2.6, effort: 3.6}})
	t.Setenv("BAIZE_ROS2_SUBSCRIBER", filepath.Join(binDir, "baize-ros2-subscriber"))
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
	time.Sleep(300 * time.Millisecond)
	second, err := collector.Collect(ctx)
	if err != nil || len(second.Samples) == 0 || second.Samples[0].Motors[0].TorqueNm != 3.6 {
		t.Fatalf("unexpected fast samples: %+v err=%v", second.Samples, err)
	}
}

func TestMotorCollectorPendingRingBuffer(t *testing.T) {
	collector := NewMotorCollector(config.MotorConfig{FastSampleRateHz: 2, FastBufferSeconds: 2})
	collector.mu.Lock()
	collector.appendPendingLocked(model.MotorSample{Motors: make([]model.MotorSampleState, 1)})
	for index := 0; index < 5; index++ {
		collector.appendPendingLocked(model.MotorSample{At: time.Unix(int64(index), 0)})
	}
	samples := collector.takePendingSamplesLocked()
	collector.mu.Unlock()
	if len(samples) != 4 || samples[0].At.Unix() != 1 || samples[3].At.Unix() != 4 {
		t.Fatalf("ring buffer did not retain newest samples: %+v", samples)
	}
}

func TestROSCommandExportsEnvironment(t *testing.T) {
	command, err := rosCommand(nil, map[string]string{"ROS_LOCALHOST_ONLY": "1"}, "", "baize-ros2-subscriber --topic '/motor/state'")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(command, "export ROS_LOCALHOST_ONLY='1'") {
		t.Fatalf("ROS environment was not exported: %s", command)
	}
}

func TestROSCommandDropsRootToProfileUser(t *testing.T) {
	command, err := wrapROSCommand("exec baize-ros2-subscriber --topic '/motor/state'", "ubuntu", 0)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(command, "/usr/bin/setpriv --reset-env") || !strings.Contains(command, "--reuid='ubuntu'") {
		t.Fatalf("ROS user transition was not configured: %s", command)
	}
}

type motorTestFrame struct {
	position float64
	velocity float64
	effort   float64
}

func writeFakeMotorSubscriber(t *testing.T, path string, frames []motorTestFrame) {
	t.Helper()
	var script strings.Builder
	script.WriteString("#!/bin/sh\n")
	for index, frame := range frames {
		if index > 0 {
			script.WriteString("sleep 0.2\n")
		}
		payload := motorTestPayload(frame, index == 0)
		script.WriteString("printf '")
		for _, value := range payload {
			script.WriteString(fmt.Sprintf("\\%03o", value))
		}
		script.WriteString("'\n")
	}
	script.WriteString("sleep 1\n")
	if err := os.WriteFile(path, []byte(script.String()), 0o750); err != nil {
		t.Fatal(err)
	}
}

func motorTestPayload(frame motorTestFrame, includeNames bool) []byte {
	payload := []byte{'B', 'Z', 'M', '1', 1, 0}
	if includeNames {
		payload[5] = 1
	}
	stamp := uint64(time.Now().UnixNano())
	var stampBytes [8]byte
	binary.LittleEndian.PutUint64(stampBytes[:], stamp)
	payload = append(payload, stampBytes[:]...)
	payload = append(payload, 1, 0)
	if includeNames {
		payload = append(payload, 3, 0, 'h', 'i', 'p')
	}
	for _, value := range []float64{frame.position, frame.velocity, frame.effort} {
		var raw [8]byte
		binary.LittleEndian.PutUint64(raw[:], math.Float64bits(value))
		payload = append(payload, raw[:]...)
	}
	return payload
}
