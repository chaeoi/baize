package dashboard

import (
	"errors"
	"flag"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"sync"
	"time"

	"baize/shared/model"
	"github.com/VictoriaMetrics/VictoriaMetrics/lib/prompb"
	"github.com/VictoriaMetrics/VictoriaMetrics/lib/storage"
)

const fastMotorRetention = 2 * time.Minute
const tsdbCacheMemory = "256MiB"

const (
	metricHostCPU                = "baize_host_cpu_percent"
	metricHostMemory             = "baize_host_memory_percent"
	metricHostDisk               = "baize_host_disk_percent"
	metricHostLoad1              = "baize_host_load_1"
	metricHostTemperature        = "baize_host_temperature_max"
	metricHostGPUUtilization     = "baize_host_gpu_utilization_percent"
	metricHostGPUTemperature     = "baize_host_gpu_temperature_celsius"
	metricHostBatterySOC         = "baize_host_battery_soc_percent"
	metricHostBatteryVoltage     = "baize_host_battery_voltage"
	metricHostBatteryCurrent     = "baize_host_battery_current"
	metricHostBatteryPower       = "baize_host_battery_power_watts"
	metricHostBatteryTemperature = "baize_host_battery_temperature"
	metricHostMotorCount         = "baize_host_motor_count"
	metricHostMotorOnline        = "baize_host_motor_topic_online"
	metricHostDiagnosticCount    = "baize_host_diagnostic_count"
	metricHostMotorPosition      = "baize_host_motor_position_rad"
	metricHostMotorVelocity      = "baize_host_motor_velocity_rad_per_sec"
	metricHostMotorTorque        = "baize_host_motor_torque_nm"

	metricMotorPosition = "baize_motor_position_rad"
	metricMotorVelocity = "baize_motor_velocity_rad_per_sec"
	metricMotorTorque   = "baize_motor_torque_nm"
)

var telemetryTSDBSettings sync.Once

type telemetryTSDB struct {
	mu     sync.RWMutex
	host   *storage.Storage
	motor  *storage.Storage
	closed bool
}

func openTelemetryTSDB(path string, retention time.Duration) (db *telemetryTSDB, err error) {
	if retention <= 0 {
		retention = 90 * 24 * time.Hour
	}
	if err := os.MkdirAll(path, 0o750); err != nil {
		return nil, err
	}
	if err := setTelemetryTSDBMemoryLimit(); err != nil {
		return nil, err
	}
	telemetryTSDBSettings.Do(func() {
		storage.SetTSIDCacheSize(16 << 20)
		storage.SetMetricNameCacheSize(8 << 20)
		storage.SetTagFiltersCacheSize(4 << 20)
		storage.SetMetadataStorageSize(1 << 20)
		storage.SetFreeDiskSpaceLimit(512 << 20)
		storage.SetDataFlushInterval(2 * time.Second)
		storage.SetDedupInterval(0)
	})
	defer func() {
		if recovered := recover(); recovered != nil {
			err = fmt.Errorf("open VictoriaMetrics storage: %v", recovered)
			db = nil
		}
	}()
	db = &telemetryTSDB{
		host:  storage.MustOpenStorage(filepath.Join(path, "host"), storage.OpenOptions{Retention: retention}),
		motor: storage.MustOpenStorage(filepath.Join(path, "motor"), storage.OpenOptions{Retention: fastMotorRetention}),
	}
	return db, nil
}

func setTelemetryTSDBMemoryLimit() error {
	if !flag.Parsed() {
		return errors.New("command flags must be parsed before opening telemetry tsdb")
	}
	overridden := false
	flag.Visit(func(item *flag.Flag) {
		if item.Name == "memory.allowedBytes" || item.Name == "memory.allowedPercent" {
			overridden = true
		}
	})
	if overridden {
		return nil
	}
	item := flag.Lookup("memory.allowedBytes")
	if item == nil {
		return errors.New("VictoriaMetrics memory flag is unavailable")
	}
	if err := item.Value.Set(tsdbCacheMemory); err != nil {
		return fmt.Errorf("set VictoriaMetrics memory limit: %w", err)
	}
	return nil
}

func (db *telemetryTSDB) Close() error {
	db.mu.Lock()
	defer db.mu.Unlock()
	if db.closed {
		return nil
	}
	db.host.MustClose()
	db.motor.MustClose()
	db.closed = true
	return nil
}

func (db *telemetryTSDB) DeleteRobot(uuid string) error {
	db.mu.Lock()
	defer db.mu.Unlock()
	if db.closed {
		return errors.New("telemetry tsdb is closed")
	}
	filters, err := robotFilters(uuid)
	if err != nil {
		return err
	}
	if _, err := db.host.DeleteSeries(nil, []*storage.TagFilters{filters}, 100_000); err != nil {
		return err
	}
	filters, err = robotFilters(uuid)
	if err != nil {
		return err
	}
	_, err = db.motor.DeleteSeries(nil, []*storage.TagFilters{filters}, 100_000)
	return err
}

func (db *telemetryTSDB) WriteHost(uuid string, point HistoryPoint) error {
	db.mu.RLock()
	defer db.mu.RUnlock()
	if db.closed {
		return errors.New("telemetry tsdb is closed")
	}
	rows := make([]storage.MetricRow, 0, 15+len(point.Motors)*3)
	appendFloat := func(metric string, value *float64) {
		if value != nil {
			rows = append(rows, telemetryRow(metric, uuid, "", point.At, *value))
		}
	}
	appendFloat(metricHostCPU, point.CPUPercent)
	appendFloat(metricHostMemory, point.MemoryPercent)
	appendFloat(metricHostDisk, point.DiskPercent)
	appendFloat(metricHostLoad1, point.Load1)
	appendFloat(metricHostTemperature, point.TemperatureMax)
	appendFloat(metricHostGPUUtilization, point.GPUUtilization)
	appendFloat(metricHostGPUTemperature, point.GPUTemperature)
	appendFloat(metricHostBatterySOC, point.BatterySOC)
	appendFloat(metricHostBatteryVoltage, point.BatteryVoltage)
	appendFloat(metricHostBatteryCurrent, point.BatteryCurrent)
	appendFloat(metricHostBatteryPower, point.BatteryPower)
	appendFloat(metricHostBatteryTemperature, point.BatteryTemperature)
	rows = append(rows,
		telemetryRow(metricHostMotorCount, uuid, "", point.At, float64(point.MotorCount)),
		telemetryRow(metricHostMotorOnline, uuid, "", point.At, boolFloat(point.MotorTopicOnline)),
		telemetryRow(metricHostDiagnosticCount, uuid, "", point.At, float64(point.DiagnosticCount)),
	)
	for _, motor := range point.Motors {
		rows = append(rows,
			telemetryRow(metricHostMotorPosition, uuid, motor.ID, point.At, motor.PositionRad),
			telemetryRow(metricHostMotorVelocity, uuid, motor.ID, point.At, motor.VelocityRadPerSec),
			telemetryRow(metricHostMotorTorque, uuid, motor.ID, point.At, motor.TorqueNm),
		)
	}
	db.host.AddRows(rows, 64)
	return nil
}

func (db *telemetryTSDB) WriteMotorSamples(uuid string, samples []model.MotorSample) error {
	if len(samples) == 0 {
		return nil
	}
	db.mu.RLock()
	defer db.mu.RUnlock()
	if db.closed {
		return errors.New("telemetry tsdb is closed")
	}
	rows := make([]storage.MetricRow, 0, len(samples)*3)
	for _, sample := range samples {
		for _, motor := range sample.Motors {
			rows = append(rows,
				telemetryRow(metricMotorPosition, uuid, motor.ID, sample.At, motor.PositionRad),
				telemetryRow(metricMotorVelocity, uuid, motor.ID, sample.At, motor.VelocityRadPerSec),
				telemetryRow(metricMotorTorque, uuid, motor.ID, sample.At, motor.TorqueNm),
			)
		}
	}
	db.motor.AddRows(rows, 64)
	return nil
}

func (db *telemetryTSDB) History(uuid string, from, to time.Time, limit int) ([]HistoryPoint, error) {
	db.mu.RLock()
	defer db.mu.RUnlock()
	if db.closed {
		return nil, errors.New("telemetry tsdb is closed")
	}
	// A user-triggered history request must include the most recent agent batch.
	// VictoriaMetrics indexes fresh in-memory rows on flush, so flush once here
	// rather than making every telemetry report perform a synchronous disk write.
	db.host.DebugFlush()
	points := make(map[int64]*HistoryPoint)
	motorsByTimestamp := make(map[int64]map[string]*MotorHistoryPoint)
	type binding struct {
		metric string
		set    func(*HistoryPoint, float64)
	}
	bindings := []binding{
		{metricHostCPU, func(point *HistoryPoint, value float64) { point.CPUPercent = floatPointer(value) }},
		{metricHostMemory, func(point *HistoryPoint, value float64) { point.MemoryPercent = floatPointer(value) }},
		{metricHostDisk, func(point *HistoryPoint, value float64) { point.DiskPercent = floatPointer(value) }},
		{metricHostLoad1, func(point *HistoryPoint, value float64) { point.Load1 = floatPointer(value) }},
		{metricHostTemperature, func(point *HistoryPoint, value float64) { point.TemperatureMax = floatPointer(value) }},
		{metricHostGPUUtilization, func(point *HistoryPoint, value float64) { point.GPUUtilization = floatPointer(value) }},
		{metricHostGPUTemperature, func(point *HistoryPoint, value float64) { point.GPUTemperature = floatPointer(value) }},
		{metricHostBatterySOC, func(point *HistoryPoint, value float64) { point.BatterySOC = floatPointer(value) }},
		{metricHostBatteryVoltage, func(point *HistoryPoint, value float64) { point.BatteryVoltage = floatPointer(value) }},
		{metricHostBatteryCurrent, func(point *HistoryPoint, value float64) { point.BatteryCurrent = floatPointer(value) }},
		{metricHostBatteryPower, func(point *HistoryPoint, value float64) { point.BatteryPower = floatPointer(value) }},
		{metricHostBatteryTemperature, func(point *HistoryPoint, value float64) { point.BatteryTemperature = floatPointer(value) }},
		{metricHostMotorCount, func(point *HistoryPoint, value float64) { point.MotorCount = int(value) }},
		{metricHostMotorOnline, func(point *HistoryPoint, value float64) { point.MotorTopicOnline = value >= 0.5 }},
		{metricHostDiagnosticCount, func(point *HistoryPoint, value float64) { point.DiagnosticCount = int(value) }},
	}
	for _, binding := range bindings {
		values, err := readMetric(db.host, binding.metric, uuid, from, to, false)
		if err != nil {
			return nil, err
		}
		for _, value := range values {
			point := points[value.timestamp]
			if point == nil {
				point = &HistoryPoint{At: time.UnixMilli(value.timestamp).UTC()}
				points[value.timestamp] = point
			}
			binding.set(point, value.value)
		}
	}
	type motorBinding struct {
		metric string
		set    func(*MotorHistoryPoint, float64)
	}
	motorBindings := []motorBinding{
		{metricHostMotorPosition, func(point *MotorHistoryPoint, value float64) { point.PositionRad = value }},
		{metricHostMotorVelocity, func(point *MotorHistoryPoint, value float64) { point.VelocityRadPerSec = value }},
		{metricHostMotorTorque, func(point *MotorHistoryPoint, value float64) { point.TorqueNm = value }},
	}
	for _, binding := range motorBindings {
		values, err := readMetric(db.host, binding.metric, uuid, from, to, true)
		if err != nil {
			return nil, err
		}
		for _, value := range values {
			point := points[value.timestamp]
			if point == nil {
				point = &HistoryPoint{At: time.UnixMilli(value.timestamp).UTC()}
				points[value.timestamp] = point
			}
			motors := motorsByTimestamp[value.timestamp]
			if motors == nil {
				motors = make(map[string]*MotorHistoryPoint)
				motorsByTimestamp[value.timestamp] = motors
			}
			motor := motors[value.motorID]
			if motor == nil {
				motor = &MotorHistoryPoint{ID: value.motorID}
				motors[value.motorID] = motor
			}
			binding.set(motor, value.value)
		}
	}
	for timestamp, motors := range motorsByTimestamp {
		ids := make([]string, 0, len(motors))
		for id := range motors {
			ids = append(ids, id)
		}
		sort.Strings(ids)
		point := points[timestamp]
		point.Motors = make([]MotorHistoryPoint, 0, len(ids))
		for _, id := range ids {
			point.Motors = append(point.Motors, *motors[id])
		}
	}
	result := orderedHistoryPoints(points, limit)
	return result, nil
}

func (db *telemetryTSDB) FastMotorHistory(uuid string, from, to time.Time, limit int) ([]HistoryPoint, error) {
	db.mu.RLock()
	defer db.mu.RUnlock()
	if db.closed {
		return nil, errors.New("telemetry tsdb is closed")
	}
	db.motor.DebugFlush()
	points := make(map[int64]map[string]*MotorHistoryPoint)
	type binding struct {
		metric string
		set    func(*MotorHistoryPoint, float64)
	}
	bindings := []binding{
		{metricMotorPosition, func(point *MotorHistoryPoint, value float64) { point.PositionRad = value }},
		{metricMotorVelocity, func(point *MotorHistoryPoint, value float64) { point.VelocityRadPerSec = value }},
		{metricMotorTorque, func(point *MotorHistoryPoint, value float64) { point.TorqueNm = value }},
	}
	for _, binding := range bindings {
		values, err := readMetric(db.motor, binding.metric, uuid, from, to, true)
		if err != nil {
			return nil, err
		}
		for _, value := range values {
			motors := points[value.timestamp]
			if motors == nil {
				motors = make(map[string]*MotorHistoryPoint)
				points[value.timestamp] = motors
			}
			motor := motors[value.motorID]
			if motor == nil {
				motor = &MotorHistoryPoint{ID: value.motorID}
				motors[value.motorID] = motor
			}
			binding.set(motor, value.value)
		}
	}
	timestamps := make([]int64, 0, len(points))
	for timestamp := range points {
		timestamps = append(timestamps, timestamp)
	}
	sort.Slice(timestamps, func(i, j int) bool { return timestamps[i] < timestamps[j] })
	if limit > 0 && len(timestamps) > limit {
		timestamps = timestamps[len(timestamps)-limit:]
	}
	result := make([]HistoryPoint, 0, len(timestamps))
	for _, timestamp := range timestamps {
		motors := points[timestamp]
		ids := make([]string, 0, len(motors))
		for id := range motors {
			ids = append(ids, id)
		}
		sort.Strings(ids)
		point := HistoryPoint{At: time.UnixMilli(timestamp).UTC(), MotorCount: len(ids), MotorTopicOnline: true, Motors: make([]MotorHistoryPoint, 0, len(ids))}
		for _, id := range ids {
			point.Motors = append(point.Motors, *motors[id])
		}
		result = append(result, point)
	}
	return result, nil
}

type metricValue struct {
	timestamp int64
	value     float64
	motorID   string
}

func readMetric(source *storage.Storage, metric, uuid string, from, to time.Time, includeMotorID bool) ([]metricValue, error) {
	filters := storage.NewTagFilters()
	if err := filters.Add(nil, []byte(metric), false, false); err != nil {
		return nil, err
	}
	if err := filters.Add([]byte("robot_uuid"), []byte(uuid), false, false); err != nil {
		return nil, err
	}
	tr := storage.TimeRange{MinTimestamp: from.UnixMilli(), MaxTimestamp: to.UnixMilli()}
	deadline := uint64(time.Now().Add(20 * time.Second).Unix())
	var search storage.Search
	search.Init(nil, source, []*storage.TagFilters{filters}, tr, 1_000, deadline)
	defer search.MustClose()
	var result []metricValue
	var timestamps []int64
	var values []float64
	for search.NextMetricBlock() {
		motorID := ""
		if includeMotorID {
			name := storage.GetMetricName()
			if err := name.Unmarshal(search.MetricBlockRef.MetricName); err != nil {
				storage.PutMetricName(name)
				return nil, fmt.Errorf("decode motor series: %w", err)
			}
			motorID = string(name.GetTagValue("motor_id"))
			storage.PutMetricName(name)
		}
		var block storage.Block
		search.MetricBlockRef.BlockRef.MustReadBlock(&block)
		if err := block.UnmarshalData(); err != nil {
			return nil, fmt.Errorf("decode metric block: %w", err)
		}
		timestamps = timestamps[:0]
		values = values[:0]
		timestamps, values = block.AppendRowsWithTimeRangeFilter(timestamps, values, tr)
		for index := range timestamps {
			result = append(result, metricValue{timestamp: timestamps[index], value: values[index], motorID: motorID})
		}
	}
	if err := search.Error(); err != nil {
		return nil, err
	}
	return result, nil
}

func robotFilters(uuid string) (*storage.TagFilters, error) {
	filters := storage.NewTagFilters()
	if err := filters.Add([]byte("robot_uuid"), []byte(uuid), false, false); err != nil {
		return nil, err
	}
	return filters, nil
}

func telemetryRow(metric, uuid, motorID string, at time.Time, value float64) storage.MetricRow {
	labels := []prompb.Label{{Name: "__name__", Value: metric}, {Name: "robot_uuid", Value: uuid}}
	if motorID != "" {
		labels = append(labels, prompb.Label{Name: "motor_id", Value: motorID})
	}
	return storage.MetricRow{MetricNameRaw: storage.MarshalMetricNameRaw(nil, labels), Timestamp: at.UnixMilli(), Value: value}
}

func orderedHistoryPoints(points map[int64]*HistoryPoint, limit int) []HistoryPoint {
	timestamps := make([]int64, 0, len(points))
	for timestamp := range points {
		timestamps = append(timestamps, timestamp)
	}
	sort.Slice(timestamps, func(i, j int) bool { return timestamps[i] < timestamps[j] })
	if limit > 0 && len(timestamps) > limit {
		if limit == 1 {
			timestamps = timestamps[len(timestamps)-1:]
		} else {
			downsampled := make([]int64, limit)
			last := len(timestamps) - 1
			for index := range downsampled {
				downsampled[index] = timestamps[index*last/(limit-1)]
			}
			timestamps = downsampled
		}
	}
	result := make([]HistoryPoint, 0, len(timestamps))
	for _, timestamp := range timestamps {
		result = append(result, *points[timestamp])
	}
	return result
}

func boolFloat(value bool) float64 {
	if value {
		return 1
	}
	return 0
}

func floatPointer(value float64) *float64 { return &value }
