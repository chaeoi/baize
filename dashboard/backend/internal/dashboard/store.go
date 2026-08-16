package dashboard

import (
	"bytes"
	"compress/zlib"
	"crypto/rand"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	"baize/shared/model"
	"golang.org/x/crypto/bcrypt"
	_ "modernc.org/sqlite"
)

type Release struct {
	ID         string    `json:"id"`
	Version    string    `json:"version"`
	OS         string    `json:"os"`
	Arch       string    `json:"arch"`
	SHA256     string    `json:"sha256"`
	Size       int64     `json:"size"`
	UploadedAt time.Time `json:"uploaded_at"`
	Filename   string    `json:"-"`
}

type RobotRecord struct {
	UUID           string          `json:"uuid"`
	Code           string          `json:"code"`
	Model          string          `json:"model"`
	Hostname       string          `json:"hostname"`
	OS             string          `json:"os"`
	Arch           string          `json:"arch"`
	AgentVersion   string          `json:"agent_version"`
	Remark         string          `json:"remark"`
	DesiredVersion string          `json:"desired_version,omitempty"`
	FirstSeen      time.Time       `json:"first_seen"`
	LastSeen       time.Time       `json:"last_seen"`
	Telemetry      model.Telemetry `json:"telemetry"`
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
	mu              sync.RWMutex
	dataDir         string
	historyDir      string
	control         *sql.DB
	history         *sql.DB
	robots          map[string]RobotRecord
	historyEvery    time.Duration
	historyKeep     time.Duration
	fastHistoryKeep time.Duration
	lastHistoryGC   time.Time
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
		options.HistorySampleInterval = time.Minute
	}
	if err := os.MkdirAll(filepath.Join(dataDir, "releases"), 0o750); err != nil {
		return nil, err
	}
	if err := os.MkdirAll(historyDir, 0o750); err != nil {
		return nil, err
	}
	control, err := openSQLite(filepath.Join(dataDir, "control.db"))
	if err != nil {
		return nil, fmt.Errorf("open control database: %w", err)
	}
	history, err := openSQLite(filepath.Join(historyDir, "history.db"))
	if err != nil {
		_ = control.Close()
		return nil, fmt.Errorf("open history database: %w", err)
	}
	store := &Store{dataDir: dataDir, historyDir: historyDir, control: control, history: history, robots: make(map[string]RobotRecord), historyEvery: options.HistorySampleInterval, historyKeep: options.HistoryRetention, fastHistoryKeep: 2 * time.Minute}
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
  remark TEXT NOT NULL DEFAULT '',
  desired_version TEXT NOT NULL DEFAULT ''
);
CREATE TABLE IF NOT EXISTS releases (
  id TEXT PRIMARY KEY,
  version TEXT NOT NULL,
  os TEXT NOT NULL,
  arch TEXT NOT NULL,
  sha256 TEXT NOT NULL,
  size INTEGER NOT NULL,
  uploaded_at INTEGER NOT NULL
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
	historySchema := `
CREATE TABLE IF NOT EXISTS telemetry_samples (
  id INTEGER PRIMARY KEY AUTOINCREMENT,
  robot_uuid TEXT NOT NULL,
  at INTEGER NOT NULL,
  received_at INTEGER NOT NULL,
  cpu_percent REAL,
  memory_percent REAL,
  disk_percent REAL,
  load_1 REAL,
  temperature_max REAL,
  gpu_utilization REAL,
  gpu_temperature REAL,
  battery_soc REAL,
  battery_voltage REAL,
  battery_current REAL,
  battery_power REAL,
  battery_temperature REAL,
  motor_count INTEGER NOT NULL DEFAULT 0,
  motor_topic_online INTEGER NOT NULL DEFAULT 0,
  diagnostic_count INTEGER NOT NULL DEFAULT 0,
  motors_json BLOB
);
CREATE INDEX IF NOT EXISTS telemetry_samples_robot_time_idx ON telemetry_samples(robot_uuid, received_at);`
	fastHistorySchema := `
CREATE TABLE IF NOT EXISTS motor_samples (
  id INTEGER PRIMARY KEY AUTOINCREMENT,
  robot_uuid TEXT NOT NULL,
  at INTEGER NOT NULL,
  received_at INTEGER NOT NULL,
  motor_count INTEGER NOT NULL DEFAULT 0,
  motors_json BLOB NOT NULL
);
CREATE INDEX IF NOT EXISTS motor_samples_robot_time_idx ON motor_samples(robot_uuid, received_at);`
	fastBatchSchema := `
CREATE TABLE IF NOT EXISTS motor_sample_batches (
  id INTEGER PRIMARY KEY AUTOINCREMENT,
  robot_uuid TEXT NOT NULL,
  started_at INTEGER NOT NULL,
  ended_at INTEGER NOT NULL,
  received_at INTEGER NOT NULL,
  sample_count INTEGER NOT NULL,
  samples_json BLOB NOT NULL
);
CREATE INDEX IF NOT EXISTS motor_sample_batches_robot_time_idx ON motor_sample_batches(robot_uuid, started_at, ended_at);`
	if _, err := s.control.Exec(controlSchema); err != nil {
		return fmt.Errorf("migrate control database: %w", err)
	}
	if _, err := s.history.Exec(historySchema); err != nil {
		return fmt.Errorf("migrate history database: %w", err)
	}
	if _, err := s.history.Exec(fastHistorySchema); err != nil {
		return fmt.Errorf("migrate fast motor history: %w", err)
	}
	if _, err := s.history.Exec(fastBatchSchema); err != nil {
		return fmt.Errorf("migrate fast motor batch history: %w", err)
	}
	if err := ensureColumn(s.history, "telemetry_samples", "motors_json", "BLOB"); err != nil {
		return fmt.Errorf("migrate motor history: %w", err)
	}
	return nil
}

func ensureColumn(db *sql.DB, table, column, declaration string) error {
	rows, err := db.Query(`PRAGMA table_info(` + table + `)`)
	if err != nil {
		return err
	}
	found := false
	for rows.Next() {
		var cid int
		var name, columnType string
		var notNull, primaryKey int
		var defaultValue any
		if err := rows.Scan(&cid, &name, &columnType, &notNull, &defaultValue, &primaryKey); err != nil {
			rows.Close()
			return err
		}
		if name == column {
			found = true
		}
	}
	if err := rows.Close(); err != nil {
		return err
	}
	if found {
		return nil
	}
	_, err = db.Exec(`ALTER TABLE ` + table + ` ADD COLUMN ` + column + ` ` + declaration)
	return err
}

type legacyState struct {
	Remarks  map[string]string  `json:"remarks"`
	Desired  map[string]string  `json:"desired"`
	Releases map[string]Release `json:"releases"`
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
	for uuid, desired := range legacy.Desired {
		if _, err := tx.Exec(`INSERT INTO robot_settings(uuid, desired_version) VALUES(?, ?) ON CONFLICT(uuid) DO UPDATE SET desired_version=excluded.desired_version`, uuid, desired); err != nil {
			return err
		}
	}
	for id, release := range legacy.Releases {
		if !safeReleaseID(id) {
			continue
		}
		if !legacyReleaseExists(s, id) {
			continue
		}
		if err := s.migrateLegacyRelease(id); err != nil {
			return err
		}
		if _, err := tx.Exec(`INSERT OR IGNORE INTO releases(id, version, os, arch, sha256, size, uploaded_at) VALUES(?, ?, ?, ?, ?, ?, ?)`, id, release.Version, release.OS, release.Arch, release.SHA256, release.Size, release.UploadedAt.UnixNano()); err != nil {
			return err
		}
	}
	if _, err := tx.Exec(`INSERT INTO control_meta(key, value) VALUES('legacy_state_imported', '1')`); err != nil {
		return err
	}
	return tx.Commit()
}

func legacyReleaseExists(s *Store, id string) bool {
	for _, path := range []string{filepath.Join(s.dataDir, "releases", id), filepath.Join(filepath.Dir(s.dataDir), "releases", id)} {
		if _, err := os.Stat(path); err == nil {
			return true
		}
	}
	return false
}

func safeReleaseID(id string) bool {
	return id != "" && id != "." && id != ".." && len(id) <= 200 && filepath.Base(id) == id && !strings.ContainsAny(id, "\x00\r\n")
}

func (s *Store) migrateLegacyRelease(id string) error {
	destination := filepath.Join(s.dataDir, "releases", id)
	if _, err := os.Stat(destination); err == nil {
		return nil
	} else if !errors.Is(err, os.ErrNotExist) {
		return err
	}
	source := filepath.Join(filepath.Dir(s.dataDir), "releases", id)
	input, err := os.Open(source)
	if errors.Is(err, os.ErrNotExist) {
		return fmt.Errorf("legacy release %s is missing", id)
	}
	if err != nil {
		return err
	}
	defer input.Close()
	temporary, err := os.CreateTemp(filepath.Dir(destination), ".legacy-release-*")
	if err != nil {
		return err
	}
	temporaryPath := temporary.Name()
	defer os.Remove(temporaryPath)
	if _, err := io.Copy(temporary, input); err != nil {
		temporary.Close()
		return err
	}
	if err := temporary.Sync(); err != nil {
		temporary.Close()
		return err
	}
	if err := temporary.Chmod(0o640); err != nil {
		temporary.Close()
		return err
	}
	if err := temporary.Close(); err != nil {
		return err
	}
	return os.Rename(temporaryPath, destination)
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
	rows, err := s.control.Query(`SELECT r.uuid, r.code, r.model, r.hostname, r.os, r.arch, r.agent_version, COALESCE(s.remark, ''), COALESCE(s.desired_version, ''), r.first_seen, r.last_seen FROM robots r LEFT JOIN robot_settings s ON s.uuid = r.uuid`)
	if err != nil {
		return err
	}
	defer rows.Close()
	for rows.Next() {
		var record RobotRecord
		var firstSeen, lastSeen int64
		if err := rows.Scan(&record.UUID, &record.Code, &record.Model, &record.Hostname, &record.OS, &record.Arch, &record.AgentVersion, &record.Remark, &record.DesiredVersion, &firstSeen, &lastSeen); err != nil {
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
	if s.history != nil {
		if err := s.history.Close(); err != nil && first == nil {
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
	remark, desired, err := s.robotSettings(identity.UUID)
	if err != nil {
		return err
	}
	record := RobotRecord{UUID: identity.UUID, Code: identity.Code, Model: identity.Model, Hostname: identity.Hostname, OS: identity.OS, Arch: identity.Arch, AgentVersion: telemetry.AgentVersion, Remark: remark, DesiredVersion: desired, LastSeen: now, Telemetry: telemetry}
	if previous, ok := s.robots[identity.UUID]; ok {
		record.FirstSeen = previous.FirstSeen
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

func (s *Store) robotSettings(uuid string) (string, string, error) {
	var remark, desired string
	err := s.control.QueryRow(`SELECT remark, desired_version FROM robot_settings WHERE uuid = ?`, uuid).Scan(&remark, &desired)
	if errors.Is(err, sql.ErrNoRows) {
		return "", "", nil
	}
	return remark, desired, err
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

func (s *Store) SetDesired(uuid, version string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	_, err := s.control.Exec(`INSERT INTO robot_settings(uuid, desired_version) VALUES(?, ?) ON CONFLICT(uuid) DO UPDATE SET desired_version=excluded.desired_version`, uuid, version)
	if err != nil {
		return err
	}
	if robot, ok := s.robots[uuid]; ok {
		robot.DesiredVersion = version
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
	if _, err := s.history.Exec(`DELETE FROM telemetry_samples WHERE robot_uuid = ?`, uuid); err != nil {
		return err
	}
	if _, err := s.history.Exec(`DELETE FROM motor_samples WHERE robot_uuid = ?`, uuid); err != nil {
		return err
	}
	if _, err := s.history.Exec(`DELETE FROM motor_sample_batches WHERE robot_uuid = ?`, uuid); err != nil {
		return err
	}
	delete(s.robots, uuid)
	return nil
}

func (s *Store) AddRelease(release Release) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	_, err := s.control.Exec(`INSERT INTO releases(id, version, os, arch, sha256, size, uploaded_at) VALUES(?, ?, ?, ?, ?, ?, ?) ON CONFLICT(id) DO UPDATE SET version=excluded.version, os=excluded.os, arch=excluded.arch, sha256=excluded.sha256, size=excluded.size, uploaded_at=excluded.uploaded_at`, release.ID, release.Version, release.OS, release.Arch, release.SHA256, release.Size, release.UploadedAt.UnixNano())
	return err
}

func (s *Store) DeleteRelease(id string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	release, ok := s.releaseByIDLocked(id)
	if !ok {
		return os.ErrNotExist
	}
	var assigned int
	if err := s.control.QueryRow(`SELECT COUNT(*) FROM robot_settings s JOIN robots r ON r.uuid = s.uuid WHERE s.desired_version = ? AND r.os = ? AND r.arch = ?`, release.Version, release.OS, release.Arch).Scan(&assigned); err != nil {
		return err
	}
	if assigned > 0 {
		return errors.New("release is assigned to a robot")
	}
	if err := os.Remove(release.Filename); err != nil && !errors.Is(err, os.ErrNotExist) {
		return err
	}
	_, err := s.control.Exec(`DELETE FROM releases WHERE id = ?`, id)
	return err
}

func (s *Store) Releases() []Release {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.releasesLocked()
}

func (s *Store) releasesLocked() []Release {
	rows, err := s.control.Query(`SELECT id, version, os, arch, sha256, size, uploaded_at FROM releases`)
	if err != nil {
		slog.Warn("list releases", "error", err)
		return nil
	}
	defer rows.Close()
	result := make([]Release, 0)
	for rows.Next() {
		var release Release
		var uploadedAt int64
		if err := rows.Scan(&release.ID, &release.Version, &release.OS, &release.Arch, &release.SHA256, &release.Size, &uploadedAt); err != nil {
			continue
		}
		release.UploadedAt = unixNano(uploadedAt)
		release.Filename = filepath.Join(s.dataDir, "releases", release.ID)
		result = append(result, release)
	}
	sort.Slice(result, func(i, j int) bool {
		comparison := compareVersions(result[i].Version, result[j].Version)
		if comparison == 0 {
			return result[i].UploadedAt.After(result[j].UploadedAt)
		}
		return comparison > 0
	})
	return result
}

func (s *Store) ReleaseByID(id string) (Release, bool) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.releaseByIDLocked(id)
}

func (s *Store) releaseByIDLocked(id string) (Release, bool) {
	var release Release
	var uploadedAt int64
	err := s.control.QueryRow(`SELECT id, version, os, arch, sha256, size, uploaded_at FROM releases WHERE id = ?`, id).Scan(&release.ID, &release.Version, &release.OS, &release.Arch, &release.SHA256, &release.Size, &uploadedAt)
	if err != nil {
		return Release{}, false
	}
	release.UploadedAt = unixNano(uploadedAt)
	release.Filename = filepath.Join(s.dataDir, "releases", release.ID)
	return release, true
}

func (s *Store) FindUpdate(uuid, current, goos, arch string, automatic bool) (Release, bool) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	var desired string
	_ = s.control.QueryRow(`SELECT desired_version FROM robot_settings WHERE uuid = ?`, uuid).Scan(&desired)
	if desired != "" && desired != current {
		for _, release := range s.releasesLocked() {
			if release.Version == desired && release.OS == goos && release.Arch == arch {
				return release, true
			}
		}
	}
	if !automatic {
		return Release{}, false
	}
	var best Release
	found := false
	for _, release := range s.releasesLocked() {
		if release.OS != goos || release.Arch != arch || compareVersions(release.Version, current) <= 0 {
			continue
		}
		if !found || compareVersions(release.Version, best.Version) > 0 {
			best, found = release, true
		}
	}
	return best, found
}

func (s *Store) insertHistory(uuid string, telemetry model.Telemetry, receivedAt time.Time) error {
	var previous sql.NullInt64
	if err := s.history.QueryRow(`SELECT received_at FROM telemetry_samples WHERE robot_uuid = ? ORDER BY received_at DESC LIMIT 1`, uuid).Scan(&previous); err != nil && !errors.Is(err, sql.ErrNoRows) {
		return err
	}
	if previous.Valid && receivedAt.Sub(time.Unix(0, previous.Int64)) < s.historyEvery {
		return nil
	}
	point := makeHistoryPoint(telemetry)
	motorsJSON, err := encodeMotorHistory(point.Motors)
	if err != nil {
		return err
	}
	values := []any{uuid, point.At.UnixNano(), receivedAt.UnixNano(), point.CPUPercent, point.MemoryPercent, point.DiskPercent, point.Load1, point.TemperatureMax, point.GPUUtilization, point.GPUTemperature, point.BatterySOC, point.BatteryVoltage, point.BatteryCurrent, point.BatteryPower, point.BatteryTemperature, point.MotorCount, boolInt(point.MotorTopicOnline), point.DiagnosticCount, motorsJSON}
	_, err = s.history.Exec(`INSERT INTO telemetry_samples(robot_uuid, at, received_at, cpu_percent, memory_percent, disk_percent, load_1, temperature_max, gpu_utilization, gpu_temperature, battery_soc, battery_voltage, battery_current, battery_power, battery_temperature, motor_count, motor_topic_online, diagnostic_count, motors_json) VALUES(?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`, values...)
	if err != nil {
		return err
	}
	if time.Since(s.lastHistoryGC) > time.Hour {
		s.lastHistoryGC = receivedAt
		cutoff := receivedAt.Add(-s.historyKeep).UnixNano()
		_, _ = s.history.Exec(`DELETE FROM telemetry_samples WHERE received_at < ?`, cutoff)
	}
	return nil
}

func (s *Store) insertFastMotorHistory(uuid string, telemetry model.Telemetry, receivedAt time.Time) error {
	if telemetry.Motors == nil || len(telemetry.Motors.Samples) == 0 {
		return nil
	}
	startedAt, endedAt := telemetry.Motors.Samples[0].At, telemetry.Motors.Samples[0].At
	for _, sample := range telemetry.Motors.Samples[1:] {
		if sample.At.Before(startedAt) {
			startedAt = sample.At
		}
		if sample.At.After(endedAt) {
			endedAt = sample.At
		}
	}
	samplesJSON, err := encodeMotorSampleBatch(telemetry.Motors.Samples)
	if err != nil {
		return err
	}
	tx, err := s.history.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()
	if _, err := tx.Exec(`INSERT INTO motor_sample_batches(robot_uuid, started_at, ended_at, received_at, sample_count, samples_json) VALUES(?, ?, ?, ?, ?, ?)`, uuid, startedAt.UnixNano(), endedAt.UnixNano(), receivedAt.UnixNano(), len(telemetry.Motors.Samples), samplesJSON); err != nil {
		return err
	}
	cutoff := receivedAt.Add(-s.fastHistoryKeep).UnixNano()
	if _, err := tx.Exec(`DELETE FROM motor_sample_batches WHERE robot_uuid = ? AND received_at < ?`, uuid, cutoff); err != nil {
		return err
	}
	return tx.Commit()
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
			if velocity == 0 && motor.VelocityRPS != 0 {
				velocity = motor.VelocityRPS
			}
			point.Motors = append(point.Motors, MotorHistoryPoint{ID: motor.ID, Label: motor.Label, PositionRad: motor.PositionRad, VelocityRadPerSec: velocity, TorqueNm: motor.TorqueNm})
		}
	}
	return point
}

func (s *Store) History(uuid string, from, to time.Time, limit int) ([]HistoryPoint, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	if limit <= 0 || limit > 5000 {
		limit = 1440
	}
	rows, err := s.history.Query(`SELECT at, cpu_percent, memory_percent, disk_percent, load_1, temperature_max, gpu_utilization, gpu_temperature, battery_soc, battery_voltage, battery_current, battery_power, battery_temperature, motor_count, motor_topic_online, diagnostic_count, motors_json FROM (SELECT at, received_at, cpu_percent, memory_percent, disk_percent, load_1, temperature_max, gpu_utilization, gpu_temperature, battery_soc, battery_voltage, battery_current, battery_power, battery_temperature, motor_count, motor_topic_online, diagnostic_count, motors_json FROM telemetry_samples WHERE robot_uuid = ? AND received_at >= ? AND received_at <= ? ORDER BY received_at DESC LIMIT ?) ORDER BY received_at ASC`, uuid, from.UnixNano(), to.UnixNano(), limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	points := make([]HistoryPoint, 0)
	for rows.Next() {
		var point HistoryPoint
		var at int64
		var cpu, memory, disk, load, temp, gpuUtil, gpuTemp, soc, voltage, current, power, batteryTemp sql.NullFloat64
		var motorCount, online, diagnostics int
		var motorsJSON []byte
		if err := rows.Scan(&at, &cpu, &memory, &disk, &load, &temp, &gpuUtil, &gpuTemp, &soc, &voltage, &current, &power, &batteryTemp, &motorCount, &online, &diagnostics, &motorsJSON); err != nil {
			return nil, err
		}
		point.At = unixNano(at)
		point.CPUPercent, point.MemoryPercent, point.DiskPercent, point.Load1 = nullableFloat(cpu), nullableFloat(memory), nullableFloat(disk), nullableFloat(load)
		point.TemperatureMax, point.GPUUtilization, point.GPUTemperature = nullableFloat(temp), nullableFloat(gpuUtil), nullableFloat(gpuTemp)
		point.BatterySOC, point.BatteryVoltage, point.BatteryCurrent, point.BatteryPower, point.BatteryTemperature = nullableFloat(soc), nullableFloat(voltage), nullableFloat(current), nullableFloat(power), nullableFloat(batteryTemp)
		point.MotorCount, point.MotorTopicOnline, point.DiagnosticCount = motorCount, online != 0, diagnostics
		if len(motorsJSON) > 0 {
			if err := decodeMotorHistory(motorsJSON, &point.Motors); err != nil {
				return nil, fmt.Errorf("decode motor history: %w", err)
			}
		}
		points = append(points, point)
	}
	return points, rows.Err()
}

func (s *Store) FastMotorHistory(uuid string, from, to time.Time, limit int) ([]HistoryPoint, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	if limit <= 0 || limit > 64_000 {
		limit = 32_000
	}
	rows, err := s.history.Query(`SELECT samples_json FROM motor_sample_batches WHERE robot_uuid = ? AND ended_at >= ? AND started_at <= ? ORDER BY started_at ASC`, uuid, from.UnixNano(), to.UnixNano())
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	points := make([]HistoryPoint, 0)
	for rows.Next() {
		var samplesJSON []byte
		if err := rows.Scan(&samplesJSON); err != nil {
			return nil, err
		}
		batch, err := decodeMotorSampleBatch(samplesJSON)
		if err != nil {
			return nil, fmt.Errorf("decode fast motor batch history: %w", err)
		}
		for _, point := range batch {
			if !point.At.Before(from) && !point.At.After(to) {
				points = append(points, point)
			}
		}
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	sort.SliceStable(points, func(i, j int) bool { return points[i].At.Before(points[j].At) })
	if len(points) > limit {
		points = points[len(points)-limit:]
	}
	return points, nil
}

type compactMotorHistory struct {
	ID       string  `json:"i"`
	Label    string  `json:"l,omitempty"`
	Position float64 `json:"p"`
	Velocity float64 `json:"v"`
	Torque   float64 `json:"t"`
}

type compactMotorSample struct {
	At     int64                 `json:"a"`
	Motors []compactMotorHistory `json:"m,omitempty"`
}

func encodeMotorSampleBatch(samples []model.MotorSample) ([]byte, error) {
	compact := make([]compactMotorSample, len(samples))
	for index, sample := range samples {
		motors := make([]compactMotorHistory, len(sample.Motors))
		for motorIndex, motor := range sample.Motors {
			motors[motorIndex] = compactMotorHistory{ID: motor.ID, Label: motor.Label, Position: motor.PositionRad, Velocity: motor.VelocityRadPerSec, Torque: motor.TorqueNm}
		}
		compact[index] = compactMotorSample{At: sample.At.UnixNano(), Motors: motors}
	}
	data, err := json.Marshal(compact)
	if err != nil {
		return nil, err
	}
	var buffer bytes.Buffer
	writer, err := zlib.NewWriterLevel(&buffer, zlib.BestSpeed)
	if err != nil {
		return nil, err
	}
	if _, err := writer.Write(data); err != nil {
		_ = writer.Close()
		return nil, err
	}
	if err := writer.Close(); err != nil {
		return nil, err
	}
	return buffer.Bytes(), nil
}

func decodeMotorSampleBatch(data []byte) ([]HistoryPoint, error) {
	reader, err := zlib.NewReader(bytes.NewReader(data))
	if err != nil {
		return nil, err
	}
	decoded, err := io.ReadAll(io.LimitReader(reader, 64<<20))
	closeErr := reader.Close()
	if err != nil {
		return nil, err
	}
	if closeErr != nil {
		return nil, closeErr
	}
	var compact []compactMotorSample
	if err := json.Unmarshal(decoded, &compact); err != nil {
		return nil, err
	}
	points := make([]HistoryPoint, len(compact))
	for index, sample := range compact {
		motors := make([]MotorHistoryPoint, len(sample.Motors))
		for motorIndex, motor := range sample.Motors {
			motors[motorIndex] = MotorHistoryPoint{ID: motor.ID, Label: motor.Label, PositionRad: motor.Position, VelocityRadPerSec: motor.Velocity, TorqueNm: motor.Torque}
		}
		points[index] = HistoryPoint{At: unixNano(sample.At), MotorCount: len(motors), MotorTopicOnline: true, Motors: motors}
	}
	return points, nil
}

func encodeMotorHistory(motors []MotorHistoryPoint) ([]byte, error) {
	compact := make([]compactMotorHistory, len(motors))
	for index, motor := range motors {
		compact[index] = compactMotorHistory{ID: motor.ID, Label: motor.Label, Position: motor.PositionRad, Velocity: motor.VelocityRadPerSec, Torque: motor.TorqueNm}
	}
	data, err := json.Marshal(compact)
	if err != nil {
		return nil, err
	}
	var buffer bytes.Buffer
	writer, err := zlib.NewWriterLevel(&buffer, zlib.BestSpeed)
	if err != nil {
		return nil, err
	}
	if _, err := writer.Write(data); err != nil {
		writer.Close()
		return nil, err
	}
	if err := writer.Close(); err != nil {
		return nil, err
	}
	return buffer.Bytes(), nil
}

func decodeMotorHistory(data []byte, target *[]MotorHistoryPoint) error {
	if len(data) > 0 && data[0] == '[' {
		return json.Unmarshal(data, target)
	}
	reader, err := zlib.NewReader(bytes.NewReader(data))
	if err != nil {
		return err
	}
	decoded, err := io.ReadAll(io.LimitReader(reader, 4<<20))
	closeErr := reader.Close()
	if err != nil {
		return err
	}
	if closeErr != nil {
		return closeErr
	}
	var compact []compactMotorHistory
	if err := json.Unmarshal(decoded, &compact); err != nil {
		return err
	}
	*target = make([]MotorHistoryPoint, len(compact))
	for index, motor := range compact {
		(*target)[index] = MotorHistoryPoint{ID: motor.ID, Label: motor.Label, PositionRad: motor.Position, VelocityRadPerSec: motor.Velocity, TorqueNm: motor.Torque}
	}
	return nil
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

func nullableFloat(value sql.NullFloat64) *float64 {
	if !value.Valid {
		return nil
	}
	return &value.Float64
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

func compareVersions(left, right string) int {
	leftParts := versionNumbers(left)
	rightParts := versionNumbers(right)
	length := len(leftParts)
	if len(rightParts) > length {
		length = len(rightParts)
	}
	for i := 0; i < length; i++ {
		var l, r int
		if i < len(leftParts) {
			l = leftParts[i]
		}
		if i < len(rightParts) {
			r = rightParts[i]
		}
		if l < r {
			return -1
		}
		if l > r {
			return 1
		}
	}
	return strings.Compare(left, right)
}

func versionNumbers(version string) []int {
	version = strings.TrimPrefix(strings.TrimPrefix(version, "v"), "V")
	if index := strings.IndexAny(version, "-+"); index >= 0 {
		version = version[:index]
	}
	parts := strings.Split(version, ".")
	result := make([]int, len(parts))
	for i, part := range parts {
		result[i], _ = strconv.Atoi(part)
	}
	return result
}
