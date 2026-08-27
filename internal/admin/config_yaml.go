package admin

import (
    "fmt"
    "log/slog"
    "net/http"
    "os"
    "path/filepath"
    "strings"

    "github.com/fusion-gateway/fusion-gateway/internal/config"
    "gopkg.in/yaml.v3"
)

// readYAMLDoc reads and parses the config YAML file into a generic map.
// Called by updateYAMLSection and future direct-read handlers.
func readYAMLDoc(path string) (map[string]interface{}, error) {
    raw, err := os.ReadFile(path)
    if err != nil {
        return nil, err
    }
    var doc map[string]interface{}
    if err := yaml.Unmarshal(raw, &doc); err != nil {
        return nil, err
    }
    return doc, nil
}

// writeYAMLDoc marshals and writes the doc map back to the config file.
// Called by updateYAMLSection after mutation. EI1: writes atomically
// (tmp file in same dir + fsync + rename) so a concurrent Reload never reads
// a half-written file. os.WriteFile is not atomic — a reload racing the write
// can unmarshal a truncated doc → validate failure → config regress.
func writeYAMLDoc(path string, doc map[string]interface{}) error {
    out, err := yaml.Marshal(doc)
    if err != nil {
        return err
    }
    return atomicWriteFile(path, out, 0600)
}

// atomicWriteFile writes data to a temp file in the same directory as path,
// fsyncs it, then renames over path. Same-dir rename is atomic on POSIX
// filesystems; fsync before rename guarantees the renamed file's bytes are on
// disk. EI1 hardening against concurrent config.Reload reading mid-write.
func atomicWriteFile(path string, data []byte, perm os.FileMode) error {
    dir := filepath.Dir(path)
    f, err := os.CreateTemp(dir, ".cfg-tmp-*")
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

// getOrCreateSection returns the sub-map at doc[keys...], creating missing levels.
// Example: getOrCreateSection(doc, "route", "rate_limit") returns doc["route"]["rate_limit"].
// Used by all config PUT handlers that need nested YAML sections.
func getOrCreateSection(doc map[string]interface{}, keys ...string) map[string]interface{} {
    current := doc
    for _, key := range keys {
        sub, ok := current[key].(map[string]interface{})
        if !ok {
            sub = make(map[string]interface{})
            current[key] = sub
        }
        current = sub
    }
    return current
}

// updateYAMLSection reads config YAML, applies fn to mutate the target section,
// then writes back. fn receives the top-level doc and should mutate it directly.
// Called by all config PUT handlers (handleUpdateRoutingConfig, handleUpdateCacheConfig, etc.).
func (h *Handler) updateYAMLSection(fn func(doc map[string]interface{}) error) (map[string]interface{}, error) {
    if h.configPath == "" {
        return nil, errNoConfigPath
    }

    h.configMutex.Lock()
    defer h.configMutex.Unlock()

    doc, err := readYAMLDoc(h.configPath)
    if err != nil {
        slog.Error("failed to read config file", "path", h.configPath, "error", err)
        return nil, err
    }

    if err := fn(doc); err != nil {
        return nil, err
    }

    if err := writeYAMLDoc(h.configPath, doc); err != nil {
        slog.Error("failed to write config file", "path", h.configPath, "error", err)
        return nil, err
    }

    // EI1: explicitly reload the in-memory snapshot right after the file write.
    // Do NOT rely on fsnotify — it is unreliable on macOS (kqueue), so a PUT
    // could write the file while the runtime kept serving the old snapshot.
    // "saved" returned to the operator must mean "snapshot updated", not just
    // "file written". config.Reload re-reads, validates, runs onReload handlers
    // (which rebuild breakers/queue/semantic cache), and swaps the snapshot.
    // If reload fails the file is already changed — surface the error so the
    // operator knows the live config is stale (manual /admin/config/reload).
    if _, err := config.Reload(h.configPath); err != nil {
        slog.Error("config file written but reload failed — live snapshot is STALE",
            "path", h.configPath, "error", err)
        return nil, fmt.Errorf("config written but reload failed, live config stale: %w", err)
    }
    slog.Info("admin config PUT reloaded live snapshot", "path", h.configPath)

    return doc, nil
}

var errNoConfigPath = &configError{msg: "config file path not configured"}

type configError struct {
    msg string
}

func (e *configError) Error() string { return e.msg }

// getString extracts a string from a map, returning "" if missing or wrong type.
func getString(m map[string]interface{}, key string) string {
    v, _ := m[key].(string)
    return v
}

// getInt extracts an int from a map, returning 0 if missing or wrong type.
func getInt(m map[string]interface{}, key string) int {
    switch v := m[key].(type) {
    case int:
        return v
    case float64:
        return int(v)
    default:
        return 0
    }
}

// getBool extracts a bool from a map, returning false if missing or wrong type.
func getBool(m map[string]interface{}, key string) bool {
    v, _ := m[key].(bool)
    return v
}

// getFloat64 extracts a float64 from a map, returning 0 if missing or wrong type.
func getFloat64(m map[string]interface{}, key string) float64 {
    v, _ := m[key].(float64)
    return v
}

// sensitiveFields tracks which config keys contain secrets.
// When these are changed via admin API, an audit log entry is emitted.
var sensitiveFields = map[string]bool{
    "master_key": true, "jwt_secret": true, "shared_token": true,
    "api_key": true, "password": true, "redis_password": true,
}

// isMaskedValue detects values that were returned by maskAPIKey() on GET.
// Writing these back to YAML would destroy the real secret.
func isMaskedValue(s string) bool {
    return strings.HasPrefix(s, "****")
}

// applyMaskedString writes a string to the section map, but skips masked values.
// Empty string means "keep existing", masked value means "client didn't change it".
func applyMaskedString(sec map[string]interface{}, key string, val *string) {
    if val == nil {
        return
    }
    if *val == "" || isMaskedValue(*val) {
        return
    }
    sec[key] = *val
}

// auditSensitiveChanges checks a section map for sensitive field changes
// and logs them. The admin username is extracted from request context if available.
func auditSensitiveChanges(section string, sec map[string]interface{}, username string) {
    for key := range sec {
        if sensitiveFields[key] {
            slog.Info("sensitive config field changed via admin API",
                "section", section, "field", key, "admin", username)
        }
    }
}

// adminUsername extracts the admin username from the request context.
func adminUsername(r *http.Request) string {
    if claims := GetAdminClaims(r.Context()); claims != nil {
        return claims.Username
    }
    return "unknown"
}
