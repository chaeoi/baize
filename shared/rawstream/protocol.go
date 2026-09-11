package rawstream

import (
	"bytes"
	"encoding/binary"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"sync"
	"time"

	"baize/shared/model"
	"github.com/klauspost/compress/zstd"
)

const (
	protocolVersion = 1
	maxBatchBytes   = 64 << 20
	KindROS         = 1
	KindHost        = 2
)

type Record struct {
	Kind             uint8
	Topic            string
	MessageType      string
	Serialization    string
	SourceTimestamp  time.Time
	ReceiveTimestamp time.Time
	Payload          []byte
}

type Batch struct {
	Version      uint8
	RobotUUID    string
	RobotCode    string
	RobotModel   string
	Hostname     string
	OS           string
	Arch         string
	AgentVersion string
	Sequence     uint64
	Records      []Record
}

// HostMetrics uses JSON only at the low-rate host sampling boundary. ROS
// messages always retain their original CDR bytes.
type HostMetrics struct {
	System *model.SystemMetrics   `json:"system,omitempty"`
	GPUs   []model.GPUMetrics     `json:"gpus,omitempty"`
	Errors []model.ComponentError `json:"errors,omitempty"`
}

func NewHostRecord(at time.Time, system *model.SystemMetrics, gpus []model.GPUMetrics, componentErrors ...model.ComponentError) (Record, error) {
	payload, err := json.Marshal(HostMetrics{System: system, GPUs: gpus, Errors: componentErrors})
	if err != nil {
		return Record{}, err
	}
	return Record{Kind: KindHost, Topic: "/baize/host", MessageType: "baize/HostMetrics", Serialization: "json", SourceTimestamp: at, ReceiveTimestamp: at, Payload: payload}, nil
}

func DecodeHostPayload(payload []byte) (HostMetrics, error) {
	var host HostMetrics
	err := json.Unmarshal(payload, &host)
	return host, err
}

var encoderPool = sync.Pool{New: func() any {
	encoder, err := zstd.NewWriter(nil, zstd.WithEncoderLevel(zstd.SpeedFastest), zstd.WithEncoderConcurrency(1))
	if err != nil {
		panic(err)
	}
	return encoder
}}

var decoderPool = sync.Pool{New: func() any {
	decoder, err := zstd.NewReader(nil, zstd.WithDecoderConcurrency(1), zstd.WithDecoderMaxMemory(maxBatchBytes), zstd.WithDecodeAllCapLimit(true))
	if err != nil {
		panic(err)
	}
	return decoder
}}

func Encode(batch Batch) ([]byte, error) {
	if batch.Version == 0 {
		batch.Version = protocolVersion
	}
	if batch.Version != protocolVersion || batch.RobotUUID == "" || batch.RobotCode == "" {
		return nil, errors.New("invalid raw batch identity or version")
	}
	if len(batch.Records) > 100_000 {
		return nil, errors.New("too many raw records")
	}
	var plain bytes.Buffer
	plain.WriteString("BZB1")
	plain.WriteByte(batch.Version)
	if err := writeString(&plain, batch.RobotUUID); err != nil {
		return nil, err
	}
	if err := writeString(&plain, batch.RobotCode); err != nil {
		return nil, err
	}
	for _, value := range []string{batch.RobotModel, batch.Hostname, batch.OS, batch.Arch, batch.AgentVersion} {
		if err := writeString(&plain, value); err != nil {
			return nil, err
		}
	}
	if err := binary.Write(&plain, binary.LittleEndian, batch.Sequence); err != nil {
		return nil, err
	}
	if err := binary.Write(&plain, binary.LittleEndian, uint32(len(batch.Records))); err != nil {
		return nil, err
	}
	for _, record := range batch.Records {
		if err := validateRecord(record); err != nil {
			return nil, err
		}
		plain.WriteByte(record.Kind)
		if err := writeString(&plain, record.Topic); err != nil {
			return nil, err
		}
		if err := writeString(&plain, record.MessageType); err != nil {
			return nil, err
		}
		if err := writeString(&plain, record.Serialization); err != nil {
			return nil, err
		}
		if err := binary.Write(&plain, binary.LittleEndian, record.SourceTimestamp.UnixNano()); err != nil {
			return nil, err
		}
		if err := binary.Write(&plain, binary.LittleEndian, record.ReceiveTimestamp.UnixNano()); err != nil {
			return nil, err
		}
		if uint64(len(record.Payload)) > uint64(maxBatchBytes) {
			return nil, errors.New("raw record payload is too large")
		}
		if err := binary.Write(&plain, binary.LittleEndian, uint32(len(record.Payload))); err != nil {
			return nil, err
		}
		plain.Write(record.Payload)
		if plain.Len() > maxBatchBytes {
			return nil, errors.New("raw batch is too large")
		}
	}
	encoder := encoderPool.Get().(*zstd.Encoder)
	compressed := encoder.EncodeAll(plain.Bytes(), nil)
	encoderPool.Put(encoder)
	var result bytes.Buffer
	result.WriteString("BZP1")
	result.WriteByte(protocolVersion)
	if err := binary.Write(&result, binary.LittleEndian, uint32(plain.Len())); err != nil {
		return nil, err
	}
	result.Write(compressed)
	return result.Bytes(), nil
}

func Decode(data []byte) (Batch, error) {
	if len(data) < 9 || string(data[:4]) != "BZP1" || data[4] != protocolVersion {
		return Batch{}, errors.New("invalid raw batch envelope")
	}
	expected := binary.LittleEndian.Uint32(data[5:9])
	if expected == 0 || expected > maxBatchBytes {
		return Batch{}, errors.New("invalid raw batch size")
	}
	decoder := decoderPool.Get().(*zstd.Decoder)
	plain, err := decoder.DecodeAll(data[9:], make([]byte, 0, int(expected)))
	decoderPool.Put(decoder)
	if err != nil {
		return Batch{}, err
	}
	if uint32(len(plain)) != expected {
		return Batch{}, errors.New("raw batch size mismatch")
	}
	reader := bytes.NewReader(plain)
	magic := make([]byte, 4)
	if _, err := reader.Read(magic); err != nil || string(magic) != "BZB1" {
		return Batch{}, errors.New("invalid raw batch payload")
	}
	version, err := reader.ReadByte()
	if err != nil || version != protocolVersion {
		return Batch{}, errors.New("unsupported raw batch version")
	}
	robotUUID, err := readString(reader)
	if err != nil {
		return Batch{}, err
	}
	robotCode, err := readString(reader)
	if err != nil {
		return Batch{}, err
	}
	if robotUUID == "" || robotCode == "" {
		return Batch{}, errors.New("raw batch identity is empty")
	}
	robotModel, err := readString(reader)
	if err != nil {
		return Batch{}, err
	}
	hostname, err := readString(reader)
	if err != nil {
		return Batch{}, err
	}
	osName, err := readString(reader)
	if err != nil {
		return Batch{}, err
	}
	arch, err := readString(reader)
	if err != nil {
		return Batch{}, err
	}
	agentVersion, err := readString(reader)
	if err != nil {
		return Batch{}, err
	}
	var sequence uint64
	if err := binary.Read(reader, binary.LittleEndian, &sequence); err != nil {
		return Batch{}, err
	}
	var count uint32
	if err := binary.Read(reader, binary.LittleEndian, &count); err != nil || count > 100_000 {
		return Batch{}, errors.New("invalid raw record count")
	}
	batch := Batch{Version: version, RobotUUID: robotUUID, RobotCode: robotCode, RobotModel: robotModel, Hostname: hostname, OS: osName, Arch: arch, AgentVersion: agentVersion, Sequence: sequence, Records: make([]Record, 0, count)}
	for i := uint32(0); i < count; i++ {
		kind, err := reader.ReadByte()
		if err != nil {
			return Batch{}, err
		}
		topic, err := readString(reader)
		if err != nil {
			return Batch{}, err
		}
		messageType, err := readString(reader)
		if err != nil {
			return Batch{}, err
		}
		serialization, err := readString(reader)
		if err != nil {
			return Batch{}, err
		}
		var sourceNS, receiveNS int64
		if err := binary.Read(reader, binary.LittleEndian, &sourceNS); err != nil {
			return Batch{}, err
		}
		if err := binary.Read(reader, binary.LittleEndian, &receiveNS); err != nil {
			return Batch{}, err
		}
		var size uint32
		if err := binary.Read(reader, binary.LittleEndian, &size); err != nil || size > uint32(reader.Len()) {
			return Batch{}, errors.New("invalid raw payload size")
		}
		// Records can share the decompressed buffer; MCAP consumes it before
		// the batch is released, so another copy of every CDR frame is needless.
		payload := plain[len(plain)-reader.Len() : len(plain)-reader.Len()+int(size)]
		if _, err := reader.Seek(int64(size), io.SeekCurrent); err != nil {
			return Batch{}, err
		}
		record := Record{Kind: kind, Topic: topic, MessageType: messageType, Serialization: serialization, SourceTimestamp: time.Unix(0, sourceNS).UTC(), ReceiveTimestamp: time.Unix(0, receiveNS).UTC(), Payload: payload}
		if err := validateRecord(record); err != nil {
			return Batch{}, err
		}
		batch.Records = append(batch.Records, record)
	}
	if reader.Len() != 0 {
		return Batch{}, fmt.Errorf("raw batch has %d trailing bytes", reader.Len())
	}
	return batch, nil
}

func validateRecord(record Record) error {
	if record.Topic == "" || record.MessageType == "" || len(record.Payload) == 0 || record.ReceiveTimestamp.UnixNano() <= 0 {
		return errors.New("invalid raw record metadata")
	}
	if (record.Kind == KindROS && record.Serialization == "cdr") ||
		(record.Kind == KindHost && record.Serialization == "json" && record.MessageType == "baize/HostMetrics") {
		return nil
	}
	return errors.New("invalid raw record kind or serialization")
}

func writeString(buffer *bytes.Buffer, value string) error {
	if len(value) > 65535 {
		return errors.New("raw metadata string is too long")
	}
	if err := binary.Write(buffer, binary.LittleEndian, uint16(len(value))); err != nil {
		return err
	}
	_, err := buffer.WriteString(value)
	return err
}

func readString(reader *bytes.Reader) (string, error) {
	var size uint16
	if err := binary.Read(reader, binary.LittleEndian, &size); err != nil {
		return "", err
	}
	data := make([]byte, size)
	if _, err := io.ReadFull(reader, data); err != nil {
		return "", err
	}
	return string(data), nil
}
