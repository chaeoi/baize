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
	"testing/iotest"

	"baize/shared/model"
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
		{latest: "2026.11.100", current: "2026.11.99", newer: true},
		{latest: "2026.11.99", current: "2026.11.100"},
		{latest: "2026.11.0", current: "2026.9.99", newer: true},
		{latest: "2027.1.0", current: "2026.12.99", newer: true},
		{latest: "2026.1.1", current: "2025.12.100", newer: true},
		{latest: "2025.12.100", current: "2026.1.1"},
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
	httpClient := server.Client()
	original := httpClient.Transport
	httpClient.Transport = releaseTransport(func(r *http.Request) (*http.Response, error) {
		if r.Method == http.MethodHead && r.URL.Host == "github.com" && r.URL.Path == "/chaeoi/baize/releases/latest" {
			versions++
			return &http.Response{StatusCode: http.StatusFound, Header: http.Header{"Location": {"https://github.com/chaeoi/baize/releases/tag/2026.11.2"}}, Body: http.NoBody, Request: r}, nil
		}
		return original.RoundTrip(r)
	})
	client := NewGitHubClient(httpClient)
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
		status, body := http.StatusOK, ""
		headers := make(http.Header)
		if r.Method == http.MethodHead {
			status = http.StatusFound
			headers.Set("Location", "https://github.com/chaeoi/baize/releases/tag/2026.11.2")
		} else if r.URL.Host == "mirror.example" {
			status = http.StatusBadGateway
		} else if strings.HasSuffix(r.URL.Path, "/SHA256SUMS") {
			body = strings.Repeat("a", 64) + "  " + asset + "\n"
		}
		return &http.Response{StatusCode: status, Status: http.StatusText(status), Header: headers, Body: io.NopCloser(strings.NewReader(body)), Request: r}, nil
	})})
	update, err := client.Check(t.Context(), "2026.11.1", runtime.GOOS, runtime.GOARCH)
	if err != nil || update == nil || update.URL != "https://github.com/chaeoi/baize/releases/download/2026.11.2/"+asset {
		t.Fatalf("fallback failed: %+v %v", update, err)
	}
}

func TestLatestReleaseRejectsInvalidRedirect(t *testing.T) {
	for _, location := range []string{
		"", "https://example.com/chaeoi/baize/releases/tag/2026.11.2",
		"https://github.com/other/baize/releases/tag/2026.11.2",
		"https://github.com/chaeoi/baize/releases/tag/invalid",
		"https://github.com/chaeoi/baize/releases/tag/2026.11.2/extra",
	} {
		t.Run(location, func(t *testing.T) {
			client := NewGitHubClient(&http.Client{Transport: releaseTransport(func(r *http.Request) (*http.Response, error) {
				if r.Method != http.MethodHead {
					t.Fatal("invalid version caused an asset download")
				}
				return &http.Response{StatusCode: http.StatusFound, Header: http.Header{"Location": {location}}, Body: http.NoBody, Request: r}, nil
			})})
			if _, err := client.Check(t.Context(), "2026.11.1", runtime.GOOS, runtime.GOARCH); err == nil {
				t.Fatal("invalid release accepted")
			}
		})
	}
}

func TestReleaseBinaryFallbackDoesNotMixSources(t *testing.T) {
	asset := "baize-agent-linux-" + runtime.GOARCH
	payload := []byte("complete release executable")
	hash := sha256.Sum256(payload)
	update := model.UpdateInfo{
		Version: "2026.11.2", OS: "linux", Arch: runtime.GOARCH,
		SHA256: hex.EncodeToString(hash[:]),
		URL:    "https://mirror.example/github.com/chaeoi/baize/releases/download/2026.11.2/" + asset,
	}
	for _, failure := range []string{"http-error", "partial-body", "wrong-checksum", "both-unavailable"} {
		t.Run(failure, func(t *testing.T) {
			t.Setenv("BAIZE_MIRROR", "https://mirror.example")
			var hosts []string
			client := NewGitHubClient(&http.Client{Transport: releaseTransport(func(r *http.Request) (*http.Response, error) {
				hosts = append(hosts, r.URL.Host)
				status := http.StatusOK
				var body io.Reader = bytes.NewReader(payload)
				if r.URL.Host == "mirror.example" {
					switch failure {
					case "http-error", "both-unavailable":
						status = http.StatusBadGateway
					case "partial-body":
						body = io.MultiReader(strings.NewReader("partial mirror response"), iotest.ErrReader(io.ErrUnexpectedEOF))
					case "wrong-checksum":
						body = strings.NewReader("corrupt mirror response")
					}
				} else if r.URL.String() != "https://github.com/chaeoi/baize/releases/download/2026.11.2/"+asset {
					t.Fatalf("fallback changed the pinned asset: %s", r.URL)
				} else if failure == "both-unavailable" {
					status = http.StatusServiceUnavailable
				}
				return &http.Response{StatusCode: status, Status: http.StatusText(status), Header: make(http.Header), Body: io.NopCloser(body), Request: r}, nil
			})})
			var output bytes.Buffer
			err := client.Download(t.Context(), update, &output)
			if strings.Join(hosts, ",") != "mirror.example,github.com" {
				t.Fatalf("fallback was not attempted: %v", hosts)
			}
			if failure == "both-unavailable" {
				if err == nil || output.Len() != 0 {
					t.Fatalf("failed sources wrote a candidate: %q, %v", output.Bytes(), err)
				}
			} else if err != nil || !bytes.Equal(output.Bytes(), payload) {
				t.Fatalf("sources were mixed or fallback failed: %q, %v", output.Bytes(), err)
			}
		})
	}
}
