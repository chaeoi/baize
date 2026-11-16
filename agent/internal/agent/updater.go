package agent

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
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

type UpdateDownloader interface {
	Download(context.Context, model.UpdateInfo, io.Writer) error
}

var ErrUpdateSuperseded = errors.New("an equal or newer Agent version is already installed")

func ApplyUpdate(ctx context.Context, client UpdateDownloader, update model.UpdateInfo, executable, configPath string, beforeReplace func() error) error {
	if update.OS != runtime.GOOS || update.Arch != runtime.GOARCH {
		return fmt.Errorf("update platform %s/%s does not match %s/%s", update.OS, update.Arch, runtime.GOOS, runtime.GOARCH)
	}
	executable, err := filepath.EvalSymlinks(executable)
	if err != nil {
		return err
	}
	return applyUpdate(ctx, client, update, executable, []string{executable, "run", "--config", configPath}, beforeReplace)
}

func applyUpdate(ctx context.Context, client UpdateDownloader, update model.UpdateInfo, executable string, arguments []string, beforeReplace func() error) error {
	// Share a lock across atomic renames and different TMPDIR settings.
	identity := sha256.Sum256([]byte(executable))
	lockPath := filepath.Join("/tmp", fmt.Sprintf("baize-agent-update-%x.lock", identity[:8]))
	fd, err := syscall.Open(lockPath, syscall.O_CREAT|syscall.O_RDWR|syscall.O_CLOEXEC|syscall.O_NOFOLLOW, 0600)
	if err != nil {
		return fmt.Errorf("open update lock: %w", err)
	}
	defer syscall.Close(fd)
	if err := syscall.Flock(fd, syscall.LOCK_EX|syscall.LOCK_NB); err != nil {
		return fmt.Errorf("another update is in progress: %w", err)
	}
	defer syscall.Flock(fd, syscall.LOCK_UN)
	versionCtx, cancelVersion := context.WithTimeout(ctx, 15*time.Second)
	output, err := exec.CommandContext(versionCtx, executable, "--version").Output()
	cancelVersion()
	if err != nil {
		return fmt.Errorf("read installed Agent version: %w", err)
	}
	current := strings.TrimSpace(string(output))
	newer, err := newerVersion(update.Version, current)
	if err != nil {
		return err
	}
	if !newer {
		return fmt.Errorf("%w: installed %s, candidate %s", ErrUpdateSuperseded, current, update.Version)
	}
	info, err := os.Stat(executable)
	if err != nil {
		return err
	}
	temporary, err := os.CreateTemp(filepath.Dir(executable), ".baize-update-*")
	if err != nil {
		return fmt.Errorf("create update next to executable: %w", err)
	}
	defer os.Remove(temporary.Name())
	defer temporary.Close()
	hash := sha256.New()
	if err := client.Download(ctx, update, io.MultiWriter(temporary, hash)); err != nil {
		return err
	}
	if err := temporary.Sync(); err != nil {
		return err
	}
	if err := temporary.Close(); err != nil {
		return err
	}
	actual := hex.EncodeToString(hash.Sum(nil))
	if actual != update.SHA256 {
		return fmt.Errorf("update SHA-256 mismatch: expected %s, got %s", update.SHA256, actual)
	}
	if err := agentbinary.Validate(temporary.Name(), update.OS, update.Arch); err != nil {
		return err
	}
	mode := info.Mode().Perm()
	if mode&0111 == 0 {
		mode |= 0755
	}
	if err := os.Chmod(temporary.Name(), mode); err != nil {
		return err
	}
	checkCtx, cancel := context.WithTimeout(ctx, 15*time.Second)
	defer cancel()
	output, err = exec.CommandContext(checkCtx, temporary.Name(), "--version").Output()
	if err != nil || strings.TrimSpace(string(output)) != update.Version {
		return fmt.Errorf("downloaded Agent version does not match %s: %v", update.Version, err)
	}
	args := append([]string(nil), arguments[1:]...)
	args = append(args, "--check-config")
	if output, err := exec.CommandContext(checkCtx, temporary.Name(), args...).CombinedOutput(); err != nil {
		return fmt.Errorf("updated Agent rejected current configuration: %w: %.4096s", err, output)
	}
	// Collection is drained only after the candidate has passed all checks.
	if beforeReplace != nil {
		if err := beforeReplace(); err != nil {
			return fmt.Errorf("prepare update handoff: %w", err)
		}
	}
	if err := os.Rename(temporary.Name(), executable); err != nil {
		return fmt.Errorf("replace executable: %w", err)
	}
	directory, err := os.Open(filepath.Dir(executable))
	if err != nil {
		return err
	}
	defer directory.Close()
	return directory.Sync()
}
