package agent

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"runtime"
	"strings"
	"testing"
	"testing/iotest"
	"time"

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
		case prefix + "download/agent-2026.11.2/SHA256SUMS":
			manifests++
			fmt.Fprintf(w, "%s  %s\n", digest, asset)
		case prefix + "download/agent-2026.11.2/" + asset:
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
		if r.URL.Host == "api.github.com" && r.URL.Path == "/repos/chaeoi/baize/releases" {
			versions++
			return releaseListResponse(r, []map[string]any{{"tag_name": "agent-2026.11.2"}}), nil
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
		if r.URL.Host == "api.github.com" {
			return releaseListResponse(r, []map[string]any{{"tag_name": "agent-2026.11.2"}}), nil
		} else if r.URL.Host == "mirror.example" {
			status = http.StatusBadGateway
		} else if strings.HasSuffix(r.URL.Path, "/SHA256SUMS") {
			body = strings.Repeat("a", 64) + "  " + asset + "\n"
		}
		return &http.Response{StatusCode: status, Status: http.StatusText(status), Header: headers, Body: io.NopCloser(strings.NewReader(body)), Request: r}, nil
	})})
	update, err := client.Check(t.Context(), "2026.11.1", runtime.GOOS, runtime.GOARCH)
	if err != nil || update == nil || update.URL != "https://github.com/chaeoi/baize/releases/download/agent-2026.11.2/"+asset {
		t.Fatalf("fallback failed: %+v %v", update, err)
	}
}

func releaseListResponse(request *http.Request, releases []map[string]any) *http.Response {
	data, _ := json.Marshal(releases)
	return &http.Response{StatusCode: http.StatusOK, Header: make(http.Header), Body: io.NopCloser(bytes.NewReader(data)), Request: request}
}

func TestLatestAgentReleaseFiltersAndPaginates(t *testing.T) {
	first := make([]map[string]any, 100)
	for i := range first {
		first[i] = map[string]any{"tag_name": "dashboard-2029.1.1"}
	}
	first[0] = map[string]any{"tag_name": "agent-2026.11.99"}
	first[1] = map[string]any{"tag_name": "agent-2026.11.9"}
	first[2] = map[string]any{"tag_name": "2029.1.100"}
	first[3] = map[string]any{"tag_name": "agent-2029.1.1", "draft": true}
	first[4] = map[string]any{"tag_name": "agent-2029.1.2", "prerelease": true}
	first[5] = map[string]any{"tag_name": "agent-2029.13.1"}
	first[6] = map[string]any{"tag_name": "agent-2029.1.01"}
	first[7] = map[string]any{"tag_name": "agent-2029.1.1/extra"}
	first[8] = map[string]any{"tag_name": "agent-2029.1.0"}
	first[9] = map[string]any{"tag_name": "agent-2029.1.4294967296"}
	for _, nextYear := range []bool{false, true} {
		t.Run(fmt.Sprint(nextYear), func(t *testing.T) {
			second := []map[string]any{{"tag_name": "agent-2026.11.100"}, {"tag_name": "agent-2025.12.100"}}
			want := "2026.11.100"
			if nextYear {
				second = append(second, map[string]any{"tag_name": "agent-2027.1.1"})
				want = "2027.1.1"
			}
			requests := 0
			client := NewGitHubClient(&http.Client{Transport: releaseTransport(func(r *http.Request) (*http.Response, error) {
				requests++
				if r.Method != http.MethodGet || r.URL.Host != "api.github.com" || r.URL.Path != "/repos/chaeoi/baize/releases" || r.URL.Query().Get("per_page") != "100" {
					t.Fatalf("unexpected lookup: %s %s", r.Method, r.URL)
				}
				switch r.URL.Query().Get("page") {
				case "1":
					return releaseListResponse(r, first), nil
				case "2":
					return releaseListResponse(r, second), nil
				default:
					t.Fatalf("unexpected page: %s", r.URL)
					return nil, nil
				}
			})})
			got, err := client.latestVersion(t.Context())
			if err != nil || got != want || requests != 2 {
				t.Fatalf("version=%q requests=%d error=%v", got, requests, err)
			}
		})
	}
}

func TestCheckWithoutPublishedAgentRelease(t *testing.T) {
	for _, releases := range [][]map[string]any{
		{},
		{{"tag_name": "2026.11.9"}},
		{{"tag_name": "dashboard-2026.11.10"}, {"tag_name": "agent-2026.11.10", "draft": true}, {"tag_name": "agent-2026.11.11", "prerelease": true}},
	} {
		requests := 0
		client := NewGitHubClient(&http.Client{Transport: releaseTransport(func(r *http.Request) (*http.Response, error) {
			requests++
			if r.URL.Host != "api.github.com" {
				t.Fatal("no published Agent release must not download assets")
			}
			return releaseListResponse(r, releases), nil
		})})
		update, err := client.Check(t.Context(), "2026.11.9", runtime.GOOS, runtime.GOARCH)
		if err != nil || update != nil || requests != 1 {
			t.Fatalf("update=%+v error=%v requests=%d", update, err, requests)
		}
	}
}

func TestReleaseListFailuresDoNotDownloadAssets(t *testing.T) {
	for _, fixture := range []struct {
		status int
		body   string
	}{
		{http.StatusForbidden, `{"message":"API rate limit exceeded"}`},
		{http.StatusBadGateway, "Bad Gateway"},
		{http.StatusOK, "{"},
		{http.StatusOK, "null"},
		{http.StatusOK, `{}`},
	} {
		t.Run(fmt.Sprintf("%d/%s", fixture.status, fixture.body), func(t *testing.T) {
			requests := 0
			client := NewGitHubClient(&http.Client{Transport: releaseTransport(func(r *http.Request) (*http.Response, error) {
				requests++
				if r.URL.Host != "api.github.com" {
					t.Fatal("failed lookup caused an asset download")
				}
				return &http.Response{StatusCode: fixture.status, Status: http.StatusText(fixture.status), Header: make(http.Header), Body: io.NopCloser(strings.NewReader(fixture.body)), Request: r}, nil
			})})
			if _, err := client.Check(t.Context(), "2026.11.9", runtime.GOOS, runtime.GOARCH); err == nil || requests != 1 {
				t.Fatalf("failed release lookup: requests=%d error=%v", requests, err)
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
		URL:    "https://mirror.example/github.com/chaeoi/baize/releases/download/agent-2026.11.2/" + asset,
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
				} else if r.URL.String() != "https://github.com/chaeoi/baize/releases/download/agent-2026.11.2/"+asset {
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

func TestReleaseDownloadHasASeparateTransferTimeout(t *testing.T) {
	payload := []byte("complete release executable")
	hash := sha256.Sum256(payload)
	server := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		time.Sleep(40 * time.Millisecond)
		w.Write(payload)
	}))
	defer server.Close()
	t.Setenv("BAIZE_MIRROR", server.URL)
	httpClient := server.Client()
	httpClient.Timeout = 10 * time.Millisecond
	transport := httpClient.Transport
	httpClient.Transport = releaseTransport(func(r *http.Request) (*http.Response, error) {
		if r.URL.Host == "github.com" {
			return nil, fmt.Errorf("unexpected fallback from a healthy mirror")
		}
		return transport.RoundTrip(r)
	})
	client := NewGitHubClient(httpClient)
	var output bytes.Buffer
	err := client.Download(t.Context(), model.UpdateInfo{
		Version: "2026.11.5", OS: "linux", Arch: runtime.GOARCH,
		SHA256: hex.EncodeToString(hash[:]), URL: server.URL + "/binary",
	}, &output)
	if err != nil || !bytes.Equal(output.Bytes(), payload) {
		t.Fatalf("telemetry timeout interrupted the release download: %v", err)
	}
	if httpClient.Timeout != 10*time.Millisecond {
		t.Fatal("download changed the shared HTTP client timeout")
	}
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	if err := client.Download(ctx, model.UpdateInfo{URL: server.URL + "/binary"}, io.Discard); !errors.Is(err, context.Canceled) {
		t.Fatalf("cancelled update did not stop: %v", err)
	}
}
