package agent

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"syscall"
	"time"

	"baize/shared/agentbinary"
	"baize/shared/model"
)

func ApplyUpdate(ctx context.Context, client *Client, update model.UpdateInfo) error {
	if update.OS != runtime.GOOS || update.Arch != runtime.GOARCH {
		return fmt.Errorf("update platform %s/%s does not match %s/%s", update.OS, update.Arch, runtime.GOOS, runtime.GOARCH)
	}
	executable, err := os.Executable()
	if err != nil {
		return err
	}
	executable, err = filepath.EvalSymlinks(executable)
	if err != nil {
		return err
	}
	return applyUpdate(ctx, client, update, executable, os.Args, os.Environ(), syscall.Exec)
}

func applyUpdate(ctx context.Context, client *Client, update model.UpdateInfo, executable string, arguments, environment []string, execute func(string, []string, []string) error) error {
	info, err := os.Stat(executable)
	if err != nil {
		return err
	}
	temporary, err := os.CreateTemp(filepath.Dir(executable), ".baize-update-*")
	if err != nil {
		return fmt.Errorf("create update next to executable: %w", err)
	}
	temporaryPath := temporary.Name()
	defer os.Remove(temporaryPath)
	hash := sha256.New()
	if err := client.Download(ctx, update, io.MultiWriter(temporary, hash)); err != nil {
		temporary.Close()
		return err
	}
	if err := temporary.Sync(); err != nil {
		temporary.Close()
		return err
	}
	if err := temporary.Close(); err != nil {
		return err
	}
	actualHash := hex.EncodeToString(hash.Sum(nil))
	if actualHash != update.SHA256 {
		return fmt.Errorf("update SHA-256 mismatch: expected %s, got %s", update.SHA256, actualHash)
	}
	if err := agentbinary.Validate(temporaryPath, update.OS, update.Arch); err != nil {
		return err
	}
	if rejected, _ := os.ReadFile(executable + ".rejected"); strings.TrimSpace(string(rejected)) == update.SHA256 {
		return fmt.Errorf("this release previously failed to start")
	}
	mode := info.Mode().Perm()
	if mode&0o111 == 0 {
		mode |= 0o755
	}
	if err := os.Chmod(temporaryPath, mode); err != nil {
		return err
	}
	checkCtx, cancel := context.WithTimeout(ctx, 15*time.Second)
	defer cancel()
	output, err := exec.CommandContext(checkCtx, temporaryPath, "--version").Output()
	if err != nil || strings.TrimSpace(string(output)) != update.Version {
		return fmt.Errorf("downloaded Agent version does not match %s: %v", update.Version, err)
	}
	args := append([]string(nil), arguments[1:]...)
	args = append(args, "--check-config")
	if output, err := exec.CommandContext(checkCtx, temporaryPath, args...).CombinedOutput(); err != nil {
		return fmt.Errorf("updated Agent rejected current configuration: %w: %.4096s", err, output)
	}
	if err := os.Link(executable, executable+".previous"); err != nil {
		return fmt.Errorf("preserve previous Agent: %w", err)
	}
	if err := os.WriteFile(executable+".pending", []byte(update.SHA256), 0o600); err != nil {
		_ = os.Remove(executable + ".previous")
		return err
	}
	if err := os.Rename(temporaryPath, executable); err != nil {
		_ = os.Remove(executable + ".previous")
		_ = os.Remove(executable + ".pending")
		return fmt.Errorf("replace executable: %w", err)
	}
	if directory, openErr := os.Open(filepath.Dir(executable)); openErr == nil {
		_ = directory.Sync()
		_ = directory.Close()
	}
	if os.Getenv("BAIZE_MANAGED_SUBSCRIBER") == "1" {
		filtered := make([]string, 0, len(environment))
		for _, variable := range environment {
			if !strings.HasPrefix(variable, "BAIZE_ROS2_SUBSCRIBER=") && !strings.HasPrefix(variable, "BAIZE_MANAGED_SUBSCRIBER=") {
				filtered = append(filtered, variable)
			}
		}
		environment = filtered
	}
	err = execute(executable, arguments, environment)
	if restoreErr := RecoverUpdate(executable); restoreErr != nil {
		return fmt.Errorf("execute update: %v; restore: %w", err, restoreErr)
	}
	return err
}

func RecoverUpdate(executable string) error {
	pending, err := os.ReadFile(executable + ".pending")
	if os.IsNotExist(err) {
		return nil
	}
	if err != nil {
		return err
	}
	if err := os.Rename(executable+".previous", executable); err != nil {
		return err
	}
	if err := os.WriteFile(executable+".rejected", pending, 0o600); err != nil {
		return err
	}
	return os.Remove(executable + ".pending")
}

func ConfirmUpdate() error {
	executable, err := os.Executable()
	if err != nil {
		return err
	}
	if err := os.Remove(executable + ".pending"); err != nil && !os.IsNotExist(err) {
		return err
	}
	if err := os.Remove(executable + ".previous"); err != nil && !os.IsNotExist(err) {
		return err
	}
	return nil
}

// Supervise keeps a root-owned launcher available if an updated worker cannot start.
// It runs as the same unprivileged account as the worker.
func Supervise(ctx context.Context, executable, configPath string) error {
	for ctx.Err() == nil {
		if err := RecoverUpdate(executable); err != nil {
			return err
		}
		command := exec.CommandContext(ctx, executable, "run", "--config", configPath)
		command.Stdout, command.Stderr = os.Stdout, os.Stderr
		command.Cancel = func() error { return command.Process.Signal(syscall.SIGTERM) }
		command.WaitDelay = 10 * time.Second
		_ = command.Run()
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(3 * time.Second):
		}
	}
	return ctx.Err()
}
