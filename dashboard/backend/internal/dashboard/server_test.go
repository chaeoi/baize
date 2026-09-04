package dashboard

import (
	"bytes"
	"compress/gzip"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"
	"time"

	"baize/shared/model"
	"github.com/gorilla/websocket"
)

func TestFrontendDisablesCachingForAssetsAndRoutes(t *testing.T) {
	frontend := t.TempDir()
	if err := os.WriteFile(frontend+"/index.html", []byte("index"), 0600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(frontend+"/app.js", []byte("app"), 0600); err != nil {
		t.Fatal(err)
	}
	server := NewServer(ServerConfig{FrontendDir: frontend, JWTSecret: "jwt-secret-long-enough-for-tests-123456"}, nil)
	for _, path := range []string{"/app.js", "/robot/public-id"} {
		response := httptest.NewRecorder()
		server.ServeHTTP(response, httptest.NewRequest(http.MethodGet, path, nil))
		if response.Code != http.StatusOK || response.Header().Get("Cache-Control") != "no-store" {
			t.Fatalf("frontend %s status=%d cache=%q", path, response.Code, response.Header().Get("Cache-Control"))
		}
	}
}

func TestParsePublicStreamOptions(t *testing.T) {
	validID := "0123456789abcdef0123"
	options, ok := parsePublicStreamOptions(httptest.NewRequest(http.MethodGet, "/api/v1/ws/robots", nil))
	if !ok || options.includeSamples {
		t.Fatalf("default public stream options = %+v, ok=%v", options, ok)
	}
	options, ok = parsePublicStreamOptions(httptest.NewRequest(http.MethodGet, "/api/v1/ws/robots?include_samples=1&robot_id="+validID, nil))
	if !ok || !options.includeSamples || options.robotID != validID || len(options.motorIDs) != 0 {
		t.Fatalf("sample stream options = %+v, ok=%v", options, ok)
	}
	options, ok = parsePublicStreamOptions(httptest.NewRequest(http.MethodGet, "/api/v1/ws/robots?include_samples=1&robot_id="+validID+"&motor_ids=left_ankle_virtual_1,right_ankle_virtual_1", nil))
	if !ok || len(options.motorIDs) != 2 {
		t.Fatalf("filtered sample stream options = %+v, ok=%v", options, ok)
	}
	if _, ok := parsePublicStreamOptions(httptest.NewRequest(http.MethodGet, "/api/v1/ws/robots?include_samples=1&robot_id=not-an-id", nil)); ok {
		t.Fatal("invalid public robot id accepted")
	}
}

func TestSessionTokenUsesJWTSecret(t *testing.T) {
	server := &Server{config: ServerConfig{JWTSecret: "jwt-secret-long-enough-for-tests-123456"}}
	expires := time.Now().Add(time.Hour)
	token, err := server.createSessionToken("session-id", expires)
	if err != nil {
		t.Fatal(err)
	}
	claims, ok := server.parseSessionToken(token)
	if !ok || claims.ID != "session-id" || claims.ExpiresAt != expires.Unix() {
		t.Fatalf("unexpected claims: %+v", claims)
	}
	if _, ok := server.parseSessionToken(token + "x"); ok {
		t.Fatal("tampered JWT was accepted")
	}
	server.config.JWTSecret = "different-secret-long-enough-for-tests"
	if _, ok := server.parseSessionToken(token); ok {
		t.Fatal("JWT signed with another secret was accepted")
	}
}

func newTestStore(t *testing.T) *Store {
	t.Helper()
	root := t.TempDir()
	store, err := NewStore(root+"/control", root+"/history", StoreOptions{
		AdminUser: "admin", BootstrapPassword: "Baize@Admin1", RequirePasswordChange: true,
		HistorySampleInterval: time.Second,
	})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = store.Close() })
	return store
}

func TestPublicRobotDataIsRedactedButManagementRequiresLogin(t *testing.T) {
	store := newTestStore(t)
	if err := store.PutTelemetry(model.Telemetry{
		SchemaVersion: model.SchemaVersion,
		Robot: model.Robot{
			UUID: "52446a60-7483-4ba7-b8c7-b85f60b2a00f", Code: "M99", Model: "2m_v0.1.2",
			Hostname: "m99", OS: "linux", Arch: "arm64",
		},
		AgentVersion: "20260814", CollectedAt: time.Now().UTC(),
	}); err != nil {
		t.Fatal(err)
	}
	server := NewServer(ServerConfig{
		AgentToken: "agent-token-long-enough-for-tests", AdminUser: "admin",
		JWTSecret: "jwt-secret-long-enough-for-tests-123456",
	}, store)

	publicRequest := httptest.NewRequest(http.MethodGet, "/api/v1/robots", nil)
	publicResponse := httptest.NewRecorder()
	server.ServeHTTP(publicResponse, publicRequest)
	if publicResponse.Code != http.StatusOK {
		t.Fatalf("public robots status = %d, want %d", publicResponse.Code, http.StatusOK)
	}
	if body := publicResponse.Body.String(); !strings.Contains(body, "M99") {
		t.Fatalf("public robots response missing robot: %s", body)
	}
	for _, sensitive := range []string{"52446a60-7483-4ba7-b8c7-b85f60b2a00f", "m99", "linux", "arm64"} {
		if strings.Contains(publicResponse.Body.String(), sensitive) {
			t.Fatalf("public robots response exposed %q: %s", sensitive, publicResponse.Body.String())
		}
	}

	adminRequest := httptest.NewRequest(http.MethodPatch, "/api/v1/admin/robots/52446a60-7483-4ba7-b8c7-b85f60b2a00f/remark", nil)
	adminResponse := httptest.NewRecorder()
	server.ServeHTTP(adminResponse, adminRequest)
	if adminResponse.Code != http.StatusUnauthorized {
		t.Fatalf("anonymous management status = %d, want %d", adminResponse.Code, http.StatusUnauthorized)
	}
}

func TestPublicRobotStreamStartsWithRedactedSnapshot(t *testing.T) {
	store := newTestStore(t)
	sampleAt := time.Now().UTC()
	if err := store.PutTelemetry(model.Telemetry{
		SchemaVersion: model.SchemaVersion,
		Robot: model.Robot{
			UUID: "52446a60-7483-4ba7-b8c7-b85f60b2a00f", Code: "M99", Model: "2m_v0.1.2",
			Hostname: "m99", OS: "linux", Arch: "arm64",
		},
		AgentVersion: "20260814", CollectedAt: time.Now().UTC(),
		Motors: &model.MotorSnapshot{SampleRateHz: 500, Motors: []model.MotorState{{ID: "hip", Label: "左髋"}}, Samples: []model.MotorSample{{At: sampleAt, Motors: []model.MotorSampleState{{ID: "hip", TorqueNm: 8.5}}}}},
	}); err != nil {
		t.Fatal(err)
	}
	server := NewServer(ServerConfig{
		AgentToken: "agent-token-long-enough-for-tests", AdminUser: "admin",
		JWTSecret: "jwt-secret-long-enough-for-tests-123456",
	}, store)
	httpServer := httptest.NewServer(server)
	defer httpServer.Close()

	streamURL := "ws" + strings.TrimPrefix(httpServer.URL, "http") + "/api/v1/ws/robots"
	connection, _, err := websocket.DefaultDialer.Dial(streamURL, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer connection.Close()
	_, message, err := connection.ReadMessage()
	if err != nil {
		t.Fatal(err)
	}
	var event struct {
		Type   string        `json:"type"`
		Robots []PublicRobot `json:"robots"`
	}
	if err := json.Unmarshal(message, &event); err != nil {
		t.Fatal(err)
	}
	if event.Type != "snapshot" || len(event.Robots) != 1 || event.Robots[0].ID == "" {
		t.Fatalf("unexpected stream snapshot: %+v", event)
	}
	for _, sensitive := range []string{"52446a60-7483-4ba7-b8c7-b85f60b2a00f", "m99", "linux", "arm64"} {
		if strings.Contains(string(message), sensitive) {
			t.Fatalf("public stream exposed %q: %s", sensitive, message)
		}
	}
	record, ok := store.Robot("52446a60-7483-4ba7-b8c7-b85f60b2a00f")
	if !ok {
		t.Fatal("stored robot not found")
	}
	var liveEvent struct {
		Type  string       `json:"type"`
		Robot *PublicRobot `json:"robot"`
	}
	if err := json.Unmarshal(server.publicRobotEvent(record), &liveEvent); err != nil {
		t.Fatal(err)
	}
	if liveEvent.Robot == nil || liveEvent.Robot.MotorSampleRateHz != 500 || len(liveEvent.Robot.MotorSamples) != 1 || liveEvent.Robot.MotorSamples[0].Motors[0].TorqueNm != 8.5 || liveEvent.Robot.MotorLabels["hip"] != "左髋" {
		t.Fatalf("live event omitted complete motor batch: %+v", liveEvent)
	}
}

func TestTelemetryAcceptsGzipMotorBatch(t *testing.T) {
	store := newTestStore(t)
	server := NewServer(ServerConfig{AgentToken: "agent-token-long-enough-for-tests", AdminUser: "admin", JWTSecret: "jwt-secret-long-enough-for-tests-123456"}, store)
	sampleAt := time.Now().UTC()
	telemetry := model.Telemetry{
		SchemaVersion: model.SchemaVersion,
		Robot:         model.Robot{UUID: "52446a60-7483-4ba7-b8c7-b85f60b2a00f", Code: "M99"},
		CollectedAt:   sampleAt,
		Motors: &model.MotorSnapshot{SampleRateHz: 500, Samples: []model.MotorSample{
			{At: sampleAt, Motors: []model.MotorSampleState{{ID: "hip", TorqueNm: 1.5}}},
			{At: sampleAt.Add(2 * time.Millisecond), Motors: []model.MotorSampleState{{ID: "hip", TorqueNm: 2.5}}},
		}},
	}
	var body bytes.Buffer
	compressed := gzip.NewWriter(&body)
	if err := json.NewEncoder(compressed).Encode(telemetry); err != nil {
		t.Fatal(err)
	}
	if err := compressed.Close(); err != nil {
		t.Fatal(err)
	}
	request := httptest.NewRequest(http.MethodPost, "/api/v1/telemetry", bytes.NewReader(body.Bytes()))
	request.Header.Set("Authorization", "Bearer agent-token-long-enough-for-tests")
	request.Header.Set("Content-Encoding", "gzip")
	response := httptest.NewRecorder()
	server.ServeHTTP(response, request)
	if response.Code != http.StatusOK {
		t.Fatalf("gzip telemetry status=%d body=%s", response.Code, response.Body.String())
	}
	points, err := store.FastMotorHistory(telemetry.Robot.UUID, sampleAt.Add(-time.Second), sampleAt.Add(time.Second), 30_000)
	if err != nil || len(points) != 2 || points[1].Motors[0].TorqueNm != 2.5 {
		t.Fatalf("gzip batch was not stored intact: points=%+v err=%v", points, err)
	}
}

func TestInvalidAgentTokenIsRejected(t *testing.T) {
	store := newTestStore(t)
	server := NewServer(ServerConfig{
		AgentToken: "agent-token-long-enough-for-tests", AdminUser: "admin",
		JWTSecret: "jwt-secret-long-enough-for-tests-123456",
	}, store)
	request := httptest.NewRequest(http.MethodPost, "/api/v1/telemetry", strings.NewReader(`{}`))
	request.RemoteAddr = "203.0.113.10:4321"
	request.Header.Set("Authorization", "Bearer wrong-agent-token")
	response := httptest.NewRecorder()
	server.ServeHTTP(response, request)
	if response.Code != http.StatusUnauthorized {
		t.Fatalf("invalid agent token status=%d body=%s", response.Code, response.Body.String())
	}
}

func TestTokenFingerprintDoesNotExposeToken(t *testing.T) {
	token := "wrong-agent-token"
	fingerprint := tokenFingerprint(token)
	if fingerprint == "" || len(fingerprint) != 12 || strings.Contains(fingerprint, token) {
		t.Fatalf("unexpected token fingerprint %q", fingerprint)
	}
	if tokenFingerprint("") != "" {
		t.Fatal("empty token should not be fingerprinted")
	}
}

func TestPublicHistoryIsCrossOriginAndRedacted(t *testing.T) {
	store := newTestStore(t)
	uuid := "52446a60-7483-4ba7-b8c7-b85f60b2a00f"
	sampleAt := time.Now().UTC()
	if err := store.PutTelemetry(model.Telemetry{
		SchemaVersion: model.SchemaVersion, Robot: model.Robot{UUID: uuid, Code: "M99", Hostname: "private-host", OS: "linux", Arch: "arm64"},
		CollectedAt: time.Now().UTC(), System: &model.SystemMetrics{CPUUsagePercent: 25},
		Motors: &model.MotorSnapshot{
			Motors:  []model.MotorState{{ID: "hip", Label: "Hip", PositionRad: 1, VelocityRadPerSec: 2, TorqueNm: 3, CANInterface: "can0"}},
			Samples: []model.MotorSample{{At: sampleAt, Motors: []model.MotorSampleState{{ID: "hip", Label: "Hip", PositionRad: 1.1, VelocityRadPerSec: 2.1, TorqueNm: 3.1}}}},
		},
	}); err != nil {
		t.Fatal(err)
	}
	server := NewServer(ServerConfig{AgentToken: "agent-token-long-enough-for-tests", AdminUser: "admin", JWTSecret: "jwt-secret-long-enough-for-tests-123456"}, store)
	id := publicRobotID(server.config.JWTSecret, uuid)
	request := httptest.NewRequest(http.MethodGet, "/api/v1/robots/"+id+"/history?hours=1", nil)
	request.Header.Set("Origin", "https://customer.example")
	response := httptest.NewRecorder()
	server.ServeHTTP(response, request)
	if response.Code != http.StatusOK || response.Header().Get("Access-Control-Allow-Origin") != "*" {
		t.Fatalf("public history status=%d CORS=%q body=%s", response.Code, response.Header().Get("Access-Control-Allow-Origin"), response.Body.String())
	}
	if policy := response.Header().Get("Content-Security-Policy"); !strings.Contains(policy, "style-src 'self' https://registry.npmmirror.com") || !strings.Contains(policy, "font-src 'self' https://registry.npmmirror.com") {
		t.Fatalf("font CDN is not allowlisted in CSP: %q", policy)
	}
	body := response.Body.String()
	for _, sensitive := range []string{uuid, "private-host", "can0", "arm64"} {
		if strings.Contains(body, sensitive) {
			t.Fatalf("public history exposed %q: %s", sensitive, body)
		}
	}
	for _, expected := range []string{"hip", "position_rad", "velocity_rad_per_sec", "torque_nm"} {
		if !strings.Contains(body, expected) {
			t.Fatalf("public history omitted %q: %s", expected, body)
		}
	}
	fastRequest := httptest.NewRequest(http.MethodGet, "/api/v1/robots/"+id+"/history?scope=motors&seconds=10", nil)
	fastResponse := httptest.NewRecorder()
	server.ServeHTTP(fastResponse, fastRequest)
	if fastResponse.Code != http.StatusOK || !strings.Contains(fastResponse.Body.String(), "3.1") {
		t.Fatalf("public fast motor history status=%d body=%s", fastResponse.Code, fastResponse.Body.String())
	}
}

func TestHistorySampleLimitUsesSamplingRateAcrossTheRequestedRange(t *testing.T) {
	request := httptest.NewRequest(http.MethodGet, "/api/v1/robots/history?sample_rate_hz=20", nil)
	limit, ok := historySampleLimit(httptest.NewRecorder(), request, time.Minute, publicAllMotorSampleRateHz, fastMotorHistoryLimit)
	if !ok || limit != 1_200 {
		t.Fatalf("20Hz one-minute limit=%d ok=%v, want 1200", limit, ok)
	}

	request = httptest.NewRequest(http.MethodGet, "/api/v1/robots/history?sample_rate_hz=500", nil)
	limit, ok = historySampleLimit(httptest.NewRecorder(), request, time.Minute, publicSingleSampleRateHz, fastMotorHistoryLimit)
	if !ok || limit != 30_000 {
		t.Fatalf("500Hz one-minute limit=%d ok=%v, want 30000", limit, ok)
	}

	request = httptest.NewRequest(http.MethodGet, "/api/v1/robots/history", nil)
	limit, ok = historySampleLimit(httptest.NewRecorder(), request, 7*24*time.Hour, publicHostSampleRateHz, historyPointLimit)
	if !ok || limit != 10_080 {
		t.Fatalf("host weekly limit=%d ok=%v, want 10080", limit, ok)
	}
}

func TestBootstrapLoginForcesPasswordChange(t *testing.T) {
	store := newTestStore(t)
	server := NewServer(ServerConfig{AgentToken: "agent-token-long-enough-for-tests", AdminUser: "admin", JWTSecret: "jwt-secret-long-enough-for-tests-123456"}, store)
	login := httptest.NewRequest(http.MethodPost, "/api/v1/session", strings.NewReader(`{"username":"admin","password":"Baize@Admin1"}`))
	loginResponse := httptest.NewRecorder()
	server.ServeHTTP(loginResponse, login)
	if loginResponse.Code != http.StatusOK || !strings.Contains(loginResponse.Body.String(), `"password_change_required":true`) {
		t.Fatalf("bootstrap login status=%d body=%s", loginResponse.Code, loginResponse.Body.String())
	}
	cookie := loginResponse.Result().Cookies()[0]
	admin := httptest.NewRequest(http.MethodGet, "/api/v1/admin/robots", nil)
	admin.AddCookie(cookie)
	adminResponse := httptest.NewRecorder()
	server.ServeHTTP(adminResponse, admin)
	if adminResponse.Code != http.StatusForbidden {
		t.Fatalf("admin before password change status=%d", adminResponse.Code)
	}
	change := httptest.NewRequest(http.MethodPost, "/api/v1/admin/password", strings.NewReader(`{"current_password":"Baize@Admin1","new_password":"Better@Password123"}`))
	change.AddCookie(cookie)
	changeResponse := httptest.NewRecorder()
	server.ServeHTTP(changeResponse, change)
	if changeResponse.Code != http.StatusOK {
		t.Fatalf("password change status=%d body=%s", changeResponse.Code, changeResponse.Body.String())
	}
	renewed := changeResponse.Result().Cookies()[0]
	admin = httptest.NewRequest(http.MethodGet, "/api/v1/admin/robots", nil)
	admin.AddCookie(renewed)
	adminResponse = httptest.NewRecorder()
	server.ServeHTTP(adminResponse, admin)
	if adminResponse.Code != http.StatusOK {
		t.Fatalf("admin after password change status=%d body=%s", adminResponse.Code, adminResponse.Body.String())
	}
}
