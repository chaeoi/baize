package agent

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"baize/shared/model"
)

const maxOutboxBytes = 256 << 20

type Outbox struct {
	directory string
	mu        sync.Mutex
}

func NewOutbox(directory string) (*Outbox, error) {
	if err := os.MkdirAll(directory, 0o700); err != nil {
		return nil, err
	}
	return &Outbox{directory: directory}, nil
}

// Enqueue snapshots mutable collector buffers before the next collection reuses them.
func (o *Outbox) Enqueue(telemetry model.Telemetry) error {
	o.mu.Lock()
	defer o.mu.Unlock()
	data, err := json.Marshal(telemetry)
	if err != nil {
		return err
	}
	entries, err := os.ReadDir(o.directory)
	if err != nil {
		return err
	}
	var size int64
	for _, entry := range entries {
		if !strings.HasSuffix(entry.Name(), ".json") {
			continue
		}
		info, err := entry.Info()
		if err != nil {
			return err
		}
		size += info.Size()
	}
	if size+int64(len(data)) > maxOutboxBytes {
		return fmt.Errorf("telemetry outbox is full (%d bytes); retaining queued data", size)
	}
	file, err := os.CreateTemp(o.directory, ".pending-*")
	if err != nil {
		return err
	}
	defer os.Remove(file.Name())
	if _, err = file.Write(data); err != nil {
		file.Close()
		return err
	}
	if err = file.Sync(); err != nil {
		file.Close()
		return err
	}
	if err = file.Close(); err != nil {
		return err
	}
	return os.Rename(file.Name(), filepath.Join(o.directory, fmt.Sprintf("%020d-%s.json", time.Now().UnixNano(), filepath.Base(file.Name()))))
}

func (o *Outbox) Flush(ctx context.Context, client *Client) error {
	o.mu.Lock()
	defer o.mu.Unlock()
	entries, err := os.ReadDir(o.directory)
	if err != nil {
		return err
	}
	for _, entry := range entries {
		if !strings.HasSuffix(entry.Name(), ".json") {
			continue
		}
		path := filepath.Join(o.directory, entry.Name())
		data, err := os.ReadFile(path)
		if err != nil {
			return err
		}
		var telemetry model.Telemetry
		if err := json.Unmarshal(data, &telemetry); err != nil {
			return fmt.Errorf("read queued telemetry: %w", err)
		}
		if err := client.Report(ctx, telemetry); err != nil {
			return err
		}
		if err := os.Remove(path); err != nil {
			return err
		}
	}
	return nil
}
