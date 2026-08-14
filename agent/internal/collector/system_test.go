package collector

import (
	"os"
	"path/filepath"
	"testing"
)

func TestReadCPUTimes(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "stat")
	if err := os.WriteFile(path, []byte("cpu  10 2 3 80 5 0 0 0 0 0\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	times, err := readCPUTimes(path)
	if err != nil {
		t.Fatal(err)
	}
	if times.total != 100 || times.idle != 85 {
		t.Fatalf("unexpected cpu times: %+v", times)
	}
}

func TestParseNVIDIAFloatNA(t *testing.T) {
	value, err := parseNVIDIAFloat(" N/A ")
	if err != nil || value != 0 {
		t.Fatalf("got %v, %v", value, err)
	}
}
