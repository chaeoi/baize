package collector

import (
	"bufio"
	"bytes"
	"context"
	"encoding/binary"
	"fmt"
	"io"
	"os/exec"
	"strings"
	"time"

	"baize/shared/rawstream"
)

const rawFrameHeaderSize = 17

// StreamRawTopic keeps ROS2 serialization out of the Agent's data model. The
// helper receives and forwards the middleware's serialized CDR bytes; the
// dashboard owns decoding and storage.
func StreamRawTopic(ctx context.Context, setup []string, environment map[string]string, user, topic, messageType string, output chan<- rawstream.Record) (result error) {
	command, err := rosSubscriberCommand(setup, environment, user, topic, messageType)
	if err != nil {
		return err
	}
	cmd := exec.CommandContext(ctx, "/bin/bash", "-lc", command)
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		return err
	}
	stderr, err := cmd.StderrPipe()
	if err != nil {
		return err
	}
	if err := cmd.Start(); err != nil {
		return err
	}
	stderrDone := make(chan struct{})
	var diagnostic bytes.Buffer
	go func() {
		_, _ = io.Copy(&diagnostic, io.LimitReader(stderr, 4096))
		_, _ = io.Copy(io.Discard, stderr)
		close(stderrDone)
	}()
	defer func() {
		_ = cmd.Process.Kill()
		<-stderrDone
		_ = cmd.Wait()
		if ctx.Err() != nil {
			result = ctx.Err()
		} else if message := strings.TrimSpace(diagnostic.String()); message != "" {
			result = fmt.Errorf("%w: %s", result, message)
		}
	}()
	reader := bufio.NewReaderSize(stdout, 256*1024)
	for {
		record, err := readRawFrame(reader)
		if err != nil {
			return err
		}
		record.Topic, record.MessageType = topic, messageType
		select {
		case output <- record:
		case <-ctx.Done():
			return ctx.Err()
		}
	}
}

func readRawFrame(reader io.Reader) (rawstream.Record, error) {
	var header [rawFrameHeaderSize]byte
	if _, err := io.ReadFull(reader, header[:]); err != nil {
		return rawstream.Record{}, err
	}
	if string(header[:4]) != "BZR1" || header[4] != 1 {
		return rawstream.Record{}, fmt.Errorf("invalid raw ROS2 frame header")
	}
	stampNS := int64(binary.LittleEndian.Uint64(header[5:13]))
	size := binary.LittleEndian.Uint32(header[13:17])
	if size == 0 || size > 64<<20 || stampNS <= 0 {
		return rawstream.Record{}, fmt.Errorf("invalid raw ROS2 frame size or timestamp")
	}
	payload := make([]byte, size)
	if _, err := io.ReadFull(reader, payload); err != nil {
		return rawstream.Record{}, err
	}
	at := time.Unix(0, stampNS).UTC()
	return rawstream.Record{Kind: rawstream.KindROS, Serialization: "cdr", SourceTimestamp: at, ReceiveTimestamp: at, Payload: payload}, nil
}
