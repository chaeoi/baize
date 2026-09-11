package dashboard

import (
	"crypto/rand"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"sort"
	"sync"
	"time"

	"baize/shared/model"
	"golang.org/x/crypto/bcrypt"
	_ "modernc.org/sqlite"
)

type RobotRecord struct {
	UUID         string          `json:"uuid"`
	Code         string          `json:"code"`
	Model        string          `json:"model"`
	Hostname     string          `json:"hostname"`
	OS           string          `json:"os"`
	Arch         string          `json:"arch"`
	AgentVersion string          `json:"agent_version"`
	Remark       string          `json:"remark"`
	FirstSeen    time.Time       `json:"first_seen"`
	LastSeen     time.Time       `json:"last_seen"`
	Telemetry    model.Telemetry `json:"telemetry"`
}

type StoreOptions struct {
	AdminUser             string
	BootstrapPassword     string
	RequirePasswordChange bool
	HistoryRetention      time.Duration
	HistorySampleInterval time.Duration
}

type HistoryPoint struct {
	At                 time.Time           `json:"at"`
	CPUPercent         *float64            `json:"cpu_percent,omitempty"`
	MemoryPercent      *float64            `json:"memory_percent,omitempty"`
	DiskPercent        *float64            `json:"disk_percent,omitempty"`
	Load1              *float64            `json:"load_1,omitempty"`
	TemperatureMax     *float64            `json:"temperature_max,omitempty"`
	GPUUtilization     *float64            `json:"gpu_utilization_percent,omitempty"`
	GPUTemperature     *float64            `json:"gpu_temperature_celsius,omitempty"`
	BatterySOC         *float64            `json:"battery_soc_percent,omitempty"`
	BatteryVoltage     *float64            `json:"battery_voltage,omitempty"`
	BatteryCurrent     *float64            `json:"battery_current,omitempty"`
	BatteryPower       *float64            `json:"battery_power_watts,omitempty"`
	BatteryTemperature *float64            `json:"battery_temperature,omitempty"`
	MotorCount         int                 `json:"motor_count"`
	MotorTopicOnline   bool                `json:"motor_topic_online"`
	DiagnosticCount    int                 `json:"diagnostic_count"`
	Motors             []MotorHistoryPoint `json:"motors,omitempty"`
}

type MotorHistoryPoint struct {
	ID                string  `json:"id"`
	Label             string  `json:"label,omitempty"`
	PositionRad       float64 `json:"position_rad"`
	VelocityRadPerSec float64 `json:"velocity_rad_per_sec"`
	TorqueNm          float64 `json:"torque_nm"`
}

type Store struct {
	mu           sync.RWMutex
	dataDir      string
	control      *sql.DB
	tsdb         *telemetryTSDB
	robots       map[string]RobotRecord
	historyEvery time.Duration
	lastHistory  map[string]time.Time
}

func NewStore(dataDir, historyDir string, options StoreOptions) (*Store, error) {
	if options.AdminUser == "" {
		options.AdminUser = "admin"
	}
	if options.BootstrapPassword == "" {
		return nil, errors.New("bootstrap admin password must not be empty")
	}
	if options.HistoryRetention <= 0 {
		options.HistoryRetention = 90 * 24 * time.Hour
	}
	if options.HistorySampleInterval <= 0 {
		options.HistorySampleInterval = 2 * time.Second
	}
	if err := os.MkdirAll(dataDir, 0o750); err != nil {
		return nil, err
	}
	if err := os.MkdirAll(historyDir, 0o750); err != nil {
		return nil, err
	}
	control, err := openSQLite(filepath.Join(dataDir, "control.db"))
	if err != nil {
		return nil, fmt.Errorf("open control database: %w", err)
	}
	tsdb, err := openTelemetryTSDB(historyDir, options.HistoryRetention)
	if err != nil {
		_ = control.Close()
		return nil, fmt.Errorf("open telemetry tsdb: %w", err)
	}
	store := &Store{dataDir: dataDir, control: control, tsdb: tsdb, robots: make(map[string]RobotRecord), historyEvery: options.HistorySampleInterval, lastHistory: make(map[string]time.Time)}
	if err := store.migrateSchema(); err != nil {
		store.Close()
		return nil, err
	}
	if err := store.importLegacyState(); err != nil {
		store.Close()
		return nil, err
	}
	if err := store.ensureAdmin(options.AdminUser, options.BootstrapPassword, options.RequirePasswordChange); err != nil {
		store.Close()
		return nil, err
	}
	if err := store.loadRobots(); err != nil {
		store.Close()
		return nil, err
	}
	return store, nil
}

func openSQLite(path string) (*sql.DB, error) {
	db, err := sql.Open("sqlite", path)
	if err != nil {
		return nil, err
	}
	db.SetMaxOpenConns(1)
	db.SetMaxIdleConns(1)
	for _, statement := range []string{"PRAGMA busy_timeout = 5000", "PRAGMA journal_mode = WAL", "PRAGMA synchronous = NORMAL", "PRAGMA foreign_keys = ON"} {
		if _, err := db.Exec(statement); err != nil {
			_ = db.Close()
			return nil, err
		}
	}
	return db, nil
}

func (s *Store) migrateSchema() error {
	controlSchema := `
CREATE TABLE IF NOT EXISTS robots (
  uuid TEXT PRIMARY KEY,
  code TEXT NOT NULL,
  model TEXT NOT NULL DEFAULT '',
  hostname TEXT NOT NULL DEFAULT '',
  os TEXT NOT NULL DEFAULT '',
  arch TEXT NOT NULL DEFAULT '',
  agent_version TEXT NOT NULL DEFAULT '',
  first_seen INTEGER NOT NULL,
  last_seen INTEGER NOT NULL
);
CREATE INDEX IF NOT EXISTS robots_code_idx ON robots(code);
CREATE TABLE IF NOT EXISTS robot_settings (
  uuid TEXT PRIMARY KEY,
  remark TEXT NOT NULL DEFAULT ''
);
CREATE TABLE IF NOT EXISTS admin_accounts (
  username TEXT PRIMARY KEY,
  password_hash TEXT NOT NULL,
  force_change INTEGER NOT NULL DEFAULT 1
);
CREATE TABLE IF NOT EXISTS control_meta (
  key TEXT PRIMARY KEY,
  value TEXT NOT NULL
);`
	if _, err := s.control.Exec(controlSchema); err != nil {
		return fmt.Errorf("migrate control database: %w", err)
	}
	return nil
}

type legacyState struct {
	Remarks map[string]string `json:"remarks"`
}

func (s *Store) importLegacyState() error {
	var imported string
	if err := s.control.QueryRow(`SELECT value FROM control_meta WHERE key = 'legacy_state_imported'`).Scan(&imported); err == nil {
		return nil
	} else if !errors.Is(err, sql.ErrNoRows) {
		return err
	}
	paths := []string{filepath.Join(s.dataDir, "state.json"), filepath.Join(filepath.Dir(s.dataDir), "state.json")}
	var data []byte
	for _, path := range paths {
		loaded, err := os.ReadFile(path)
		if err == nil {
			data = loaded
			break
		}
		if !errors.Is(err, os.ErrNotExist) {
			return err
		}
	}
	legacy := legacyState{}
	if len(data) > 0 {
		if err := json.Unmarshal(data, &legacy); err != nil {
			return fmt.Errorf("read legacy dashboard state: %w", err)
		}
	}
	tx, err := s.control.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()
	for uuid, remark := range legacy.Remarks {
		if _, err := tx.Exec(`INSERT INTO robot_settings(uuid, remark) VALUES(?, ?) ON CONFLICT(uuid) DO UPDATE SET remark=excluded.remark`, uuid, remark); err != nil {
			return err
		}
	}
	if _, err := tx.Exec(`INSERT INTO control_meta(key, value) VALUES('legacy_state_imported', '1')`); err != nil {
		return err
	}
	return tx.Commit()
}

func (s *Store) ensureAdmin(username, bootstrapPassword string, forceChange bool) error {
	var exists int
	err := s.control.QueryRow(`SELECT 1 FROM admin_accounts WHERE username = ?`, username).Scan(&exists)
	if err == nil {
		return nil
	}
	if !errors.Is(err, sql.ErrNoRows) {
		return err
	}
	hash, err := bcrypt.GenerateFromPassword([]byte(bootstrapPassword), bcrypt.DefaultCost)
	if err != nil {
		return fmt.Errorf("hash bootstrap password: %w", err)
	}
	_, err = s.control.Exec(`INSERT INTO admin_accounts(username, password_hash, force_change) VALUES(?, ?, ?)`, username, string(hash), boolInt(forceChange))
	return err
}

// Secret returns a persistent control-plane secret. Secrets are kept in the
// control database so copying that database is sufficient to migrate the
// Dashboard identity; the disposable history database is never involved.
func (s *Store) Secret(key string, size int) (string, bool, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	var value string
	err := s.control.QueryRow(`SELECT value FROM control_meta WHERE key = ?`, key).Scan(&value)
	if err == nil {
		return value, false, nil
	}
	if !errors.Is(err, sql.ErrNoRows) {
		return "", false, err
	}
	data := make([]byte, size)
	if _, err := rand.Read(data); err != nil {
		return "", false, err
	}
	value = hex.EncodeToString(data)
	if _, err := s.control.Exec(`INSERT INTO control_meta(key, value) VALUES(?, ?)`, key, value); err != nil {
		return "", false, err
	}
	return value, true, nil
}

func (s *Store) loadRobots() error {
	rows, err := s.control.Query(`SELECT r.uuid, r.code, r.model, r.hostname, r.os, r.arch, r.agent_version, COALESCE(s.remark, ''), r.first_seen, r.last_seen FROM robots r LEFT JOIN robot_settings s ON s.uuid = r.uuid`)
	if err != nil {
		return err
	}
	defer rows.Close()
	for rows.Next() {
		var record RobotRecord
		var firstSeen, lastSeen int64
		if err := rows.Scan(&record.UUID, &record.Code, &record.Model, &record.Hostname, &record.OS, &record.Arch, &record.AgentVersion, &record.Remark, &firstSeen, &lastSeen); err != nil {
			return err
		}
		record.FirstSeen = unixNano(firstSeen)
		record.LastSeen = unixNano(lastSeen)
		record.Telemetry = model.Telemetry{SchemaVersion: model.SchemaVersion, Robot: model.Robot{UUID: record.UUID, Code: record.Code, Model: record.Model, Hostname: record.Hostname, OS: record.OS, Arch: record.Arch}, AgentVersion: record.AgentVersion}
		if telemetry, ok := s.latestHistoryTelemetry(record); ok {
			record.Telemetry = telemetry
		}
		s.robots[record.UUID] = record
	}
	return rows.Err()
}

func (s *Store) latestHistoryTelemetry(record RobotRecord) (model.Telemetry, bool) {
	points, err := s.History(record.UUID, time.Unix(0, 0), time.Now().UTC().Add(24*time.Hour), 1)
	if err != nil || len(points) == 0 {
		return model.Telemetry{}, false
	}
	point := points[0]
	telemetry := model.Telemetry{SchemaVersion: model.SchemaVersion, Robot: model.Robot{UUID: record.UUID, Code: record.Code, Model: record.Model, Hostname: record.Hostname, OS: record.OS, Arch: record.Arch}, AgentVersion: record.AgentVersion, CollectedAt: point.At}
	if point.CPUPercent != nil || point.MemoryPercent != nil || point.DiskPercent != nil || point.Load1 != nil || point.TemperatureMax != nil {
		system := &model.SystemMetrics{}
		if point.CPUPercent != nil {
			system.CPUUsagePercent = *point.CPUPercent
		}
		if point.MemoryPercent != nil {
			system.MemoryTotalBytes, system.MemoryUsedBytes = 10_000, uint64(clampPercent(*point.MemoryPercent)*100)
		}
		if point.DiskPercent != nil {
			system.Disks = []model.DiskMetrics{{Path: "/", TotalBytes: 10_000, UsedBytes: uint64(clampPercent(*point.DiskPercent) * 100)}}
		}
		if point.Load1 != nil {
			system.Load1 = *point.Load1
		}
		if point.TemperatureMax != nil {
			system.Temperatures = []model.Temperature{{Name: "last_sample_max", Celsius: *point.TemperatureMax}}
		}
		telemetry.System = system
	}
	if point.GPUUtilization != nil || point.GPUTemperature != nil {
		gpu := model.GPUMetrics{}
		if point.GPUUtilization != nil {
			gpu.UtilizationPercent = *point.GPUUtilization
		}
		if point.GPUTemperature != nil {
			gpu.TemperatureCelsius = *point.GPUTemperature
		}
		telemetry.GPUs = []model.GPUMetrics{gpu}
	}
	if point.BatterySOC != nil || point.BatteryVoltage != nil || point.BatteryCurrent != nil || point.BatteryTemperature != nil {
		battery := &model.BMSMetrics{Enabled: true, Online: true}
		if point.BatterySOC != nil {
			battery.SOCPercent = *point.BatterySOC
		}
		if point.BatteryVoltage != nil {
			battery.Voltage = *point.BatteryVoltage
		}
		if point.BatteryCurrent != nil {
			battery.Current = *point.BatteryCurrent
		}
		if point.BatteryPower != nil {
			battery.PowerWatts = *point.BatteryPower
		}
		if point.BatteryTemperature != nil {
			battery.Temperature = *point.BatteryTemperature
		}
		telemetry.BMS = battery
	}
	if point.MotorCount > 0 || len(point.Motors) > 0 {
		motors := &model.MotorSnapshot{Enabled: true, Source: "history", TopicOnline: point.MotorTopicOnline, SampledAt: point.At, Motors: make([]model.MotorState, len(point.Motors))}
		for index, motor := range point.Motors {
			motors.Motors[index] = model.MotorState{ID: motor.ID, Label: motor.Label, PositionRad: motor.PositionRad, VelocityRadPerSec: motor.VelocityRadPerSec, TorqueNm: motor.TorqueNm}
		}
		telemetry.Motors = motors
	}
	for range point.DiagnosticCount {
		telemetry.Errors = append(telemetry.Errors, model.ComponentError{Component: "historical", At: point.At})
	}
	return telemetry, true
}

func clampPercent(value float64) float64 {
	if value < 0 {
		return 0
	}
	if value > 100 {
		return 100
	}
	return value
}

func (s *Store) Close() error {
	var first error
	if s.control != nil {
		if err := s.control.Close(); err != nil {
			first = err
		}
	}
	if s.tsdb != nil {
		if err := s.tsdb.Close(); err != nil && first == nil {
			first = err
		}
	}
	return first
}

func (s *Store) PutTelemetry(telemetry model.Telemetry) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	now := time.Now().UTC()
	identity := telemetry.Robot
	_, err := s.control.Exec(`INSERT INTO robots(uuid, code, model, hostname, os, arch, agent_version, first_seen, last_seen) VALUES(?, ?, ?, ?, ?, ?, ?, ?, ?) ON CONFLICT(uuid) DO UPDATE SET code=excluded.code, model=excluded.model, hostname=excluded.hostname, os=excluded.os, arch=excluded.arch, agent_version=excluded.agent_version, last_seen=excluded.last_seen`, identity.UUID, identity.Code, identity.Model, identity.Hostname, identity.OS, identity.Arch, telemetry.AgentVersion, now.UnixNano(), now.UnixNano())
	if err != nil {
		return err
	}
	remark, err := s.robotSettings(identity.UUID)
	if err != nil {
		return err
	}
	record := RobotRecord{UUID: identity.UUID, Code: identity.Code, Model: identity.Model, Hostname: identity.Hostname, OS: identity.OS, Arch: identity.Arch, AgentVersion: telemetry.AgentVersion, Remark: remark, LastSeen: now, Telemetry: telemetry}
	if previous, ok := s.robots[identity.UUID]; ok {
		record.FirstSeen = previous.FirstSeen
		if telemetry.CollectedAt.Before(previous.Telemetry.CollectedAt) {
			record.Telemetry = previous.Telemetry
		}
	} else {
		record.FirstSeen = now
	}
	s.robots[identity.UUID] = record
	if err := s.insertHistory(identity.UUID, telemetry, now); err != nil {
		slog.Warn("store telemetry history", "robot_uuid", identity.UUID, "error", err)
	}
	if err := s.insertFastMotorHistory(identity.UUID, telemetry, now); err != nil {
		slog.Warn("store fast motor history", "robot_uuid", identity.UUID, "error", err)
	}
	return nil
}

func (s *Store) robotSettings(uuid string) (string, error) {
	var remark string
	err := s.control.QueryRow(`SELECT remark FROM robot_settings WHERE uuid = ?`, uuid).Scan(&remark)
	if errors.Is(err, sql.ErrNoRows) {
		return "", nil
	}
	return remark, err
}

func (s *Store) Robots() []RobotRecord {
	s.mu.RLock()
	defer s.mu.RUnlock()
	result := make([]RobotRecord, 0, len(s.robots))
	for _, robot := range s.robots {
		result = append(result, robot)
	}
	sort.Slice(result, func(i, j int) bool {
		if result[i].Code == result[j].Code {
			return result[i].UUID < result[j].UUID
		}
		return result[i].Code < result[j].Code
	})
	return result
}

func (s *Store) Robot(uuid string) (RobotRecord, bool) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	record, ok := s.robots[uuid]
	return record, ok
}

func (s *Store) LatestMotorSampleAt(uuid string) (time.Time, bool) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	record, ok := s.robots[uuid]
	if !ok || record.Telemetry.Motors == nil {
		return time.Time{}, false
	}
	var latest time.Time
	for _, sample := range record.Telemetry.Motors.Samples {
		if sample.At.After(latest) {
			latest = sample.At
		}
	}
	return latest, !latest.IsZero()
}

func (s *Store) SetRemark(uuid, remark string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	_, err := s.control.Exec(`INSERT INTO robot_settings(uuid, remark) VALUES(?, ?) ON CONFLICT(uuid) DO UPDATE SET remark=excluded.remark`, uuid, remark)
	if err != nil {
		return err
	}
	if robot, ok := s.robots[uuid]; ok {
		robot.Remark = remark
		s.robots[uuid] = robot
	}
	return nil
}

func (s *Store) RemoveRobot(uuid string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if _, ok := s.robots[uuid]; !ok {
		return os.ErrNotExist
	}
	if _, err := s.control.Exec(`DELETE FROM robots WHERE uuid = ?`, uuid); err != nil {
		return err
	}
	if _, err := s.control.Exec(`DELETE FROM robot_settings WHERE uuid = ?`, uuid); err != nil {
		return err
	}
	if err := s.tsdb.DeleteRobot(uuid); err != nil {
		return err
	}
	delete(s.lastHistory, uuid)
	delete(s.robots, uuid)
	return nil
}

func (s *Store) insertHistory(uuid string, telemetry model.Telemetry, receivedAt time.Time) error {
	point := makeHistoryPoint(telemetry)
	if previous, ok := s.lastHistory[uuid]; ok && !point.At.Before(previous) && point.At.Sub(previous) < s.historyEvery {
		return nil
	}
	if err := s.tsdb.WriteHost(uuid, point); err != nil {
		return err
	}
	if point.At.After(s.lastHistory[uuid]) {
		s.lastHistory[uuid] = point.At
	}
	return nil
}

func (s *Store) insertFastMotorHistory(uuid string, telemetry model.Telemetry, receivedAt time.Time) error {
	if telemetry.Motors == nil || len(telemetry.Motors.Samples) == 0 {
		return nil
	}
	return s.tsdb.WriteMotorSamples(uuid, telemetry.Motors.Samples)
}

func makeHistoryPoint(telemetry model.Telemetry) HistoryPoint {
	at := telemetry.CollectedAt
	if at.IsZero() {
		at = time.Now().UTC()
	}
	point := HistoryPoint{At: at, DiagnosticCount: len(telemetry.Errors)}
	if system := telemetry.System; system != nil {
		cpu := system.CPUUsagePercent
		memory := percent(system.MemoryUsedBytes, system.MemoryTotalBytes)
		disk := float64(0)
		if len(system.Disks) > 0 {
			disk = percent(system.Disks[0].UsedBytes, system.Disks[0].TotalBytes)
		}
		load := system.Load1
		point.CPUPercent, point.MemoryPercent, point.DiskPercent, point.Load1 = &cpu, &memory, &disk, &load
		for _, temperature := range system.Temperatures {
			if point.TemperatureMax == nil || temperature.Celsius > *point.TemperatureMax {
				value := temperature.Celsius
				point.TemperatureMax = &value
			}
		}
	}
	if len(telemetry.GPUs) > 0 {
		utilization, temperature := telemetry.GPUs[0].UtilizationPercent, telemetry.GPUs[0].TemperatureCelsius
		point.GPUUtilization, point.GPUTemperature = &utilization, &temperature
	}
	if battery := telemetry.BMS; battery != nil {
		soc, voltage, current, power, temperature := battery.SOCPercent, battery.Voltage, battery.Current, battery.PowerWatts, battery.Temperature
		point.BatterySOC, point.BatteryVoltage, point.BatteryCurrent, point.BatteryPower, point.BatteryTemperature = &soc, &voltage, &current, &power, &temperature
	}
	if motors := telemetry.Motors; motors != nil {
		point.MotorCount, point.MotorTopicOnline = len(motors.Motors), motors.TopicOnline
		point.Motors = make([]MotorHistoryPoint, 0, len(motors.Motors))
		for _, motor := range motors.Motors {
			velocity := motor.VelocityRadPerSec
			point.Motors = append(point.Motors, MotorHistoryPoint{ID: motor.ID, Label: motor.Label, PositionRad: motor.PositionRad, VelocityRadPerSec: velocity, TorqueNm: motor.TorqueNm})
		}
	}
	return point
}

func (s *Store) History(uuid string, from, to time.Time, limit int) ([]HistoryPoint, error) {
	if limit <= 0 || limit > historyPointLimit {
		limit = 1440
	}
	return s.tsdb.History(uuid, from, to, limit)
}

func (s *Store) HostHistory(uuid string, from, to time.Time, limit int) ([]HistoryPoint, error) {
	if limit <= 0 || limit > historyPointLimit {
		limit = 1440
	}
	return s.tsdb.hostHistory(uuid, from, to, limit, false)
}

func (s *Store) FastMotorHistory(uuid string, from, to time.Time, limit int) ([]HistoryPoint, error) {
	return s.fastMotorHistory(uuid, from, to, limit, "")
}

func (s *Store) fastMotorHistory(uuid string, from, to time.Time, limit int, motorID string) ([]HistoryPoint, error) {
	if limit <= 0 || limit > 64_000 {
		limit = 32_000
	}
	points, err := s.tsdb.fastMotorHistory(uuid, from, to, limit, motorID)
	if err != nil {
		return nil, err
	}
	labels := make(map[string]string)
	s.mu.RLock()
	if record, ok := s.robots[uuid]; ok && record.Telemetry.Motors != nil {
		for _, motor := range record.Telemetry.Motors.Motors {
			labels[motor.ID] = motor.Label
		}
	}
	s.mu.RUnlock()
	for pointIndex := range points {
		for motorIndex := range points[pointIndex].Motors {
			if label := labels[points[pointIndex].Motors[motorIndex].ID]; label != "" {
				points[pointIndex].Motors[motorIndex].Label = label
			}
		}
	}
	return points, nil
}

func (s *Store) FastMotorHistoryFiltered(uuid string, from, to time.Time, limit int, motorID string) ([]HistoryPoint, error) {
	return s.fastMotorHistory(uuid, from, to, limit, motorID)
}

func (s *Store) AuthenticateAdmin(username, password string) (bool, bool, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	var hash string
	var force int
	err := s.control.QueryRow(`SELECT password_hash, force_change FROM admin_accounts WHERE username = ?`, username).Scan(&hash, &force)
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return false, false, nil
		}
		return false, false, err
	}
	if err := bcrypt.CompareHashAndPassword([]byte(hash), []byte(password)); err != nil {
		return false, false, nil
	}
	return true, force != 0, nil
}

func (s *Store) ChangeAdminPassword(username, current, next string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	var hash string
	if err := s.control.QueryRow(`SELECT password_hash FROM admin_accounts WHERE username = ?`, username).Scan(&hash); err != nil {
		return err
	}
	if err := bcrypt.CompareHashAndPassword([]byte(hash), []byte(current)); err != nil {
		return errors.New("current password is incorrect")
	}
	newHash, err := bcrypt.GenerateFromPassword([]byte(next), bcrypt.DefaultCost)
	if err != nil {
		return err
	}
	_, err = s.control.Exec(`UPDATE admin_accounts SET password_hash = ?, force_change = 0 WHERE username = ?`, string(newHash), username)
	return err
}

func (s *Store) PasswordChangeRequired(username string) (bool, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	var force int
	err := s.control.QueryRow(`SELECT force_change FROM admin_accounts WHERE username = ?`, username).Scan(&force)
	if errors.Is(err, sql.ErrNoRows) {
		return true, nil
	}
	return force != 0, err
}

func boolInt(value bool) int {
	if value {
		return 1
	}
	return 0
}

func unixNano(value int64) time.Time {
	if value == 0 {
		return time.Time{}
	}
	return time.Unix(0, value).UTC()
}
