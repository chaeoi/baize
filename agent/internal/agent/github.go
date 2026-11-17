package agent

import (
	"bufio"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"runtime"
	"slices"
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
	if err != nil || version == "" {
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
	latest := ""
	var highest [3]uint64
	query := "&cachebust=" + strconv.FormatInt(time.Now().UnixNano(), 10)
	for page := 1; ; page++ {
		address := fmt.Sprintf("https://api.github.com/repos/%s/releases?per_page=100&page=%d%s", updateRepository, page, query)
		data, err := c.downloadBytes(ctx, address, 4<<20)
		if err != nil {
			return "", err
		}
		var releases []struct {
			Tag        string `json:"tag_name"`
			Draft      bool   `json:"draft"`
			Prerelease bool   `json:"prerelease"`
		}
		if err := json.Unmarshal(data, &releases); err != nil {
			return "", fmt.Errorf("decode Agent releases: %w", err)
		}
		if releases == nil {
			return "", fmt.Errorf("GitHub returned an invalid release list")
		}
		for _, release := range releases {
			if release.Draft || release.Prerelease || !strings.HasPrefix(release.Tag, "agent-") {
				continue
			}
			version := strings.TrimPrefix(release.Tag, "agent-")
			number, err := releaseVersion(version)
			if err != nil || number[2] == 0 {
				continue
			}
			if slices.Compare(number[:], highest[:]) > 0 {
				latest, highest = version, number
			}
		}
		if len(releases) < 100 {
			return latest, nil
		}
	}
}

func (c *githubClient) checkRelease(ctx context.Context, base, version, goos, arch string) (*model.UpdateInfo, error) {
	query := "?cachebust=" + strconv.FormatInt(time.Now().UnixNano(), 10)
	// Pin all remaining downloads to the version we just checked.
	release := base + "/download/agent-" + version
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

func releaseVersion(value string) ([3]uint64, error) {
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

func newerVersion(latest, current string) (bool, error) {
	want, err := releaseVersion(latest)
	if err != nil {
		return false, err
	}
	have, err := releaseVersion(current)
	if err != nil {
		return false, err
	}
	return slices.Compare(want[:], have[:]) > 0, nil
}

func (c *githubClient) downloadBytes(ctx context.Context, address string, limit int64) ([]byte, error) {
	request, err := http.NewRequestWithContext(ctx, http.MethodGet, address, nil)
	if err != nil {
		return nil, err
	}
	request.Header.Set("User-Agent", "baize-agent/update")
	request.Header.Set("Accept", "application/vnd.github+json")
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
	addresses := []string{update.URL}
	if parsed.Host != "github.com" {
		addresses = append(addresses, "https://github.com/"+updateRepository+"/releases/download/agent-"+update.Version+"/baize-agent-"+update.OS+"-"+update.Arch)
	}
	// Stage each source separately so a partial mirror response cannot be mixed
	// with the fallback download in the caller's file or checksum stream.
	staging, err := os.CreateTemp("", "baize-download-*")
	if err != nil {
		return err
	}
	defer os.Remove(staging.Name())
	defer staging.Close()
	var failures []error
	for _, address := range addresses {
		if err := ctx.Err(); err != nil {
			return err
		}
		if err := staging.Truncate(0); err != nil {
			return err
		}
		if _, err := staging.Seek(0, io.SeekStart); err != nil {
			return err
		}
		hash := sha256.New()
		err := c.downloadAsset(ctx, address, update.Version, io.MultiWriter(staging, hash))
		if err == nil && hex.EncodeToString(hash.Sum(nil)) != update.SHA256 {
			err = fmt.Errorf("downloaded Agent SHA-256 does not match release")
		}
		if err != nil {
			failures = append(failures, err)
			continue
		}
		if _, err := staging.Seek(0, io.SeekStart); err != nil {
			return err
		}
		_, err = io.Copy(writer, staging)
		return err
	}
	return errors.Join(failures...)
}

func (c *githubClient) downloadAsset(ctx context.Context, address, version string, writer io.Writer) error {
	request, err := http.NewRequestWithContext(ctx, http.MethodGet, address, nil)
	if err != nil {
		return err
	}
	request.Header.Set("User-Agent", "baize-agent/"+version)
	client := *c.http
	client.Timeout = 2 * time.Minute
	response, err := client.Do(request)
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
