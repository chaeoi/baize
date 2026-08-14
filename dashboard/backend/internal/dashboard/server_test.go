package dashboard

import (
	"testing"
	"time"
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
