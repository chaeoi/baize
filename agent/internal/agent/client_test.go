package agent

import (
	"bytes"
	"compress/gzip"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"baize/shared/model"
)

func TestReportCompressesCompleteMotorBatch(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		if request.Header.Get("Content-Encoding") != "gzip" {
			t.Errorf("report was not compressed: %q", request.Header.Get("Content-Encoding"))
			http.Error(writer, "not compressed", http.StatusBadRequest)
			return
		}
		compressed, err := io.ReadAll(request.Body)
		if err != nil {
			t.Error(err)
			http.Error(writer, err.Error(), http.StatusBadRequest)
			return
		}
		reader, err := gzip.NewReader(bytes.NewReader(compressed))
		if err != nil {
			t.Error(err)
			http.Error(writer, err.Error(), http.StatusBadRequest)
			return
		}
		var telemetry model.Telemetry
		if err := json.NewDecoder(reader).Decode(&telemetry); err != nil {
			t.Error(err)
			http.Error(writer, err.Error(), http.StatusBadRequest)
			return
		}
		if err := reader.Close(); err != nil {
			t.Error(err)
			http.Error(writer, err.Error(), http.StatusBadRequest)
			return
		}
		if telemetry.Motors == nil || len(telemetry.Motors.Samples) != 1000 || len(telemetry.Motors.Samples[999].Motors) != 32 || telemetry.Motors.Samples[999].Motors[31].TorqueNm != 1030.5 {
			t.Errorf("complete motor batch was not preserved: %+v", telemetry.Motors)
			http.Error(writer, "incomplete batch", http.StatusBadRequest)
			return
		}
		writer.WriteHeader(http.StatusOK)
	}))
	defer server.Close()

	samples := make([]model.MotorSample, 1000)
	for index := range samples {
		motors := make([]model.MotorSampleState, 32)
		for motorIndex := range motors {
			motors[motorIndex] = model.MotorSampleState{ID: "motor", TorqueNm: float64(index+motorIndex) + 0.5}
		}
		samples[index] = model.MotorSample{At: time.Unix(0, int64(index)*2_000_000).UTC(), Motors: motors}
	}
	client := NewClient(server.URL, "agent-token", server.Client())
	err := client.Report(t.Context(), model.Telemetry{
		SchemaVersion: model.SchemaVersion,
		Robot:         model.Robot{UUID: "52446a60-7483-4ba7-b8c7-b85f60b2a00f", Code: "TEST"},
		Motors:        &model.MotorSnapshot{SampleRateHz: 500, Samples: samples},
	})
	if err != nil {
		t.Fatal(err)
	}
}
