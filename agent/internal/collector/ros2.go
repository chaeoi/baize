package collector

import (
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"time"
)

var rosEnvironmentNamePattern = regexp.MustCompile(`^[A-Z_][A-Z0-9_]{0,63}$`)

func rosSubscriberCommand(setup []string, environment map[string]string, topic, messageType string, readTimeout time.Duration) (string, error) {
	if topic == "" || messageType == "" {
		return "", fmt.Errorf("ROS2 subscriber topic and message type are required")
	}
	binary := os.Getenv("BAIZE_ROS2_SUBSCRIBER")
	if binary == "" {
		return "", fmt.Errorf("ROS2 subscriber is not prepared")
	}
	arguments := shellQuote(binary) +
		" --topic " + shellQuote(topic) + " --message-type " + shellQuote(messageType) +
		" --read-timeout " + shellQuote(readTimeout.String())
	return rosCommand(setup, environment, arguments)
}

func rosCommand(setupFiles []string, environment map[string]string, finalCommand string) (string, error) {
	parts := make([]string, 0, len(setupFiles)+len(environment)+1)
	for _, path := range setupFiles {
		if !strings.HasPrefix(path, "/") || strings.ContainsAny(path, "\x00\n\r") {
			return "", fmt.Errorf("invalid ROS setup path %q", path)
		}
		parts = append(parts, "source "+shellQuote(path))
	}
	names := make([]string, 0, len(environment))
	for name := range environment {
		names = append(names, name)
	}
	sort.Strings(names)
	for _, name := range names {
		value := environment[name]
		if !rosEnvironmentNamePattern.MatchString(name) || strings.ContainsAny(value, "\x00\n\r") {
			return "", fmt.Errorf("invalid ROS environment variable %q", name)
		}
		parts = append(parts, "export "+name+"="+shellQuote(value))
	}
	if profile := os.Getenv("FASTRTPS_DEFAULT_PROFILES_FILE"); filepath.Base(profile) == "ros2-udp.xml" {
		// Humble adds SHM back when ROS_LOCALHOST_ONLY=1, even with an XML
		// transport override. This profile restricts UDP to loopback itself.
		parts = append(parts, "export FASTRTPS_DEFAULT_PROFILES_FILE="+shellQuote(profile), "export ROS_LOCALHOST_ONLY='0'")
	}
	parts = append(parts, "exec "+finalCommand)
	command := strings.Join(parts, " && ")
	return command, nil
}

func shellQuote(value string) string {
	return "'" + strings.ReplaceAll(value, "'", "'\"'\"'") + "'"
}
