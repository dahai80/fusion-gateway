package server

import (
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"syscall"

	"github.com/fusion-gateway/fusion-gateway/internal/config"
)

// ErrInstanceLockHeld is returned when another gateway process already owns
// the data dir.
var ErrInstanceLockHeld = fmt.Errorf("another gateway instance is using the data dir")

// AcquireInstanceLock takes an exclusive, non-blocking flock on
// <data_dir>/gateway.lock and returns its release function.
//
// #401 follow-up: fusion-supervisor relaunches the gateway the instant a
// manual `start.sh stop` reaps it, so a restart can briefly run two processes
// against one data dir. Both then write store.data_dir/shell_manifests.json
// through the same .tmp path, and the loser's rename failed with
// "no such file or directory" (the in-process saveMu cannot serialize across
// processes). One of them also loses the port bind. The lock makes that
// deterministic: the second process fails before touching any state.
func AcquireInstanceLock(dataDir string) (func(), error) {
	dir := config.ExpandPath(dataDir)
	if dir == "" {
		// Every other DataDir consumer treats empty as "persistence off"
		// (server.go:557, shell.go:56, shell.go:172): no state is written, so
		// there is nothing for a second process to corrupt. Failing here would
		// refuse to boot a stateless gateway — visible warning instead.
		slog.Warn("instance lock skipped: store.data_dir is unset (no persistent state to guard; set it to serialize multiple processes)")
		return func() {}, nil
	}
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return nil, fmt.Errorf("instance lock: create %s: %w", dir, err)
	}
	path := filepath.Join(dir, "gateway.lock")
	f, err := os.OpenFile(path, os.O_CREATE|os.O_RDWR, 0o600)
	if err != nil {
		return nil, fmt.Errorf("instance lock: open %s: %w", path, err)
	}
	if err := syscall.Flock(int(f.Fd()), syscall.LOCK_EX|syscall.LOCK_NB); err != nil {
		f.Close()
		return nil, fmt.Errorf("%w (lock %s): %v", ErrInstanceLockHeld, path, err)
	}
	if err := f.Truncate(0); err == nil {
		_, _ = f.WriteString(fmt.Sprintf("%d\n", os.Getpid()))
	}
	slog.Info("instance lock acquired", "path", path, "pid", os.Getpid())
	return func() {
		if err := syscall.Flock(int(f.Fd()), syscall.LOCK_UN); err != nil {
			slog.Warn("instance lock release failed", "path", path, "error", err)
		}
		_ = f.Close()
	}, nil
}
