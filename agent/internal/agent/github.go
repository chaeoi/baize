package agent

import (
	"bufio"
	"context"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"runtime"
	"strconv"
	"strings"

	"baize/shared/model"
)

const updateRepository = "chaeoi/baize"

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

func (c *githubClient) Download(ctx context.Context, update model.UpdateInfo, writer io.Writer) error {
	parsed, err := url.Parse(update.URL)
	if err != nil || parsed.Scheme != "https" || parsed.Host != "github.com" {
		return fmt.Errorf("GitHub returned an invalid update URL")
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
