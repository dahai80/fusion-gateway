//go:build !windows

package server

import (
    "os"
    "syscall"
)

// flockExclusive/unflock are the unix advisory-lock primitives behind
// AcquireInstanceLock (instance_lock.go). Extracted behind build tags so the
// windows packaging target (#647) compiles: syscall.Flock does not exist on
// windows, which substitutes LockFileEx (flock_windows.go).
func flockExclusiveNB(f *os.File) error {
    return syscall.Flock(int(f.Fd()), syscall.LOCK_EX|syscall.LOCK_NB)
}

func flockRelease(f *os.File) error {
    return syscall.Flock(int(f.Fd()), syscall.LOCK_UN)
}
