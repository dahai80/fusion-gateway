package server

import (
	"errors"
	"path/filepath"
	"testing"
)

func TestAcquireInstanceLock_SecondAcquireFails(t *testing.T) {
	dir := t.TempDir()

	release, err := AcquireInstanceLock(dir)
	if err != nil {
		t.Fatalf("first acquire: %v", err)
	}
	defer release()

	if _, err := AcquireInstanceLock(dir); !errors.Is(err, ErrInstanceLockHeld) {
		t.Fatalf("second acquire err = %v, want ErrInstanceLockHeld", err)
	}
}

func TestAcquireInstanceLock_ReleaseAllowsReacquire(t *testing.T) {
	dir := t.TempDir()

	release, err := AcquireInstanceLock(dir)
	if err != nil {
		t.Fatalf("first acquire: %v", err)
	}
	release()

	release2, err := AcquireInstanceLock(dir)
	if err != nil {
		t.Fatalf("reacquire after release: %v", err)
	}
	release2()
}

func TestAcquireInstanceLock_CreatesDataDir(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "nested", "data")

	release, err := AcquireInstanceLock(dir)
	if err != nil {
		t.Fatalf("acquire on missing dir: %v", err)
	}
	release()
}

func TestAcquireInstanceLock_EmptyDataDirIsNoop(t *testing.T) {
	release, err := AcquireInstanceLock("")
	if err != nil {
		t.Fatalf("empty data dir: want no-op, got error %v", err)
	}
	if release == nil {
		t.Fatal("empty data dir: want a callable no-op release, got nil")
	}
	release()
}
