package dashboard

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"echobot/shared/model"
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

func TestRobotDisplayIsPublicButManagementRequiresLogin(t *testing.T) {
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

	adminRequest := httptest.NewRequest(http.MethodPatch, "/api/v1/admin/robots/52446a60-7483-4ba7-b8c7-b85f60b2a00f/remark", nil)
	adminResponse := httptest.NewRecorder()
	server.ServeHTTP(adminResponse, adminRequest)
	if adminResponse.Code != http.StatusUnauthorized {
		t.Fatalf("anonymous management status = %d, want %d", adminResponse.Code, http.StatusUnauthorized)
	}
}
