//go:build !windows

package shell

import (
    "fmt"
    "log/slog"
    "os"
    "syscall"
)

// lockStore takes an exclusive advisory flock on path+".lock" so two gateway
// processes sharing one data_dir serialise their shell manifest store
// read-modify-write cycles (#542). The lock file itself persists on disk;
// only the lock is transient. Caller must releaseStore the result.
func lockStore(path string) (*os.File, error) {
    lf, err := os.OpenFile(path+".lock", os.O_CREATE|os.O_RDWR, 0o600)
    if err != nil {
        return nil, fmt.Errorf("open shell manifest lock: %w", err)
    }
    if err := syscall.Flock(int(lf.Fd()), syscall.LOCK_EX); err != nil {
        lf.Close()
        return nil, fmt.Errorf("lock shell manifest store: %w", err)
    }
    return lf, nil
}

func releaseStore(lf *os.File) {
    if err := syscall.Flock(int(lf.Fd()), syscall.LOCK_UN); err != nil {
        slog.Warn("shell manifest store unlock failed", "error", err)
    }
    lf.Close()
}
