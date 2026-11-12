//go:build windows

package server

import (
    "os"

    "golang.org/x/sys/windows"
)

// flockExclusiveNB/unflock are the windows counterpart of flock_unix.go.
// LOCKFILE_FAIL_IMMEDIATELY reproduces flock LOCK_NB (try-lock, no blocking);
// LOCKFILE_EXCLUSIVE_LOCK reproduces LOCK_EX. Locking the range [0, maxUint32)
// covers every byte the instance-lock file holds (pid line).
func flockExclusiveNB(f *os.File) error {
    return windows.LockFileEx(windows.Handle(f.Fd()), windows.LOCKFILE_EXCLUSIVE_LOCK|windows.LOCKFILE_FAIL_IMMEDIATELY, 0, 0xFFFFFFFF, 0xFFFFFFFF, new(windows.Overlapped))
}

func flockRelease(f *os.File) error {
    return windows.UnlockFileEx(windows.Handle(f.Fd()), 0, 0xFFFFFFFF, 0xFFFFFFFF, new(windows.Overlapped))
}
