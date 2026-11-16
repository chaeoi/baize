package agent

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

type releaseTransport func(*http.Request) (*http.Response, error)

func (f releaseTransport) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }

func TestGitHubDetectsChangedBuildWithSameVersion(t *testing.T) {
	asset := "baize-agent-linux-" + runtime.GOARCH
	digest := strings.Repeat("a", 64)
	client := NewGitHubClient(&http.Client{Transport: releaseTransport(func(r *http.Request) (*http.Response, error) {
		h := make(http.Header)
		status, body := 200, digest+"  "+asset+"\n"
		if r.Method == http.MethodHead {
			status = 302
			h.Set("Location", "https://github.com/chaeoi/baize/releases/download/20261116/"+asset)
		}
		return &http.Response{StatusCode: status, Header: h, Body: io.NopCloser(strings.NewReader(body)), Request: r}, nil
	})}, "/proc/self/exe")
	client.currentHash = strings.Repeat("b", 64)
	update, err := client.checkGitHub(t.Context(), "20261116", runtime.GOOS, runtime.GOARCH)
	if err != nil || update == nil || update.SHA256 != digest {
		t.Fatalf("same-day build missed: %+v %v", update, err)
	}
	client.currentHash = digest
	update, err = client.checkGitHub(t.Context(), "20261116", runtime.GOOS, runtime.GOARCH)
	if err != nil || update != nil {
		t.Fatalf("identical build must be skipped: %+v %v", update, err)
	}
}

func TestMirrorChecksManifestAndRetriesConsumedCandidate(t *testing.T) {
	root := t.TempDir()
	source := filepath.Join(root, "agent", "cmd", "baize-agent")
	if err := os.MkdirAll(source, 0700); err != nil {
		t.Fatal(err)
	}
	for name, body := range map[string]string{
		"go.mod":                        "module baize\n\ngo 1.26.5\n",
		"agent/cmd/baize-agent/main.go": "package main\nimport \"fmt\"\nfunc main(){fmt.Println(\"20261116\")}\n",
	} {
		if err := os.WriteFile(filepath.Join(root, name), []byte(body), 0600); err != nil {
			t.Fatal(err)
		}
	}
	path := filepath.Join(root, "candidate")
	command := exec.Command("go", "build", "-o", path, "./agent/cmd/baize-agent")
	command.Dir = root
	command.Env = append(os.Environ(), "CGO_ENABLED=0")
	if output, err := command.CombinedOutput(); err != nil {
		t.Fatalf("build fixture: %v %s", err, output)
	}
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	sum := sha256.Sum256(data)
	digest := hex.EncodeToString(sum[:])
	asset := "baize-agent-linux-" + runtime.GOARCH
	downloads, manifests := 0, 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("ETag", "unchanged-etag")
		if strings.HasSuffix(r.URL.Path, "SHA256SUMS") {
			manifests++
			fmt.Fprintf(w, "%s  %s\n", digest, asset)
		} else {
			downloads++
			w.Write(data)
		}
	}))
	defer server.Close()
	t.Setenv("BAIZE_MIRROR", server.URL)
	client := NewGitHubClient(server.Client(), path)
	update, err := client.checkMirror(t.Context(), "20261116", runtime.GOOS, runtime.GOARCH)
	if err != nil || update != nil || downloads != 0 {
		t.Fatalf("unchanged binary downloaded: %v %v downloads=%d", update, err, downloads)
	}
	client.currentHash = strings.Repeat("b", 64)
	for attempt := 0; attempt < 2; attempt++ {
		update, err = client.checkMirror(t.Context(), "20261116", runtime.GOOS, runtime.GOARCH)
		if err != nil || update == nil {
			t.Fatalf("changed build missed on attempt %d: %v %v", attempt, update, err)
		}
		if err := client.Download(t.Context(), *update, io.Discard); err != nil {
			t.Fatal(err)
		}
	}
	if downloads != 2 || manifests != 3 {
		t.Fatalf("downloads=%d manifests=%d", downloads, manifests)
	}
}
