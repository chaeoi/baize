package dashboard

import (
	"encoding/json"
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
	telemetry := model.Telemetry{
		SchemaVersion: model.SchemaVersion,
		Robot:         model.Robot{UUID: "52446a60-7483-4ba7-b8c7-b85f60b2a00f", Code: "M99", Model: "2m_v0.1.2", Hostname: "m99", OS: "linux", Arch: "arm64"},
		AgentVersion:  "20260815", CollectedAt: time.Now().UTC(),
		System: &model.SystemMetrics{CPUUsagePercent: 37, MemoryTotalBytes: 100, MemoryUsedBytes: 42},
		Motors: &model.MotorSnapshot{TopicOnline: true, Motors: []model.MotorState{{ID: "left_hip", PositionRad: 1.2, VelocityRadPerSec: 2.3, TorqueNm: 4.5}}},
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
	secret, created, err := store.Secret("agent_token", 32)
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
	reused, created, err := reopened.Secret("agent_token", 32)
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
	if !ok || record.Remark != "legacy remark" || record.DesiredVersion != "20260814" {
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
