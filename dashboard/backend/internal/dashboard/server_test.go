package dashboard

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"baize/shared/model"
	"github.com/gorilla/websocket"
)

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
}

func TestPublicHistoryIsCrossOriginAndRedacted(t *testing.T) {
	store := newTestStore(t)
	uuid := "52446a60-7483-4ba7-b8c7-b85f60b2a00f"
	if err := store.PutTelemetry(model.Telemetry{
		SchemaVersion: model.SchemaVersion, Robot: model.Robot{UUID: uuid, Code: "M99", Hostname: "private-host", OS: "linux", Arch: "arm64"},
		CollectedAt: time.Now().UTC(), System: &model.SystemMetrics{CPUUsagePercent: 25},
		Motors: &model.MotorSnapshot{Motors: []model.MotorState{{ID: "hip", Label: "Hip", PositionRad: 1, VelocityRadPerSec: 2, TorqueNm: 3, CANInterface: "can0"}}},
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
