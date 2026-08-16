package service

import (
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
	"time"

	"baize/agent/internal/config"
)

func TestParseInstallOptions(t *testing.T) {
	options, err := parseInstallOptions([]string{
		"--dashboard-url=https://baize.example.test",
		"--token", "long-enough-token",
		"--robot-code", "M99",
		"--robot-model", "2m_v0.1.2",
		"--force-config",
	})
	if err != nil {
		t.Fatal(err)
	}
	if options.dashboardURL != "https://baize.example.test" || options.robotCode != "M99" || !options.forceConfig {
		t.Fatalf("unexpected parsed options: %+v", options)
	}
}

func TestInstallOptionsValidatePartialValues(t *testing.T) {
	partial := installOptions{uuid: "7fd34256-bf3a-4cf6-8da0-fbce40f34d11"}
	if err := partial.validateProvided(); err != nil {
		t.Fatal(err)
	}
	complete := installOptions{
		dashboardURL: "https://baize.example.test",
		token:        "long-enough-token",
		robotCode:    "M99",
		robotModel:   "2m_v0.1.2",
	}
	if err := complete.validateProvided(); err != nil {
		t.Fatal(err)
	}
	canonicalUUID := regexp.MustCompile(`^[0-9a-f]{8}-[0-9a-f]{4}-4[0-9a-f]{3}-[89ab][0-9a-f]{3}-[0-9a-f]{12}$`)
	generated, err := newUUID()
	if err != nil || !canonicalUUID.MatchString(generated) {
		t.Fatalf("generated UUID is not canonical v4: %q (%v)", generated, err)
	}
}

func TestConfiguredContentRoundTrips(t *testing.T) {
	options := installOptions{
		dashboardURL: "https://baize.example.test/path",
		token:        "token-with-\"quote",
		robotCode:    "M99",
		robotModel:   "2m_v0.1.2",
		uuid:         "7fd34256-bf3a-4cf6-8da0-fbce40f34d11",
	}
	content, err := configuredContent(options)
	if err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(t.TempDir(), "config.yml")
	if err := os.WriteFile(path, content, 0o600); err != nil {
		t.Fatal(err)
	}
	loaded, err := config.Load(path)
	if err != nil {
		t.Fatal(err)
	}
	if loaded.Agent.Token != options.token || loaded.Agent.ReportInterval.Value() != 2*time.Second || loaded.Agent.HTTPTimeout.Value() != 10*time.Second {
		t.Fatalf("unexpected round-trip config: %+v", loaded.Agent)
	}
}

func TestPlanConfigPreservesValidConfigWithoutOptions(t *testing.T) {
	originalPath := installedConfig
	installedConfig = filepath.Join(t.TempDir(), "config.yml")
	t.Cleanup(func() { installedConfig = originalPath })

	content, err := configuredContent(installOptions{
		dashboardURL: "https://baize.example.test",
		token:        "long-enough-token",
		robotCode:    "M99",
		robotModel:   "2m_v0.1.2",
		uuid:         "7fd34256-bf3a-4cf6-8da0-fbce40f34d11",
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(installedConfig, content, 0o600); err != nil {
		t.Fatal(err)
	}
	plan, err := planConfig(installOptions{})
	if err != nil {
		t.Fatal(err)
	}
	if plan.replace || !plan.valid {
		t.Fatalf("valid existing config should be preserved: %+v", plan)
	}
	plan, err = planConfig(installOptions{uuid: "6e1f6b5e-80f1-4a0d-9bcf-8e8f6e2c0f71"})
	if err != nil {
		t.Fatal(err)
	}
	updatedPath := filepath.Join(t.TempDir(), "updated.yml")
	if err := os.WriteFile(updatedPath, plan.content, 0o600); err != nil {
		t.Fatal(err)
	}
	updated, err := config.Load(updatedPath)
	if err != nil || updated.Agent.UUID != "6e1f6b5e-80f1-4a0d-9bcf-8e8f6e2c0f71" {
		t.Fatalf("--uuid should be written into config, got %+v (%v)", updated.Agent, err)
	}
}

func TestValidConfigRejectsSymbolicLink(t *testing.T) {
	directory := t.TempDir()
	target := filepath.Join(directory, "target.yml")
	content, err := configuredContent(installOptions{
		dashboardURL: "https://baize.example.test",
		token:        "long-enough-token",
		robotCode:    "M99",
		robotModel:   "2m_v0.1.2",
		uuid:         "7fd34256-bf3a-4cf6-8da0-fbce40f34d11",
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(target, content, 0o600); err != nil {
		t.Fatal(err)
	}
	link := filepath.Join(directory, "config.yml")
	if err := os.Symlink(target, link); err != nil {
		t.Fatal(err)
	}
	if validConfig(link) {
		t.Fatal("symbolic link must not be accepted as the installed config")
	}
}

func TestDefaultConfigIsGeneratedButIntentionallyInvalid(t *testing.T) {
	uuid := "7fd34256-bf3a-4cf6-8da0-fbce40f34d11"
	content := defaultConfig(uuid)
	if !strings.Contains(content, "uuid: \""+uuid+"\"") || !strings.Contains(content, "robot_model: \"\"") || !strings.Contains(content, "#   2m_v0.1.2") {
		t.Fatalf("default config is missing editable identity or supported model: %s", content)
	}
	path := filepath.Join(t.TempDir(), "config.yml")
	if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := config.Load(path); err == nil {
		t.Fatal("expected generated default config to require editing")
	}
}

func TestServiceUnitRunsInstalledBinaryWithConfig(t *testing.T) {
	unit := serviceUnit()
	expected := "ExecStart=/opt/baize/agent/baize-agent run --config /opt/baize/agent/config.yml"
	if !strings.Contains(unit, expected) || !strings.Contains(unit, "User=ubuntu") || !strings.Contains(unit, "NoNewPrivileges=true") {
		t.Fatalf("unexpected service unit: %s", unit)
	}
}
