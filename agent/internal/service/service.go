package service

import (
	"bytes"
	"crypto/rand"
	_ "embed"
	"errors"
	"flag"
	"fmt"
	"io"
	"net/url"
	"os"
	"os/exec"
	"os/user"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"unicode"

	"baize/agent/internal/config"
)

const (
	serviceName   = "baize-agent"
	serviceUser   = "ubuntu"
	logDir        = "/var/log/baize-agent"
	privateLogDir = "/var/log/private/baize-agent"
)

// The release workflow replaces this development stub with the C++ binary for
// the target architecture before compiling the static Go Agent.
//
//go:embed baize-ros2-subscriber
var embeddedROS2Subscriber []byte

var (
	installDir              = "/opt/baize/agent"
	installedBinary         = filepath.Join(installDir, "baize-agent")
	installedRuntime        = filepath.Join(installDir, "bin", "baize-agent")
	installedROS2Subscriber = filepath.Join(installDir, "baize-ros2-subscriber")
	installedConfig         = filepath.Join(installDir, "config.yml")
	unitPath                = "/etc/systemd/system/baize-agent.service"
)

var (
	serviceUUIDPattern = regexp.MustCompile(`^[0-9a-fA-F]{8}-[0-9a-fA-F]{4}-[1-5][0-9a-fA-F]{3}-[89aAbB][0-9a-fA-F]{3}-[0-9a-fA-F]{12}$`)
	serviceCodePattern = regexp.MustCompile(`^[A-Za-z0-9._-]{1,64}$`)
)

type installOptions struct {
	dashboardURL string
	token        string
	robotCode    string
	robotModel   string
	uuid         string
	forceConfig  bool
}

func Execute(arguments []string, executablePath string) error {
	if len(arguments) == 0 {
		return errors.New(serviceUsage)
	}
	if arguments[0] == "help" || arguments[0] == "--help" || arguments[0] == "-h" {
		fmt.Print(serviceUsage)
		return nil
	}
	switch arguments[0] {
	case "install":
		if len(arguments) == 2 && (arguments[1] == "help" || arguments[1] == "--help" || arguments[1] == "-h") {
			fmt.Print(installUsage)
			return nil
		}
		options, err := parseInstallOptions(arguments[1:])
		if err != nil {
			return err
		}
		return install(options, executablePath)
	case "uninstall":
		if len(arguments) != 1 {
			return errors.New("service uninstall takes no options")
		}
		return uninstall()
	case "status":
		if len(arguments) != 1 {
			return errors.New("service status takes no options")
		}
		return runCommand("systemctl", "status", serviceName+".service")
	default:
		return fmt.Errorf("unknown service command: %s", arguments[0])
	}
}

// PrepareROS2Subscriber materializes the embedded architecture-matched helper
// in systemd's writable StateDirectory. This also updates the helper whenever
// the Agent replaces itself through the dashboard updater.
func PrepareROS2Subscriber() (string, error) {
	if override := os.Getenv("BAIZE_ROS2_SUBSCRIBER"); override != "" {
		return override, nil
	}
	directory := os.Getenv("STATE_DIRECTORY")
	if directory == "" {
		executable, err := os.Executable()
		if err != nil {
			return "", err
		}
		candidate := filepath.Join(filepath.Dir(executable), "baize-ros2-subscriber")
		if info, err := os.Stat(candidate); err == nil && info.Mode().IsRegular() {
			return candidate, nil
		}
		return "", fmt.Errorf("ROS2 subscriber is not installed beside %s", executable)
	}
	if !filepath.IsAbs(directory) || strings.ContainsAny(directory, "\x00\n\r") {
		return "", errors.New("systemd STATE_DIRECTORY is invalid")
	}
	if err := os.MkdirAll(directory, 0o750); err != nil {
		return "", err
	}
	destination := filepath.Join(directory, "baize-ros2-subscriber")
	if current, err := os.ReadFile(destination); err == nil && bytes.Equal(current, embeddedROS2Subscriber) {
		return destination, nil
	}
	temporary, err := os.CreateTemp(directory, ".baize-ros2-subscriber-*")
	if err != nil {
		return "", err
	}
	temporaryPath := temporary.Name()
	defer os.Remove(temporaryPath)
	if _, err := temporary.Write(embeddedROS2Subscriber); err != nil {
		_ = temporary.Close()
		return "", err
	}
	if err := temporary.Sync(); err != nil {
		_ = temporary.Close()
		return "", err
	}
	if err := temporary.Chmod(0o755); err != nil {
		_ = temporary.Close()
		return "", err
	}
	if err := temporary.Close(); err != nil {
		return "", err
	}
	if err := os.Rename(temporaryPath, destination); err != nil {
		return "", err
	}
	return destination, nil
}

func parseInstallOptions(arguments []string) (installOptions, error) {
	var options installOptions
	flags := flag.NewFlagSet("baize-agent service install", flag.ContinueOnError)
	flags.SetOutput(io.Discard)
	flags.StringVar(&options.dashboardURL, "dashboard-url", "", "Dashboard base URL")
	flags.StringVar(&options.token, "token", "", "Dashboard Agent token")
	flags.StringVar(&options.robotCode, "robot-code", "", "public robot code")
	flags.StringVar(&options.robotModel, "robot-model", "", "built-in robot model")
	flags.StringVar(&options.uuid, "uuid", "", "fixed robot UUID")
	flags.BoolVar(&options.forceConfig, "force-config", false, "regenerate config from the default template")
	if err := flags.Parse(arguments); err != nil {
		return options, err
	}
	if flags.NArg() != 0 {
		return options, fmt.Errorf("unexpected service install argument: %s", flags.Arg(0))
	}
	return options, nil
}

func install(options installOptions, executablePath string) error {
	if err := requireRoot(); err != nil {
		return err
	}
	serviceAccount, err := user.Lookup(serviceUser)
	if err != nil {
		return fmt.Errorf("lookup service user %q: %w", serviceUser, err)
	}
	groupID, err := strconv.Atoi(serviceAccount.Gid)
	if err != nil {
		return fmt.Errorf("parse service group id: %w", err)
	}
	userID, err := strconv.Atoi(serviceAccount.Uid)
	if err != nil {
		return fmt.Errorf("parse service user id: %w", err)
	}
	plan, err := planConfig(options)
	if err != nil {
		return err
	}
	if err := os.MkdirAll(installDir, 0o750); err != nil {
		return err
	}
	// The service runs as the ubuntu account. Ensure the parent is traversable
	// on fresh systems where /opt/baize was created by root with mode 0750.
	parentDir := filepath.Dir(installDir)
	if err := os.Chown(parentDir, 0, groupID); err != nil {
		return err
	}
	if err := os.Chmod(parentDir, 0o750); err != nil {
		return err
	}
	if err := os.Chown(installDir, 0, groupID); err != nil {
		return err
	}
	if err := os.Chmod(installDir, 0o750); err != nil {
		return err
	}
	if err := installBinary(executablePath); err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(installedRuntime), 0o750); err != nil {
		return err
	}
	if err := os.Chown(filepath.Dir(installedRuntime), userID, groupID); err != nil {
		return err
	}
	content, err := os.ReadFile(installedBinary)
	if err != nil {
		return err
	}
	if err := writeFileAtomic(installedRuntime, content, 0o755, userID, groupID); err != nil {
		return err
	}
	if err := writeFileAtomic(installedROS2Subscriber, embeddedROS2Subscriber, 0o755, 0, 0); err != nil {
		return fmt.Errorf("install ROS2 subscriber: %w", err)
	}
	if plan.replace {
		if err := writeFileAtomic(installedConfig, plan.content, 0o640, 0, groupID); err != nil {
			return err
		}
	} else {
		if err := os.Chown(installedConfig, 0, groupID); err != nil {
			return fmt.Errorf("set config ownership: %w", err)
		}
		if err := os.Chmod(installedConfig, 0o640); err != nil {
			return fmt.Errorf("set config permissions: %w", err)
		}
	}

	if err := writeFileAtomic(unitPath, []byte(serviceUnit()), 0o644, 0, 0); err != nil {
		return err
	}
	if err := runCommand("systemctl", "daemon-reload"); err != nil {
		return err
	}
	if !plan.valid {
		_ = runCommand("systemctl", "disable", "--now", serviceName+".service")
		fmt.Printf("installed %s.service; complete %s, then run: %s service install\n", serviceName, installedConfig, installedBinary)
		return nil
	}
	if err := runCommand("systemctl", "enable", serviceName+".service"); err != nil {
		return err
	}
	if err := runCommand("systemctl", "restart", serviceName+".service"); err != nil {
		return err
	}
	fmt.Printf("installed and started %s.service with %s\n", serviceName, installedConfig)
	return nil
}

func uninstall() error {
	if err := requireRoot(); err != nil {
		return err
	}
	_ = runCommand("systemctl", "disable", "--now", serviceName+".service")
	if err := os.Remove(unitPath); err != nil && !errors.Is(err, os.ErrNotExist) {
		return err
	}
	if err := runCommand("systemctl", "daemon-reload"); err != nil {
		return err
	}
	_ = runCommand("systemctl", "reset-failed", serviceName+".service")
	for _, path := range []string{logDir, privateLogDir} {
		if err := os.RemoveAll(path); err != nil {
			return fmt.Errorf("remove logs %s: %w", path, err)
		}
	}
	fmt.Printf("uninstalled %s.service; preserved %s and removed logs\n", serviceName, installDir)
	return nil
}

type configPlan struct {
	content []byte
	replace bool
	valid   bool
}

func planConfig(options installOptions) (configPlan, error) {
	hasValues := options.hasValues()
	if hasValues {
		if err := options.validateProvided(); err != nil {
			return configPlan{}, err
		}
		base := config.Default()
		if !options.forceConfig {
			if existing, err := config.Load(installedConfig); err == nil {
				base = existing
			}
		}
		if base.Agent.UUID == "" {
			uuid, err := newUUID()
			if err != nil {
				return configPlan{}, err
			}
			base.Agent.UUID = uuid
		}
		if err := applyOptions(&base.Agent, options); err != nil {
			return configPlan{}, err
		}
		content, valid, err := renderInstallConfig(base)
		if err != nil {
			return configPlan{}, err
		}
		return configPlan{content: content, replace: true, valid: valid}, nil
	}
	if options.forceConfig || !validConfig(installedConfig) {
		uuid, err := newUUID()
		if err != nil {
			return configPlan{}, err
		}
		return configPlan{content: []byte(defaultConfig(uuid)), replace: true, valid: false}, nil
	}
	return configPlan{valid: true}, nil
}

func (options installOptions) hasValues() bool {
	return options.dashboardURL != "" || options.token != "" || options.robotCode != "" || options.robotModel != "" || options.uuid != ""
}

func (options *installOptions) validateProvided() error {
	for _, option := range []struct {
		name  string
		value string
	}{
		{"--dashboard-url", options.dashboardURL},
		{"--token", options.token},
		{"--robot-code", options.robotCode},
		{"--robot-model", options.robotModel},
		{"--uuid", options.uuid},
	} {
		if containsControl(option.value) {
			return fmt.Errorf("%s contains a control character", option.name)
		}
	}
	if options.uuid != "" && !serviceUUIDPattern.MatchString(options.uuid) {
		return errors.New("--uuid must be a canonical UUID")
	}
	if options.robotCode != "" && !serviceCodePattern.MatchString(options.robotCode) {
		return errors.New("--robot-code may contain only letters, numbers, dot, underscore and dash")
	}
	if options.token != "" && len(options.token) < 12 {
		return errors.New("--token must contain at least 12 characters")
	}
	if options.dashboardURL != "" {
		parsed, err := url.Parse(options.dashboardURL)
		if err != nil || parsed.Host == "" || (parsed.Scheme != "http" && parsed.Scheme != "https") {
			return errors.New("--dashboard-url must be an http or https URL")
		}
	}
	if options.robotModel != "" {
		supported := false
		for _, model := range config.SupportedRobotModels() {
			if model == options.robotModel {
				supported = true
				break
			}
		}
		if !supported {
			return fmt.Errorf("robot model %q is not supported by this Agent release", options.robotModel)
		}
	}
	return nil
}

func applyOptions(agent *config.AgentConfig, options installOptions) error {
	if options.uuid != "" {
		agent.UUID = options.uuid
	}
	if options.robotCode != "" {
		agent.RobotCode = options.robotCode
	}
	if options.robotModel != "" {
		agent.RobotModel = options.robotModel
	}
	if options.dashboardURL != "" {
		agent.DashboardURL = options.dashboardURL
	}
	if options.token != "" {
		agent.Token = options.token
	}
	return nil
}

func configuredContent(options installOptions) ([]byte, error) {
	if err := options.validateProvided(); err != nil {
		return nil, err
	}
	cfg := config.Default()
	agent := &cfg.Agent
	if options.uuid == "" {
		uuid, err := newUUID()
		if err != nil {
			return nil, err
		}
		agent.UUID = uuid
	}
	if err := applyOptions(agent, options); err != nil {
		return nil, err
	}
	content, valid, err := renderInstallConfig(cfg)
	if err != nil {
		return nil, err
	}
	if !valid {
		return nil, errors.New("service install configuration is incomplete")
	}
	return content, nil
}

func renderInstallConfig(cfg config.Config) ([]byte, bool, error) {
	agent := cfg.Agent
	built, err := config.Build(agent)
	if err == nil {
		cfg.Agent = built.Agent
		cfg.Motor, cfg.BMS = built.Motor, built.BMS
		if err := cfg.Validate(); err != nil {
			return nil, false, err
		}
		content, err := config.Marshal(cfg)
		return content, true, err
	}
	if agent.UUID == "" || agent.RobotCode == "" || agent.RobotModel == "" || agent.DashboardURL == "" || agent.Token == "" {
		content, err := config.Marshal(cfg)
		return content, false, err
	}
	return nil, false, err
}

func validConfig(path string) bool {
	info, err := os.Lstat(path)
	if err != nil || !info.Mode().IsRegular() {
		return false
	}
	_, err = config.Load(path)
	return err == nil
}

func installBinary(source string) error {
	info, err := os.Stat(source)
	if err != nil {
		return fmt.Errorf("read executable: %w", err)
	}
	if !info.Mode().IsRegular() {
		return errors.New("service install executable is not a regular file")
	}
	if destination, err := os.Stat(installedBinary); err == nil && os.SameFile(info, destination) {
		if err := os.Chown(installedBinary, 0, 0); err != nil {
			return err
		}
		return os.Chmod(installedBinary, 0o755)
	}
	input, err := os.Open(source)
	if err != nil {
		return err
	}
	defer input.Close()
	temporary, err := os.CreateTemp(installDir, ".baize-agent-*")
	if err != nil {
		return err
	}
	temporaryPath := temporary.Name()
	defer os.Remove(temporaryPath)
	if _, err := io.Copy(temporary, input); err != nil {
		_ = temporary.Close()
		return err
	}
	if err := temporary.Sync(); err != nil {
		_ = temporary.Close()
		return err
	}
	if err := temporary.Chmod(0o755); err != nil {
		_ = temporary.Close()
		return err
	}
	if err := temporary.Chown(0, 0); err != nil {
		_ = temporary.Close()
		return err
	}
	if err := temporary.Close(); err != nil {
		return err
	}
	return os.Rename(temporaryPath, installedBinary)
}

func writeFileAtomic(path string, content []byte, mode os.FileMode, owner, group int) error {
	temporary, err := os.CreateTemp(filepath.Dir(path), ".baize-agent-*")
	if err != nil {
		return err
	}
	temporaryPath := temporary.Name()
	defer os.Remove(temporaryPath)
	if _, err := temporary.Write(content); err != nil {
		_ = temporary.Close()
		return err
	}
	if err := temporary.Sync(); err != nil {
		_ = temporary.Close()
		return err
	}
	if err := temporary.Chmod(mode); err != nil {
		_ = temporary.Close()
		return err
	}
	if err := temporary.Chown(owner, group); err != nil {
		_ = temporary.Close()
		return err
	}
	if err := temporary.Close(); err != nil {
		return err
	}
	return os.Rename(temporaryPath, path)
}

func runCommand(name string, arguments ...string) error {
	command := exec.Command(name, arguments...)
	command.Stdout = os.Stdout
	command.Stderr = os.Stderr
	return command.Run()
}

func requireRoot() error {
	if os.Geteuid() != 0 {
		return errors.New("service changes require root; run with sudo")
	}
	return nil
}

func newUUID() (string, error) {
	var value [16]byte
	if _, err := rand.Read(value[:]); err != nil {
		return "", err
	}
	value[6] = (value[6] & 0x0f) | 0x40
	value[8] = (value[8] & 0x3f) | 0x80
	return fmt.Sprintf("%08x-%04x-%04x-%04x-%012x", value[0:4], value[4:6], value[6:8], value[8:10], value[10:16]), nil
}

func defaultConfig(uuid string) string {
	var lines strings.Builder
	lines.WriteString("# Fill every agent field, then run: /opt/baize/agent/baize-agent service install\n")
	lines.WriteString("# Supported robot models:\n")
	for _, model := range config.SupportedRobotModels() {
		lines.WriteString("#   ")
		lines.WriteString(model)
		lines.WriteByte('\n')
	}
	lines.WriteString("model: \"\"\n\nagent:\n  uuid: ")
	lines.WriteString(yamlString(uuid))
	lines.WriteString("\n  robot_code: \"\"\n  dashboard_url: \"\"\n  token: \"\"\n  report_interval: \"2s\"\n  http_timeout: \"10s\"\n\nsystem:\n  enabled: true\n  disk_paths: [\"/\"]\n\ngpu:\n  enabled: true\n  command: \"nvidia-smi\"\n  timeout: \"3s\"\n\nupdate:\n  enabled: true\n  check_interval: \"1m\"\n")
	return lines.String()
}

func yamlString(value string) string {
	return strconv.Quote(value)
}

func containsControl(value string) bool {
	return strings.IndexFunc(value, unicode.IsControl) >= 0
}

func serviceUnit() string {
	return "[Unit]\nDescription=Baize robot monitoring agent\nAfter=network-online.target\nWants=network-online.target\n\n[Service]\nType=simple\nUser=ubuntu\nGroup=ubuntu\nWorkingDirectory=/opt/baize/agent\nExecStart=/opt/baize/agent/baize-agent supervise --config /opt/baize/agent/config.yml\nRestart=always\nRestartSec=3\nEnvironment=ROS_LOG_DIR=/var/log/baize-agent/ros\nLogsDirectory=baize-agent\nLogsDirectoryMode=0750\nStateDirectory=baize-agent\nStateDirectoryMode=0750\nNoNewPrivileges=true\nProtectSystem=strict\nProtectHome=read-only\nReadOnlyPaths=/opt/baize/agent\nReadWritePaths=/opt/baize/agent/bin\nPrivateTmp=true\nProtectKernelTunables=true\nProtectControlGroups=true\nRestrictSUIDSGID=true\n\n[Install]\nWantedBy=multi-user.target\n"
}

const serviceUsage = `Usage: baize-agent service <command>

Commands:
  install    install or update the systemd service
  uninstall  stop, remove, and disable the systemd service
  status     show systemd status
`

const installUsage = `Usage: baize-agent service install [options]

With no options, a default config is generated at /opt/baize/agent/config.yml.
Pass all identity values to create and start a configured service:
  --dashboard-url URL --token TOKEN --robot-code CODE --robot-model MODEL

Options:
  --uuid UUID                 optional fixed robot UUID (otherwise generated)
  --force-config              regenerate config from the default template
`
