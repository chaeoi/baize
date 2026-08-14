package agent

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"runtime"
	"syscall"

	"echobot/shared/model"
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
	info, err := os.Stat(executable)
	if err != nil {
		return err
	}
	temporary, err := os.CreateTemp(filepath.Dir(executable), ".echobot-update-*")
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
	mode := info.Mode().Perm()
	if mode&0o111 == 0 {
		mode |= 0o755
	}
	if err := os.Chmod(temporaryPath, mode); err != nil {
		return err
	}
	if err := os.Rename(temporaryPath, executable); err != nil {
		return fmt.Errorf("replace executable: %w", err)
	}
	if directory, openErr := os.Open(filepath.Dir(executable)); openErr == nil {
		_ = directory.Sync()
		_ = directory.Close()
	}
	return syscall.Exec(executable, os.Args, os.Environ())
}
