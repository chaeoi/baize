package agent

import (
	"bytes"
	"container/list"
	"context"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"time"
)

func (c *Client) ReportRaw(ctx context.Context, encoded []byte) error {
	request, err := http.NewRequestWithContext(ctx, http.MethodPost, c.baseURL+"/api/v1/raw", bytes.NewReader(encoded))
	if err != nil {
		return err
	}
	request.Header.Set("Authorization", "Bearer "+c.token)
	request.Header.Set("Content-Type", "application/octet-stream")
	response, err := c.http.Do(request)
	if err != nil {
		return err
	}
	defer response.Body.Close()
	if response.StatusCode != http.StatusOK {
		message, _ := io.ReadAll(io.LimitReader(response.Body, 4096))
		return fmt.Errorf("dashboard raw endpoint returned %s: %s", response.Status, strings.TrimSpace(string(message)))
	}
	_, err = io.Copy(io.Discard, io.LimitReader(response.Body, 4096))
	if err != nil {
		return err
	}
	return nil
}

// RawOutbox keeps a FIFO index in memory and compressed batches on disk.
// The network sender never holds the queue lock while waiting for HTTP.
type RawOutbox struct {
	directory     string
	maxBytes      int64
	total         int64
	nextSequence  uint64
	sequenceLimit uint64
	entries       *list.List
	mu            sync.Mutex
	sendMu        sync.Mutex
	wake          chan struct{}
}

type rawEntry struct {
	name string
	size int64
}

func NewRawOutbox(directory string, maxBytes int64) (*RawOutbox, error) {
	if maxBytes < 1<<20 {
		return nil, fmt.Errorf("raw cache limit must be at least 1MiB")
	}
	if err := os.MkdirAll(directory, 0700); err != nil {
		return nil, err
	}
	outbox := &RawOutbox{directory: directory, maxBytes: maxBytes, nextSequence: 1, entries: list.New(), wake: make(chan struct{}, 1)}
	items, err := os.ReadDir(directory) // sorted by batch sequence, once at startup
	if err != nil {
		return nil, err
	}
	for _, item := range items {
		if item.IsDir() {
			continue
		}
		if strings.HasPrefix(item.Name(), ".raw-pending-") || strings.HasPrefix(item.Name(), ".raw-sequence-") {
			if err := os.Remove(filepath.Join(directory, item.Name())); err != nil {
				return nil, err
			}
			continue
		}
		if !strings.HasSuffix(item.Name(), ".raw") {
			continue
		}
		sequence, parseErr := strconv.ParseUint(strings.TrimSuffix(item.Name(), ".raw"), 10, 64)
		if parseErr != nil || sequence == 0 || sequence == ^uint64(0) || len(item.Name()) != 24 {
			return nil, fmt.Errorf("invalid raw cache filename %q", item.Name())
		}
		info, err := item.Info()
		if err != nil {
			return nil, err
		}
		outbox.entries.PushBack(rawEntry{name: item.Name(), size: info.Size()})
		outbox.total += info.Size()
		if sequence+1 > outbox.nextSequence {
			outbox.nextSequence = sequence + 1
		}
	}
	if data, readErr := os.ReadFile(filepath.Join(directory, ".raw-sequence")); readErr == nil {
		sequence, parseErr := strconv.ParseUint(strings.TrimSpace(string(data)), 10, 64)
		if parseErr != nil || sequence == 0 {
			return nil, fmt.Errorf("invalid raw sequence state; refusing to reuse sequence numbers")
		}
		if sequence > outbox.nextSequence {
			outbox.nextSequence = sequence
		}
	} else if !os.IsNotExist(readErr) {
		return nil, readErr
	}
	if err := outbox.trimLocked(); err != nil {
		return nil, err
	}
	return outbox, nil
}

// ReserveSequence reserves numbers in blocks to avoid an extra fsync per
// batch. On restart unused numbers are skipped. Gaps are
// harmless after a crash, while a reused value would make the dashboard drop
// fresh data as a duplicate.
func (o *RawOutbox) ReserveSequence() (uint64, error) {
	o.mu.Lock()
	defer o.mu.Unlock()
	sequence := o.nextSequence
	if sequence == 0 || sequence == ^uint64(0) {
		return 0, fmt.Errorf("raw sequence exhausted")
	}
	if sequence >= o.sequenceLimit {
		if err := o.persistSequenceLocked(); err != nil {
			return 0, err
		}
	}
	o.nextSequence = sequence + 1
	return sequence, nil
}

func (o *RawOutbox) persistSequenceLocked() error {
	limit := o.nextSequence + min(uint64(1024), ^uint64(0)-o.nextSequence)
	temporary, err := os.CreateTemp(o.directory, ".raw-sequence-")
	if err != nil {
		return err
	}
	temporaryName := temporary.Name()
	defer os.Remove(temporaryName)
	if _, err = temporary.WriteString(strconv.FormatUint(limit, 10)); err == nil {
		err = temporary.Sync()
	}
	if closeErr := temporary.Close(); err == nil {
		err = closeErr
	}
	if err != nil {
		return err
	}
	if err := os.Rename(temporaryName, filepath.Join(o.directory, ".raw-sequence")); err != nil {
		return err
	}
	if err := o.syncDirectory(); err != nil {
		return err
	}
	o.sequenceLimit = limit
	return nil
}

func (o *RawOutbox) syncDirectory() error {
	dir, err := os.Open(o.directory)
	if err != nil {
		return err
	}
	defer dir.Close()
	return dir.Sync()
}

func (o *RawOutbox) Enqueue(sequence uint64, data []byte) error {
	o.mu.Lock()
	defer o.mu.Unlock()
	if int64(len(data)) > o.maxBytes {
		return fmt.Errorf("batch exceeds raw cache limit")
	}
	if sequence == 0 || sequence == ^uint64(0) {
		return fmt.Errorf("invalid raw sequence")
	}
	temporary, err := os.CreateTemp(o.directory, ".raw-pending-")
	if err != nil {
		return err
	}
	defer os.Remove(temporary.Name())
	if _, err := temporary.Write(data); err != nil {
		temporary.Close()
		return err
	}
	if err := temporary.Sync(); err != nil {
		temporary.Close()
		return err
	}
	if err := temporary.Close(); err != nil {
		return err
	}
	name := fmt.Sprintf("%020d.raw", sequence)
	path := filepath.Join(o.directory, name)
	if _, err := os.Stat(path); err == nil {
		return fmt.Errorf("raw sequence already queued: %d", sequence)
	} else if !os.IsNotExist(err) {
		return err
	}
	if err := os.Rename(temporary.Name(), path); err != nil {
		return err
	}
	o.entries.PushBack(rawEntry{name: name, size: int64(len(data))})
	o.total += int64(len(data))
	if sequence >= o.nextSequence && sequence < ^uint64(0) {
		o.nextSequence = sequence + 1
	}
	if sequence >= o.sequenceLimit {
		if err := o.persistSequenceLocked(); err != nil {
			return err
		}
	}
	if err := o.trimLocked(); err != nil {
		return err
	}
	if err := o.syncDirectory(); err != nil {
		return err
	}
	select {
	case o.wake <- struct{}{}:
	default:
	}
	return nil
}

func (o *RawOutbox) trimLocked() error {
	for o.total > o.maxBytes {
		entry := o.entries.Front()
		if entry == nil {
			break
		}
		if err := o.removeLocked(entry); err != nil {
			return err
		}
	}
	return nil
}

func (o *RawOutbox) removeLocked(element *list.Element) error {
	entry := element.Value.(rawEntry)
	if err := os.Remove(filepath.Join(o.directory, entry.name)); err != nil && !os.IsNotExist(err) {
		return err
	}
	o.entries.Remove(element)
	o.total -= entry.size
	return nil
}

func (o *RawOutbox) Flush(ctx context.Context, client *Client) error {
	o.sendMu.Lock()
	defer o.sendMu.Unlock()
	for ctx.Err() == nil {
		o.mu.Lock()
		element := o.entries.Front()
		if element == nil {
			o.mu.Unlock()
			return nil
		}
		entry := element.Value.(rawEntry)
		data, err := os.ReadFile(filepath.Join(o.directory, entry.name))
		o.mu.Unlock()
		if err != nil {
			return err
		}
		if err := client.ReportRaw(ctx, data); err != nil {
			return err
		}
		o.mu.Lock()
		// Overflow may have evicted this batch while the request was in flight.
		if o.entries.Front() == element {
			err = o.removeLocked(element)
		}
		o.mu.Unlock()
		if err != nil {
			return err
		}
	}
	return ctx.Err()
}

func (o *RawOutbox) Run(ctx context.Context, client *Client) {
	for ctx.Err() == nil {
		if err := o.Flush(ctx, client); err != nil && ctx.Err() == nil {
			slog.Warn("forward raw batches", "error", err)
			timer := time.NewTimer(2 * time.Second)
			select {
			case <-ctx.Done():
				timer.Stop()
				return
			case <-timer.C:
			}
			continue
		}
		select {
		case <-ctx.Done():
			return
		case <-o.wake:
		}
	}
}

func RawCachePath(directory string) string { return filepath.Join(directory, "raw-outbox") }
