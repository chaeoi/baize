package collector

import (
	"bufio"
	"context"
	"encoding/binary"
	"fmt"
	"io"
	"math"
	"os/exec"
	"time"
)

const motorBinaryHeaderSize = 16

func (c *MotorCollector) readBinaryProcess(ctx context.Context, command string) error {
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
	go func() {
		_, _ = io.Copy(io.Discard, stderr)
		close(stderrDone)
	}()
	stop := func(readErr error) error {
		_ = cmd.Process.Kill()
		<-stderrDone
		_ = cmd.Wait()
		return readErr
	}
	reader := bufio.NewReaderSize(stdout, 256*1024)
	names := []string(nil)
	header := make([]byte, motorBinaryHeaderSize)
	values := make([]float64, 0, 3*32)
	rawValues := make([]byte, 0, 32*8)
	for {
		frame, err := readMotorFrame(reader, names, header, values, rawValues)
		if err != nil {
			if err == io.EOF || err == io.ErrUnexpectedEOF {
				break
			}
			return stop(err)
		}
		names, values, rawValues = frame.names, frame.values, frame.rawValues
		if err := c.consumeMotorValues(names, values[:frame.count], values[frame.count:2*frame.count], values[2*frame.count:], frame.sampledAt); err != nil {
			return stop(err)
		}
	}
	<-stderrDone
	if err := cmd.Wait(); err != nil && ctx.Err() == nil {
		return fmt.Errorf("optimized ROS topic stream exited: %w", err)
	}
	return nil
}

func (c *MotorCollector) readBinaryOnceProcess(ctx context.Context, command string) error {
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
	go func() {
		_, _ = io.Copy(io.Discard, stderr)
		close(stderrDone)
	}()
	reader := bufio.NewReaderSize(stdout, 256*1024)
	frame, err := readMotorFrame(reader, nil, make([]byte, motorBinaryHeaderSize), nil, nil)
	if err == nil {
		err = c.consumeMotorValues(frame.names, frame.values[:frame.count], frame.values[frame.count:2*frame.count], frame.values[2*frame.count:], frame.sampledAt)
	}
	_ = cmd.Process.Kill()
	<-stderrDone
	_ = cmd.Wait()
	return err
}

type motorFrame struct {
	names     []string
	values    []float64
	rawValues []byte
	count     int
	sampledAt time.Time
}

func readMotorFrame(reader io.Reader, names []string, header []byte, values []float64, rawValues []byte) (motorFrame, error) {
	if _, err := io.ReadFull(reader, header); err != nil {
		return motorFrame{}, err
	}
	if string(header[:4]) != "BZM1" || header[4] != 1 {
		return motorFrame{}, fmt.Errorf("invalid optimized motor frame header")
	}
	flags := header[5]
	stampNS := int64(binary.LittleEndian.Uint64(header[6:14]))
	count := int(binary.LittleEndian.Uint16(header[14:16]))
	if count < 1 || count > 1024 {
		return motorFrame{}, fmt.Errorf("optimized motor frame has invalid item count %d", count)
	}
	if flags&1 != 0 {
		names = make([]string, count)
		for index := range names {
			var lengthBytes [2]byte
			if _, err := io.ReadFull(reader, lengthBytes[:]); err != nil {
				return motorFrame{}, err
			}
			length := int(binary.LittleEndian.Uint16(lengthBytes[:]))
			value := make([]byte, length)
			if _, err := io.ReadFull(reader, value); err != nil {
				return motorFrame{}, err
			}
			names[index] = string(value)
		}
	}
	if len(names) != count {
		return motorFrame{}, fmt.Errorf("optimized motor frame omitted initial names")
	}
	if cap(values) < count*3 {
		values = make([]float64, count*3)
	} else {
		values = values[:count*3]
	}
	if cap(rawValues) < count*8 {
		rawValues = make([]byte, count*8)
	} else {
		rawValues = rawValues[:count*8]
	}
	for group := 0; group < 3; group++ {
		if _, err := io.ReadFull(reader, rawValues); err != nil {
			return motorFrame{}, err
		}
		for index := 0; index < count; index++ {
			values[group*count+index] = math.Float64frombits(binary.LittleEndian.Uint64(rawValues[index*8:]))
		}
	}
	sampledAt := time.Time{}
	if stampNS > 0 {
		sampledAt = time.Unix(0, stampNS).UTC()
	}
	return motorFrame{names: names, values: values, rawValues: rawValues, count: count, sampledAt: sampledAt}, nil
}
