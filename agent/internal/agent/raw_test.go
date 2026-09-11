package agent

import (
	"bytes"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

func TestRawOutboxEvictsOldestAndSurvivesRestart(t *testing.T) {
	dir := t.TempDir()
	queue, err := NewRawOutbox(dir, 1<<20)
	if err != nil {
		t.Fatal(err)
	}
	for i := uint64(1); i <= 3; i++ {
		if err := queue.Enqueue(i, bytes.Repeat([]byte{byte(i)}, 400<<10)); err != nil {
			t.Fatal(err)
		}
	}
	if queue.entries.Len() != 2 || queue.total != 800<<10 {
		t.Fatalf("cache size %d / %d", queue.entries.Len(), queue.total)
	}
	queue, err = NewRawOutbox(dir, 1<<20)
	if err != nil {
		t.Fatal(err)
	}
	var fail atomic.Bool
	fail.Store(true)
	var received []byte
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if fail.Load() {
			w.WriteHeader(503)
			return
		}
		data, _ := io.ReadAll(r.Body)
		received = append(received, data[0])
	}))
	defer server.Close()
	client := NewClient(server.URL, "test-token", server.Client())
	if err := queue.Flush(t.Context(), client); err == nil {
		t.Fatal("acknowledged failed upload")
	}
	if queue.entries.Len() != 2 {
		t.Fatal("lost failed upload")
	}
	fail.Store(false)
	if err := queue.Flush(t.Context(), client); err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(received, []byte{2, 3}) {
		t.Fatalf("wrong retained order %v", received)
	}
	files, _ := os.ReadDir(dir)
	for _, file := range files {
		if strings.HasSuffix(file.Name(), ".raw") {
			t.Fatal("acknowledged data remains")
		}
	}
	if queue.entries.Len() != 0 {
		t.Fatal("acknowledged data remains")
	}
}

func TestRawOutboxSequenceSurvivesRestart(t *testing.T) {
	dir := t.TempDir()
	queue, err := NewRawOutbox(dir, 1<<20)
	if err != nil {
		t.Fatal(err)
	}
	first, err := queue.ReserveSequence()
	if err != nil || first != 1 {
		t.Fatalf("first sequence=%d err=%v", first, err)
	}
	queue, err = NewRawOutbox(dir, 1<<20)
	if err != nil {
		t.Fatal(err)
	}
	second, err := queue.ReserveSequence()
	if err != nil || second <= first {
		t.Fatalf("second sequence=%d err=%v", second, err)
	}
}

func TestRawOutboxNetworkDoesNotBlockCollection(t *testing.T) {
	queue, err := NewRawOutbox(t.TempDir(), 1<<20)
	if err != nil {
		t.Fatal(err)
	}
	queue.Enqueue(1, []byte("first"))
	started, release := make(chan struct{}), make(chan struct{})
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		data, _ := io.ReadAll(r.Body)
		if string(data) == "first" {
			close(started)
			<-release
		}
	}))
	defer server.Close()
	done := make(chan error, 1)
	go func() { done <- queue.Flush(t.Context(), NewClient(server.URL, "test", server.Client())) }()
	<-started
	enqueued := make(chan error, 1)
	go func() { enqueued <- queue.Enqueue(2, []byte("second")) }()
	select {
	case err := <-enqueued:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(time.Second):
		close(release)
		t.Fatal("network blocked enqueue")
	}
	close(release)
	if err := <-done; err != nil {
		t.Fatal(err)
	}
}

func TestRawSequencesReserveBlocksAndRejectCorruptState(t *testing.T) {
	dir := t.TempDir()
	q, err := NewRawOutbox(dir, 1<<20)
	if err != nil {
		t.Fatal(err)
	}
	first, err := q.ReserveSequence()
	if err != nil {
		t.Fatal(err)
	}
	state, err := os.ReadFile(filepath.Join(dir, ".raw-sequence"))
	if err != nil {
		t.Fatal(err)
	}
	for range 20 {
		if _, err := q.ReserveSequence(); err != nil {
			t.Fatal(err)
		}
	}
	current, _ := os.ReadFile(filepath.Join(dir, ".raw-sequence"))
	if !bytes.Equal(state, current) {
		t.Fatal("persisted each sequence instead of reserving blocks")
	}
	if err := q.Enqueue(first, []byte("oldest")); err != nil {
		t.Fatal(err)
	}
	reopened, err := NewRawOutbox(dir, 1<<20)
	if err != nil {
		t.Fatal(err)
	}
	next, err := reopened.ReserveSequence()
	if err != nil || next <= q.nextSequence {
		t.Fatalf("sequence reused after restart: %d %v", next, err)
	}
	if err := os.WriteFile(filepath.Join(dir, ".raw-sequence"), []byte("broken"), 0600); err != nil {
		t.Fatal(err)
	}
	if _, err := NewRawOutbox(dir, 1<<20); err == nil {
		t.Fatal("silently reset corrupt sequence")
	}
}
