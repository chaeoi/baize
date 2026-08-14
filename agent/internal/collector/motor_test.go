package collector

import "testing"

func TestParseJointState(t *testing.T) {
	data := []byte(`header:
  stamp:
    sec: 1
    nanosec: 2
name:
- motor_id_01
- motor_id_02
position:
- 1.25
- -2.5
velocity:
- 3.0
- 4.0
effort:
- 5.0
- 6.0
---
`)
	motors, err := parseJointState(data, map[string]string{"motor_id_01": "waist_1"}, nil)
	if err != nil {
		t.Fatal(err)
	}
	if len(motors) != 2 || motors[0].Label != "waist_1" || motors[1].TorqueNm != 6 {
		t.Fatalf("unexpected motors: %+v", motors)
	}
}

func TestParseJointStateRejectsMismatchedArrays(t *testing.T) {
	_, err := parseJointState([]byte("name: [a, b]\nposition: [1]\nvelocity: [1, 2]\neffort: [1, 2]\n"), nil, nil)
	if err == nil {
		t.Fatal("expected array mismatch error")
	}
}
