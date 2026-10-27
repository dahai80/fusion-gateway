package config

import (
	"errors"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"time"
)

// Config corruption protection (防错 + 纠错):
//   - 防错: every admin write already passes ProbeValidate + atomicWriteFile;
//     BackupFile keeps a last-good copy so any external textual corruption of
//     config.yaml is always recoverable.
//   - 纠错: LoadWithRecovery / ReloadWithRecovery detect a broken config file,
//     quarantine it, restore the last-good copy, and keep serving. A gateway
//     start never fatal-loops on a corrupt file again.
//
// ExitCodeConfigFatal is the process exit code for unrecoverable config
// errors (no last-good copy available). fusion-supervisor can distinguish it
// from a crash and back off instead of restart-looping.

const ExitCodeConfigFatal = 78 // sysexits.h EX_CONFIG

// ConfigFatalError marks an error as config-fatal so main() can exit with
// ExitCodeConfigFatal instead of the generic 1.
type ConfigFatalError struct{ Err error }

func (e *ConfigFatalError) Error() string { return e.Err.Error() }
func (e *ConfigFatalError) Unwrap() error { return e.Err }

// LastGoodPath returns the last-good backup path for a config file.
func LastGoodPath(path string) string { return path + ".lastgood" }

// BackupFile atomically copies path to path+".lastgood". No-op when path does
// not exist or the content is already identical to the backup (avoids wearing
// the disk on every admin PUT that lands the same bytes). Called before every
// config overwrite so the pre-write bytes always survive.
func BackupFile(path string) error {
	raw, err := os.ReadFile(path)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return nil
		}
		return fmt.Errorf("read for backup: %w", err)
	}
	bak := LastGoodPath(path)
	if old, err := os.ReadFile(bak); err == nil && string(old) == string(raw) {
		return nil
	}
	if err := atomicWrite(bak, raw, 0600); err != nil {
		return fmt.Errorf("write backup: %w", err)
	}
	return nil
}

// atomicWrite is the shared tmp+fsync+rename writer (same contract as the
// admin package's atomicWriteFile; kept local so the guard has no dependency
// on internal/admin).
func atomicWrite(path string, data []byte, perm os.FileMode) error {
	dir := filepath.Dir(path)
	f, err := os.CreateTemp(dir, ".cfg-bak-*")
	if err != nil {
		return err
	}
	tmp := f.Name()
	cleanup := func() { _ = os.Remove(tmp) }
	if _, err := f.Write(data); err != nil {
		_ = f.Close()
		cleanup()
		return err
	}
	if err := f.Sync(); err != nil {
		_ = f.Close()
		cleanup()
		return err
	}
	if err := f.Close(); err != nil {
		cleanup()
		return err
	}
	if err := os.Chmod(tmp, perm); err != nil {
		cleanup()
		return err
	}
	if err := os.Rename(tmp, path); err != nil {
		cleanup()
		return err
	}
	return nil
}

// quarantine moves a broken config file aside (path+".corrupt-<ts>") so the
// offending bytes are preserved for post-mortem before the last-good copy is
// restored over the live path.
func quarantine(path string) string {
	ts := time.Now().Format("20060102T150405")
	q := fmt.Sprintf("%s.corrupt-%s", path, ts)
	if err := os.Rename(path, q); err != nil {
		slog.Error("config guard: failed to quarantine corrupt config",
			"path", path, "quarantine", q, "error", err)
		return ""
	}
	return q
}

// LoadWithRecovery wraps Load with self-healing: when the config file fails
// to parse or validate, it quarantines the broken file, restores the
// last-good copy, and loads that instead. Returns a *ConfigFatalError only
// when no recovery was possible (no backup or the backup itself is broken).
func LoadWithRecovery(path string) (*ConfigSnapshot, error) {
	snap, err := Load(path)
	if err == nil {
		// Keep the backup fresh: first successful start after a manual
		// config repair should update the last-good copy.
		if berr := BackupFile(path); berr != nil {
			slog.Warn("config guard: failed to refresh last-good backup", "path", path, "error", berr)
		}
		return snap, nil
	}

	bak := LastGoodPath(path)
	if _, statErr := os.Stat(bak); statErr != nil {
		return nil, &ConfigFatalError{Err: fmt.Errorf(
			"config load failed and no last-good backup exists (%s): %w", bak, err)}
	}

	slog.Error("config guard: config file broken, attempting last-good recovery",
		"path", path, "backup", bak, "error", err)

	recovered, rerr := Load(bak)
	if rerr != nil {
		return nil, &ConfigFatalError{Err: fmt.Errorf(
			"config load failed (%v) and last-good backup also broken: %w", err, rerr)}
	}

	bakRaw, rerr2 := os.ReadFile(bak)
	if rerr2 != nil {
		return nil, &ConfigFatalError{Err: fmt.Errorf(
			"config broken and last-good backup unreadable: %w", rerr2)}
	}
	q := quarantine(path)
	if q == "" {
		return nil, &ConfigFatalError{Err: fmt.Errorf(
			"config broken and could not quarantine it for recovery: %w", err)}
	}
	if cerr := atomicWrite(path, bakRaw, 0600); cerr != nil {
		return nil, &ConfigFatalError{Err: fmt.Errorf(
			"config broken and restore from last-good failed: %w", cerr)}
	}
	slog.Error("config guard: RECOVERED from last-good backup — broken file preserved for inspection",
		"path", path, "quarantined", q, "backup", bak)
	return recovered, nil
}

// ReloadWithRecovery wraps Reload with the same self-healing: a corrupt
// external edit to config.yaml no longer leaves a broken file on disk while
// the process keeps serving a stale snapshot. The corrupt bytes are
// quarantined and the file is restored so the next restart is clean.
func ReloadWithRecovery(path string) (*ConfigSnapshot, error) {
	snap, err := Reload(path)
	if err == nil {
		return snap, nil
	}

	bak := LastGoodPath(path)
	if _, statErr := os.Stat(bak); statErr != nil {
		return nil, err
	}
	recovered, rerr := Reload(bak)
	if rerr != nil {
		return nil, err
	}
	bakRaw, rerr2 := os.ReadFile(bak)
	if rerr2 != nil {
		slog.Error("config guard: last-good backup unreadable, file left as-is", "path", path, "error", rerr2)
		return recovered, nil
	}
	q := quarantine(path)
	if q == "" {
		return recovered, nil
	}
	if cerr := atomicWrite(path, bakRaw, 0600); cerr != nil {
		slog.Error("config guard: reload recovery failed to restore file", "path", path, "error", cerr)
		return recovered, nil
	}
	slog.Error("config guard: reload recovered from last-good backup — broken file quarantined",
		"path", path, "quarantined", q, "backup", bak, "original_error", err)
	return recovered, nil
}
