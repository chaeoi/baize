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

func TestPublicRobotDataIsRedactedButManagementRequiresLogin(t *testing.T) {
	store, err := NewStore(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	store.PutTelemetry(model.Telemetry{
		SchemaVersion: model.SchemaVersion,
		Robot: model.Robot{
			UUID: "52446a60-7483-4ba7-b8c7-b85f60b2a00f", Code: "M99", Model: "2m_v0.1.2",
			Hostname: "m99", OS: "linux", Arch: "arm64",
		},
		AgentVersion: "20260814", CollectedAt: time.Now().UTC(),
	})
	server := NewServer(ServerConfig{
		AgentToken: "agent-token-long-enough-for-tests", AdminUser: "admin", AdminPassword: "password",
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
	store, err := NewStore(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	store.PutTelemetry(model.Telemetry{
		SchemaVersion: model.SchemaVersion,
		Robot: model.Robot{
			UUID: "52446a60-7483-4ba7-b8c7-b85f60b2a00f", Code: "M99", Model: "2m_v0.1.2",
			Hostname: "m99", OS: "linux", Arch: "arm64",
		},
		AgentVersion: "20260814", CollectedAt: time.Now().UTC(),
	})
	server := NewServer(ServerConfig{
		AgentToken: "agent-token-long-enough-for-tests", AdminUser: "admin", AdminPassword: "password",
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
