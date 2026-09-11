package collector

import (
	"bytes"
	"encoding/binary"
	"testing"
	"time"
)

func TestRawFramePreservesPayloadAndRejectsTruncation(t *testing.T) {
	at := time.Unix(1700000000, 123456789).UTC()
	frame := make([]byte, rawFrameHeaderSize)
	copy(frame, "BZR1\x01")
	binary.LittleEndian.PutUint64(frame[5:13], uint64(at.UnixNano()))
	payload := []byte{0, 1, 0, 0, 255, 12, 0, 128}
	binary.LittleEndian.PutUint32(frame[13:17], uint32(len(payload)))
	frame = append(frame, payload...)
	record, err := readRawFrame(bytes.NewReader(frame))
	if err != nil || !bytes.Equal(record.Payload, payload) || !record.SourceTimestamp.Equal(at) {
		t.Fatalf("raw frame changed: %+v %v", record, err)
	}
	for end := 0; end < len(frame); end++ {
		if _, err := readRawFrame(bytes.NewReader(frame[:end])); err == nil {
			t.Fatalf("accepted truncated frame at %d", end)
		}
	}
	binary.LittleEndian.PutUint32(frame[13:17], 64<<20+1)
	if _, err := readRawFrame(bytes.NewReader(frame)); err == nil {
		t.Fatal("accepted oversized frame")
	}
}
