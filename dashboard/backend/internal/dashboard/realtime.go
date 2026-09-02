package dashboard

import (
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"net/http"
	"net/url"
	"strings"
	"sync"
	"time"

	"baize/shared/model"
	"github.com/gorilla/websocket"
)

const (
	maxStreamClients = 64
	streamQueueSize  = 16
	streamPingEvery  = 20 * time.Second
	streamWriteWait  = 5 * time.Second
	streamReadWait   = 45 * time.Second
	onlineAfter      = 12 * time.Second
)

var robotStreamUpgrader = websocket.Upgrader{
	ReadBufferSize:    2048,
	WriteBufferSize:   8192,
	EnableCompression: true,
	CheckOrigin: func(request *http.Request) bool {
		origin := request.Header.Get("Origin")
		if origin == "" {
			return true
		}
		parsed, err := url.Parse(origin)
		return err == nil && parsed.Host == request.Host && (parsed.Scheme == "http" || parsed.Scheme == "https")
	},
}

var publicRobotStreamUpgrader = websocket.Upgrader{
	ReadBufferSize:    2048,
	WriteBufferSize:   8192,
	EnableCompression: true,
	// This stream contains the same deliberately redacted data as the public REST API.
	CheckOrigin: func(*http.Request) bool { return true },
}

// PublicRobot deliberately contains no UUID, hostname, OS, architecture, or
// collection configuration. Those fields are available only to an admin stream.
type PublicRobot struct {
	ID                string              `json:"id"`
	Code              string              `json:"code"`
	Model             string              `json:"model"`
	Remark            string              `json:"remark,omitempty"`
	Online            bool                `json:"online"`
	LastSeen          time.Time           `json:"last_seen"`
	CollectedAt       time.Time           `json:"collected_at"`
	Summary           PublicSummary       `json:"summary"`
	MotorSamples      []model.MotorSample `json:"motor_samples,omitempty"`
	MotorLabels       map[string]string   `json:"motor_labels,omitempty"`
	MotorSampleRateHz float64             `json:"motor_sample_rate_hz,omitempty"`
}

type PublicSummary struct {
	HasTelemetry     bool           `json:"has_telemetry"`
	CPUPercent       float64        `json:"cpu_percent"`
	MemoryPercent    float64        `json:"memory_percent"`
	DiskPercent      float64        `json:"disk_percent"`
	Load1            float64        `json:"load_1"`
	UptimeSeconds    float64        `json:"uptime_seconds"`
	TemperatureMax   *float64       `json:"temperature_max,omitempty"`
	TemperatureMin   *float64       `json:"temperature_min,omitempty"`
	GPU              *PublicGPU     `json:"gpu,omitempty"`
	Battery          *PublicBattery `json:"battery,omitempty"`
	MotorCount       int            `json:"motor_count"`
	MotorTopicOnline bool           `json:"motor_topic_online"`
	DiagnosticCount  int            `json:"diagnostic_count"`
}

type PublicGPU struct {
	UtilizationPercent float64 `json:"utilization_percent"`
	TemperatureCelsius float64 `json:"temperature_celsius"`
}

type PublicBattery struct {
	Online            bool    `json:"online"`
	Present           bool    `json:"present"`
	SOCPercent        float64 `json:"soc_percent"`
	Voltage           float64 `json:"voltage"`
	Current           float64 `json:"current"`
	Temperature       float64 `json:"temperature"`
	PowerWatts        float64 `json:"power_watts"`
	PowerSupplyStatus string  `json:"power_supply_status,omitempty"`
}

type robotStreamEvent struct {
	Type       string        `json:"type"`
	ServerTime time.Time     `json:"server_time"`
	Robots     []PublicRobot `json:"robots,omitempty"`
	Robot      *PublicRobot  `json:"robot,omitempty"`
	ID         string        `json:"id,omitempty"`
}

type adminStreamEvent struct {
	Type       string        `json:"type"`
	ServerTime time.Time     `json:"server_time"`
	Robots     []RobotRecord `json:"robots,omitempty"`
	Robot      *RobotRecord  `json:"robot,omitempty"`
	UUID       string        `json:"uuid,omitempty"`
}

type streamClient struct {
	conn          *websocket.Conn
	send          chan []byte
	done          chan struct{}
	stopOnce      sync.Once
	publicOptions *publicStreamOptions
}

// publicStreamOptions lets a detail page receive high-rate samples only for
// the robot and motors it is actually displaying or recording.
type publicStreamOptions struct {
	robotID        string
	includeSamples bool
	motorIDs       map[string]struct{}
}

func (client *streamClient) stop() {
	client.stopOnce.Do(func() {
		close(client.done)
		_ = client.conn.Close()
	})
}

type streamHub struct {
	mu      sync.Mutex
	clients map[*streamClient]struct{}
}

func newStreamHub() *streamHub {
	return &streamHub{clients: make(map[*streamClient]struct{})}
}

func (hub *streamHub) add(client *streamClient) bool {
	hub.mu.Lock()
	defer hub.mu.Unlock()
	if len(hub.clients) >= maxStreamClients {
		return false
	}
	hub.clients[client] = struct{}{}
	return true
}

func (hub *streamHub) remove(client *streamClient) {
	hub.mu.Lock()
	delete(hub.clients, client)
	hub.mu.Unlock()
	client.stop()
}

func (hub *streamHub) broadcast(message []byte) {
	hub.broadcastWith(func(*streamClient) []byte { return message })
}

func (hub *streamHub) broadcastWith(message func(*streamClient) []byte) {
	hub.mu.Lock()
	defer hub.mu.Unlock()
	for client := range hub.clients {
		payload := message(client)
		if len(payload) == 0 {
			continue
		}
		select {
		case client.send <- payload:
		default:
			// A slow browser must not retain every telemetry update or block
			// other viewers. It will reconnect and receive a fresh snapshot.
			delete(hub.clients, client)
			client.stop()
		}
	}
}

func (s *Server) publicRobot(record RobotRecord) PublicRobot {
	return s.publicRobotWithSamples(record, false)
}

func (s *Server) publicRobotWithSamples(record RobotRecord, includeSamples bool) PublicRobot {
	telemetry := record.Telemetry
	summary := PublicSummary{HasTelemetry: !telemetry.CollectedAt.IsZero(), DiagnosticCount: len(telemetry.Errors)}
	if telemetry.System != nil {
		summary.CPUPercent = telemetry.System.CPUUsagePercent
		summary.MemoryPercent = percent(telemetry.System.MemoryUsedBytes, telemetry.System.MemoryTotalBytes)
		if len(telemetry.System.Disks) > 0 {
			summary.DiskPercent = percent(telemetry.System.Disks[0].UsedBytes, telemetry.System.Disks[0].TotalBytes)
		}
		summary.Load1 = telemetry.System.Load1
		summary.UptimeSeconds = telemetry.System.UptimeSeconds
		for _, temperature := range telemetry.System.Temperatures {
			if summary.TemperatureMax == nil || temperature.Celsius > *summary.TemperatureMax {
				value := temperature.Celsius
				summary.TemperatureMax = &value
			}
			if summary.TemperatureMin == nil || temperature.Celsius < *summary.TemperatureMin {
				value := temperature.Celsius
				summary.TemperatureMin = &value
			}
		}
	}
	if len(telemetry.GPUs) > 0 {
		gpu := telemetry.GPUs[0]
		summary.GPU = &PublicGPU{UtilizationPercent: gpu.UtilizationPercent, TemperatureCelsius: gpu.TemperatureCelsius}
	}
	if telemetry.BMS != nil {
		bms := telemetry.BMS
		summary.Battery = &PublicBattery{
			Online: bms.Online, Present: bms.Present, SOCPercent: bms.SOCPercent, Voltage: bms.Voltage,
			Current: bms.Current, Temperature: bms.Temperature, PowerWatts: bms.PowerWatts,
			PowerSupplyStatus: bms.PowerSupplyStatus,
		}
	}
	if telemetry.Motors != nil {
		summary.MotorCount = len(telemetry.Motors.Motors)
		summary.MotorTopicOnline = telemetry.Motors.TopicOnline
	}
	collectedAt := telemetry.CollectedAt
	robot := PublicRobot{
		ID: publicRobotID(s.config.JWTSecret, record.UUID), Code: record.Code, Model: record.Model,
		Remark: record.Remark, Online: time.Since(record.LastSeen) <= onlineAfter,
		LastSeen: record.LastSeen, CollectedAt: collectedAt, Summary: summary,
	}
	if includeSamples && telemetry.Motors != nil && len(telemetry.Motors.Samples) > 0 {
		robot.MotorSamples = telemetry.Motors.Samples
		robot.MotorSampleRateHz = telemetry.Motors.SampleRateHz
		for _, motor := range telemetry.Motors.Motors {
			if motor.ID == "" || motor.Label == "" {
				continue
			}
			if robot.MotorLabels == nil {
				robot.MotorLabels = make(map[string]string)
			}
			robot.MotorLabels[motor.ID] = motor.Label
		}
	}
	return robot
}

func publicRobotID(secret, uuid string) string {
	mac := hmac.New(sha256.New, []byte(secret))
	_, _ = mac.Write([]byte(uuid))
	return hex.EncodeToString(mac.Sum(nil)[:10])
}

func percent(used, total uint64) float64 {
	if total == 0 {
		return 0
	}
	return float64(used) * 100 / float64(total)
}

func (s *Server) publicRobotSnapshot() []PublicRobot {
	records := s.store.Robots()
	robots := make([]PublicRobot, 0, len(records))
	for _, record := range records {
		robots = append(robots, s.publicRobot(record))
	}
	return robots
}

func (s *Server) publicRobotEvent(record RobotRecord) []byte {
	return s.publicRobotEventForOptions(record, nil)
}

func (s *Server) publicRobotEventForOptions(record RobotRecord, options *publicStreamOptions) []byte {
	includeSamples := options == nil || (options.includeSamples && options.robotID == publicRobotID(s.config.JWTSecret, record.UUID))
	robot := s.publicRobotWithSamples(record, includeSamples)
	if includeSamples && options != nil && len(options.motorIDs) > 0 {
		filterPublicMotorSamples(&robot, options.motorIDs)
	}
	message, _ := json.Marshal(robotStreamEvent{Type: "robot", ServerTime: time.Now().UTC(), Robot: &robot})
	return message
}

func filterPublicMotorSamples(robot *PublicRobot, motorIDs map[string]struct{}) {
	if len(robot.MotorSamples) == 0 {
		return
	}
	filtered := robot.MotorSamples[:0]
	for _, sample := range robot.MotorSamples {
		motors := sample.Motors[:0]
		for _, motor := range sample.Motors {
			if _, ok := motorIDs[motor.ID]; ok {
				motors = append(motors, motor)
			}
		}
		if len(motors) > 0 {
			sample.Motors = motors
			filtered = append(filtered, sample)
		}
	}
	robot.MotorSamples = filtered
}

func (s *Server) publicSnapshotEvent() []byte {
	message, _ := json.Marshal(robotStreamEvent{Type: "snapshot", ServerTime: time.Now().UTC(), Robots: s.publicRobotSnapshot()})
	return message
}

func (s *Server) adminRobotEvent(record RobotRecord) []byte {
	message, _ := json.Marshal(adminStreamEvent{Type: "robot", ServerTime: time.Now().UTC(), Robot: &record})
	return message
}

func (s *Server) adminSnapshotEvent() []byte {
	message, _ := json.Marshal(adminStreamEvent{Type: "snapshot", ServerTime: time.Now().UTC(), Robots: s.store.Robots()})
	return message
}

func (s *Server) publicRemovalEvent(record RobotRecord) []byte {
	message, _ := json.Marshal(robotStreamEvent{Type: "removed", ServerTime: time.Now().UTC(), ID: publicRobotID(s.config.JWTSecret, record.UUID)})
	return message
}

func (s *Server) adminRemovalEvent(uuid string) []byte {
	message, _ := json.Marshal(adminStreamEvent{Type: "removed", ServerTime: time.Now().UTC(), UUID: uuid})
	return message
}

func (s *Server) serveRobotStream(writer http.ResponseWriter, request *http.Request, admin bool) {
	if request.Method != http.MethodGet {
		methodNotAllowed(writer)
		return
	}
	upgrader := robotStreamUpgrader
	if !admin {
		upgrader = publicRobotStreamUpgrader
	}
	connection, err := upgrader.Upgrade(writer, request, nil)
	if err != nil {
		return
	}
	hub := s.publicStream
	snapshot := s.publicSnapshotEvent()
	if admin {
		hub = s.adminStream
		snapshot = s.adminSnapshotEvent()
	}
	client := &streamClient{conn: connection, send: make(chan []byte, streamQueueSize), done: make(chan struct{})}
	if !admin {
		options, ok := parsePublicStreamOptions(request)
		if !ok {
			_ = connection.WriteControl(websocket.CloseMessage, websocket.FormatCloseMessage(websocket.ClosePolicyViolation, "invalid stream options"), time.Now().Add(streamWriteWait))
			client.stop()
			return
		}
		client.publicOptions = &options
	}
	client.send <- snapshot
	if !hub.add(client) {
		_ = connection.WriteControl(websocket.CloseMessage, websocket.FormatCloseMessage(websocket.ClosePolicyViolation, "too many live viewers"), time.Now().Add(streamWriteWait))
		client.stop()
		return
	}
	defer hub.remove(client)

	connection.SetReadLimit(4096)
	_ = connection.SetReadDeadline(time.Now().Add(streamReadWait))
	connection.SetPongHandler(func(string) error {
		return connection.SetReadDeadline(time.Now().Add(streamReadWait))
	})
	readDone := make(chan struct{})
	go func() {
		defer close(readDone)
		for {
			if _, _, err := connection.ReadMessage(); err != nil {
				return
			}
		}
	}()

	ping := time.NewTicker(streamPingEvery)
	defer ping.Stop()
	for {
		select {
		case message := <-client.send:
			_ = connection.SetWriteDeadline(time.Now().Add(streamWriteWait))
			if err := connection.WriteMessage(websocket.TextMessage, message); err != nil {
				return
			}
		case <-ping.C:
			if err := connection.WriteControl(websocket.PingMessage, nil, time.Now().Add(streamWriteWait)); err != nil {
				return
			}
		case <-readDone:
			return
		case <-client.done:
			return
		}
	}
}

func parsePublicStreamOptions(request *http.Request) (publicStreamOptions, bool) {
	options := publicStreamOptions{}
	query := request.URL.Query()
	if query.Get("include_samples") != "1" {
		return options, true
	}
	options.includeSamples = true
	options.robotID = query.Get("robot_id")
	if len(options.robotID) != 20 || !isHexString(options.robotID) {
		return publicStreamOptions{}, false
	}
	motorIDs := strings.TrimSpace(query.Get("motor_ids"))
	if motorIDs == "" || motorIDs == "all" {
		return options, true
	}
	options.motorIDs = make(map[string]struct{})
	for _, motorID := range strings.Split(motorIDs, ",") {
		motorID = strings.TrimSpace(motorID)
		if !motorIDPattern.MatchString(motorID) || len(options.motorIDs) >= 64 {
			return publicStreamOptions{}, false
		}
		options.motorIDs[motorID] = struct{}{}
	}
	return options, true
}

func isHexString(value string) bool {
	for _, character := range value {
		if !(character >= '0' && character <= '9') && !(character >= 'a' && character <= 'f') && !(character >= 'A' && character <= 'F') {
			return false
		}
	}
	return true
}
