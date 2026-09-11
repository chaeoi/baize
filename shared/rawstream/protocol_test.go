package rawstream

import (
	"bytes"
	"encoding/binary"
	"testing"
	"time"

	"baize/shared/model"
)

func TestRoundTrip(t *testing.T) {
	at := time.Unix(1700000000, 123).UTC()
	want := Batch{RobotUUID: "52446a60-7483-4ba7-b8c7-b85f60b2a00f", RobotCode: "M99", Sequence: 7, Records: []Record{{Kind: KindROS, Topic: "/motor/joint_states", MessageType: "sensor_msgs/msg/JointState", Serialization: "cdr", SourceTimestamp: at, ReceiveTimestamp: at.Add(time.Millisecond), Payload: []byte{1, 2, 3}}, {Kind: KindHost, Topic: "baize/host", MessageType: "baize/HostMetrics", Serialization: "json", SourceTimestamp: at, ReceiveTimestamp: at, Payload: []byte(`{"system":{}}`)}}}
	data, err := Encode(want)
	if err != nil {
		t.Fatal(err)
	}
	got, err := Decode(data)
	if err != nil {
		t.Fatal(err)
	}
	if got.RobotUUID != want.RobotUUID || got.RobotCode != want.RobotCode || got.Sequence != want.Sequence || len(got.Records) != len(want.Records) {
		t.Fatalf("header mismatch: %+v", got)
	}
	for i := range want.Records {
		if got.Records[i].Kind != want.Records[i].Kind || got.Records[i].Topic != want.Records[i].Topic || !bytes.Equal(got.Records[i].Payload, want.Records[i].Payload) {
			t.Fatalf("record mismatch: got=%+v want=%+v", got.Records[i], want.Records[i])
		}
		if !got.Records[i].SourceTimestamp.Equal(want.Records[i].SourceTimestamp) || !got.Records[i].ReceiveTimestamp.Equal(want.Records[i].ReceiveTimestamp) {
			t.Fatal("lost nanosecond timestamps")
		}
	}
}

func TestHostAndDamagedBatches(t *testing.T) {
	at := time.Now().UTC()
	wantError := model.ComponentError{Component: "gpu", Message: "GPU query failed", At: at}
	record, err := NewHostRecord(at, &model.SystemMetrics{CPUCores: 8, CPUUsagePercent: 37}, nil, wantError)
	if err != nil {
		t.Fatal(err)
	}
	host, err := DecodeHostPayload(record.Payload)
	if err != nil || host.System.CPUCores != 8 || len(host.Errors) != 1 || host.Errors[0] != wantError {
		t.Fatalf("lost host values or errors: %+v %v", host, err)
	}
	data, err := Encode(Batch{RobotUUID: "test-uuid", RobotCode: "test", Records: []Record{record}})
	if err != nil {
		t.Fatal(err)
	}
	for end := 0; end < len(data); end++ {
		if _, err := Decode(data[:end]); err == nil {
			t.Fatalf("accepted truncated batch at %d", end)
		}
	}
	for _, expectedSize := range []uint32{0, 1, 64<<20 + 1} {
		invalid := bytes.Clone(data)
		binary.LittleEndian.PutUint32(invalid[5:9], expectedSize)
		if _, err := Decode(invalid); err == nil {
			t.Fatalf("accepted impossible declared size %d", expectedSize)
		}
	}
	data[len(data)-1] ^= 0xff
	if _, err := Decode(data); err == nil {
		t.Fatal("accepted corrupt compressed batch")
	}
}
