package dashboard

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"testing"
	"time"

	"baize/shared/model"
)

func TestCompareVersions(t *testing.T) {
	cases := []struct {
		left, right string
		want        int
	}{{"20260814", "20260813", 1}, {"1.2.0", "1.1.9", 1}, {"v2.0.0", "1.99.0", 1}, {"1.0.0", "1.0.0", 0}, {"1.0.0", "1.0.1", -1}}
	for _, test := range cases {
		got := compareVersions(test.left, test.right)
		if (got > 0) != (test.want > 0) || (got < 0) != (test.want < 0) {
			t.Fatalf("compareVersions(%q, %q)=%d want sign %d", test.left, test.right, got, test.want)
		}
	}
}

func TestVersionPattern(t *testing.T) {
	for _, version := range []string{"20260814"} {
		if !versionPattern.MatchString(version) {
			t.Fatalf("version %q should be accepted", version)
		}
	}
	for _, version := range []string{"2026-08-14", "latest", "1", "0.1.0"} {
		if versionPattern.MatchString(version) {
			t.Fatalf("version %q should be rejected", version)
		}
	}
}

func TestStoreSeparatesControlAndHistoryAndKeepsOfflineRobots(t *testing.T) {
	root := t.TempDir()
	options := StoreOptions{AdminUser: "admin", BootstrapPassword: "Baize@Admin1", RequirePasswordChange: true, HistorySampleInterval: time.Second}
	sampleAt := time.Now().UTC()
	telemetry := model.Telemetry{
		SchemaVersion: model.SchemaVersion,
		Robot:         model.Robot{UUID: "52446a60-7483-4ba7-b8c7-b85f60b2a00f", Code: "M99", Model: "2m_v0.1.2", Hostname: "m99", OS: "linux", Arch: "arm64"},
		AgentVersion:  "20260815", CollectedAt: time.Now().UTC(),
		System: &model.SystemMetrics{CPUUsagePercent: 37, MemoryTotalBytes: 100, MemoryUsedBytes: 42},
		Motors: &model.MotorSnapshot{
			TopicOnline: true,
			Motors:      []model.MotorState{{ID: "left_hip", PositionRad: 1.2, VelocityRadPerSec: 2.3, TorqueNm: 4.5}},
			Samples:     []model.MotorSample{{At: sampleAt, Motors: []model.MotorSampleState{{ID: "left_hip", PositionRad: 1.3, VelocityRadPerSec: 2.4, TorqueNm: 5.6}}}},
		},
	}
	store, err := NewStore(root+"/control", root+"/history", options)
	if err != nil {
		t.Fatal(err)
	}
	if err := store.PutTelemetry(telemetry); err != nil {
		t.Fatal(err)
	}
	if _, err := store.control.Exec(`INSERT INTO robot_settings(uuid, remark) VALUES(?, ?)`, telemetry.Robot.UUID, "lab unit"); err != nil {
		t.Fatal(err)
	}
	if err := store.Close(); err != nil {
		t.Fatal(err)
	}

	reopened, err := NewStore(root+"/control", root+"/history", options)
	if err != nil {
		t.Fatal(err)
	}
	defer reopened.Close()
	record, ok := reopened.Robot(telemetry.Robot.UUID)
	if !ok || record.Remark != "lab unit" || record.Code != "M99" {
		t.Fatalf("persistent robot record missing: %+v", record)
	}
	if record.Telemetry.CollectedAt.IsZero() || record.Telemetry.System == nil || record.Telemetry.System.CPUUsagePercent != 37 || len(record.Telemetry.Motors.Motors) != 1 {
		t.Fatalf("last historical snapshot was not restored: %+v", record.Telemetry)
	}
	if time.Since(record.LastSeen) < 0 {
		t.Fatalf("last seen is in the future: %v", record.LastSeen)
	}
	points, err := reopened.History(telemetry.Robot.UUID, time.Now().Add(-time.Hour), time.Now().Add(time.Hour), 100)
	if err != nil || len(points) != 1 || len(points[0].Motors) != 1 || points[0].Motors[0].TorqueNm != 4.5 {
		t.Fatalf("history not persisted: points=%+v err=%v", points, err)
	}
	fastPoints, err := reopened.FastMotorHistory(telemetry.Robot.UUID, sampleAt.Add(-time.Second), sampleAt.Add(time.Second), 100)
	if err != nil || len(fastPoints) != 1 || len(fastPoints[0].Motors) != 1 || fastPoints[0].Motors[0].TorqueNm != 5.6 {
		t.Fatalf("fast motor history not persisted: points=%+v err=%v", fastPoints, err)
	}

	if err := reopened.RemoveRobot(telemetry.Robot.UUID); err != nil {
		t.Fatal(err)
	}
	if _, ok := reopened.Robot(telemetry.Robot.UUID); ok {
		t.Fatal("removed robot remained in control database")
	}
	points, err = reopened.History(telemetry.Robot.UUID, time.Now().Add(-time.Hour), time.Now().Add(time.Hour), 100)
	if err != nil || len(points) != 0 {
		t.Fatalf("removed robot history remained: points=%+v err=%v", points, err)
	}
	fastPoints, err = reopened.FastMotorHistory(telemetry.Robot.UUID, time.Now().Add(-time.Hour), time.Now().Add(time.Hour), 100)
	if err != nil || len(fastPoints) != 0 {
		t.Fatalf("removed fast motor history remained: points=%+v err=%v", fastPoints, err)
	}
}

func TestStoreRoundTripsComplete500HzMotorBatch(t *testing.T) {
	root := t.TempDir()
	store, err := NewStore(root+"/control", root+"/history", StoreOptions{AdminUser: "admin", BootstrapPassword: "Baize@Admin1"})
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()

	startedAt := time.Now().UTC().Add(-2 * time.Second)
	samples := make([]model.MotorSample, 1000)
	for sampleIndex := range samples {
		motors := make([]model.MotorSampleState, 32)
		for motorIndex := range motors {
			motors[motorIndex] = model.MotorSampleState{ID: fmt.Sprintf("motor-%02d", motorIndex), PositionRad: float64(sampleIndex), VelocityRadPerSec: float64(motorIndex), TorqueNm: float64(sampleIndex+motorIndex) + 0.25}
		}
		samples[sampleIndex] = model.MotorSample{At: startedAt.Add(time.Duration(sampleIndex) * 2 * time.Millisecond), Motors: motors}
	}
	telemetry := model.Telemetry{
		SchemaVersion: model.SchemaVersion,
		Robot:         model.Robot{UUID: "52446a60-7483-4ba7-b8c7-b85f60b2a00f", Code: "M99"},
		CollectedAt:   time.Now().UTC(),
		Motors:        &model.MotorSnapshot{SampleRateHz: 500, Samples: samples},
	}
	if err := store.PutTelemetry(telemetry); err != nil {
		t.Fatal(err)
	}
	points, err := store.FastMotorHistory(telemetry.Robot.UUID, startedAt.Add(-time.Millisecond), time.Now().UTC().Add(time.Second), 32_000)
	if err != nil {
		t.Fatal(err)
	}
	if len(points) != 1000 {
		t.Fatalf("500Hz batch point count=%d, want 1000", len(points))
	}
	if len(points[999].Motors) != 32 || points[999].Motors[31].TorqueNm != 1030.25 {
		t.Fatalf("500Hz batch last point was not preserved: %+v", points[999])
	}
	limited, err := store.FastMotorHistory(telemetry.Robot.UUID, startedAt.Add(-time.Millisecond), time.Now().UTC().Add(time.Second), 10)
	if err != nil {
		t.Fatal(err)
	}
	if len(limited) != 10 {
		t.Fatalf("downsampled point count=%d, want 10", len(limited))
	}
	if limited[0].At.UnixMilli() != samples[0].At.UnixMilli() || limited[len(limited)-1].At.UnixMilli() != samples[len(samples)-1].At.UnixMilli() {
		t.Fatalf("downsampled range=%v..%v, want %v..%v", limited[0].At, limited[len(limited)-1].At, samples[0].At, samples[len(samples)-1].At)
	}
	if limited[1].At.UnixMilli() != samples[111].At.UnixMilli() {
		t.Fatalf("downsampled timestamps did not span the range: second=%v, want %v", limited[1].At, samples[111].At)
	}
}

func TestOrderedHistoryPointsDownsamplesAcrossTheRequestedRange(t *testing.T) {
	points := make(map[int64]*HistoryPoint, 10)
	for index := int64(0); index < 10; index++ {
		points[index] = &HistoryPoint{At: time.UnixMilli(index)}
	}
	result := orderedHistoryPoints(points, 3)
	if len(result) != 3 {
		t.Fatalf("downsampled point count=%d, want 3", len(result))
	}
	got := []int64{result[0].At.UnixMilli(), result[1].At.UnixMilli(), result[2].At.UnixMilli()}
	want := []int64{0, 4, 9}
	for index := range want {
		if got[index] != want[index] {
			t.Fatalf("downsampled timestamps=%v, want %v", got, want)
		}
	}
}

func TestAdminBootstrapPasswordRequiresChange(t *testing.T) {
	store := newTestStore(t)
	valid, required, err := store.AuthenticateAdmin("admin", "Baize@Admin1")
	if err != nil || !valid || !required {
		t.Fatalf("bootstrap authentication = valid:%v required:%v err:%v", valid, required, err)
	}
	if err := store.ChangeAdminPassword("admin", "Baize@Admin1", "Better@Password123"); err != nil {
		t.Fatal(err)
	}
	valid, required, err = store.AuthenticateAdmin("admin", "Better@Password123")
	if err != nil || !valid || required {
		t.Fatalf("changed authentication = valid:%v required:%v err:%v", valid, required, err)
	}
}

func TestControlSecretPersistsOutsideHistoryDatabase(t *testing.T) {
	root := t.TempDir()
	options := StoreOptions{AdminUser: "admin", BootstrapPassword: "Baize@Admin1"}
	store, err := NewStore(filepath.Join(root, "control"), filepath.Join(root, "history"), options)
	if err != nil {
		t.Fatal(err)
	}
	secret, created, err := store.Secret("jwt_secret", 32)
	if err != nil || !created || len(secret) != 64 {
		t.Fatalf("initial secret = %q created=%v err=%v", secret, created, err)
	}
	if err := store.Close(); err != nil {
		t.Fatal(err)
	}
	if err := os.RemoveAll(filepath.Join(root, "history")); err != nil {
		t.Fatal(err)
	}
	reopened, err := NewStore(filepath.Join(root, "control"), filepath.Join(root, "history"), options)
	if err != nil {
		t.Fatal(err)
	}
	defer reopened.Close()
	reused, created, err := reopened.Secret("jwt_secret", 32)
	if err != nil || created || reused != secret {
		t.Fatalf("persistent secret = %q created=%v err=%v", reused, created, err)
	}
}

func TestLegacyStateAndReleaseAreMigratedIntoControlDatabase(t *testing.T) {
	root := t.TempDir()
	uuid := "52446a60-7483-4ba7-b8c7-b85f60b2a00f"
	release := Release{ID: "linux-arm64-legacy", Version: "20260814", OS: "linux", Arch: "arm64", SHA256: "abc", Size: 6, UploadedAt: time.Now().UTC()}
	legacy := legacyState{Remarks: map[string]string{uuid: "legacy remark"}, Desired: map[string]string{uuid: "20260814"}, Releases: map[string]Release{release.ID: release}}
	data, err := json.Marshal(legacy)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, "state.json"), data, 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Join(root, "releases"), 0o750); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, "releases", release.ID), []byte("legacy"), 0o640); err != nil {
		t.Fatal(err)
	}
	store, err := NewStore(filepath.Join(root, "control"), filepath.Join(root, "history"), StoreOptions{AdminUser: "admin", BootstrapPassword: "Baize@Admin1"})
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	if err := store.PutTelemetry(model.Telemetry{SchemaVersion: model.SchemaVersion, Robot: model.Robot{UUID: uuid, Code: "M99", OS: "linux", Arch: "arm64"}, CollectedAt: time.Now().UTC()}); err != nil {
		t.Fatal(err)
	}
	record, ok := store.Robot(uuid)
	if !ok || record.Remark != "legacy remark" {
		t.Fatalf("legacy settings missing: %+v", record)
	}
	migrated, ok := store.ReleaseByID(release.ID)
	if !ok {
		t.Fatal("legacy release metadata missing")
	}
	if content, err := os.ReadFile(migrated.Filename); err != nil || string(content) != "legacy" {
		t.Fatalf("legacy release file missing: content=%q err=%v", content, err)
	}
}
