package collector

import (
	"bytes"
	"context"
	"fmt"
	"os"
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
	streamNames  []string
	pending      []model.MotorSample
	pendingHead  int
	pendingCount int
	recycled     []model.MotorSample
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
	command, err := rosSubscriberCommand(c.config.ROSSetup, c.config.ROSEnvironment, c.config.ROSUser, c.config.Topic, c.config.MessageType)
	if err != nil {
		return snapshot, err
	}
	if err := c.readBinaryOnceProcess(readCtx, command); err != nil {
		return snapshot, fmt.Errorf("ROS2 topic read: %w", err)
	}
	c.mu.Lock()
	snapshot = cloneMotorSnapshot(c.latest)
	c.mu.Unlock()
	snapshot.Samples = nil
	snapshot.SampleRateHz = 0
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
	snapshot.Samples = c.takePendingSamplesLocked()
	snapshot.SampleRateHz = c.config.FastSampleRateHz
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
	command, err := rosSubscriberCommand(c.config.ROSSetup, c.config.ROSEnvironment, c.config.ROSUser, c.config.Topic, c.config.MessageType)
	if err != nil {
		return err
	}
	return c.readBinaryProcess(ctx, command)
}

func (c *MotorCollector) consumeMotorValues(names []string, positions, velocities, efforts []float64, sampledAt time.Time) error {
	count := len(names)
	if count == 0 || len(positions) != count || len(velocities) != count || len(efforts) != count {
		return fmt.Errorf("JointState array size mismatch: name=%d position=%d velocity=%d effort=%d",
			count, len(positions), len(velocities), len(efforts))
	}
	now := sampledAt
	if now.IsZero() {
		now = time.Now().UTC()
	}
	c.mu.Lock()
	if !sameStrings(c.streamNames, names) {
		c.streamNames = append(c.streamNames[:0], names...)
		c.latest.Motors = make([]model.MotorState, count)
		for index, name := range names {
			definition := c.config.Definitions[name]
			c.latest.Motors[index] = model.MotorState{
				ID: name, Label: c.config.JointLabels[name], Brand: definition.Brand,
				Model: definition.Model, CANInterface: definition.CANInterface,
				ControlMode: definition.ControlMode, VirtualJoint: definition.VirtualJoint,
			}
		}
	}
	for index := range names {
		c.latest.Motors[index].PositionRad = positions[index]
		c.latest.Motors[index].VelocityRadPerSec = velocities[index]
		c.latest.Motors[index].TorqueNm = efforts[index]
	}
	c.latest.Enabled = true
	c.latest.Source = "ros2_joint_state"
	c.latest.Topic = c.config.Topic
	c.latest.TopicOnline = true
	c.latest.SampledAt = now
	c.latest.SampleRateHz = c.config.FastSampleRateHz
	pendingIndex := c.appendPendingLocked(model.MotorSample{At: now})
	compact := c.pending[pendingIndex].Motors
	if cap(compact) < count {
		compact = make([]model.MotorSampleState, count)
	} else {
		compact = compact[:count]
	}
	for index, motor := range c.latest.Motors {
		compact[index] = model.MotorSampleState{
			ID: motor.ID, Label: motor.Label, PositionRad: positions[index],
			VelocityRadPerSec: velocities[index], TorqueNm: efforts[index],
		}
	}
	c.pending[pendingIndex].Motors = compact
	c.streamErr = nil
	c.mu.Unlock()
	c.readyOnce.Do(func() { close(c.ready) })
	return nil
}

func sameStrings(left, right []string) bool {
	if len(left) != len(right) {
		return false
	}
	for index := range left {
		if left[index] != right[index] {
			return false
		}
	}
	return true
}

func (c *MotorCollector) appendPendingLocked(sample model.MotorSample) int {
	maxPending := int(c.config.FastSampleRateHz * float64(c.config.FastBufferSeconds))
	if maxPending < 1 {
		maxPending = 1
	}
	if cap(c.pending) != maxPending {
		c.pending = make([]model.MotorSample, maxPending)
		c.pendingHead = 0
		c.pendingCount = 0
	}
	index := (c.pendingHead + c.pendingCount) % maxPending
	if c.pendingCount == maxPending {
		index = c.pendingHead
		c.pendingHead = (c.pendingHead + 1) % maxPending
	} else {
		c.pendingCount++
	}
	// Preserve the slot's reusable motor slice when the new sample only
	// replaces its timestamp. This keeps allocations bounded after warm-up.
	motors := c.pending[index].Motors
	c.pending[index] = sample
	if c.pending[index].Motors == nil {
		c.pending[index].Motors = motors
	}
	return index
}

func (c *MotorCollector) takePendingSamplesLocked() []model.MotorSample {
	if c.pendingCount == 0 {
		return nil
	}
	if cap(c.recycled) < c.pendingCount {
		c.recycled = make([]model.MotorSample, c.pendingCount)
	} else {
		c.recycled = c.recycled[:c.pendingCount]
	}
	result := c.recycled
	for index := range result {
		source := (c.pendingHead + index) % len(c.pending)
		result[index], c.pending[source] = c.pending[source], result[index]
	}
	c.pendingHead = 0
	c.pendingCount = 0
	return result
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

func parseJointState(data []byte, labels map[string]string, definitions map[string]config.MotorDefinition) ([]model.MotorState, error) {
	decoder := yaml.NewDecoder(bytes.NewReader(data))
	var message jointStateMessage
	if err := decoder.Decode(&message); err != nil {
		return nil, fmt.Errorf("decode JointState YAML: %w", err)
	}
	return motorStates(message.Name, message.Position, message.Velocity, message.Effort, labels, definitions)
}

func parseMotorMessage(data []byte, labels map[string]string, definitions map[string]config.MotorDefinition) ([]model.MotorState, time.Time, error) {
	trimmed := bytes.TrimSpace(data)
	if len(trimmed) > 0 && trimmed[0] == '{' {
		return parseJointStateJSON(trimmed, labels, definitions)
	}
	motors, err := parseJointState(trimmed, labels, definitions)
	return motors, time.Time{}, err
}

func motorStates(names []string, positions, velocities, efforts []float64, labels map[string]string, definitions map[string]config.MotorDefinition) ([]model.MotorState, error) {
	return motorStatesInto(nil, names, positions, velocities, efforts, labels, definitions)
}

func motorStatesInto(result []model.MotorState, names []string, positions, velocities, efforts []float64, labels map[string]string, definitions map[string]config.MotorDefinition) ([]model.MotorState, error) {
	count := len(names)
	if count == 0 {
		count = max(len(positions), len(velocities), len(efforts))
		names = make([]string, count)
		for i := range names {
			names[i] = fmt.Sprintf("motor_id_%02d", i+1)
		}
	}
	if len(positions) != count || len(velocities) != count || len(efforts) != count {
		return nil, fmt.Errorf("JointState array size mismatch: name=%d position=%d velocity=%d effort=%d",
			count, len(positions), len(velocities), len(efforts))
	}
	if cap(result) < count {
		result = make([]model.MotorState, count)
	} else {
		result = result[:count]
	}
	for i := 0; i < count; i++ {
		definition := definitions[names[i]]
		result[i] = model.MotorState{
			ID:                names[i],
			Label:             labels[names[i]],
			PositionRad:       positions[i],
			VelocityRadPerSec: velocities[i],
			TorqueNm:          efforts[i],
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
