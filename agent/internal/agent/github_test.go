package agent

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"runtime"
	"strings"
	"testing"
)

type releaseTransport func(*http.Request) (*http.Response, error)

func (f releaseTransport) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }

func TestReleaseVersionOrdering(t *testing.T) {
	for _, test := range []struct {
		latest, current string
		newer, invalid  bool
	}{
		{latest: "2026.11.1", current: "2026.11.1"},
		{latest: "2026.11.2", current: "2026.11.1", newer: true},
		{latest: "2026.11.10", current: "2026.11.9", newer: true},
		{latest: "2026.11.9", current: "2026.11.10"},
		{latest: "2026.11.0", current: "2026.9.99", newer: true},
		{latest: "2027.1.0", current: "2026.12.99", newer: true},
		{latest: "2026.10.99", current: "2026.11.1"},
		{latest: "2026.13.1", current: "2026.11.1", invalid: true},
		{latest: "2026.0.1", current: "2026.11.1", invalid: true},
		{latest: "2026.11.01", current: "2026.11.1", invalid: true},
		{latest: "2026.11.1-extra", current: "2026.11.1", invalid: true},
		{latest: "2026.11.4294967296", current: "2026.11.1", invalid: true},
		{latest: "2026.11.2", current: "dev", invalid: true},
		{latest: "20261116", current: "2026.11.1", invalid: true},
	} {
		t.Run(test.latest+"/"+test.current, func(t *testing.T) {
			newer, err := newerVersion(test.latest, test.current)
			if (err != nil) != test.invalid || newer != test.newer {
				t.Fatalf("newer=%v error=%v", newer, err)
			}
		})
	}
}

func TestMirrorOnlyDownloadsNewerVersionAndPinsAssets(t *testing.T) {
	asset := "baize-agent-linux-" + runtime.GOARCH
	data := []byte("release payload")
	sum := sha256.Sum256(data)
	digest := hex.EncodeToString(sum[:])
	versions, manifests, downloads := 0, 0, 0
	const prefix = "/github.com/chaeoi/baize/releases/"
	server := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case prefix + "latest/download/VERSION":
			versions++
			fmt.Fprintln(w, "2026.11.2")
		case prefix + "download/2026.11.2/SHA256SUMS":
			manifests++
			fmt.Fprintf(w, "%s  %s\n", digest, asset)
		case prefix + "download/2026.11.2/" + asset:
			downloads++
			w.Write(data)
		default:
			t.Errorf("unexpected download: %s", r.URL.Path)
			http.NotFound(w, r)
		}
	}))
	defer server.Close()
	t.Setenv("BAIZE_MIRROR", server.URL)
	client := NewGitHubClient(server.Client())
	for _, current := range []string{"2026.11.2", "2026.11.10"} {
		update, err := client.Check(t.Context(), current, runtime.GOOS, runtime.GOARCH)
		if err != nil || update != nil || downloads != 0 || manifests != 0 {
			t.Fatalf("unchanged or older release downloaded: %+v %v", update, err)
		}
	}
	update, err := client.Check(t.Context(), "2026.11.1", runtime.GOOS, runtime.GOARCH)
	if err != nil || update == nil || update.Version != "2026.11.2" || update.SHA256 != digest {
		t.Fatalf("new release missed: %+v %v", update, err)
	}
	if downloads != 0 {
		t.Fatal("version check downloaded an executable")
	}
	var output bytes.Buffer
	if err := client.Download(t.Context(), *update, &output); err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(output.Bytes(), data) || versions != 3 || manifests != 1 || downloads != 1 {
		t.Fatalf("versions=%d manifests=%d downloads=%d", versions, manifests, downloads)
	}
}

func TestReleaseCheckFallsBackToGitHub(t *testing.T) {
	t.Setenv("BAIZE_MIRROR", "https://mirror.example")
	asset := "baize-agent-linux-" + runtime.GOARCH
	client := NewGitHubClient(&http.Client{Transport: releaseTransport(func(r *http.Request) (*http.Response, error) {
		status, body := http.StatusOK, "2026.11.2\n"
		if r.URL.Host == "mirror.example" {
			status = http.StatusBadGateway
		} else if strings.HasSuffix(r.URL.Path, "/SHA256SUMS") {
			body = strings.Repeat("a", 64) + "  " + asset + "\n"
		}
		return &http.Response{StatusCode: status, Status: http.StatusText(status), Body: io.NopCloser(strings.NewReader(body)), Request: r}, nil
	})})
	update, err := client.Check(t.Context(), "2026.11.1", runtime.GOOS, runtime.GOARCH)
	if err != nil || update == nil || update.URL != "https://github.com/chaeoi/baize/releases/download/2026.11.2/"+asset {
		t.Fatalf("fallback failed: %+v %v", update, err)
	}
}
