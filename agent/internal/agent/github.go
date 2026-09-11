package agent

import (
	"bufio"
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"os/exec"
	"runtime"
	"strconv"
	"strings"
	"sync"
	"time"

	"baize/shared/agentbinary"
	"baize/shared/model"
)

const updateRepository = "chaeoi/baize"
const defaultMirror = "https://gitwarp.canghai.org"

type githubClient struct {
	http       *http.Client
	mu         sync.Mutex
	mirrorETag string
	cachedURL  string
	cachedHash string
	cachedData []byte
}

func NewGitHubClient(httpClient *http.Client) *githubClient {
	return &githubClient{http: httpClient}
}

func (c *githubClient) Check(ctx context.Context, current, goos, arch string) (*model.UpdateInfo, error) {
	if goos != runtime.GOOS || arch != runtime.GOARCH {
		return nil, fmt.Errorf("unsupported update platform %s/%s", goos, arch)
	}
	if update, err := c.checkMirror(ctx, current, goos, arch); err == nil {
		return update, nil
	}
	return c.checkGitHub(ctx, current, goos, arch)
}

func (c *githubClient) checkGitHub(ctx context.Context, current, goos, arch string) (*model.UpdateInfo, error) {
	assetName := "baize-agent-" + goos + "-" + arch
	latestURL := "https://github.com/" + updateRepository + "/releases/latest/download/" + assetName
	request, err := http.NewRequestWithContext(ctx, http.MethodHead, latestURL, nil)
	if err != nil {
		return nil, err
	}
	request.Header.Set("User-Agent", "baize-agent/"+current)
	redirectClient := *c.http
	redirectClient.CheckRedirect = func(_ *http.Request, _ []*http.Request) error { return http.ErrUseLastResponse }
	response, err := redirectClient.Do(request)
	if err != nil {
		return nil, err
	}
	defer response.Body.Close()
	if response.StatusCode < http.StatusMultipleChoices || response.StatusCode >= http.StatusMultipleChoices+100 {
		message, _ := io.ReadAll(io.LimitReader(response.Body, 4096))
		return nil, fmt.Errorf("GitHub release redirect returned %s: %s", response.Status, strings.TrimSpace(string(message)))
	}
	location := response.Header.Get("Location")
	parsed, err := url.Parse(location)
	if err != nil || parsed.Scheme != "https" || parsed.Host != "github.com" {
		return nil, fmt.Errorf("GitHub returned an invalid latest release location")
	}
	owner, repository, _ := strings.Cut(updateRepository, "/")
	parts := strings.Split(strings.Trim(parsed.Path, "/"), "/")
	if len(parts) != 6 || parts[0] != owner || parts[1] != repository ||
		parts[2] != "releases" || parts[3] != "download" || parts[5] != assetName {
		return nil, fmt.Errorf("GitHub returned an invalid latest release path")
	}
	version := parts[4]
	if !isNewerVersion(version, current) {
		return nil, nil
	}
	checksumURL := "https://github.com/" + updateRepository + "/releases/download/" + version + "/SHA256SUMS"
	checksumRequest, err := http.NewRequestWithContext(ctx, http.MethodGet, checksumURL, nil)
	if err != nil {
		return nil, err
	}
	checksumRequest.Header.Set("User-Agent", "baize-agent/"+current)
	checksumResponse, err := c.http.Do(checksumRequest)
	if err != nil {
		return nil, err
	}
	defer checksumResponse.Body.Close()
	if checksumResponse.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("GitHub checksum download returned %s", checksumResponse.Status)
	}
	digest := ""
	scanner := bufio.NewScanner(io.LimitReader(checksumResponse.Body, 64*1024))
	for scanner.Scan() {
		fields := strings.Fields(scanner.Text())
		if len(fields) >= 2 && strings.TrimPrefix(fields[1], "*") == assetName {
			digest = fields[0]
			break
		}
	}
	if err := scanner.Err(); err != nil {
		return nil, err
	}
	if len(digest) != 64 {
		return nil, fmt.Errorf("GitHub release %s has no SHA-256 checksum for %s", version, assetName)
	}
	return &model.UpdateInfo{Version: version, OS: goos, Arch: arch, SHA256: digest, URL: "https://github.com/" + updateRepository + "/releases/download/" + version + "/" + assetName}, nil
}

func (c *githubClient) checkMirror(ctx context.Context, current, goos, arch string) (*model.UpdateInfo, error) {
	assetName := "baize-agent-" + goos + "-" + arch
	base := strings.TrimRight(os.Getenv("BAIZE_MIRROR"), "/")
	if base == "" {
		base = defaultMirror
	}
	assetURL := base + "/github.com/" + updateRepository + "/releases/latest/download/" + assetName
	request, err := http.NewRequestWithContext(ctx, http.MethodHead, assetURL, nil)
	if err != nil {
		return nil, err
	}
	request.Header.Set("User-Agent", "baize-agent/"+current)
	response, err := c.http.Do(request)
	if err != nil {
		return nil, err
	}
	response.Body.Close()
	if response.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("mirror release returned %s", response.Status)
	}
	etag := response.Header.Get("ETag")
	c.mu.Lock()
	unchanged := etag != "" && etag == c.mirrorETag
	c.mu.Unlock()
	if unchanged {
		return nil, nil
	}

	data, err := c.downloadBytes(ctx, assetURL)
	if err != nil {
		return nil, err
	}
	hash := sha256.Sum256(data)
	digest := hex.EncodeToString(hash[:])
	checksumURL := base + "/github.com/" + updateRepository + "/releases/latest/download/SHA256SUMS"
	checksumData, err := c.downloadBytes(ctx, checksumURL)
	if err != nil {
		return nil, err
	}
	if checksum := checksumForAsset(checksumData, assetName); checksum == "" || checksum != digest {
		return nil, fmt.Errorf("mirror release checksum mismatch for %s", assetName)
	}
	if err := validateCandidate(data, goos, arch); err != nil {
		return nil, err
	}
	version, err := candidateVersion(ctx, data)
	if err != nil {
		return nil, err
	}
	c.mu.Lock()
	c.mirrorETag = etag
	if version != "" && isNewerVersion(version, current) {
		c.cachedURL, c.cachedHash, c.cachedData = assetURL, digest, data
	}
	c.mu.Unlock()
	if !isNewerVersion(version, current) {
		return nil, nil
	}
	return &model.UpdateInfo{Version: version, OS: goos, Arch: arch, SHA256: digest, Size: int64(len(data)), URL: assetURL}, nil
}

func (c *githubClient) downloadBytes(ctx context.Context, address string) ([]byte, error) {
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
		return nil, fmt.Errorf("mirror download returned %s", response.Status)
	}
	data, err := io.ReadAll(io.LimitReader(response.Body, 128<<20+1))
	if err != nil {
		return nil, err
	}
	if len(data) > 128<<20 {
		return nil, fmt.Errorf("mirror download exceeds 128 MiB")
	}
	return data, nil
}

func validateCandidate(data []byte, goos, arch string) error {
	temporary, err := os.CreateTemp("", ".baize-update-check-*")
	if err != nil {
		return err
	}
	path := temporary.Name()
	defer os.Remove(path)
	if err := temporary.Chmod(0o700); err != nil {
		temporary.Close()
		return err
	}
	if _, err := temporary.Write(data); err != nil {
		temporary.Close()
		return err
	}
	if err := temporary.Close(); err != nil {
		return err
	}
	return agentbinary.Validate(path, goos, arch)
}

func candidateVersion(ctx context.Context, data []byte) (string, error) {
	temporary, err := os.CreateTemp("", ".baize-update-version-*")
	if err != nil {
		return "", err
	}
	path := temporary.Name()
	defer os.Remove(path)
	if err := temporary.Chmod(0o700); err != nil {
		temporary.Close()
		return "", err
	}
	if _, err := temporary.Write(data); err != nil {
		temporary.Close()
		return "", err
	}
	if err := temporary.Close(); err != nil {
		return "", err
	}
	checkCtx, cancel := context.WithTimeout(ctx, 15*time.Second)
	defer cancel()
	output, err := exec.CommandContext(checkCtx, path, "--version").Output()
	if err != nil {
		return "", fmt.Errorf("read candidate Agent version: %w", err)
	}
	version := strings.TrimSpace(string(output))
	if version == "" {
		return "", fmt.Errorf("candidate Agent returned an empty version")
	}
	return version, nil
}

func checksumForAsset(data []byte, asset string) string {
	scanner := bufio.NewScanner(strings.NewReader(string(data)))
	for scanner.Scan() {
		fields := strings.Fields(scanner.Text())
		if len(fields) >= 2 && strings.TrimPrefix(fields[1], "*") == asset && len(fields[0]) == 64 {
			return fields[0]
		}
	}
	return ""
}

func (c *githubClient) Download(ctx context.Context, update model.UpdateInfo, writer io.Writer) error {
	c.mu.Lock()
	if c.cachedURL == update.URL && c.cachedHash == update.SHA256 && len(c.cachedData) > 0 {
		data := append([]byte(nil), c.cachedData...)
		c.cachedURL, c.cachedHash, c.cachedData = "", "", nil
		c.mu.Unlock()
		_, err := io.Copy(writer, bytes.NewReader(data))
		return err
	}
	c.mu.Unlock()
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
	limited := io.LimitReader(response.Body, 128<<20+1)
	written, err := io.Copy(writer, limited)
	if err != nil {
		return err
	}
	if written > 128<<20 {
		return fmt.Errorf("update exceeds 128 MiB")
	}
	return nil
}

func isNewerVersion(candidate, current string) bool {
	return versionNumber(candidate) > versionNumber(current)
}

func versionNumber(value string) int64 {
	value = strings.TrimPrefix(strings.TrimPrefix(strings.TrimSpace(value), "v"), "V")
	number, _ := strconv.ParseInt(value, 10, 64)
	return number
}
