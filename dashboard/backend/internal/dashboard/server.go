package dashboard

import (
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
	"mime/multipart"
	"net/http"
	"os"
	"path/filepath"
	"regexp"
	"runtime"
	"sort"
	"strings"
	"sync"
	"time"

	"echobot/shared/model"
)

const (
	maxTelemetryBytes = 4 << 20
	maxReleaseBytes   = 128 << 20
)

var (
	uuidPattern = regexp.MustCompile(`^[0-9a-fA-F]{8}-[0-9a-fA-F]{4}-[1-5][0-9a-fA-F]{3}-[89aAbB][0-9a-fA-F]{3}-[0-9a-fA-F]{12}$`)
	// All release channels use the public projects' UTC YYYYMMDD version scheme.
	versionPattern  = regexp.MustCompile(`^[0-9]{8}$`)
	platformPattern = regexp.MustCompile(`^[a-z0-9_-]{2,20}$`)
)

type ServerConfig struct {
	AgentToken    string
	AdminUser     string
	AdminPassword string
	JWTSecret     string
	FrontendDir   string
}

type Server struct {
	config   ServerConfig
	store    *Store
	sessions map[string]time.Time
	mu       sync.Mutex
	handler  http.Handler
}

func NewServer(cfg ServerConfig, store *Store) *Server {
	server := &Server{config: cfg, store: store, sessions: make(map[string]time.Time)}
	mux := http.NewServeMux()
	mux.HandleFunc("/healthz", server.health)
	mux.HandleFunc("/api/v1/session", server.session)
	mux.HandleFunc("/api/v1/robots", server.robots)
	mux.HandleFunc("/api/v1/telemetry", server.requireAgent(server.telemetry))
	mux.HandleFunc("/api/v1/update/check", server.requireAgent(server.updateCheck))
	mux.HandleFunc("/api/v1/update/files/", server.requireAgent(server.updateFile))
	mux.HandleFunc("/api/v1/admin/robots", server.requireAdmin(server.robots))
	mux.HandleFunc("/api/v1/admin/robots/", server.requireAdmin(server.robotAction))
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
		http.ServeFile(writer, request, target)
		return
	}
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
		writeJSON(writer, http.StatusOK, map[string]any{"authenticated": s.validSession(request), "username": s.config.AdminUser})
	case http.MethodPost:
		var body struct {
			Username string `json:"username"`
			Password string `json:"password"`
		}
		if err := decodeJSON(writer, request, &body, 4096); err != nil {
			return
		}
		if !secureEqual(body.Username, s.config.AdminUser) || !secureEqual(body.Password, s.config.AdminPassword) {
			time.Sleep(300 * time.Millisecond)
			writeError(writer, http.StatusUnauthorized, "invalid password")
			return
		}
		sessionID, err := randomToken()
		if err != nil {
			writeError(writer, http.StatusInternalServerError, "create session")
			return
		}
		expires := time.Now().Add(12 * time.Hour)
		token, err := s.createSessionToken(sessionID, expires)
		if err != nil {
			writeError(writer, http.StatusInternalServerError, "create session")
			return
		}
		s.mu.Lock()
		s.sessions[sessionID] = expires
		s.mu.Unlock()
		http.SetCookie(writer, &http.Cookie{Name: "echobot_session", Value: token, Path: "/", HttpOnly: true, SameSite: http.SameSiteStrictMode, Expires: expires})
		writeJSON(writer, http.StatusOK, map[string]bool{"authenticated": true})
	case http.MethodDelete:
		if cookie, err := request.Cookie("echobot_session"); err == nil {
			if claims, ok := s.parseSessionToken(cookie.Value); ok {
				s.mu.Lock()
				delete(s.sessions, claims.ID)
				s.mu.Unlock()
			}
		}
		http.SetCookie(writer, &http.Cookie{Name: "echobot_session", Path: "/", MaxAge: -1, HttpOnly: true, SameSite: http.SameSiteStrictMode})
		writer.WriteHeader(http.StatusNoContent)
	default:
		methodNotAllowed(writer)
	}
}

func (s *Server) telemetry(writer http.ResponseWriter, request *http.Request) {
	if request.Method != http.MethodPost {
		methodNotAllowed(writer)
		return
	}
	var telemetry model.Telemetry
	if err := decodeJSON(writer, request, &telemetry, maxTelemetryBytes); err != nil {
		return
	}
	if telemetry.SchemaVersion != model.SchemaVersion || !uuidPattern.MatchString(telemetry.Robot.UUID) || telemetry.Robot.Code == "" {
		writeError(writer, http.StatusBadRequest, "invalid telemetry identity or schema")
		return
	}
	s.store.PutTelemetry(telemetry)
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
	release, found := s.store.FindUpdate(uuid, current, goos, arch, query.Get("automatic") == "1")
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
	if strings.Contains(id, "/") || id == "" {
		writeError(writer, http.StatusBadRequest, "invalid release id")
		return
	}
	release, ok := s.store.ReleaseByID(id)
	if !ok {
		writeError(writer, http.StatusNotFound, "release not found")
		return
	}
	writer.Header().Set("Content-Type", "application/octet-stream")
	writer.Header().Set("Content-Disposition", `attachment; filename="echobot-agent"`)
	writer.Header().Set("X-Content-SHA256", release.SHA256)
	http.ServeFile(writer, request, release.Filename)
}

func (s *Server) robots(writer http.ResponseWriter, request *http.Request) {
	if request.Method != http.MethodGet {
		methodNotAllowed(writer)
		return
	}
	writeJSON(writer, http.StatusOK, map[string]any{"robots": s.store.Robots(), "server_time": time.Now().UTC()})
}

func (s *Server) robotAction(writer http.ResponseWriter, request *http.Request) {
	path := strings.TrimPrefix(request.URL.Path, "/api/v1/admin/robots/")
	parts := strings.Split(path, "/")
	if len(parts) != 2 || !uuidPattern.MatchString(parts[0]) {
		writeError(writer, http.StatusNotFound, "route not found")
		return
	}
	uuid, action := parts[0], parts[1]
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
		if err := s.store.SetRemark(uuid, body.Remark); err != nil {
			writeError(writer, http.StatusInternalServerError, err.Error())
			return
		}
		writeJSON(writer, http.StatusOK, map[string]bool{"ok": true})
	case "update":
		if request.Method == http.MethodDelete {
			if err := s.store.SetDesired(uuid, ""); err != nil {
				writeError(writer, http.StatusInternalServerError, err.Error())
				return
			}
			writer.WriteHeader(http.StatusNoContent)
			return
		}
		if request.Method != http.MethodPost {
			methodNotAllowed(writer)
			return
		}
		var body struct {
			Version string `json:"version"`
		}
		if err := decodeJSON(writer, request, &body, 2048); err != nil {
			return
		}
		if !versionPattern.MatchString(body.Version) {
			writeError(writer, http.StatusBadRequest, "invalid version")
			return
		}
		if err := s.store.SetDesired(uuid, body.Version); err != nil {
			writeError(writer, http.StatusInternalServerError, err.Error())
			return
		}
		writeJSON(writer, http.StatusOK, map[string]bool{"ok": true})
	default:
		writeError(writer, http.StatusNotFound, "route not found")
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
		writeError(writer, http.StatusInternalServerError, err.Error())
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
	if id == "" || strings.Contains(id, "/") {
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
		token := strings.TrimPrefix(request.Header.Get("Authorization"), "Bearer ")
		if !secureEqual(token, s.config.AgentToken) {
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
		next(writer, request)
	}
}

func (s *Server) validSession(request *http.Request) bool {
	cookie, err := request.Cookie("echobot_session")
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
		next.ServeHTTP(writer, request)
	})
}

func decodeJSON(writer http.ResponseWriter, request *http.Request, target any, limit int64) error {
	request.Body = http.MaxBytesReader(writer, request.Body, limit)
	decoder := json.NewDecoder(request.Body)
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(target); err != nil {
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

func randomToken() (string, error) {
	data := make([]byte, 32)
	if _, err := rand.Read(data); err != nil {
		return "", err
	}
	return hex.EncodeToString(data), nil
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
