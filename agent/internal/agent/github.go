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
	"strings"
	"sync"
	"time"

	"baize/shared/agentbinary"
	"baize/shared/model"
)

const updateRepository = "chaeoi/baize"
const defaultMirror = "https://gitwarp.canghai.org"

type githubClient struct {
	http        *http.Client
	binaryPath  string
	mu          sync.Mutex
	currentHash string
	cachedURL   string
	cachedHash  string
	cachedData  []byte
}

func NewGitHubClient(httpClient *http.Client, binaryPath string) *githubClient {
	return &githubClient{http: httpClient, binaryPath: binaryPath}
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
	checksumData, err := io.ReadAll(io.LimitReader(checksumResponse.Body, 64*1024))
	if err != nil {
		return nil, err
	}
	digest := checksumForAsset(checksumData, assetName)
	if digest == "" {
		return nil, fmt.Errorf("GitHub release %s has no SHA-256 checksum for %s", version, assetName)
	}
	currentHash, err := c.binaryHash()
	if err != nil {
		return nil, err
	}
	if digest == currentHash {
		return nil, nil
	}
	return &model.UpdateInfo{Version: version, OS: goos, Arch: arch, SHA256: digest, URL: "https://github.com/" + updateRepository + "/releases/download/" + version + "/" + assetName}, nil
}

func (c *githubClient) checkMirror(ctx context.Context, current, goos, arch string) (*model.UpdateInfo, error) {
	assetName := "baize-agent-" + goos + "-" + arch
	base := strings.TrimRight(os.Getenv("BAIZE_MIRROR"), "/")
	if base == "" {
		base = defaultMirror
	}
	// Check the small manifest on every poll. ETags and date versions do not
	// identify builds reliably when a release is rebuilt on the same day.
	query := "?download=1&cachebust=" + fmt.Sprint(time.Now().UnixNano())
	assetURL := base + "/github.com/" + updateRepository + "/releases/latest/download/" + assetName + query
	checksumURL := base + "/github.com/" + updateRepository + "/releases/latest/download/SHA256SUMS" + query
	checksumData, err := c.downloadBytes(ctx, checksumURL)
	if err != nil {
		return nil, err
	}
	digest := checksumForAsset(checksumData, assetName)
	if digest == "" {
		return nil, fmt.Errorf("mirror release has no SHA-256 checksum for %s", assetName)
	}
	currentHash, err := c.binaryHash()
	if err != nil {
		return nil, err
	}
	if digest == currentHash {
		return nil, nil
	}
	data, err := c.downloadBytes(ctx, assetURL)
	if err != nil {
		return nil, err
	}
	hash := sha256.Sum256(data)
	if hex.EncodeToString(hash[:]) != digest {
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
	c.cachedURL, c.cachedHash, c.cachedData = assetURL, digest, data
	c.mu.Unlock()
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
			if _, err := hex.DecodeString(fields[0]); err == nil {
				return strings.ToLower(fields[0])
			}
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

func (c *githubClient) binaryHash() (string, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.currentHash != "" {
		return c.currentHash, nil
	}
	file, err := os.Open(c.binaryPath)
	if err != nil {
		return "", err
	}
	defer file.Close()
	hash := sha256.New()
	if _, err := io.Copy(hash, file); err != nil {
		return "", err
	}
	c.currentHash = hex.EncodeToString(hash.Sum(nil))
	return c.currentHash, nil
}
