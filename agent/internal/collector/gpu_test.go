package collector

import "testing"

func TestParseNVIDIAOutputDetectsJetsonNAValues(t *testing.T) {
	metrics, available, err := parseNVIDIAOutput([]byte("0, Orin (nvgpu), [N/A], [N/A], [N/A], [N/A], [N/A]\n"))
	if err != nil {
		t.Fatal(err)
	}
	if available || len(metrics) != 1 || metrics[0].Name != "Orin (nvgpu)" {
		t.Fatalf("unexpected NVIDIA metrics: available=%v metrics=%+v", available, metrics)
	}
}

func TestParseNVIDIAOutputKeepsZeroAsAvailableValue(t *testing.T) {
	metrics, available, err := parseNVIDIAOutput([]byte("0, RTX, 0, 8192, 1024, 45, 20\n"))
	if err != nil {
		t.Fatal(err)
	}
	if !available || metrics[0].MemoryTotalBytes != 8192*1024*1024 {
		t.Fatalf("unexpected NVIDIA metrics: available=%v metrics=%+v", available, metrics)
	}
}
