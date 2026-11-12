//go:build windows

package shell

import (
    "fmt"
    "log/slog"
    "os"

    "golang.org/x/sys/windows"
)

// lockStore is the windows counterpart of lockfile_unix.go: an exclusive
// advisory byte-range lock over the whole lock file via LockFileEx (blocking,
// matching unix flock LOCK_EX semantics) so two gateway processes sharing one
// data_dir serialise their shell manifest store read-modify-write cycles
// (#542). Locking the range [0, maxUint32) covers every file the store writes.
func lockStore(path string) (*os.File, error) {
    lf, err := os.OpenFile(path+".lock", os.O_CREATE|os.O_RDWR, 0o600)
    if err != nil {
        return nil, fmt.Errorf("open shell manifest lock: %w", err)
    }
    ol := new(windows.Overlapped)
    if err := windows.LockFileEx(windows.Handle(lf.Fd()), windows.LOCKFILE_EXCLUSIVE_LOCK, 0, 0xFFFFFFFF, 0xFFFFFFFF, ol); err != nil {
        lf.Close()
        return nil, fmt.Errorf("lock shell manifest store: %w", err)
    }
    return lf, nil
}

func releaseStore(lf *os.File) {
    ol := new(windows.Overlapped)
    if err := windows.UnlockFileEx(windows.Handle(lf.Fd()), 0, 0xFFFFFFFF, 0xFFFFFFFF, ol); err != nil {
        slog.Warn("shell manifest store unlock failed", "error", err)
    }
    lf.Close()
}
