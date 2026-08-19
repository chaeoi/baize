package robotmodel

import (
	"testing"
	"time"
)

func TestEmbeddedModels(t *testing.T) {
	if err := Validate(); err != nil {
		t.Fatal(err)
	}
	names, err := Names()
	if err != nil {
		t.Fatal(err)
	}
	if len(names) != 1 || names[0] != "2m_v0.1.2" {
		t.Fatalf("unexpected embedded models: %v", names)
	}
	profile, err := Select("2m_v0.1.2")
	if err != nil {
		t.Fatal(err)
	}
	if profile.Motor.Topic != "/motor/q2w_upper_motor_joint_state" || profile.Motor.FastSampleRateHz != 500 || profile.Motor.FastBatchInterval.Value() != 2*time.Second {
		t.Fatalf("unexpected motor profile: %+v", profile.Motor)
	}
	if len(profile.Motor.Joints) != 32 || profile.BMS.Topic != "/batcan/data" {
		t.Fatalf("unexpected model contents: joints=%d bms=%+v", len(profile.Motor.Joints), profile.BMS)
	}
}

func TestUnknownModel(t *testing.T) {
	if _, err := Select("not-a-model"); err == nil {
		t.Fatal("expected unknown robot model error")
	}
}
