package dashboard

import (
	"bytes"
	"encoding/base64"
	"encoding/binary"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"
	"time"

	"baize/shared/model"
	"baize/shared/rawstream"
	"github.com/foxglove/mcap/go/mcap"
)

// Produced by ROS2 Humble rclpy.serialization.serialize_message, including
// nonzero padding bytes. These fixtures are independent of our CDR decoder.
const jointCDR = "AAEAAADxU2UVzVsHBQAAAGJhc2UAAAAAAgAAAAwAAABtb3Rvcl9pZF8wMQAMAAAAbW90b3JfaWRfMDIAAgAAAOg1Q6YAAAAAAAD0PwAAAAAAAATAAgAAACMPAAAAAAAAAADgPwAAAAAAAPg/AgAAAEg1Q6YAAAAAAAAkQAAAAAAAADTA"
const batteryCDR = "AAEAAADxU2UVzVsHBQAAAGJhc2UAAAAAAQAAAAEAAAATAAAAYmF0Y2FuL2piZC9zdW1tYXJ5AAASAAAAQk1TIGRhdGEgcmVjZWl2ZWQAAAAFAAAAYm1zMQDXoIYEAAAACAAAAHZvbHRhZ2UABQAAADM4LjIAAAAACwAAAHBlcmNlbnRhZ2UAAAQAAAAwLjUACAAAAGN1cnJlbnQAAwAAAC0yAIYUAAAAcG93ZXJfc3VwcGx5X3N0YXR1cwACAAAAMgA="
const rawTestUUID = "52446a60-7483-4ba7-b8c7-b85f60b2a00f"

func fixtureCDR(t *testing.T, encoded string) []byte {
	t.Helper()
	data, err := base64.StdEncoding.DecodeString(encoded)
	if err != nil {
		t.Fatal(err)
	}
	return data
}

func TestDecodeROSHumbleCDR(t *testing.T) {
	at := time.Unix(1700000000, 123456789).UTC()
	joint := fixtureCDR(t, jointCDR)
	motors, err := decodeJointState(joint, "/motor", at.Add(time.Second), "2m_v0.1.2")
	if err != nil {
		t.Fatal(err)
	}
	if !motors.SampledAt.Equal(at) || len(motors.Motors) != 2 || motors.Motors[0].PositionRad != 1.25 || motors.Motors[1].VelocityRadPerSec != 1.5 || motors.Motors[1].TorqueNm != -20 {
		t.Fatalf("wrong JointState: %+v", motors)
	}
	battery, err := decodeDiagnosticArrayCDR(fixtureCDR(t, batteryCDR), "/batcan/data", at)
	if err != nil {
		t.Fatal(err)
	}
	if battery.Voltage != 38.2 || battery.SOCPercent != 50 || battery.Current != -2 || battery.PowerSupplyStatus != "discharging" || !battery.Present {
		t.Fatalf("wrong BMS: %+v", battery)
	}
	clear(joint[4:12]) // zero ROS header stamp must fall back to receive time
	motors, err = decodeJointState(joint, "/motor", at, "")
	if err != nil || !motors.SampledAt.Equal(at) {
		t.Fatalf("zero stamp fallback: %+v %v", motors, err)
	}
	for _, encoded := range []string{jointCDR, batteryCDR} {
		data := fixtureCDR(t, encoded)
		for length := 0; length < len(data); length++ {
			if encoded == jointCDR {
				if _, err := decodeJointState(data[:length], "/motor", at, ""); err == nil {
					t.Fatalf("accepted truncated joint at %d", length)
				}
			} else if _, err := decodeDiagnosticArrayCDR(data[:length], "/bms", at); err == nil {
				t.Fatalf("accepted truncated battery at %d", length)
			}
		}
	}
}

func TestRawIngestDrawsWithoutRecordingAndDoesNotRepeatSamples(t *testing.T) {
	store := newTestStore(t)
	server := NewServer(ServerConfig{AgentToken: "test-agent-token", RecordingDir: t.TempDir()}, store)
	t.Cleanup(func() { server.Close() })
	at := time.Now().UTC()
	host, err := rawstream.NewHostRecord(at, &model.SystemMetrics{CPUUsagePercent: 37, CPUCores: 8}, nil)
	if err != nil {
		t.Fatal(err)
	}
	joint := fixtureCDR(t, jointCDR)
	binary.LittleEndian.PutUint32(joint[4:8], uint32(at.Unix()))
	binary.LittleEndian.PutUint32(joint[8:12], uint32(at.Nanosecond()))
	batch := rawstream.Batch{RobotUUID: rawTestUUID, RobotCode: "M99", RobotModel: "2m_v0.1.2", AgentVersion: "test", Sequence: 1,
		Records: []rawstream.Record{host, {Kind: rawstream.KindROS, Topic: "/motor", MessageType: "sensor_msgs/msg/JointState", Serialization: "cdr", SourceTimestamp: at, ReceiveTimestamp: at, Payload: joint}}}
	post := func(batch rawstream.Batch) {
		t.Helper()
		data, err := rawstream.Encode(batch)
		if err != nil {
			t.Fatal(err)
		}
		request := httptest.NewRequest(http.MethodPost, "/api/v1/raw", bytes.NewReader(data))
		request.Header.Set("Authorization", "Bearer test-agent-token")
		response := httptest.NewRecorder()
		server.ServeHTTP(response, request)
		if response.Code != 200 {
			t.Fatalf("raw ingest: %d %s", response.Code, response.Body.String())
		}
	}
	post(batch)
	record, ok := store.Robot(rawTestUUID)
	if !ok || record.Telemetry.System.CPUUsagePercent != 37 || len(record.Telemetry.Motors.Samples) != 1 {
		t.Fatalf("live state: %+v", record)
	}
	var event robotStreamEvent
	err = json.Unmarshal(server.publicRobotEventForOptions(record, &publicStreamOptions{includeSamples: true, robotID: publicRobotID("", rawTestUUID)}), &event)
	if err != nil || len(event.Robot.MotorSamples) != 1 {
		t.Fatalf("live event: %+v %v", event, err)
	}
	post(batch) // acknowledgement lost: retry must not process the batch twice
	batch.Sequence = 2
	host.ReceiveTimestamp, host.SourceTimestamp = at.Add(time.Second), at.Add(time.Second)
	batch.Records = []rawstream.Record{host}
	post(batch)
	record, _ = store.Robot(rawTestUUID)
	if len(record.Telemetry.Motors.Samples) != 0 || record.Telemetry.Motors.Motors[0].TorqueNm != 10 {
		t.Fatalf("stale samples replayed: %+v", record.Telemetry.Motors)
	}
}

func TestMCAPPreservesEveryMessageAndDelayedStop(t *testing.T) {
	dir := t.TempDir()
	manager, err := newRecordingManager(dir)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { manager.Close() })
	if err := manager.Start(rawTestUUID); err != nil {
		t.Fatal(err)
	}
	session := manager.active[rawTestUUID]
	at := time.Unix(1700000000, 0).UTC()
	session.startedAt = at
	session.stoppedAt = at.Add(time.Second)
	battery := fixtureCDR(t, batteryCDR)
	host, err := rawstream.NewHostRecord(at.Add(100*time.Millisecond), &model.SystemMetrics{CPUCores: 8, CPUUsagePercent: 37}, nil)
	if err != nil {
		t.Fatal(err)
	}
	batch := rawstream.Batch{RobotUUID: rawTestUUID, RobotCode: "M99", Sequence: 1, Records: []rawstream.Record{host}}
	for index := 0; index < 500; index++ {
		stamp := at.Add(time.Duration(index) * time.Millisecond)
		batch.Records = append(batch.Records, rawstream.Record{Kind: rawstream.KindROS, Topic: "/batcan/data", MessageType: "diagnostic_msgs/msg/DiagnosticArray", Serialization: "cdr", SourceTimestamp: stamp.Add(-time.Millisecond), ReceiveTimestamp: stamp, Payload: battery})
	}
	if err := manager.Ingest(batch); err != nil {
		t.Fatal(err)
	}
	if !manager.Status(rawTestUUID).Stopping {
		t.Fatal("closed before buffered data finished")
	}
	// This batch arrives after the stop request, but includes two messages
	// generated inside the recording interval and one outside it.
	batch.Records = []rawstream.Record{batch.Records[1], batch.Records[1], batch.Records[1]}
	batch.Records[0].ReceiveTimestamp = at.Add(600 * time.Millisecond)
	batch.Records[1].ReceiveTimestamp = at.Add(700 * time.Millisecond)
	batch.Records[2].ReceiveTimestamp = at.Add(2 * time.Second)
	if err := manager.Ingest(batch); err != nil {
		t.Fatal(err)
	}
	path, ok := manager.Latest(rawTestUUID)
	if !ok {
		t.Fatal("recording not ready")
	}
	file, err := os.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer file.Close()
	reader, err := mcap.NewReader(file)
	if err != nil {
		t.Fatal(err)
	}
	defer reader.Close()
	iterator, err := reader.Messages()
	if err != nil {
		t.Fatal(err)
	}
	count, batteries := 0, 0
	sequences := make(map[uint16]uint32)
	for {
		schema, channel, message, err := iterator.Next(nil)
		if err == io.EOF {
			break
		}
		if err != nil {
			t.Fatal(err)
		}
		count++
		if message.Sequence != sequences[channel.ID] {
			t.Fatal("MCAP message sequence must be continuous within each channel")
		}
		sequences[channel.ID]++
		if channel.Topic == "/batcan/data" {
			batteries++
			if !bytes.Equal(message.Data, battery) || schema.Encoding != "ros2msg" || !strings.Contains(string(schema.Data), "MSG: diagnostic_msgs/KeyValue") {
				t.Fatal("raw bytes or complete schema lost")
			}
		} else if !json.Valid(message.Data) || schema.Encoding != "jsonschema" {
			t.Fatal("host encoding invalid")
		}
	}
	if count != 503 || batteries != 502 {
		t.Fatalf("recorded %d total, %d battery messages", count, batteries)
	}
	reopened, err := newRecordingManager(dir)
	if err != nil {
		t.Fatal(err)
	}
	if _, ok := reopened.Latest(rawTestUUID); !ok {
		t.Fatal("completed recording not found after restart")
	}
}

func TestRecordingJournalRecoversAcknowledgedBatchAfterRestart(t *testing.T) {
	dir := t.TempDir()
	manager, err := newRecordingManager(dir)
	if err != nil {
		t.Fatal(err)
	}
	if err := manager.Start(rawTestUUID); err != nil {
		t.Fatal(err)
	}
	at := time.Now().UTC()
	host, err := rawstream.NewHostRecord(at, &model.SystemMetrics{CPUUsagePercent: 42}, nil)
	if err != nil {
		t.Fatal(err)
	}
	batch := rawstream.Batch{RobotUUID: rawTestUUID, RobotCode: "M99", RobotModel: "2m_v0.1.2", Sequence: 1, Records: []rawstream.Record{host}}
	encoded, err := rawstream.Encode(batch)
	if err != nil {
		t.Fatal(err)
	}
	if err := manager.IngestEncoded(batch, encoded); err != nil {
		t.Fatal(err)
	}
	// Simulate an abrupt process exit: the MCAP writer is not closed, while
	// the acknowledged journal frame is already durable.
	session := manager.active[rawTestUUID]
	_ = session.file.Close()
	_ = session.journal.Close()
	restarted, err := newRecordingManager(dir)
	if err != nil {
		t.Fatal(err)
	}
	path, ok := restarted.Latest(rawTestUUID)
	if !ok {
		t.Fatal("journal was not recovered")
	}
	file, err := os.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer file.Close()
	reader, err := mcap.NewReader(file)
	if err != nil {
		t.Fatal(err)
	}
	defer reader.Close()
	messages, err := reader.Messages()
	if err != nil {
		t.Fatal(err)
	}
	if _, _, _, err := messages.Next(nil); err != nil {
		t.Fatalf("recovered MCAP has no message: %v", err)
	}
}

func TestRawHealthUsesReceiptClockAndHostSnapshotReplacesPrevious(t *testing.T) {
	at := time.Now().UTC()
	host, err := rawstream.NewHostRecord(at, nil, []model.GPUMetrics{{UtilizationPercent: 12}}, model.ComponentError{Component: "gpu", Message: "query failed", At: at})
	if err != nil {
		t.Fatal(err)
	}
	status := rawRobotState{}
	batch := rawstream.Batch{RobotUUID: rawTestUUID, RobotCode: "M99", RobotModel: "2m_v0.1.2", Records: []rawstream.Record{
		host,
		{Kind: rawstream.KindROS, Topic: "/motor", MessageType: "sensor_msgs/msg/JointState", SourceTimestamp: at, ReceiveTimestamp: at, Payload: fixtureCDR(t, jointCDR)},
		{Kind: rawstream.KindROS, Topic: "/batcan/data", MessageType: "diagnostic_msgs/msg/DiagnosticArray", SourceTimestamp: at, ReceiveTimestamp: at, Payload: fixtureCDR(t, batteryCDR)},
	}}
	previous, err := decodeRawBatch(batch, &status)
	if err != nil || len(previous.Errors) != 1 || previous.Errors[0].Message != "query failed" {
		t.Fatalf("lost host diagnostics: %+v %v", previous.Errors, err)
	}
	// The fixtures have a years-old ROS clock. Recent receipt still means online.
	for _, delay := range []time.Duration{time.Second, 6 * time.Second} {
		host, err = rawstream.NewHostRecord(at.Add(delay), nil, nil)
		if err != nil {
			t.Fatal(err)
		}
		batch.Records = []rawstream.Record{host}
		current, err := decodeRawBatch(batch, &status)
		if err != nil {
			t.Fatal(err)
		}
		current = mergeRawTelemetry(previous, current, status)
		wantOnline := delay == time.Second
		if current.Motors.TopicOnline != wantOnline || current.BMS.Online != wantOnline {
			t.Fatalf("wrong topic health at %s: motors=%v battery=%v", delay, current.Motors.TopicOnline, current.BMS.Online)
		}
		if len(current.GPUs) != 0 || len(current.Errors) != 0 || len(current.Motors.Samples) != 0 {
			t.Fatal("empty host snapshot failed to clear old GPUs/errors, or motor samples were replayed")
		}
	}
}
