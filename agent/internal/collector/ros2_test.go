package collector

import (
	"strings"
	"testing"
)

func TestROSCommandExportsEnvironment(t *testing.T) {
	command, err := rosCommand(nil, map[string]string{"ROS_LOCALHOST_ONLY": "1"}, "baize-ros2-subscriber --topic '/motor/state' --read-timeout '5s'")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(command, "export ROS_LOCALHOST_ONLY='1'") {
		t.Fatalf("ROS environment was not exported: %s", command)
	}
}

func TestLocalUDPProfileDoesNotReenableSharedMemory(t *testing.T) {
	t.Setenv("FASTRTPS_DEFAULT_PROFILES_FILE", "/opt/baize/agent/ros2-udp.xml")
	command, err := rosCommand(nil, map[string]string{"ROS_LOCALHOST_ONLY": "1"}, "subscriber")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(command, "export FASTRTPS_DEFAULT_PROFILES_FILE='/opt/baize/agent/ros2-udp.xml' && export ROS_LOCALHOST_ONLY='0' && exec subscriber") {
		t.Fatalf("Humble would restore SHM transport: %s", command)
	}
}
