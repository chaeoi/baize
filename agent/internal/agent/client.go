package agent

import (
	"bytes"
	"compress/gzip"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"

	"baize/shared/model"
)

type Client struct {
	baseURL string
	token   string
	http    *http.Client
}

func NewClient(baseURL, token string, httpClient *http.Client) *Client {
	return &Client{baseURL: strings.TrimRight(baseURL, "/"), token: token, http: httpClient}
}

func (c *Client) Report(ctx context.Context, telemetry model.Telemetry) error {
	var body bytes.Buffer
	compressed, err := gzip.NewWriterLevel(&body, gzip.BestSpeed)
	if err != nil {
		return err
	}
	if err := json.NewEncoder(compressed).Encode(telemetry); err != nil {
		_ = compressed.Close()
		return err
	}
	if err := compressed.Close(); err != nil {
		return err
	}
	request, err := http.NewRequestWithContext(ctx, http.MethodPost, c.baseURL+"/api/v1/telemetry", bytes.NewReader(body.Bytes()))
	if err != nil {
		return err
	}
	request.Header.Set("Authorization", "Bearer "+c.token)
	request.Header.Set("Content-Type", "application/json")
	request.Header.Set("Content-Encoding", "gzip")
	response, err := c.http.Do(request)
	if err != nil {
		return err
	}
	defer response.Body.Close()
	if response.StatusCode != http.StatusOK {
		message, _ := io.ReadAll(io.LimitReader(response.Body, 4096))
		return fmt.Errorf("dashboard returned %s: %s", response.Status, strings.TrimSpace(string(message)))
	}
	return nil
}

func (c *Client) CheckUpdate(ctx context.Context, uuid, version, goos, arch string, automatic bool) (*model.UpdateInfo, error) {
	query := url.Values{}
	query.Set("uuid", uuid)
	query.Set("version", version)
	query.Set("os", goos)
	query.Set("arch", arch)
	if automatic {
		query.Set("automatic", "1")
	}
	request, err := http.NewRequestWithContext(ctx, http.MethodGet, c.baseURL+"/api/v1/update/check?"+query.Encode(), nil)
	if err != nil {
		return nil, err
	}
	request.Header.Set("Authorization", "Bearer "+c.token)
	response, err := c.http.Do(request)
	if err != nil {
		return nil, err
	}
	defer response.Body.Close()
	if response.StatusCode != http.StatusOK {
		message, _ := io.ReadAll(io.LimitReader(response.Body, 4096))
		return nil, fmt.Errorf("update check returned %s: %s", response.Status, strings.TrimSpace(string(message)))
	}
	var result struct {
		Available bool             `json:"available"`
		Update    model.UpdateInfo `json:"update"`
	}
	if err := json.NewDecoder(io.LimitReader(response.Body, 64*1024)).Decode(&result); err != nil {
		return nil, err
	}
	if !result.Available {
		return nil, nil
	}
	return &result.Update, nil
}

func (c *Client) Download(ctx context.Context, update model.UpdateInfo, writer io.Writer) error {
	parsed, err := url.Parse(update.URL)
	if err != nil || parsed.IsAbs() || !strings.HasPrefix(parsed.Path, "/api/v1/update/files/") {
		return fmt.Errorf("dashboard returned an invalid update URL")
	}
	request, err := http.NewRequestWithContext(ctx, http.MethodGet, c.baseURL+update.URL, nil)
	if err != nil {
		return err
	}
	request.Header.Set("Authorization", "Bearer "+c.token)
	response, err := c.http.Do(request)
	if err != nil {
		return err
	}
	defer response.Body.Close()
	if response.StatusCode != http.StatusOK {
		return fmt.Errorf("update download returned %s", response.Status)
	}
	limited := io.LimitReader(response.Body, update.Size+1)
	written, err := io.Copy(writer, limited)
	if err != nil {
		return err
	}
	if written != update.Size {
		return fmt.Errorf("update size mismatch: expected %d, downloaded %d", update.Size, written)
	}
	return nil
}
