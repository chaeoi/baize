package dashboard

import (
	"encoding/binary"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"baize/shared/rawstream"
	"github.com/foxglove/mcap/go/mcap"
)

type recordingManager struct {
	mu       sync.Mutex
	dir      string
	active   map[string]*recordingSession
	latest   map[string]string
	journals map[string]string
	failures map[string]string
}

type recordingSession struct {
	file                 *os.File
	journal              *os.File
	journalPath          string
	path                 string
	writer               *mcap.Writer
	channels             map[string]*recordingChannel
	startedAt, stoppedAt time.Time
	watermark            time.Time
	messages             uint64
}

type recordingChannel struct {
	id       uint16
	sequence uint32
}

type recordingStatus struct {
	Active   bool   `json:"active"`
	Stopping bool   `json:"stopping"`
	Name     string `json:"name,omitempty"`
	Messages uint64 `json:"messages"`
	Error    string `json:"error,omitempty"`
}

func (s *Server) recordingAction(w http.ResponseWriter, r *http.Request) {
	if s.recordings == nil {
		writeError(w, http.StatusServiceUnavailable, "recording storage is unavailable")
		return
	}
	// Recording controls are same-origin, unlike the public read-only streams.
	if origin := r.Header.Get("Origin"); origin != "" {
		parsed, err := url.Parse(origin)
		if err != nil || parsed.Host != r.Host {
			writeError(w, http.StatusForbidden, "cross-origin recording request")
			return
		}
	}
	parts := strings.Split(strings.TrimPrefix(r.URL.Path, "/api/v1/recordings/"), "/")
	if len(parts) != 2 || len(parts[0]) != 20 {
		writeError(w, http.StatusNotFound, "recording route not found")
		return
	}
	var uuid string
	for _, robot := range s.store.Robots() {
		if publicRobotID(s.config.JWTSecret, robot.UUID) == parts[0] {
			uuid = robot.UUID
			break
		}
	}
	if uuid == "" {
		writeError(w, http.StatusNotFound, "robot not found")
		return
	}
	switch parts[1] {
	case "status":
		if r.Method != http.MethodGet {
			methodNotAllowed(w)
			return
		}
	case "start":
		if r.Method != http.MethodPost {
			methodNotAllowed(w)
			return
		}
		if err := s.recordings.Start(uuid); err != nil {
			writeError(w, http.StatusConflict, err.Error())
			return
		}
	case "stop":
		if r.Method != http.MethodPost {
			methodNotAllowed(w)
			return
		}
		if err := s.recordings.Stop(uuid); err != nil {
			writeError(w, http.StatusConflict, err.Error())
			return
		}
	case "download":
		if r.Method != http.MethodGet {
			methodNotAllowed(w)
			return
		}
		path, ok := s.recordings.Latest(uuid)
		if !ok {
			writeError(w, http.StatusNotFound, "recording is not ready")
			return
		}
		w.Header().Set("Content-Type", "application/octet-stream")
		w.Header().Set("Content-Disposition", fmt.Sprintf("attachment; filename=%q", filepath.Base(path)))
		// Only large downloads opt out of the server's normal write deadline.
		if err := http.NewResponseController(w).SetWriteDeadline(time.Time{}); err != nil && !errors.Is(err, http.ErrNotSupported) {
			writeError(w, http.StatusInternalServerError, "configure download deadline")
			return
		}
		http.ServeFile(w, r, path)
		return
	default:
		writeError(w, http.StatusNotFound, "recording route not found")
		return
	}
	writeJSON(w, http.StatusOK, s.recordings.Status(uuid))
}

func newRecordingManager(dir string) (*recordingManager, error) {
	manager := &recordingManager{dir: dir, active: make(map[string]*recordingSession), latest: make(map[string]string), journals: make(map[string]string), failures: make(map[string]string)}
	if dir == "" {
		return manager, nil
	}
	if err := os.MkdirAll(dir, 0750); err != nil {
		return nil, err
	}
	files, err := os.ReadDir(dir)
	if err != nil {
		return nil, err
	}
	for _, file := range files {
		name := file.Name()
		if file.IsDir() || !strings.HasSuffix(name, ".mcap") || len(name) < 38 || !uuidPattern.MatchString(name[:36]) {
			continue
		}
		manager.latest[name[:36]] = filepath.Join(dir, name) // sorted chronologically
	}
	for _, file := range files {
		name := file.Name()
		if file.IsDir() || !strings.HasSuffix(name, ".mcap.journal") || len(name) < 45 || !uuidPattern.MatchString(name[:36]) {
			continue
		}
		uuid := name[:36]
		manager.journals[uuid] = filepath.Join(dir, name)
		if err := manager.recoverJournalLocked(uuid); err != nil {
			manager.failures[uuid] = "recover interrupted recording: " + err.Error()
			slog.Error("recover recording", "robot_uuid", uuid, "error", err)
		}
	}
	return manager, nil
}

func (m *recordingManager) Start(uuid string) error {
	if m == nil || m.dir == "" {
		return errors.New("recording storage is unavailable")
	}
	if !uuidPattern.MatchString(uuid) {
		return errors.New("invalid robot identity")
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.active[uuid] != nil {
		return errors.New("recording is active or waiting for buffered data")
	}
	if m.journals[uuid] != "" {
		if err := m.recoverJournalLocked(uuid); err != nil {
			return fmt.Errorf("recover interrupted recording: %w", err)
		}
	}
	at := time.Now().UTC()
	path := filepath.Join(m.dir, fmt.Sprintf("%s-%s.mcap", uuid, at.Format("20060102T150405.000000000Z")))
	file, err := os.OpenFile(path+".partial", os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0640)
	if err != nil {
		return err
	}
	writer, err := mcap.NewWriter(file, &mcap.WriterOptions{Chunked: true, Compression: mcap.CompressionZSTD, CompressionLevel: mcap.CompressionLevelFastest, ChunkSize: 4 << 20})
	if err == nil {
		// This is a mixed MCAP: original ROS2 CDR plus native host JSON.
		err = writer.WriteHeader(&mcap.Header{Library: "baize"})
	}
	if err != nil {
		file.Close()
		os.Remove(path + ".partial")
		return err
	}
	journalPath := path + ".journal"
	journal, err := os.OpenFile(journalPath, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0640)
	if err != nil {
		_ = file.Close()
		_ = os.Remove(path + ".partial")
		return err
	}
	m.active[uuid] = &recordingSession{file: file, journal: journal, journalPath: journalPath, path: path, writer: writer, channels: make(map[string]*recordingChannel), startedAt: at}
	if err := syncRecordingDir(m.dir); err != nil {
		return m.abortLocked(uuid, err)
	}
	delete(m.failures, uuid)
	return nil
}

func (m *recordingManager) Stop(uuid string) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	session := m.active[uuid]
	if session == nil {
		return nil
	} // repeated requests are harmless
	if session.stoppedAt.IsZero() {
		session.stoppedAt = time.Now().UTC()
	}
	return nil
}

func (m *recordingManager) Status(uuid string) recordingStatus {
	m.mu.Lock()
	defer m.mu.Unlock()
	if session := m.active[uuid]; session != nil {
		return recordingStatus{Active: session.stoppedAt.IsZero(), Stopping: !session.stoppedAt.IsZero(), Messages: session.messages}
	}
	status := recordingStatus{Error: m.failures[uuid]}
	if path := m.latest[uuid]; path != "" {
		status.Name = filepath.Base(path)
	}
	return status
}

func (m *recordingManager) Latest(uuid string) (string, bool) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.active[uuid] == nil && m.journals[uuid] != "" {
		if err := m.recoverJournalLocked(uuid); err != nil {
			return "", false
		}
	}
	path, ok := m.latest[uuid]
	return path, ok && m.active[uuid] == nil
}

func (m *recordingManager) Ingest(batch rawstream.Batch) error {
	return m.ingest(batch, nil)
}

func (m *recordingManager) IngestEncoded(batch rawstream.Batch, encoded []byte) error {
	return m.ingest(batch, encoded)
}

func (m *recordingManager) ingest(batch rawstream.Batch, encoded []byte) error {
	if m == nil {
		return nil
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	session := m.active[batch.RobotUUID]
	if session == nil {
		return nil
	}
	{
		journalBatch := batch
		journalBatch.Records = make([]rawstream.Record, 0, len(batch.Records))
		for _, record := range batch.Records {
			at := record.ReceiveTimestamp
			if !at.Before(session.startedAt) && (session.stoppedAt.IsZero() || at.Before(session.stoppedAt)) {
				journalBatch.Records = append(journalBatch.Records, record)
			}
		}
		if len(journalBatch.Records) > 0 {
			journalData := encoded
			if len(journalBatch.Records) != len(batch.Records) || journalData == nil {
				var err error
				journalData, err = rawstream.Encode(journalBatch)
				if err != nil {
					return m.abortLocked(batch.RobotUUID, err)
				}
			}
			if err := appendJournal(session, journalData); err != nil {
				return m.abortLocked(batch.RobotUUID, err)
			}
		}
	}
	if err := session.writeBatch(batch); err != nil {
		return m.abortLocked(batch.RobotUUID, err)
	}
	// Ordered batches crossing the stop boundary prove that queued data from
	// the recording interval has arrived (apart from explicitly evicted data).
	if !session.stoppedAt.IsZero() && !session.watermark.Before(session.stoppedAt) {
		return m.finishLocked(batch.RobotUUID)
	}
	return nil
}

func appendJournal(session *recordingSession, encoded []byte) error {
	if len(encoded) == 0 || len(encoded) > 80<<20 {
		return errors.New("invalid recording journal batch")
	}
	var length [4]byte
	binary.LittleEndian.PutUint32(length[:], uint32(len(encoded)))
	if _, err := session.journal.Write(length[:]); err != nil {
		return err
	}
	if _, err := session.journal.Write(encoded); err != nil {
		return err
	}
	return session.journal.Sync()
}

func (session *recordingSession) writeBatch(batch rawstream.Batch) error {
	for _, record := range batch.Records {
		at := record.ReceiveTimestamp
		if at.After(session.watermark) {
			session.watermark = at
		}
		if at.Before(session.startedAt) || (!session.stoppedAt.IsZero() && !at.Before(session.stoppedAt)) {
			continue
		}
		key := record.Topic + "\x00" + record.MessageType + "\x00" + record.Serialization
		channel, ok := session.channels[key]
		if !ok {
			if len(session.channels) >= 65535 {
				return errors.New("too many recording channels")
			}
			channel = &recordingChannel{id: uint16(len(session.channels) + 1)}
			schema, encoding := schemaData(record)
			if len(schema) == 0 {
				return fmt.Errorf("missing MCAP schema for %s", record.MessageType)
			}
			if err := session.writer.WriteSchema(&mcap.Schema{ID: channel.id, Name: record.MessageType, Encoding: encoding, Data: schema}); err != nil {
				return err
			}
			if err := session.writer.WriteChannel(&mcap.Channel{ID: channel.id, SchemaID: channel.id, Topic: record.Topic, MessageEncoding: record.Serialization, Metadata: map[string]string{"robot_uuid": batch.RobotUUID, "robot_code": batch.RobotCode, "robot_model": batch.RobotModel}}); err != nil {
				return err
			}
			session.channels[key] = channel
		}
		publishTime := record.SourceTimestamp
		if publishTime.IsZero() || publishTime.UnixNano() <= 0 {
			publishTime = at
		}
		if err := session.writer.WriteMessage(&mcap.Message{ChannelID: channel.id, Sequence: channel.sequence, LogTime: uint64(at.UnixNano()), PublishTime: uint64(publishTime.UnixNano()), Data: record.Payload}); err != nil {
			return err
		}
		channel.sequence++
		session.messages++
	}
	return nil
}

func (m *recordingManager) abortLocked(uuid string, cause error) error {
	m.failures[uuid] = "recording stopped: " + cause.Error()
	session := m.active[uuid]
	if session == nil {
		return cause
	}
	_ = session.file.Close()
	if session.journal != nil {
		_ = session.journal.Close()
	}
	if session.journalPath != "" {
		// Keep the durable journal. A later dashboard start can replay the
		// acknowledged batches into a fresh MCAP after the underlying failure
		// is repaired.
		m.journals[uuid] = session.journalPath
	}
	delete(m.active, uuid)
	return cause
}

func (m *recordingManager) finishLocked(uuid string) error {
	session := m.active[uuid]
	if err := session.writer.Close(); err != nil {
		return m.abortLocked(uuid, err)
	}
	if err := session.file.Sync(); err != nil {
		return m.abortLocked(uuid, err)
	}
	if err := session.file.Close(); err != nil {
		return m.abortLocked(uuid, err)
	}
	if session.journal != nil {
		if err := session.journal.Close(); err != nil {
			return m.abortLocked(uuid, err)
		}
	}
	if err := os.Rename(session.path+".partial", session.path); err != nil {
		return m.abortLocked(uuid, err)
	}
	if err := syncRecordingDir(m.dir); err != nil {
		return m.abortLocked(uuid, err)
	}
	m.noteLatest(uuid, session.path)
	if session.journalPath != "" {
		_ = os.Remove(session.journalPath)
		delete(m.journals, uuid)
	}
	delete(m.active, uuid)
	return nil
}

func (m *recordingManager) recoverJournalLocked(uuid string) error {
	journalPath := m.journals[uuid]
	if journalPath == "" {
		return nil
	}
	input, err := os.Open(journalPath)
	if err != nil {
		return err
	}
	defer input.Close()
	// Reuse the original name so retries are idempotent and chronological
	// ordering is preserved. An unrelated older MCAP cannot satisfy recovery.
	path := strings.TrimSuffix(journalPath, ".journal")
	if info, err := os.Stat(path); err == nil {
		if !info.Mode().IsRegular() {
			return errors.New("recording destination is not a regular file")
		}
		m.noteLatest(uuid, path)
		if err := syncRecordingDir(m.dir); err != nil {
			return err
		}
		if err := os.Remove(journalPath); err != nil {
			return err
		}
		delete(m.journals, uuid)
		return nil
	} else if !os.IsNotExist(err) {
		return err
	}
	file, err := os.CreateTemp(m.dir, ".mcap-recovery-")
	if err != nil {
		return err
	}
	defer file.Close()
	defer os.Remove(file.Name())
	writer, err := mcap.NewWriter(file, &mcap.WriterOptions{Chunked: true, Compression: mcap.CompressionZSTD, CompressionLevel: mcap.CompressionLevelFastest, ChunkSize: 4 << 20})
	if err == nil {
		err = writer.WriteHeader(&mcap.Header{Library: "baize"})
	}
	if err != nil {
		return err
	}
	session := &recordingSession{file: file, path: path, writer: writer, channels: make(map[string]*recordingChannel), startedAt: time.Unix(0, 0).UTC()}
	for {
		var length uint32
		if err := binary.Read(input, binary.LittleEndian, &length); err != nil {
			if errors.Is(err, io.EOF) || errors.Is(err, io.ErrUnexpectedEOF) {
				break
			}
			return err
		}
		if length == 0 || length > 80<<20 {
			return errors.New("invalid recording journal entry")
		}
		data := make([]byte, length)
		if _, err := io.ReadFull(input, data); err != nil {
			if errors.Is(err, io.EOF) || errors.Is(err, io.ErrUnexpectedEOF) {
				break
			}
			return err
		}
		batch, err := rawstream.Decode(data)
		if err != nil {
			return fmt.Errorf("decode recording journal (original retained): %w", err)
		}
		if batch.RobotUUID != uuid {
			return errors.New("recording journal robot identity mismatch")
		}
		if err := session.writeBatch(batch); err != nil {
			return err
		}
	}
	if err := writer.Close(); err != nil {
		return err
	}
	if err := file.Sync(); err != nil {
		return err
	}
	if err := file.Close(); err != nil {
		return err
	}
	if err := os.Rename(file.Name(), path); err != nil {
		return err
	}
	if err := syncRecordingDir(m.dir); err != nil {
		return err
	}
	m.noteLatest(uuid, path)
	if err := os.Remove(journalPath); err != nil {
		return err
	}
	_ = os.Remove(path + ".partial") // now superseded by the recovered MCAP
	delete(m.journals, uuid)
	return nil
}

func (m *recordingManager) noteLatest(uuid, path string) {
	if path > m.latest[uuid] {
		m.latest[uuid] = path
	}
}

func syncRecordingDir(path string) error {
	dir, err := os.Open(path)
	if err != nil {
		return err
	}
	defer dir.Close()
	return dir.Sync()
}

// Close finalizes received data on a graceful dashboard shutdown. An abrupt
// crash leaves the durable batch journal for recovery on the next start.
func (m *recordingManager) Close() error {
	if m == nil {
		return nil
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	var result error
	for uuid := range m.active {
		result = errors.Join(result, m.finishLocked(uuid))
	}
	return result
}
