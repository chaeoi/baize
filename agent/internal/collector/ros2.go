package collector

import (
	"fmt"
	"os"
	"regexp"
	"sort"
	"strings"
)

var rosEnvironmentNamePattern = regexp.MustCompile(`^[A-Z_][A-Z0-9_]{0,63}$`)
var rosUserNamePattern = regexp.MustCompile(`^[a-z_][a-z0-9_-]{0,31}$`)

const defaultROS2SubscriberBinary = "/opt/baize/agent/baize-ros2-subscriber"

func rosSubscriberCommand(setup []string, environment map[string]string, user, topic, messageType string) (string, error) {
	if topic == "" || messageType == "" {
		return "", fmt.Errorf("ROS2 subscriber topic and message type are required")
	}
	binary := os.Getenv("BAIZE_ROS2_SUBSCRIBER")
	if binary == "" {
		binary = defaultROS2SubscriberBinary
	}
	arguments := shellQuote(binary) +
		" --topic " + shellQuote(topic) + " --message-type " + shellQuote(messageType)
	return rosCommand(setup, environment, user, arguments)
}

func rosCommand(setupFiles []string, environment map[string]string, user, finalCommand string) (string, error) {
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
	parts = append(parts, "exec "+finalCommand)
	command := strings.Join(parts, " && ")
	return wrapROSCommand(command, user, os.Geteuid())
}

func wrapROSCommand(command, user string, euid int) (string, error) {
	if user == "" || euid != 0 {
		return command, nil
	}
	if !rosUserNamePattern.MatchString(user) {
		return "", fmt.Errorf("invalid ROS user %q", user)
	}
	// setpriv execs the lowered-privilege shell in place. This keeps the ROS
	// client in CommandContext's process tree, so a read timeout kills it too.
	return "exec /usr/bin/setpriv --reset-env --reuid=" + shellQuote(user) + " --regid=" + shellQuote(user) + " --init-groups -- /bin/bash -lc " + shellQuote(command), nil
}

func shellQuote(value string) string {
	return "'" + strings.ReplaceAll(value, "'", "'\"'\"'") + "'"
}
