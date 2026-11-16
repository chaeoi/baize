package agent

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"testing"

	"baize/shared/model"
)

func TestUpdateValidatesBeforeDrainingAndReplacesSingleBinary(t *testing.T) {
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
func main(){for i,arg:=range os.Args[1:]{if arg=="--version"{fmt.Println("2026.11.2");return};if arg=="--config"{data,err:=os.ReadFile(os.Args[i+2]);if err!=nil||string(data)!="accept"{os.Exit(3)}}};return}
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
	client := testUpdateDownloader{client: server.Client(), url: server.URL}
	hash := sha256.Sum256(binary)
	update := model.UpdateInfo{Version: "2026.11.2", OS: "linux", Arch: runtime.GOARCH, SHA256: hex.EncodeToString(hash[:])}
	for _, test := range []struct {
		name         string
		invalid      bool
		handoffError bool
	}{
		{name: "version-mismatch", invalid: true},
		{name: "checksum-mismatch", invalid: true},
		{name: "config-rejected", invalid: true},
		{name: "handoff-failed", handoffError: true},
		{name: "success"},
	} {
		t.Run(test.name, func(t *testing.T) {
			path := filepath.Join(t.TempDir(), "agent")
			old := []byte("working executable")
			if err := os.WriteFile(path, old, 0755); err != nil {
				t.Fatal(err)
			}
			configPath := path + ".yml"
			content := "accept"
			if test.name == "config-rejected" {
				content = "reject"
			}
			if err := os.WriteFile(configPath, []byte(content), 0600); err != nil {
				t.Fatal(err)
			}
			candidate := update
			if test.name == "version-mismatch" {
				candidate.Version = "2026.11.3"
			}
			if test.name == "checksum-mismatch" {
				candidate.SHA256 = "wrong"
			}
			drained := false
			err := applyUpdate(t.Context(), client, candidate, path, []string{path, "run", "--config", configPath}, func() error {
				drained = true
				data, _ := os.ReadFile(path)
				if !bytes.Equal(data, old) {
					t.Error("binary changed before collection drained")
				}
				if test.handoffError {
					return errors.New("flush failed")
				}
				return nil
			})
			wantFailure := test.invalid || test.handoffError
			if (err != nil) != wantFailure {
				t.Fatalf("unexpected update result: %v", err)
			}
			if drained == test.invalid {
				t.Fatalf("wrong drain decision: %v", drained)
			}
			expected := binary
			if wantFailure {
				expected = old
			}
			actual, _ := os.ReadFile(path)
			if !bytes.Equal(actual, expected) {
				t.Fatal("wrong installed binary")
			}
			items, err := os.ReadDir(filepath.Dir(path))
			if err != nil {
				t.Fatal(err)
			}
			if len(items) != 2 {
				t.Fatalf("unexpected update artifacts: %v", items)
			}
		})
	}
}

type testUpdateDownloader struct {
	client *http.Client
	url    string
}

func (d testUpdateDownloader) Download(ctx context.Context, _ model.UpdateInfo, writer io.Writer) error {
	request, err := http.NewRequestWithContext(ctx, http.MethodGet, d.url, nil)
	if err != nil {
		return err
	}
	response, err := d.client.Do(request)
	if err != nil {
		return err
	}
	defer response.Body.Close()
	_, err = io.Copy(writer, response.Body)
	return err
}
