package collector

import (
	"strings"
	"testing"
)

func TestROSCommandExportsEnvironment(t *testing.T) {
	command, err := rosCommand(nil, map[string]string{"ROS_LOCALHOST_ONLY": "1"}, "", "baize-ros2-subscriber --topic '/motor/state'")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(command, "export ROS_LOCALHOST_ONLY='1'") {
		t.Fatalf("ROS environment was not exported: %s", command)
	}
}

func TestROSCommandDropsRootToProfileUser(t *testing.T) {
	command, err := wrapROSCommand("exec baize-ros2-subscriber --topic '/motor/state'", "ubuntu", 0)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(command, "/usr/bin/setpriv --reset-env") || !strings.Contains(command, "--reuid='ubuntu'") {
		t.Fatalf("ROS user transition was not configured: %s", command)
	}
}
