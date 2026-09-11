package dashboard

import (
	"bytes"
	"encoding/binary"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"baize/shared/model"
	"baize/shared/rawstream"
	"github.com/foxglove/mcap/go/mcap"
)

func recordingBatch(t *testing.T, at time.Time, seq uint64) rawstream.Batch {
	t.Helper()
	host, err := rawstream.NewHostRecord(at, &model.SystemMetrics{CPUUsagePercent: float64(seq)}, nil)
	if err != nil {
		t.Fatal(err)
	}
	return rawstream.Batch{RobotUUID: rawTestUUID, RobotCode: "M99", Sequence: seq, Records: []rawstream.Record{host}}
}

func readRecording(t *testing.T, path string) []*mcap.Message {
	t.Helper()
	file, err := os.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer file.Close()
	reader, err := mcap.NewReader(file)
	if err != nil {
		t.Fatal(err)
	}
	defer reader.Close()
	it, err := reader.Messages()
	if err != nil {
		t.Fatal(err)
	}
	var result []*mcap.Message
	for {
		_, _, message, err := it.Next(nil)
		if err == io.EOF {
			break
		}
		if err != nil {
			t.Fatal(err)
		}
		result = append(result, message)
	}
	return result
}

func TestRecoveryDoesNotDeleteNewJournalWhenOldMCAPExists(t *testing.T) {
	dir := t.TempDir()
	m, err := newRecordingManager(dir)
	if err != nil {
		t.Fatal(err)
	}
	if err := m.Start(rawTestUUID); err != nil {
		t.Fatal(err)
	}
	if err := m.Ingest(recordingBatch(t, time.Now().UTC(), 1)); err != nil {
		t.Fatal(err)
	}
	if err := m.Close(); err != nil {
		t.Fatal(err)
	}
	old, _ := m.Latest(rawTestUUID)
	if err := m.Start(rawTestUUID); err != nil {
		t.Fatal(err)
	}
	session := m.active[rawTestUUID]
	inside := session.startedAt.Add(time.Second)
	batch := recordingBatch(t, inside, 2)
	before := batch.Records[0]
	before.ReceiveTimestamp = session.startedAt.Add(-time.Second)
	batch.Records = append([]rawstream.Record{before}, batch.Records...)
	if err := m.Ingest(batch); err != nil {
		t.Fatal(err)
	}
	// A torn trailing frame was never acknowledged and must not hide the
	// earlier complete frame. No MCAP Close is performed before restart.
	if _, err := session.journal.Write([]byte{9, 0}); err != nil {
		t.Fatal(err)
	}
	session.journal.Close()
	session.file.Close()
	restarted, err := newRecordingManager(dir)
	if err != nil {
		t.Fatal(err)
	}
	defer restarted.Close()
	status := restarted.Status(rawTestUUID)
	if status.Name != filepath.Base(session.path) || status.Error != "" {
		t.Fatalf("recovery is not visible immediately after startup: %+v", status)
	}
	path, ok := restarted.Latest(rawTestUUID)
	if !ok || path == old {
		t.Fatal("new journal was lost behind old MCAP")
	}
	messages := readRecording(t, path)
	if len(messages) != 1 || messages[0].LogTime != uint64(inside.UnixNano()) {
		t.Fatalf("recovery lost recording boundary: %+v", messages)
	}
	for _, path := range []string{session.journalPath, session.path + ".partial"} {
		if _, err := os.Stat(path); !os.IsNotExist(err) {
			t.Fatalf("recovery residue: %s %v", path, err)
		}
	}
	if len(readRecording(t, old)) != 1 {
		t.Fatal("older recording changed")
	}
}

func TestCorruptCompleteJournalIsRetainedAndReported(t *testing.T) {
	dir := t.TempDir()
	m, _ := newRecordingManager(dir)
	if err := m.Start(rawTestUUID); err != nil {
		t.Fatal(err)
	}
	session := m.active[rawTestUUID]
	if err := m.Ingest(recordingBatch(t, time.Now().UTC(), 1)); err != nil {
		t.Fatal(err)
	}
	var frame bytes.Buffer
	binary.Write(&frame, binary.LittleEndian, uint32(4))
	frame.WriteString("oops")
	if _, err := session.journal.Write(frame.Bytes()); err != nil {
		t.Fatal(err)
	}
	session.journal.Close()
	session.file.Close()
	restarted, err := newRecordingManager(dir)
	if err != nil {
		t.Fatal(err)
	}
	if restarted.Status(rawTestUUID).Error == "" {
		t.Fatal("corruption hidden")
	}
	if _, err := os.Stat(session.journalPath); err != nil {
		t.Fatal("original journal removed", err)
	}
	if _, err := os.Stat(session.path); !os.IsNotExist(err) {
		t.Fatal("corrupt journal falsely finalized")
	}
}

func TestRecordingDiskFailureDoesNotBlockRawAndCanRestart(t *testing.T) {
	s := NewServer(ServerConfig{AgentToken: "test-agent-token", RecordingDir: t.TempDir()}, newTestStore(t))
	defer s.Close()
	if err := s.recordings.Start(rawTestUUID); err != nil {
		t.Fatal(err)
	}
	s.recordings.active[rawTestUUID].journal.Close() // inject write failure
	for seq := uint64(1); seq <= 2; seq++ {
		batch := recordingBatch(t, time.Now().UTC(), seq)
		data, err := rawstream.Encode(batch)
		if err != nil {
			t.Fatal(err)
		}
		r := httptest.NewRequest(http.MethodPost, "/api/v1/raw", bytes.NewReader(data))
		r.Header.Set("Authorization", "Bearer test-agent-token")
		w := httptest.NewRecorder()
		s.ServeHTTP(w, r)
		if w.Code != 200 {
			t.Fatal(w.Code, w.Body.String())
		}
	}
	status := s.recordings.Status(rawTestUUID)
	if status.Active || status.Stopping || status.Error == "" {
		t.Fatalf("failure not reported: %+v", status)
	}
	robot, _ := s.store.Robot(rawTestUUID)
	if robot.Telemetry.System.CPUUsagePercent != 2 {
		t.Fatal("live feed blocked")
	}
	if err := s.recordings.Stop(rawTestUUID); err != nil {
		t.Fatal(err)
	}
	if err := s.recordings.Start(rawTestUUID); err != nil {
		t.Fatal("restart blocked", err)
	}
	if s.recordings.Status(rawTestUUID).Error != "" {
		t.Fatal("failure not cleared")
	}
}

func TestRecordingRoutesRequireChangedPasswordAndSession(t *testing.T) {
	store := newTestStore(t)
	s := NewServer(ServerConfig{AdminUser: "admin", JWTSecret: "test-secret", RecordingDir: t.TempDir()}, store)
	defer s.Close()
	batch := recordingBatch(t, time.Now().UTC(), 1)
	telemetry, err := decodeRawBatch(batch, &rawRobotState{})
	if err != nil {
		t.Fatal(err)
	}
	if err := store.PutTelemetry(telemetry); err != nil {
		t.Fatal(err)
	}
	login := httptest.NewRecorder()
	if err := s.startSession(login, httptest.NewRequest("GET", "/", nil)); err != nil {
		t.Fatal(err)
	}
	cookie := login.Result().Cookies()[0]
	base := "/api/v1/recordings/" + publicRobotID(s.config.JWTSecret, rawTestUUID) + "/"
	for _, action := range []string{"start", "stop", "status", "download"} {
		method := "GET"
		if action == "start" || action == "stop" {
			method = "POST"
		}
		for _, authenticated := range []bool{false, true} {
			r := httptest.NewRequest(method, base+action, nil)
			want := 401
			if authenticated {
				r.AddCookie(cookie)
				want = 403
			}
			w := httptest.NewRecorder()
			s.ServeHTTP(w, r)
			if w.Code != want {
				t.Fatalf("%s: got %d want %d", action, w.Code, want)
			}
		}
	}
	if err := store.ChangeAdminPassword("admin", "Baize@Admin1", "Changed@Password123"); err != nil {
		t.Fatal(err)
	}
	r := httptest.NewRequest("POST", base+"start", nil)
	r.AddCookie(cookie)
	r.Header.Set("Origin", "https://untrusted.example")
	w := httptest.NewRecorder()
	s.ServeHTTP(w, r)
	if w.Code != 403 {
		t.Fatal("cross-origin accepted", w.Code)
	}
	r.Header.Del("Origin")
	w = httptest.NewRecorder()
	s.ServeHTTP(w, r)
	if w.Code != 200 {
		t.Fatal(w.Code, w.Body.String())
	}
	if err := s.recordings.Ingest(recordingBatch(t, time.Now().UTC(), 2)); err != nil {
		t.Fatal(err)
	}
	if err := s.recordings.Close(); err != nil {
		t.Fatal(err)
	}
	// Expired global write deadline must be cleared for a download, including
	// byte ranges, without removing the server-wide timeout for other routes.
	server := httptest.NewUnstartedServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.NewResponseController(w).SetWriteDeadline(time.Now().Add(-time.Second))
		s.ServeHTTP(w, r)
	}))
	server.Start()
	defer server.Close()
	request, _ := http.NewRequest("GET", server.URL+base+"download", nil)
	request.AddCookie(cookie)
	request.Header.Set("Range", "bytes=0-7")
	response, err := server.Client().Do(request)
	if err != nil {
		t.Fatal(err)
	}
	defer response.Body.Close()
	data, err := io.ReadAll(response.Body)
	if err != nil || response.StatusCode != 206 || !strings.Contains(string(data), "MCAP") {
		t.Fatalf("download: %d %q %v", response.StatusCode, data, err)
	}
}
