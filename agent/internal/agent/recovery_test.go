package agent

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"testing"
	"time"

	"baize/shared/model"
)

func TestOutboxSurvivesFailureAndRestart(t *testing.T) {
	directory := t.TempDir()
	outbox, err := NewOutbox(directory)
	if err != nil {
		t.Fatal(err)
	}
	telemetry := model.Telemetry{SchemaVersion: 1, CollectedAt: time.Now(), Motors: &model.MotorSnapshot{Samples: []model.MotorSample{{At: time.Now(), Motors: []model.MotorSampleState{{ID: "hip", TorqueNm: 7}}}}}}
	if err := outbox.Enqueue(telemetry); err != nil {
		t.Fatal(err)
	}
	telemetry.Motors.Samples[0].Motors[0].TorqueNm = 99
	failure := true
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if failure {
			w.WriteHeader(503)
			return
		}
		w.WriteHeader(200)
	}))
	defer server.Close()
	client := NewClient(server.URL, "token", server.Client())
	if err := outbox.Flush(t.Context(), client); err == nil {
		t.Fatal("failed request was acknowledged")
	}
	entries, _ := os.ReadDir(directory)
	if len(entries) != 1 {
		t.Fatal("failed batch was lost")
	}
	data, _ := os.ReadFile(filepath.Join(directory, entries[0].Name()))
	var saved model.Telemetry
	if err := json.Unmarshal(data, &saved); err != nil {
		t.Fatal(err)
	}
	if saved.Motors.Samples[0].Motors[0].TorqueNm != 7 {
		t.Fatal("queued snapshot changed with collector buffer")
	}
	failure = false
	reopened, err := NewOutbox(directory)
	if err != nil {
		t.Fatal(err)
	}
	if err := reopened.Flush(t.Context(), client); err != nil {
		t.Fatal(err)
	}
	entries, _ = os.ReadDir(directory)
	if len(entries) != 0 {
		t.Fatal("acknowledged batch was not removed")
	}
}

func TestRecoverInterruptedUpdate(t *testing.T) {
	path := filepath.Join(t.TempDir(), "agent")
	for suffix, value := range map[string]string{"": "broken", ".previous": "working", ".pending": "release-hash"} {
		if err := os.WriteFile(path+suffix, []byte(value), 0700); err != nil {
			t.Fatal(err)
		}
	}
	if err := RecoverUpdate(path); err != nil {
		t.Fatal(err)
	}
	data, _ := os.ReadFile(path)
	if string(data) != "working" {
		t.Fatal("previous executable not restored")
	}
	data, _ = os.ReadFile(path + ".rejected")
	if string(data) != "release-hash" {
		t.Fatal("failed release not remembered")
	}
	if err := RecoverUpdate(path); err != nil {
		t.Fatal("recovery must be idempotent", err)
	}
}

func TestUpdateValidatesBeforeReplacementAndRestoresOnExecFailure(t *testing.T) {
	root := t.TempDir()
	if err := os.WriteFile(filepath.Join(root, "go.mod"), []byte("module baize\n\ngo 1.26.5\n"), 0600); err != nil {
		t.Fatal(err)
	}
	source := filepath.Join(root, "agent", "cmd", "baize-agent")
	if err := os.MkdirAll(source, 0700); err != nil {
		t.Fatal(err)
	}
	program := `package main
import("fmt";"os")
func main(){for _,arg:=range os.Args[1:]{if arg=="--version"{fmt.Println("20260905");return};if arg=="--check-config"{return}};os.Exit(1)}
`
	if err := os.WriteFile(filepath.Join(source, "main.go"), []byte(program), 0600); err != nil {
		t.Fatal(err)
	}
	newPath := filepath.Join(root, "release")
	command := exec.Command("go", "build", "-trimpath", "-o", newPath, "./agent/cmd/baize-agent")
	command.Dir = root
	command.Env = append(os.Environ(), "CGO_ENABLED=0")
	if output, err := command.CombinedOutput(); err != nil {
		t.Fatalf("build fixture: %v: %s", err, output)
	}
	binary, err := os.ReadFile(newPath)
	if err != nil {
		t.Fatal(err)
	}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { w.Write(binary) }))
	defer server.Close()
	client := NewClient(server.URL, "token", server.Client())
	hash := sha256.Sum256(binary)
	update := model.UpdateInfo{Version: "20260905", OS: "linux", Arch: runtime.GOARCH, SHA256: hex.EncodeToString(hash[:]), Size: int64(len(binary)), URL: "/api/v1/update/files/test"}
	for _, wrongVersion := range []bool{true, false} {
		t.Run(map[bool]string{true: "version-mismatch", false: "exec-failure"}[wrongVersion], func(t *testing.T) {
			path := filepath.Join(t.TempDir(), "agent")
			old := []byte("previous working executable")
			if err := os.WriteFile(path, old, 0755); err != nil {
				t.Fatal(err)
			}
			candidate := update
			if wrongVersion {
				candidate.Version = "20260906"
			}
			called := false
			err := applyUpdate(t.Context(), client, candidate, path, []string{path, "run", "--config", "test.yml"}, os.Environ(), func(name string, args, environment []string) error {
				called = true
				data, _ := os.ReadFile(name)
				if !bytes.Equal(data, binary) {
					t.Error("replacement not installed")
				}
				return errors.New("exec failed")
			})
			if err == nil {
				t.Fatal("update unexpectedly succeeded")
			}
			if called == wrongVersion {
				t.Fatal("wrong execution decision")
			}
			data, _ := os.ReadFile(path)
			if !bytes.Equal(data, old) {
				t.Fatal("working binary lost")
			}
		})
	}
}
