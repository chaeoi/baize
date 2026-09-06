package dashboard

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"baize/shared/model"
	"github.com/gorilla/websocket"
)

func TestPublicSubscriptionsIsolateSamples(t *testing.T) {
	store := newTestStore(t)
	s := NewServer(ServerConfig{AgentToken: "test-token", JWTSecret: "test-secret"}, store)
	httpServer := httptest.NewServer(s)
	defer httpServer.Close()
	uuid := "52446a60-7483-4ba7-b8c7-b85f60b2a00f"
	id := publicRobotID(s.config.JWTSecret, uuid)
	connections := make([]*websocket.Conn, 0, 3)
	for _, query := range []string{"", "?include_samples=1&robot_id=" + id + "&motor_ids=knee", "?include_samples=1&robot_id=" + id} {
		conn, _, err := websocket.DefaultDialer.Dial("ws"+strings.TrimPrefix(httpServer.URL, "http")+"/api/v1/ws/robots"+query, nil)
		if err != nil {
			t.Fatal(err)
		}
		defer conn.Close()
		conn.SetReadDeadline(time.Now().Add(3 * time.Second))
		if _, _, err := conn.ReadMessage(); err != nil {
			t.Fatal(err)
		}
		connections = append(connections, conn)
	}
	telemetry := model.Telemetry{SchemaVersion: model.SchemaVersion, Robot: model.Robot{UUID: uuid, Code: "M99"}, CollectedAt: time.Now(), Motors: &model.MotorSnapshot{SampleRateHz: 500, Motors: []model.MotorState{{ID: "hip"}, {ID: "knee"}}, Samples: []model.MotorSample{{At: time.Now(), Motors: []model.MotorSampleState{{ID: "hip", TorqueNm: 1}, {ID: "knee", TorqueNm: 2}}}}}}
	data, _ := json.Marshal(telemetry)
	request := httptest.NewRequest(http.MethodPost, "/api/v1/telemetry", bytes.NewReader(data))
	request.Header.Set("Authorization", "Bearer test-token")
	response := httptest.NewRecorder()
	s.ServeHTTP(response, request)
	if response.Code != 200 {
		t.Fatal(response.Body.String())
	}
	for index, conn := range connections {
		var event robotStreamEvent
		if err := conn.ReadJSON(&event); err != nil {
			t.Fatal(err)
		}
		if event.Robot == nil || len(event.Robot.MotorLabels) != 2 {
			t.Fatalf("missing motor catalogue: %+v", event)
		}
		if index == 0 && len(event.Robot.MotorSamples) != 0 {
			t.Fatal("unsubscribed client received samples")
		}
		if index == 1 && (len(event.Robot.MotorSamples[0].Motors) != 1 || event.Robot.MotorSamples[0].Motors[0].ID != "knee") {
			t.Fatal("filter not applied")
		}
		if index == 2 && len(event.Robot.MotorSamples[0].Motors) != 2 {
			t.Fatal("another subscriber corrupted full samples")
		}
	}
	s.broadcastRobot(uuid)
	record, _ := store.Robot(uuid)
	if len(record.Telemetry.Motors.Samples[0].Motors) != 2 {
		t.Fatal("filter mutated stored telemetry")
	}
}

func TestAdminStreamClosesWhenSessionRevoked(t *testing.T) {
	for _, changePassword := range []bool{false, true} {
		t.Run(map[bool]string{false: "logout", true: "password"}[changePassword], func(t *testing.T) {
			store := newTestStore(t)
			if err := store.ChangeAdminPassword("admin", "Baize@Admin1", "Current@Password123"); err != nil {
				t.Fatal(err)
			}
			s := NewServer(ServerConfig{AdminUser: "admin", JWTSecret: "secret"}, store)
			login := httptest.NewRecorder()
			s.ServeHTTP(login, httptest.NewRequest(http.MethodPost, "/api/v1/session", strings.NewReader(`{"username":"admin","password":"Current@Password123"}`)))
			cookie := login.Result().Cookies()[0]
			httpServer := httptest.NewServer(s)
			defer httpServer.Close()
			conn, _, err := websocket.DefaultDialer.Dial("ws"+strings.TrimPrefix(httpServer.URL, "http")+"/api/v1/admin/ws/robots", http.Header{"Cookie": {cookie.String()}})
			if err != nil {
				t.Fatal(err)
			}
			defer conn.Close()
			conn.SetReadDeadline(time.Now().Add(3 * time.Second))
			if _, _, err := conn.ReadMessage(); err != nil {
				t.Fatal(err)
			}
			request := httptest.NewRequest(http.MethodDelete, "/api/v1/session", nil)
			if changePassword {
				request = httptest.NewRequest(http.MethodPost, "/api/v1/admin/password", strings.NewReader(`{"current_password":"Current@Password123","new_password":"Changed@Password123"}`))
			}
			request.AddCookie(cookie)
			response := httptest.NewRecorder()
			s.ServeHTTP(response, request)
			if response.Code >= 300 {
				t.Fatal(response.Body.String())
			}
			_, _, err = conn.ReadMessage()
			if err == nil {
				t.Fatal("revoked stream remains open")
			}
			if timeout, ok := err.(interface{ Timeout() bool }); ok && timeout.Timeout() {
				t.Fatal("stream timed out instead of closing")
			}
		})
	}
}

func TestAutomaticUpdatesIgnoreLegacyPinsAndNeverDowngrade(t *testing.T) {
	store := newTestStore(t)
	if _, err := store.control.Exec(`INSERT INTO robot_settings(uuid,desired_version) VALUES('old-robot','20260901')`); err != nil {
		t.Fatal(err)
	}
	for _, version := range []string{"20260901", "20260905"} {
		if err := store.AddRelease(Release{ID: version, Version: version, OS: "linux", Arch: "amd64", UploadedAt: time.Now()}); err != nil {
			t.Fatal(err)
		}
	}
	release, ok := store.FindUpdate("20260901", "linux", "amd64")
	if !ok || release.Version != "20260905" {
		t.Fatalf("unexpected update: %+v", release)
	}
	if _, ok := store.FindUpdate("20260905", "linux", "amd64"); ok {
		t.Fatal("latest version received another update")
	}
	if _, ok := store.FindUpdate("20260906", "linux", "amd64"); ok {
		t.Fatal("downgrade offered")
	}
	if _, ok := store.FindUpdate("20260901", "linux", "arm64"); ok {
		t.Fatal("wrong architecture offered")
	}
}
