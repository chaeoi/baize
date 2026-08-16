package collector

import (
	"bufio"
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"regexp"
	"sort"
	"strings"
	"sync"
	"time"

	"baize/agent/internal/config"
	"baize/shared/model"
	"gopkg.in/yaml.v3"
)

type jointStateMessage struct {
	Name     []string  `yaml:"name"`
	Position []float64 `yaml:"position"`
	Velocity []float64 `yaml:"velocity"`
	Effort   []float64 `yaml:"effort"`
}

type MotorCollector struct {
	config       config.MotorConfig
	streamOnce   sync.Once
	readyOnce    sync.Once
	ready        chan struct{}
	mu           sync.Mutex
	latest       model.MotorSnapshot
	pending      []model.MotorSample
	lastSampleAt time.Time
	streamErr    error
}

var rosEnvironmentNamePattern = regexp.MustCompile(`^[A-Z_][A-Z0-9_]{0,63}$`)
var rosUserNamePattern = regexp.MustCompile(`^[a-z_][a-z0-9_-]{0,31}$`)

func NewMotorCollector(cfg config.MotorConfig) *MotorCollector {
	return &MotorCollector{config: cfg, ready: make(chan struct{})}
}

func (c *MotorCollector) Collect(ctx context.Context) (model.MotorSnapshot, error) {
	if c.config.FastSampleRateHz > 0 {
		return c.collectStream(ctx)
	}
	return c.collectOnce(ctx)
}

func (c *MotorCollector) collectOnce(ctx context.Context) (model.MotorSnapshot, error) {
	snapshot := model.MotorSnapshot{
		Enabled:                 true,
		Source:                  "ros2_joint_state",
		Topic:                   c.config.Topic,
		PerMotorOnlineSupported: false,
		TemperatureSupported:    false,
	}
	readCtx, cancel := context.WithTimeout(ctx, c.config.ReadTimeout.Value())
	defer cancel()
	command, err := rosCommand(c.config.ROSSetup, c.config.ROSEnvironment, c.config.ROSUser,
		"ros2 topic echo --no-daemon --once "+shellQuote(c.config.Topic)+" "+shellQuote(c.config.MessageType))
	if err != nil {
		return snapshot, err
	}
	cmd := exec.CommandContext(readCtx, "/bin/bash", "-lc", command)
	var stderr bytes.Buffer
	cmd.Stderr = &stderr
	output, err := cmd.Output()
	if err != nil {
		if errors.Is(readCtx.Err(), context.DeadlineExceeded) {
			return snapshot, fmt.Errorf("topic read timed out after %s", c.config.ReadTimeout.Value())
		}
		message := strings.TrimSpace(stderr.String())
		if message == "" {
			message = err.Error()
		}
		return snapshot, fmt.Errorf("ros2 topic read: %s", message)
	}
	motors, err := parseJointState(output, c.config.JointLabels, c.config.Definitions)
	if err != nil {
		return snapshot, err
	}
	snapshot.TopicOnline = true
	snapshot.SampledAt = time.Now().UTC()
	snapshot.Motors = motors
	return snapshot, nil
}

func (c *MotorCollector) collectStream(ctx context.Context) (model.MotorSnapshot, error) {
	c.streamOnce.Do(func() { go c.streamLoop(ctx) })
	wait := c.config.ReadTimeout.Value()
	if wait <= 0 {
		wait = 3 * time.Second
	}
	select {
	case <-c.ready:
	case <-ctx.Done():
		return c.emptySnapshot(), ctx.Err()
	case <-time.After(wait):
		c.mu.Lock()
		err := c.streamErr
		snapshot := cloneMotorSnapshot(c.latest)
		c.mu.Unlock()
		if !snapshot.TopicOnline {
			if err == nil {
				err = fmt.Errorf("motor topic did not publish within %s", wait)
			}
			return snapshot, err
		}
	}

	c.mu.Lock()
	snapshot := cloneMotorSnapshot(c.latest)
	snapshot.Samples = append([]model.MotorSample(nil), c.pending...)
	snapshot.SampleRateHz = c.config.FastSampleRateHz
	c.pending = c.pending[:0]
	c.mu.Unlock()
	return snapshot, nil
}

func (c *MotorCollector) emptySnapshot() model.MotorSnapshot {
	return model.MotorSnapshot{Enabled: true, Source: "ros2_joint_state", Topic: c.config.Topic, SampleRateHz: c.config.FastSampleRateHz}
}

func (c *MotorCollector) streamLoop(ctx context.Context) {
	for ctx.Err() == nil {
		if err := c.readStreamProcess(ctx); err != nil && ctx.Err() == nil {
			c.mu.Lock()
			c.streamErr = err
			c.mu.Unlock()
			select {
			case <-ctx.Done():
				return
			case <-time.After(time.Second):
			}
		}
	}
}

func (c *MotorCollector) readStreamProcess(ctx context.Context) error {
	command, err := rosCommand(c.config.ROSSetup, c.config.ROSEnvironment, c.config.ROSUser,
		"ros2 topic echo --no-daemon "+shellQuote(c.config.Topic)+" "+shellQuote(c.config.MessageType))
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
	go func() {
		_, _ = io.Copy(io.Discard, stderr)
		close(stderrDone)
	}()

	scanner := bufio.NewScanner(stdout)
	scanner.Buffer(make([]byte, 16*1024), 2*1024*1024)
	var message strings.Builder
	for scanner.Scan() {
		line := scanner.Text()
		if strings.TrimSpace(line) == "---" {
			c.consumeStreamMessage([]byte(message.String()))
			message.Reset()
			continue
		}
		message.WriteString(line)
		message.WriteByte('\n')
	}
	if message.Len() > 0 {
		c.consumeStreamMessage([]byte(message.String()))
	}
	if err := scanner.Err(); err != nil {
		_ = cmd.Process.Kill()
		<-stderrDone
		_ = cmd.Wait()
		return err
	}
	<-stderrDone
	if err := cmd.Wait(); err != nil && ctx.Err() == nil {
		return fmt.Errorf("ROS topic stream exited: %w", err)
	}
	return nil
}

func (c *MotorCollector) consumeStreamMessage(data []byte) {
	if len(bytes.TrimSpace(data)) == 0 {
		return
	}
	motors, err := parseJointState(data, c.config.JointLabels, c.config.Definitions)
	if err != nil {
		c.mu.Lock()
		c.streamErr = err
		c.mu.Unlock()
		return
	}
	now := time.Now().UTC()
	c.mu.Lock()
	c.latest = model.MotorSnapshot{Enabled: true, Source: "ros2_joint_state", Topic: c.config.Topic, TopicOnline: true, SampledAt: now, Motors: cloneMotorStates(motors), SampleRateHz: c.config.FastSampleRateHz}
	if c.lastSampleAt.IsZero() || now.Sub(c.lastSampleAt) >= time.Duration(float64(time.Second)/c.config.FastSampleRateHz) {
		c.pending = append(c.pending, model.MotorSample{At: now, Motors: compactMotorStates(motors)})
		maxPending := int(c.config.FastSampleRateHz * float64(c.config.FastBufferSeconds))
		if maxPending < 1 {
			maxPending = 1
		}
		if len(c.pending) > maxPending {
			c.pending = append([]model.MotorSample(nil), c.pending[len(c.pending)-maxPending:]...)
		}
		c.lastSampleAt = now
	}
	c.streamErr = nil
	c.mu.Unlock()
	c.readyOnce.Do(func() { close(c.ready) })
}

func cloneMotorSnapshot(snapshot model.MotorSnapshot) model.MotorSnapshot {
	snapshot.Motors = cloneMotorStates(snapshot.Motors)
	snapshot.Samples = append([]model.MotorSample(nil), snapshot.Samples...)
	return snapshot
}

func cloneMotorStates(motors []model.MotorState) []model.MotorState {
	if len(motors) == 0 {
		return nil
	}
	return append([]model.MotorState(nil), motors...)
}

func compactMotorStates(motors []model.MotorState) []model.MotorSampleState {
	result := make([]model.MotorSampleState, len(motors))
	for index, motor := range motors {
		velocity := motor.VelocityRadPerSec
		if velocity == 0 && motor.VelocityRPS != 0 {
			velocity = motor.VelocityRPS
		}
		result[index] = model.MotorSampleState{ID: motor.ID, Label: motor.Label, PositionRad: motor.PositionRad, VelocityRadPerSec: velocity, TorqueNm: motor.TorqueNm}
	}
	return result
}

func parseJointState(data []byte, labels map[string]string, definitions map[string]config.MotorDefinition) ([]model.MotorState, error) {
	decoder := yaml.NewDecoder(bytes.NewReader(data))
	var message jointStateMessage
	if err := decoder.Decode(&message); err != nil {
		return nil, fmt.Errorf("decode JointState YAML: %w", err)
	}
	count := len(message.Name)
	if count == 0 {
		count = max(len(message.Position), len(message.Velocity), len(message.Effort))
		message.Name = make([]string, count)
		for i := range message.Name {
			message.Name[i] = fmt.Sprintf("motor_id_%02d", i+1)
		}
	}
	if len(message.Position) != count || len(message.Velocity) != count || len(message.Effort) != count {
		return nil, fmt.Errorf("JointState array size mismatch: name=%d position=%d velocity=%d effort=%d",
			count, len(message.Position), len(message.Velocity), len(message.Effort))
	}
	result := make([]model.MotorState, count)
	for i := 0; i < count; i++ {
		definition := definitions[message.Name[i]]
		result[i] = model.MotorState{
			ID:                message.Name[i],
			Label:             labels[message.Name[i]],
			PositionRad:       message.Position[i],
			VelocityRadPerSec: message.Velocity[i],
			TorqueNm:          message.Effort[i],
			Brand:             definition.Brand,
			Model:             definition.Model,
			CANInterface:      definition.CANInterface,
			ControlMode:       definition.ControlMode,
			VirtualJoint:      definition.VirtualJoint,
		}
	}
	return result, nil
}

func rosCommand(setupFiles []string, environment map[string]string, user, finalCommand string) (string, error) {
	parts := make([]string, 0, len(setupFiles)+len(environment)+1)
	for _, path := range setupFiles {
		if !strings.HasPrefix(path, "/") || strings.ContainsAny(path, "\x00\n\r") {
			return "", fmt.Errorf("invalid ROS setup path %q", path)
		}
		parts = append(parts, "source "+shellQuote(path))
	}
	names := make([]string, 0, len(environment))
	for name := range environment {
		names = append(names, name)
	}
	sort.Strings(names)
	for _, name := range names {
		value := environment[name]
		if !rosEnvironmentNamePattern.MatchString(name) || strings.ContainsAny(value, "\x00\n\r") {
			return "", fmt.Errorf("invalid ROS environment variable %q", name)
		}
		parts = append(parts, "export "+name+"="+shellQuote(value))
	}
	parts = append(parts, "exec "+finalCommand)
	command := strings.Join(parts, " && ")
	return wrapROSCommand(command, user, os.Geteuid())
}

func wrapROSCommand(command, user string, euid int) (string, error) {
	if user == "" || euid != 0 {
		return command, nil
	}
	if !rosUserNamePattern.MatchString(user) {
		return "", fmt.Errorf("invalid ROS user %q", user)
	}
	// setpriv execs the lowered-privilege shell in place. This keeps the ROS
	// client in CommandContext's process tree, so a read timeout kills it too.
	return "exec /usr/bin/setpriv --reset-env --reuid=" + shellQuote(user) + " --regid=" + shellQuote(user) + " --init-groups -- /bin/bash -lc " + shellQuote(command), nil
}

func shellQuote(value string) string {
	return "'" + strings.ReplaceAll(value, "'", "'\"'\"'") + "'"
}

func max(values ...int) int {
	result := 0
	for _, value := range values {
		if value > result {
			result = value
		}
	}
	return result
}
