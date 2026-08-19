package model

import "time"

const SchemaVersion = 1

type Telemetry struct {
	SchemaVersion int              `json:"schema_version"`
	Robot         Robot            `json:"robot"`
	AgentVersion  string           `json:"agent_version"`
	CollectedAt   time.Time        `json:"collected_at"`
	System        *SystemMetrics   `json:"system,omitempty"`
	GPUs          []GPUMetrics     `json:"gpus,omitempty"`
	BMS           *BMSMetrics      `json:"bms,omitempty"`
	Motors        *MotorSnapshot   `json:"motors,omitempty"`
	Errors        []ComponentError `json:"errors,omitempty"`
}

type Robot struct {
	UUID     string `json:"uuid"`
	Code     string `json:"code"`
	Model    string `json:"model"`
	Hostname string `json:"hostname"`
	OS       string `json:"os"`
	Arch     string `json:"arch"`
}

type ComponentError struct {
	Component string    `json:"component"`
	Message   string    `json:"message"`
	At        time.Time `json:"at"`
}

type SystemMetrics struct {
	CPUModel         string        `json:"cpu_model,omitempty"`
	CPUCores         int           `json:"cpu_cores"`
	CPUUsagePercent  float64       `json:"cpu_usage_percent"`
	Load1            float64       `json:"load_1"`
	Load5            float64       `json:"load_5"`
	Load15           float64       `json:"load_15"`
	MemoryTotalBytes uint64        `json:"memory_total_bytes"`
	MemoryUsedBytes  uint64        `json:"memory_used_bytes"`
	SwapTotalBytes   uint64        `json:"swap_total_bytes"`
	SwapUsedBytes    uint64        `json:"swap_used_bytes"`
	UptimeSeconds    float64       `json:"uptime_seconds"`
	Disks            []DiskMetrics `json:"disks,omitempty"`
	Temperatures     []Temperature `json:"temperatures,omitempty"`
}

type DiskMetrics struct {
	Path       string `json:"path"`
	TotalBytes uint64 `json:"total_bytes"`
	UsedBytes  uint64 `json:"used_bytes"`
}

type Temperature struct {
	Name    string  `json:"name"`
	Celsius float64 `json:"celsius"`
}

type GPUMetrics struct {
	Index              int     `json:"index"`
	Name               string  `json:"name"`
	UtilizationPercent float64 `json:"utilization_percent"`
	MemoryTotalBytes   uint64  `json:"memory_total_bytes"`
	MemoryUsedBytes    uint64  `json:"memory_used_bytes"`
	TemperatureCelsius float64 `json:"temperature_celsius"`
	PowerWatts         float64 `json:"power_watts"`
}

type BMSMetrics struct {
	Enabled           bool      `json:"enabled"`
	Online            bool      `json:"online"`
	Present           bool      `json:"present"`
	Protocol          string    `json:"protocol"`
	Interface         string    `json:"interface"`
	Voltage           float64   `json:"voltage"`
	Current           float64   `json:"current"`
	Temperature       float64   `json:"temperature"`
	SOCPercent        float64   `json:"soc_percent"`
	PowerSupplyStatus string    `json:"power_supply_status"`
	LastFrameAt       time.Time `json:"last_frame_at,omitempty"`
	PowerWatts        float64   `json:"power_watts,omitempty"`
}

type MotorSnapshot struct {
	Enabled                 bool          `json:"enabled"`
	Source                  string        `json:"source"`
	Topic                   string        `json:"topic"`
	TopicOnline             bool          `json:"topic_online"`
	PerMotorOnlineSupported bool          `json:"per_motor_online_supported"`
	TemperatureSupported    bool          `json:"temperature_supported"`
	SampledAt               time.Time     `json:"sampled_at,omitempty"`
	Motors                  []MotorState  `json:"items,omitempty"`
	Samples                 []MotorSample `json:"samples,omitempty"`
	SampleRateHz            float64       `json:"sample_rate_hz,omitempty"`
}

// MotorSample is a compact, short-window sample used for high-rate charts.
// It deliberately omits driver metadata because that data is stable in the
// latest MotorState and does not belong in every high-frequency frame.
type MotorSample struct {
	At     time.Time          `json:"at"`
	Motors []MotorSampleState `json:"motors,omitempty"`
}

type MotorSampleState struct {
	ID                string  `json:"id"`
	Label             string  `json:"label,omitempty"`
	PositionRad       float64 `json:"position_rad"`
	VelocityRadPerSec float64 `json:"velocity_rad_per_sec"`
	TorqueNm          float64 `json:"torque_nm"`
}

type MotorState struct {
	ID                string  `json:"id"`
	Label             string  `json:"label,omitempty"`
	PositionRad       float64 `json:"position_rad"`
	VelocityRadPerSec float64 `json:"velocity_rad_per_sec"`
	// VelocityRPS is accepted for compatibility with agents before the unit name was corrected.
	VelocityRPS  float64 `json:"velocity_rps,omitempty"`
	TorqueNm     float64 `json:"torque_nm"`
	Brand        string  `json:"brand,omitempty"`
	Model        string  `json:"model,omitempty"`
	CANInterface string  `json:"can_interface,omitempty"`
	ControlMode  string  `json:"control_mode,omitempty"`
	VirtualJoint bool    `json:"virtual_joint,omitempty"`
}

type UpdateInfo struct {
	Version string `json:"version"`
	OS      string `json:"os"`
	Arch    string `json:"arch"`
	SHA256  string `json:"sha256"`
	Size    int64  `json:"size"`
	URL     string `json:"url"`
}
