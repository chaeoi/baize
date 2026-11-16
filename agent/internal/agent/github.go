package agent

import (
	"bufio"
	"context"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"runtime"
	"strconv"
	"strings"
	"time"

	"baize/shared/model"
)

const updateRepository = "chaeoi/baize"
const defaultMirror = "https://gitwarp.canghai.org"

type githubClient struct {
	http *http.Client
}

func NewGitHubClient(httpClient *http.Client) *githubClient {
	return &githubClient{http: httpClient}
}

func (c *githubClient) Check(ctx context.Context, current, goos, arch string) (*model.UpdateInfo, error) {
	if goos != runtime.GOOS || arch != runtime.GOARCH {
		return nil, fmt.Errorf("unsupported update platform %s/%s", goos, arch)
	}
	version, err := c.latestVersion(ctx)
	if err != nil {
		return nil, err
	}
	newer, err := newerVersion(version, current)
	if err != nil || !newer {
		return nil, err
	}
	base := strings.TrimRight(os.Getenv("BAIZE_MIRROR"), "/")
	if base == "" {
		base = defaultMirror
	}
	update, mirrorErr := c.checkRelease(ctx, base+"/github.com/"+updateRepository+"/releases", version, goos, arch)
	if mirrorErr == nil {
		return update, nil
	}
	update, err = c.checkRelease(ctx, "https://github.com/"+updateRepository+"/releases", version, goos, arch)
	if err != nil {
		return nil, errors.Join(mirrorErr, err)
	}
	return update, nil
}

func (c *githubClient) latestVersion(ctx context.Context) (string, error) {
	address := "https://github.com/" + updateRepository + "/releases/latest?cachebust=" + strconv.FormatInt(time.Now().UnixNano(), 10)
	request, err := http.NewRequestWithContext(ctx, http.MethodHead, address, nil)
	if err != nil {
		return "", err
	}
	request.Header.Set("User-Agent", "baize-agent/update")
	client := *c.http
	client.CheckRedirect = func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }
	response, err := client.Do(request)
	if err != nil {
		return "", err
	}
	defer response.Body.Close()
	if response.StatusCode < 300 || response.StatusCode >= 400 {
		return "", fmt.Errorf("latest release lookup returned %s", response.Status)
	}
	location, err := response.Location()
	if err != nil {
		return "", err
	}
	prefix := "/" + updateRepository + "/releases/tag/"
	if location.Scheme != "https" || location.Host != "github.com" || !strings.HasPrefix(location.Path, prefix) {
		return "", fmt.Errorf("GitHub returned an invalid release location")
	}
	return strings.TrimPrefix(location.Path, prefix), nil
}

func (c *githubClient) checkRelease(ctx context.Context, base, version, goos, arch string) (*model.UpdateInfo, error) {
	query := "?cachebust=" + strconv.FormatInt(time.Now().UnixNano(), 10)
	// Pin all remaining downloads to the version we just checked.
	release := base + "/download/" + version
	asset := "baize-agent-" + goos + "-" + arch
	sums, err := c.downloadBytes(ctx, release+"/SHA256SUMS"+query, 64*1024)
	if err != nil {
		return nil, err
	}
	digest := checksumForAsset(sums, asset)
	if digest == "" {
		return nil, fmt.Errorf("release %s has no SHA-256 checksum for %s", version, asset)
	}
	return &model.UpdateInfo{Version: version, OS: goos, Arch: arch, SHA256: digest, URL: release + "/" + asset}, nil
}

func newerVersion(latest, current string) (bool, error) {
	parse := func(value string) ([3]uint64, error) {
		var result [3]uint64
		parts := strings.Split(value, ".")
		if len(parts) != 3 || len(parts[0]) != 4 {
			return result, fmt.Errorf("invalid release version %q; expected year.month.sequence", value)
		}
		for index, part := range parts {
			number, err := strconv.ParseUint(part, 10, 32)
			if err != nil || strconv.FormatUint(number, 10) != part {
				return result, fmt.Errorf("invalid release version %q", value)
			}
			result[index] = number
		}
		if result[0] < 2000 || result[1] < 1 || result[1] > 12 {
			return result, fmt.Errorf("invalid release version %q", value)
		}
		return result, nil
	}
	want, err := parse(latest)
	if err != nil {
		return false, err
	}
	have, err := parse(current)
	if err != nil {
		return false, err
	}
	for index := range want {
		if want[index] != have[index] {
			return want[index] > have[index], nil
		}
	}
	return false, nil
}

func (c *githubClient) downloadBytes(ctx context.Context, address string, limit int64) ([]byte, error) {
	request, err := http.NewRequestWithContext(ctx, http.MethodGet, address, nil)
	if err != nil {
		return nil, err
	}
	request.Header.Set("User-Agent", "baize-agent/update")
	response, err := c.http.Do(request)
	if err != nil {
		return nil, err
	}
	defer response.Body.Close()
	if response.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("release metadata download returned %s", response.Status)
	}
	data, err := io.ReadAll(io.LimitReader(response.Body, limit+1))
	if err != nil {
		return nil, err
	}
	if int64(len(data)) > limit {
		return nil, fmt.Errorf("release metadata exceeds %d bytes", limit)
	}
	return data, nil
}

func checksumForAsset(data []byte, asset string) string {
	scanner := bufio.NewScanner(strings.NewReader(string(data)))
	for scanner.Scan() {
		fields := strings.Fields(scanner.Text())
		if len(fields) >= 2 && strings.TrimPrefix(fields[1], "*") == asset && len(fields[0]) == 64 {
			if _, err := hex.DecodeString(fields[0]); err == nil {
				return strings.ToLower(fields[0])
			}
		}
	}
	return ""
}

func (c *githubClient) Download(ctx context.Context, update model.UpdateInfo, writer io.Writer) error {
	parsed, err := url.Parse(update.URL)
	if err != nil || parsed == nil || parsed.Scheme != "https" || parsed.Host != "github.com" {
		mirror := strings.TrimRight(os.Getenv("BAIZE_MIRROR"), "/")
		if mirror == "" {
			mirror = defaultMirror
		}
		mirrorURL, mirrorErr := url.Parse(mirror)
		if mirrorErr != nil || mirrorURL == nil || mirrorURL.Scheme != "https" || parsed == nil || parsed.Scheme != "https" || parsed.Host != mirrorURL.Host {
			return fmt.Errorf("update source returned an invalid update URL")
		}
	}
	request, err := http.NewRequestWithContext(ctx, http.MethodGet, update.URL, nil)
	if err != nil {
		return err
	}
	request.Header.Set("User-Agent", "baize-agent/"+update.Version)
	response, err := c.http.Do(request)
	if err != nil {
		return err
	}
	defer response.Body.Close()
	if response.StatusCode != http.StatusOK {
		return fmt.Errorf("GitHub update download returned %s", response.Status)
	}
	written, err := io.Copy(writer, io.LimitReader(response.Body, 128<<20+1))
	if err != nil {
		return err
	}
	if written > 128<<20 {
		return fmt.Errorf("update exceeds 128 MiB")
	}
	return nil
}
