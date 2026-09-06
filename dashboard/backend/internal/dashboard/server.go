package dashboard

import (
	"bytes"
	"compress/gzip"
	"context"
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"math"
	"mime/multipart"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"regexp"
	"runtime"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"
	"unicode"

	"baize/shared/agentbinary"
	"baize/shared/model"
)

const (
	maxTelemetryBytes           = 32 << 20
	maxCompressedTelemetryBytes = 16 << 20
	maxReleaseBytes             = 128 << 20
	fastMotorHistoryLimit       = 32_000
	historyPointLimit           = 400_000
	publicHostSampleRateHz      = 0.5
	publicAllMotorSampleRateHz  = 20.0
	publicSingleSampleRateHz    = 500.0
)

var (
	uuidPattern = regexp.MustCompile(`^[0-9a-fA-F]{8}-[0-9a-fA-F]{4}-[1-5][0-9a-fA-F]{3}-[89aAbB][0-9a-fA-F]{3}-[0-9a-fA-F]{12}$`)
	// All release channels use the public projects' UTC YYYYMMDD version scheme.
	versionPattern  = regexp.MustCompile(`^[0-9]{8}$`)
	platformPattern = regexp.MustCompile(`^[a-z0-9_-]{2,20}$`)
	motorIDPattern  = regexp.MustCompile(`^[A-Za-z0-9_.:-]{1,128}$`)
)

type ServerConfig struct {
	AgentToken   string
	AdminUser    string
	JWTSecret    string
	FrontendDir  string
	CookieSecure bool
}

type Server struct {
	config        ServerConfig
	store         *Store
	sessions      map[string]time.Time
	loginAttempts map[string]loginAttempt
	publicStream  *streamHub
	adminStream   *streamHub
	mu            sync.Mutex
	handler       http.Handler
}

type loginAttempt struct {
	Failures int
	Last     time.Time
}

func NewServer(cfg ServerConfig, store *Store) *Server {
	server := &Server{
		config: cfg, store: store, sessions: make(map[string]time.Time), loginAttempts: make(map[string]loginAttempt),
		publicStream: newStreamHub(), adminStream: newStreamHub(),
	}
	mux := http.NewServeMux()
	mux.HandleFunc("/healthz", server.health)
	mux.HandleFunc("/api/v1/session", server.session)
	mux.HandleFunc("/api/v1/admin/password", server.requireSession(server.changePassword))
	mux.HandleFunc("/api/v1/admin/agent-token", server.requireAdmin(server.agentToken))
	mux.HandleFunc("/api/v1/robots", server.robots)
	mux.HandleFunc("/api/v1/robots/", server.publicRobotAction)
	mux.HandleFunc("/api/v1/ws/robots", server.publicRobotStream)
	mux.HandleFunc("/api/v1/telemetry", server.requireAgent(server.telemetry))
	mux.HandleFunc("/api/v1/update/check", server.requireAgent(server.updateCheck))
	mux.HandleFunc("/api/v1/update/files/", server.requireAgent(server.updateFile))
	mux.HandleFunc("/api/v1/admin/robots", server.requireAdmin(server.adminRobots))
	mux.HandleFunc("/api/v1/admin/robots/", server.requireAdmin(server.robotAction))
	mux.HandleFunc("/api/v1/admin/ws/robots", server.requireAdmin(server.adminRobotStream))
	mux.HandleFunc("/api/v1/admin/releases", server.requireAdmin(server.releases))
	mux.HandleFunc("/api/v1/admin/releases/", server.requireAdmin(server.releaseAction))
	mux.HandleFunc("/api/", func(writer http.ResponseWriter, _ *http.Request) {
		writeError(writer, http.StatusNotFound, "API route not found")
	})
	if cfg.FrontendDir != "" {
		mux.HandleFunc("/", server.frontend)
	}
	server.handler = server.securityHeaders(mux)
	return server
}

func (s *Server) frontend(writer http.ResponseWriter, request *http.Request) {
	if request.Method != http.MethodGet && request.Method != http.MethodHead {
		methodNotAllowed(writer)
		return
	}
	cleanPath := strings.TrimPrefix(filepath.Clean("/"+request.URL.Path), "/")
	target := filepath.Join(s.config.FrontendDir, cleanPath)
	if info, err := os.Stat(target); err == nil && !info.IsDir() {
		writer.Header().Set("Cache-Control", "no-store")
		http.ServeFile(writer, request, target)
		return
	}
	writer.Header().Set("Cache-Control", "no-store")
	http.ServeFile(writer, request, filepath.Join(s.config.FrontendDir, "index.html"))
}

func (s *Server) ServeHTTP(writer http.ResponseWriter, request *http.Request) {
	s.handler.ServeHTTP(writer, request)
}

func (s *Server) health(writer http.ResponseWriter, _ *http.Request) {
	writeJSON(writer, http.StatusOK, map[string]any{"ok": true, "go_version": runtime.Version()})
}

func (s *Server) session(writer http.ResponseWriter, request *http.Request) {
	switch request.Method {
	case http.MethodGet:
		authenticated := s.validSession(request)
		passwordChangeRequired := false
		if authenticated {
			passwordChangeRequired, _ = s.store.PasswordChangeRequired(s.config.AdminUser)
		}
		writeJSON(writer, http.StatusOK, map[string]any{"authenticated": authenticated, "username": s.config.AdminUser, "password_change_required": passwordChangeRequired})
	case http.MethodPost:
		var body struct {
			Username string `json:"username"`
			Password string `json:"password"`
		}
		if err := decodeJSON(writer, request, &body, 4096); err != nil {
			return
		}
		if !s.loginAllowed(request) {
			writeError(writer, http.StatusTooManyRequests, "too many login attempts; try again later")
			return
		}
		valid, passwordChangeRequired, err := s.store.AuthenticateAdmin(body.Username, body.Password)
		if err != nil {
			writeError(writer, http.StatusInternalServerError, "authenticate administrator")
			return
		}
		if !secureEqual(body.Username, s.config.AdminUser) || !valid {
			s.recordLoginFailure(request)
			time.Sleep(250 * time.Millisecond)
			writeError(writer, http.StatusUnauthorized, "invalid password")
			return
		}
		s.resetLoginFailures(request)
		if err := s.startSession(writer, request); err != nil {
			writeError(writer, http.StatusInternalServerError, "create session")
			return
		}
		writeJSON(writer, http.StatusOK, map[string]bool{"authenticated": true, "password_change_required": passwordChangeRequired})
	case http.MethodDelete:
		if cookie, err := request.Cookie("baize_session"); err == nil {
			if claims, ok := s.parseSessionToken(cookie.Value); ok {
				s.mu.Lock()
				delete(s.sessions, claims.ID)
				s.mu.Unlock()
				s.adminStream.closeSession(claims.ID)
			}
		}
		http.SetCookie(writer, &http.Cookie{Name: "baize_session", Path: "/", MaxAge: -1, HttpOnly: true, Secure: s.secureCookie(request), SameSite: http.SameSiteStrictMode})
		writer.WriteHeader(http.StatusNoContent)
	default:
		methodNotAllowed(writer)
	}
}

func (s *Server) changePassword(writer http.ResponseWriter, request *http.Request) {
	if request.Method != http.MethodPost {
		methodNotAllowed(writer)
		return
	}
	var body struct {
		CurrentPassword string `json:"current_password"`
		NewPassword     string `json:"new_password"`
	}
	if err := decodeJSON(writer, request, &body, 4096); err != nil {
		return
	}
	if err := validateAdminPassword(body.NewPassword); err != nil {
		writeError(writer, http.StatusBadRequest, err.Error())
		return
	}
	if secureEqual(body.CurrentPassword, body.NewPassword) {
		writeError(writer, http.StatusBadRequest, "new password must be different from the current password")
		return
	}
	if err := s.store.ChangeAdminPassword(s.config.AdminUser, body.CurrentPassword, body.NewPassword); err != nil {
		if err.Error() == "current password is incorrect" {
			writeError(writer, http.StatusUnauthorized, err.Error())
			return
		}
		writeError(writer, http.StatusInternalServerError, "change administrator password")
		return
	}
	s.mu.Lock()
	s.sessions = make(map[string]time.Time)
	s.mu.Unlock()
	s.adminStream.closeSession("")
	if err := s.startSession(writer, request); err != nil {
		writeError(writer, http.StatusInternalServerError, "renew session")
		return
	}
	writeJSON(writer, http.StatusOK, map[string]bool{"authenticated": true, "password_change_required": false})
}

func (s *Server) agentToken(writer http.ResponseWriter, request *http.Request) {
	if request.Method != http.MethodGet {
		methodNotAllowed(writer)
		return
	}
	writeJSON(writer, http.StatusOK, map[string]string{"agent_token": s.config.AgentToken})
}

func (s *Server) startSession(writer http.ResponseWriter, request *http.Request) error {
	sessionID, err := randomToken()
	if err != nil {
		return err
	}
	expires := time.Now().Add(12 * time.Hour)
	token, err := s.createSessionToken(sessionID, expires)
	if err != nil {
		return err
	}
	s.mu.Lock()
	s.sessions[sessionID] = expires
	s.mu.Unlock()
	http.SetCookie(writer, &http.Cookie{Name: "baize_session", Value: token, Path: "/", HttpOnly: true, Secure: s.secureCookie(request), SameSite: http.SameSiteStrictMode, Expires: expires})
	return nil
}

func (s *Server) telemetry(writer http.ResponseWriter, request *http.Request) {
	if request.Method != http.MethodPost {
		methodNotAllowed(writer)
		return
	}
	var telemetry model.Telemetry
	if err := decodeTelemetryJSON(writer, request, &telemetry); err != nil {
		return
	}
	if telemetry.SchemaVersion != model.SchemaVersion || !uuidPattern.MatchString(telemetry.Robot.UUID) || telemetry.Robot.Code == "" {
		writeError(writer, http.StatusBadRequest, "invalid telemetry identity or schema")
		return
	}
	if err := s.store.PutTelemetry(telemetry); err != nil {
		writeError(writer, http.StatusInternalServerError, "store telemetry")
		return
	}
	s.broadcastRobot(telemetry.Robot.UUID)
	writeJSON(writer, http.StatusOK, map[string]bool{"ok": true})
}

func (s *Server) updateCheck(writer http.ResponseWriter, request *http.Request) {
	if request.Method != http.MethodGet {
		methodNotAllowed(writer)
		return
	}
	query := request.URL.Query()
	uuid, current := query.Get("uuid"), query.Get("version")
	goos, arch := query.Get("os"), query.Get("arch")
	if !uuidPattern.MatchString(uuid) || !platformPattern.MatchString(goos) || !platformPattern.MatchString(arch) {
		writeError(writer, http.StatusBadRequest, "invalid update query")
		return
	}
	release, found := s.store.FindUpdate(current, goos, arch)
	if !found {
		writeJSON(writer, http.StatusOK, map[string]bool{"available": false})
		return
	}
	writeJSON(writer, http.StatusOK, map[string]any{
		"available": true,
		"update": model.UpdateInfo{
			Version: release.Version, OS: release.OS, Arch: release.Arch,
			SHA256: release.SHA256, Size: release.Size, URL: "/api/v1/update/files/" + release.ID,
		},
	})
}

func (s *Server) updateFile(writer http.ResponseWriter, request *http.Request) {
	if request.Method != http.MethodGet {
		methodNotAllowed(writer)
		return
	}
	id := strings.TrimPrefix(request.URL.Path, "/api/v1/update/files/")
	if !safeReleaseID(id) {
		writeError(writer, http.StatusBadRequest, "invalid release id")
		return
	}
	release, ok := s.store.ReleaseByID(id)
	if !ok {
		writeError(writer, http.StatusNotFound, "release not found")
		return
	}
	writer.Header().Set("Content-Type", "application/octet-stream")
	writer.Header().Set("Content-Disposition", `attachment; filename="baize-agent"`)
	writer.Header().Set("X-Content-SHA256", release.SHA256)
	http.ServeFile(writer, request, release.Filename)
}

func (s *Server) robots(writer http.ResponseWriter, request *http.Request) {
	allowPublicAPI(writer)
	if request.Method == http.MethodOptions {
		writer.WriteHeader(http.StatusNoContent)
		return
	}
	if request.Method != http.MethodGet {
		methodNotAllowed(writer)
		return
	}
	writeJSON(writer, http.StatusOK, map[string]any{"robots": s.publicRobotSnapshot(), "server_time": time.Now().UTC()})
}

func (s *Server) publicRobotAction(writer http.ResponseWriter, request *http.Request) {
	allowPublicAPI(writer)
	if request.Method == http.MethodOptions {
		writer.WriteHeader(http.StatusNoContent)
		return
	}
	if request.Method != http.MethodGet {
		methodNotAllowed(writer)
		return
	}
	parts := strings.Split(strings.Trim(strings.TrimPrefix(request.URL.Path, "/api/v1/robots/"), "/"), "/")
	if len(parts) != 2 || parts[1] != "history" || len(parts[0]) != 20 {
		writeError(writer, http.StatusNotFound, "route not found")
		return
	}
	var uuid string
	for _, record := range s.store.Robots() {
		if subtle.ConstantTimeCompare([]byte(publicRobotID(s.config.JWTSecret, record.UUID)), []byte(parts[0])) == 1 {
			uuid = record.UUID
			break
		}
	}
	if uuid == "" {
		writeError(writer, http.StatusNotFound, "robot not found")
		return
	}
	scope := request.URL.Query().Get("scope")
	if scope != "" && scope != "host" && scope != "motors" {
		writeError(writer, http.StatusBadRequest, "history scope must be host or motors")
		return
	}
	duration, ok := historyDuration(writer, request, scope == "motors")
	if !ok {
		return
	}
	to := time.Now().UTC()
	if scope == "motors" {
		if latest, found := s.store.LatestMotorSampleAt(uuid); found {
			to = latest
		}
	}
	from := to.Add(-duration)
	var points []HistoryPoint
	var err error
	if scope == "motors" {
		fallbackRate := float64(publicAllMotorSampleRateHz)
		if request.URL.Query().Get("motor_id") != "" {
			fallbackRate = publicSingleSampleRateHz
		}
		limit, motorID, valid := fastMotorHistoryOptions(writer, request, duration, fallbackRate)
		if !valid {
			return
		}
		if motorID != "" {
			points, err = s.store.FastMotorHistoryFiltered(uuid, from, to, limit, motorID)
		} else {
			points, err = s.store.FastMotorHistory(uuid, from, to, limit)
		}
	} else {
		limit, valid := historySampleLimit(writer, request, duration, publicHostSampleRateHz, historyPointLimit)
		if !valid {
			return
		}
		if scope == "host" {
			points, err = s.store.HostHistory(uuid, from, to, limit)
		} else {
			points, err = s.store.History(uuid, from, to, limit)
		}
	}
	if err != nil {
		writeError(writer, http.StatusInternalServerError, "read telemetry history")
		return
	}
	writeJSON(writer, http.StatusOK, map[string]any{"points": points, "from": from, "to": to, "scope": scope})
}

func (s *Server) adminRobots(writer http.ResponseWriter, request *http.Request) {
	if request.Method != http.MethodGet {
		methodNotAllowed(writer)
		return
	}
	writeJSON(writer, http.StatusOK, map[string]any{"robots": s.store.Robots(), "server_time": time.Now().UTC()})
}

func (s *Server) publicRobotStream(writer http.ResponseWriter, request *http.Request) {
	s.serveRobotStream(writer, request, false)
}

func (s *Server) adminRobotStream(writer http.ResponseWriter, request *http.Request) {
	s.serveRobotStream(writer, request, true)
}

func (s *Server) robotAction(writer http.ResponseWriter, request *http.Request) {
	path := strings.Trim(strings.TrimPrefix(request.URL.Path, "/api/v1/admin/robots/"), "/")
	parts := strings.Split(path, "/")
	if len(parts) == 0 || !uuidPattern.MatchString(parts[0]) {
		writeError(writer, http.StatusNotFound, "route not found")
		return
	}
	uuid := parts[0]
	if len(parts) == 1 {
		if request.Method != http.MethodDelete {
			methodNotAllowed(writer)
			return
		}
		record, ok := s.store.Robot(uuid)
		if !ok {
			writeError(writer, http.StatusNotFound, "robot not found")
			return
		}
		if err := s.store.RemoveRobot(uuid); err != nil {
			writeError(writer, http.StatusInternalServerError, "remove robot")
			return
		}
		s.publicStream.broadcast(s.publicRemovalEvent(record))
		s.adminStream.broadcast(s.adminRemovalEvent(uuid))
		writer.WriteHeader(http.StatusNoContent)
		return
	}
	if len(parts) != 2 {
		writeError(writer, http.StatusNotFound, "route not found")
		return
	}
	action := parts[1]
	switch action {
	case "remark":
		if request.Method != http.MethodPatch {
			methodNotAllowed(writer)
			return
		}
		var body struct {
			Remark string `json:"remark"`
		}
		if err := decodeJSON(writer, request, &body, 2048); err != nil {
			return
		}
		body.Remark = strings.TrimSpace(body.Remark)
		if len([]rune(body.Remark)) > 200 {
			writeError(writer, http.StatusBadRequest, "remark is longer than 200 characters")
			return
		}
		if _, ok := s.store.Robot(uuid); !ok {
			writeError(writer, http.StatusNotFound, "robot not found")
			return
		}
		if err := s.store.SetRemark(uuid, body.Remark); err != nil {
			writeError(writer, http.StatusInternalServerError, err.Error())
			return
		}
		s.broadcastRobot(uuid)
		writeJSON(writer, http.StatusOK, map[string]bool{"ok": true})
	case "history":
		if request.Method != http.MethodGet {
			methodNotAllowed(writer)
			return
		}
		if _, ok := s.store.Robot(uuid); !ok {
			writeError(writer, http.StatusNotFound, "robot not found")
			return
		}
		scope := request.URL.Query().Get("scope")
		if scope != "" && scope != "host" && scope != "motors" {
			writeError(writer, http.StatusBadRequest, "history scope must be host or motors")
			return
		}
		duration, ok := historyDuration(writer, request, scope == "motors")
		if !ok {
			return
		}
		to := time.Now().UTC()
		if scope == "motors" {
			if latest, found := s.store.LatestMotorSampleAt(uuid); found {
				to = latest
			}
		}
		from := to.Add(-duration)
		var points []HistoryPoint
		var err error
		if scope == "motors" {
			fallbackRate := publicAllMotorSampleRateHz
			if request.URL.Query().Get("motor_id") != "" {
				fallbackRate = publicSingleSampleRateHz
			}
			limit, motorID, valid := fastMotorHistoryOptions(writer, request, duration, fallbackRate)
			if !valid {
				return
			}
			if motorID != "" {
				points, err = s.store.FastMotorHistoryFiltered(uuid, from, to, limit, motorID)
			} else {
				points, err = s.store.FastMotorHistory(uuid, from, to, limit)
			}
		} else {
			limit, valid := historySampleLimit(writer, request, duration, publicHostSampleRateHz, historyPointLimit)
			if !valid {
				return
			}
			if scope == "host" {
				points, err = s.store.HostHistory(uuid, from, to, limit)
			} else {
				points, err = s.store.History(uuid, from, to, limit)
			}
		}
		if err != nil {
			writeError(writer, http.StatusInternalServerError, "read telemetry history")
			return
		}
		writeJSON(writer, http.StatusOK, map[string]any{"points": points, "from": from, "to": to, "scope": scope})
	default:
		writeError(writer, http.StatusNotFound, "route not found")
	}
}

func historyHours(writer http.ResponseWriter, request *http.Request) (int, bool) {
	hours := 24
	if value := request.URL.Query().Get("hours"); value != "" {
		parsed, err := strconv.Atoi(value)
		if err != nil || parsed < 1 || parsed > 24*365 {
			writeError(writer, http.StatusBadRequest, "history hours must be between 1 and 8760")
			return 0, false
		}
		hours = parsed
	}
	return hours, true
}

func historyDuration(writer http.ResponseWriter, request *http.Request, fast bool) (time.Duration, bool) {
	if !fast {
		hours, ok := historyHours(writer, request)
		return time.Duration(hours) * time.Hour, ok
	}
	seconds := 60
	if value := request.URL.Query().Get("seconds"); value != "" {
		parsed, err := strconv.Atoi(value)
		if err != nil || parsed < 1 || parsed > 60 {
			writeError(writer, http.StatusBadRequest, "fast motor history seconds must be between 1 and 60")
			return 0, false
		}
		seconds = parsed
	}
	return time.Duration(seconds) * time.Second, true
}

func fastMotorHistoryOptions(writer http.ResponseWriter, request *http.Request, duration time.Duration, fallbackRate float64) (int, string, bool) {
	motorID := request.URL.Query().Get("motor_id")
	if motorID != "" && !motorIDPattern.MatchString(motorID) {
		writeError(writer, http.StatusBadRequest, "invalid motor id")
		return 0, "", false
	}
	limit, valid := historySampleLimit(writer, request, duration, fallbackRate, fastMotorHistoryLimit)
	if !valid {
		return 0, "", false
	}
	return limit, motorID, true
}

func historySampleLimit(writer http.ResponseWriter, request *http.Request, duration time.Duration, fallbackRate float64, maximum int) (int, bool) {
	value := request.URL.Query().Get("sample_rate_hz")
	rate := fallbackRate
	if value != "" {
		parsed, err := strconv.ParseFloat(value, 64)
		if err != nil {
			writeError(writer, http.StatusBadRequest, "history sample_rate_hz must be greater than 0 and at most 500")
			return 0, false
		}
		rate = parsed
	}
	if math.IsNaN(rate) || math.IsInf(rate, 0) || rate <= 0 || rate > publicSingleSampleRateHz {
		writeError(writer, http.StatusBadRequest, "history sample_rate_hz must be greater than 0 and at most 500")
		return 0, false
	}
	limit := int(math.Ceil(duration.Seconds() * rate))
	if limit < 1 || limit > maximum {
		writeError(writer, http.StatusBadRequest, "history sampling rate is too high for the requested duration")
		return 0, false
	}
	return limit, true
}

func allowPublicAPI(writer http.ResponseWriter) {
	writer.Header().Set("Access-Control-Allow-Origin", "*")
	writer.Header().Set("Access-Control-Allow-Methods", "GET, OPTIONS")
	writer.Header().Set("Access-Control-Allow-Headers", "Accept")
	writer.Header().Set("Cache-Control", "no-store")
}

func (s *Server) broadcastRobot(uuid string) {
	if robot, ok := s.store.Robot(uuid); ok {
		s.publicStream.broadcastWith(func(client *streamClient) []byte {
			return s.publicRobotEventForOptions(robot, client.publicOptions)
		})
		s.adminStream.broadcast(s.adminRobotEvent(robot))
	}
}

func (s *Server) releases(writer http.ResponseWriter, request *http.Request) {
	switch request.Method {
	case http.MethodGet:
		writeJSON(writer, http.StatusOK, map[string]any{"releases": s.store.Releases()})
	case http.MethodPost:
		s.uploadRelease(writer, request)
	default:
		methodNotAllowed(writer)
	}
}

func (s *Server) uploadRelease(writer http.ResponseWriter, request *http.Request) {
	request.Body = http.MaxBytesReader(writer, request.Body, maxReleaseBytes+(1<<20))
	if err := request.ParseMultipartForm(maxReleaseBytes + (1 << 20)); err != nil {
		writeError(writer, http.StatusBadRequest, "invalid or oversized upload")
		return
	}
	defer request.MultipartForm.RemoveAll()
	version, goos, arch := request.FormValue("version"), request.FormValue("os"), request.FormValue("arch")
	if !versionPattern.MatchString(version) || !platformPattern.MatchString(goos) || !platformPattern.MatchString(arch) {
		writeError(writer, http.StatusBadRequest, "invalid release metadata")
		return
	}
	file, _, err := request.FormFile("binary")
	if err != nil {
		writeError(writer, http.StatusBadRequest, "binary is required")
		return
	}
	defer file.Close()
	release, err := s.saveReleaseFile(file, version, goos, arch)
	if err != nil {
		writeError(writer, http.StatusBadRequest, err.Error())
		return
	}
	if err := s.store.AddRelease(release); err != nil {
		_ = os.Remove(release.Filename)
		writeError(writer, http.StatusInternalServerError, err.Error())
		return
	}
	writeJSON(writer, http.StatusCreated, release)
}

func (s *Server) saveReleaseFile(source multipart.File, version, goos, arch string) (Release, error) {
	temporary, err := os.CreateTemp(filepath.Join(s.store.dataDir, "releases"), ".upload-*")
	if err != nil {
		return Release{}, err
	}
	temporaryPath := temporary.Name()
	defer os.Remove(temporaryPath)
	hash := sha256.New()
	written, err := io.Copy(io.MultiWriter(temporary, hash), io.LimitReader(source, maxReleaseBytes+1))
	if err != nil {
		temporary.Close()
		return Release{}, err
	}
	if written == 0 || written > maxReleaseBytes {
		temporary.Close()
		return Release{}, errors.New("binary must be between 1 byte and 128 MiB")
	}
	if err := temporary.Sync(); err != nil {
		temporary.Close()
		return Release{}, err
	}
	if err := temporary.Close(); err != nil {
		return Release{}, err
	}
	digest := hex.EncodeToString(hash.Sum(nil))
	if err := agentbinary.Validate(temporaryPath, goos, arch); err != nil {
		return Release{}, err
	}
	id := fmt.Sprintf("%s-%s-%s-%s", goos, arch, sanitizeVersion(version), digest[:12])
	destination := filepath.Join(s.store.dataDir, "releases", id)
	if err := os.Rename(temporaryPath, destination); err != nil {
		return Release{}, err
	}
	if err := os.Chmod(destination, 0o640); err != nil {
		return Release{}, err
	}
	return Release{ID: id, Version: version, OS: goos, Arch: arch, SHA256: digest, Size: written, UploadedAt: time.Now().UTC(), Filename: destination}, nil
}

func (s *Server) releaseAction(writer http.ResponseWriter, request *http.Request) {
	if request.Method != http.MethodDelete {
		methodNotAllowed(writer)
		return
	}
	id := strings.TrimPrefix(request.URL.Path, "/api/v1/admin/releases/")
	if !safeReleaseID(id) {
		writeError(writer, http.StatusBadRequest, "invalid release id")
		return
	}
	if err := s.store.DeleteRelease(id); err != nil {
		if errors.Is(err, os.ErrNotExist) {
			writeError(writer, http.StatusNotFound, "release not found")
		} else {
			writeError(writer, http.StatusConflict, err.Error())
		}
		return
	}
	writer.WriteHeader(http.StatusNoContent)
}

func (s *Server) requireAgent(next http.HandlerFunc) http.HandlerFunc {
	return func(writer http.ResponseWriter, request *http.Request) {
		authorization := request.Header.Get("Authorization")
		token := strings.TrimPrefix(authorization, "Bearer ")
		if !secureEqual(token, s.config.AgentToken) {
			slog.Warn("invalid agent token",
				"remote_ip", requestClientIP(request),
				"remote_addr", request.RemoteAddr,
				"method", request.Method,
				"path", request.URL.Path,
				"token_fingerprint", tokenFingerprint(token),
				"authorization_present", authorization != "",
				"user_agent", request.UserAgent(),
			)
			writeError(writer, http.StatusUnauthorized, "invalid agent token")
			return
		}
		next(writer, request)
	}
}

func (s *Server) requireAdmin(next http.HandlerFunc) http.HandlerFunc {
	return func(writer http.ResponseWriter, request *http.Request) {
		if !s.validSession(request) {
			writeError(writer, http.StatusUnauthorized, "not authenticated")
			return
		}
		required, err := s.store.PasswordChangeRequired(s.config.AdminUser)
		if err != nil {
			writeError(writer, http.StatusInternalServerError, "read administrator state")
			return
		}
		if required {
			writeError(writer, http.StatusForbidden, "password change required")
			return
		}
		next(writer, request)
	}
}

func (s *Server) requireSession(next http.HandlerFunc) http.HandlerFunc {
	return func(writer http.ResponseWriter, request *http.Request) {
		if !s.validSession(request) {
			writeError(writer, http.StatusUnauthorized, "not authenticated")
			return
		}
		next(writer, request)
	}
}

func (s *Server) validSession(request *http.Request) bool {
	cookie, err := request.Cookie("baize_session")
	if err != nil {
		return false
	}
	claims, ok := s.parseSessionToken(cookie.Value)
	if !ok {
		return false
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	expires, ok := s.sessions[claims.ID]
	if !ok || time.Now().After(expires) || claims.ExpiresAt <= time.Now().Unix() {
		delete(s.sessions, claims.ID)
		return false
	}
	return true
}

type sessionClaims struct {
	ID        string `json:"jti"`
	ExpiresAt int64  `json:"exp"`
	IssuedAt  int64  `json:"iat"`
}

func (s *Server) createSessionToken(id string, expires time.Time) (string, error) {
	header := base64.RawURLEncoding.EncodeToString([]byte(`{"alg":"HS256","typ":"JWT"}`))
	claims, err := json.Marshal(sessionClaims{ID: id, ExpiresAt: expires.Unix(), IssuedAt: time.Now().Unix()})
	if err != nil {
		return "", err
	}
	payload := base64.RawURLEncoding.EncodeToString(claims)
	input := header + "." + payload
	mac := hmac.New(sha256.New, []byte(s.config.JWTSecret))
	_, _ = mac.Write([]byte(input))
	signature := base64.RawURLEncoding.EncodeToString(mac.Sum(nil))
	return input + "." + signature, nil
}

func (s *Server) parseSessionToken(token string) (sessionClaims, bool) {
	parts := strings.Split(token, ".")
	if len(parts) != 3 {
		return sessionClaims{}, false
	}
	mac := hmac.New(sha256.New, []byte(s.config.JWTSecret))
	_, _ = mac.Write([]byte(parts[0] + "." + parts[1]))
	signature, err := base64.RawURLEncoding.DecodeString(parts[2])
	if err != nil || !hmac.Equal(signature, mac.Sum(nil)) {
		return sessionClaims{}, false
	}
	claimsData, err := base64.RawURLEncoding.DecodeString(parts[1])
	if err != nil {
		return sessionClaims{}, false
	}
	var claims sessionClaims
	if err := json.Unmarshal(claimsData, &claims); err != nil || claims.ID == "" || claims.ExpiresAt <= 0 {
		return sessionClaims{}, false
	}
	return claims, true
}

func (s *Server) securityHeaders(next http.Handler) http.Handler {
	return http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		writer.Header().Set("X-Content-Type-Options", "nosniff")
		writer.Header().Set("X-Frame-Options", "DENY")
		writer.Header().Set("Referrer-Policy", "no-referrer")
		writer.Header().Set("Permissions-Policy", "camera=(), microphone=(), geolocation=()")
		writer.Header().Set("Content-Security-Policy", "default-src 'self'; connect-src 'self' ws: wss:; img-src 'self' data:; style-src 'self' https://registry.npmmirror.com; font-src 'self' https://registry.npmmirror.com; script-src 'self'; object-src 'none'; base-uri 'self'; frame-ancestors 'none'; form-action 'self'")
		next.ServeHTTP(writer, request)
	})
}

func decodeJSON(writer http.ResponseWriter, request *http.Request, target any, limit int64) error {
	request.Body = http.MaxBytesReader(writer, request.Body, limit)
	return decodeJSONReader(writer, request.Body, target)
}

func decodeTelemetryJSON(writer http.ResponseWriter, request *http.Request, target any) error {
	if strings.EqualFold(strings.TrimSpace(request.Header.Get("Content-Encoding")), "gzip") {
		request.Body = http.MaxBytesReader(writer, request.Body, maxCompressedTelemetryBytes)
		compressed, err := gzip.NewReader(request.Body)
		if err != nil {
			writeError(writer, http.StatusBadRequest, "invalid gzip telemetry")
			return err
		}
		defer compressed.Close()
		data, err := io.ReadAll(io.LimitReader(compressed, maxTelemetryBytes+1))
		if err != nil {
			writeError(writer, http.StatusBadRequest, "invalid telemetry body")
			return err
		}
		if int64(len(data)) > maxTelemetryBytes {
			err := errors.New("telemetry body is too large")
			writeError(writer, http.StatusRequestEntityTooLarge, err.Error())
			return err
		}
		return decodeJSONReader(writer, bytes.NewReader(data), target)
	}
	return decodeJSON(writer, request, target, maxTelemetryBytes)
}

func decodeJSONReader(writer http.ResponseWriter, reader io.Reader, target any) error {
	decoder := json.NewDecoder(reader)
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(target); err != nil {
		writeError(writer, http.StatusBadRequest, "invalid JSON: "+err.Error())
		return err
	}
	var trailing any
	if err := decoder.Decode(&trailing); !errors.Is(err, io.EOF) {
		if err == nil {
			err = errors.New("multiple JSON values are not allowed")
		}
		writeError(writer, http.StatusBadRequest, "invalid JSON: "+err.Error())
		return err
	}
	return nil
}

func writeJSON(writer http.ResponseWriter, status int, value any) {
	writer.Header().Set("Content-Type", "application/json; charset=utf-8")
	writer.WriteHeader(status)
	if err := json.NewEncoder(writer).Encode(value); err != nil {
		slog.Warn("write JSON response", "error", err)
	}
}

func writeError(writer http.ResponseWriter, status int, message string) {
	writeJSON(writer, status, map[string]string{"error": message})
}

func methodNotAllowed(writer http.ResponseWriter) {
	writeError(writer, http.StatusMethodNotAllowed, "method not allowed")
}

func secureEqual(left, right string) bool {
	leftHash, rightHash := sha256.Sum256([]byte(left)), sha256.Sum256([]byte(right))
	return subtle.ConstantTimeCompare(leftHash[:], rightHash[:]) == 1
}

func validateAdminPassword(password string) error {
	if len(password) < 12 || len(password) > 128 {
		return errors.New("new password must contain between 12 and 128 characters")
	}
	var upper, lower, digit, symbol bool
	for _, character := range password {
		switch {
		case unicode.IsUpper(character):
			upper = true
		case unicode.IsLower(character):
			lower = true
		case unicode.IsDigit(character):
			digit = true
		case unicode.IsPunct(character) || unicode.IsSymbol(character):
			symbol = true
		}
	}
	if !upper || !lower || !digit || !symbol {
		return errors.New("new password must include upper-case, lower-case, number and symbol characters")
	}
	return nil
}

func (s *Server) secureCookie(request *http.Request) bool {
	return s.config.CookieSecure || request.TLS != nil
}

func requestClientIP(request *http.Request) string {
	host, _, err := net.SplitHostPort(request.RemoteAddr)
	if err == nil {
		return host
	}
	return request.RemoteAddr
}

func loginKey(request *http.Request) string {
	return requestClientIP(request)
}

func (s *Server) loginAllowed(request *http.Request) bool {
	key := loginKey(request)
	s.mu.Lock()
	defer s.mu.Unlock()
	attempt := s.loginAttempts[key]
	if time.Since(attempt.Last) > 10*time.Minute {
		delete(s.loginAttempts, key)
		return true
	}
	return attempt.Failures < 5
}

func (s *Server) recordLoginFailure(request *http.Request) {
	key := loginKey(request)
	s.mu.Lock()
	attempt := s.loginAttempts[key]
	attempt.Failures++
	attempt.Last = time.Now()
	s.loginAttempts[key] = attempt
	s.mu.Unlock()
}

func (s *Server) resetLoginFailures(request *http.Request) {
	s.mu.Lock()
	delete(s.loginAttempts, loginKey(request))
	s.mu.Unlock()
}

func randomToken() (string, error) {
	data := make([]byte, 32)
	if _, err := rand.Read(data); err != nil {
		return "", err
	}
	return hex.EncodeToString(data), nil
}

func tokenFingerprint(token string) string {
	if token == "" {
		return ""
	}
	digest := sha256.Sum256([]byte(token))
	return hex.EncodeToString(digest[:])[:12]
}

func sanitizeVersion(version string) string {
	return strings.NewReplacer("+", "_", "-", "_", ".", "_").Replace(version)
}

func sortedKeys[V any](values map[string]V) []string {
	keys := make([]string, 0, len(values))
	for key := range values {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	return keys
}

func shutdownServer(ctx context.Context, server *http.Server) error { return server.Shutdown(ctx) }
