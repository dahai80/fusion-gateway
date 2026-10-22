package shell

import (
    "encoding/json"
    "fmt"
    "log/slog"
    "os"
    "path/filepath"
    "sort"
    "sync"
)

// ConfigOverrides is the policy-override store behind
// /api/v1/shell/config/{key} (#275, migration E6): an open key vocabulary
// (pricing_policies, banned_words, ...) where modules keep template defaults
// and the operator layers override JSON on top (L2 merge at module runtime).
// PUT is effective immediately — the store is in-memory authoritative and
// the file is a mirror, so a runtime read after PUT sees the new value with
// no reload step.
type ConfigOverrides struct {
    mu        sync.RWMutex
    overrides map[string]json.RawMessage
    path      string // "" = memory only (tests)
    // defaults supplies the template defaults projection for GET. nil = the
    // gateway holds no defaults for any key (module-side templates).
    defaults func(key string) json.RawMessage
}

func NewConfigOverrides(path string) *ConfigOverrides {
    return &ConfigOverrides{
        overrides: make(map[string]json.RawMessage),
        path:      path,
    }
}

// SetDefaultsFn wires the defaults projection (optional).
func (s *ConfigOverrides) SetDefaultsFn(fn func(key string) json.RawMessage) {
    s.mu.Lock()
    s.defaults = fn
    s.mu.Unlock()
}

func (s *ConfigOverrides) Load() error {
    if s.path == "" {
        return nil
    }
    raw, err := os.ReadFile(s.path)
    if os.IsNotExist(err) {
        return nil
    }
    if err != nil {
        return fmt.Errorf("read shell config overrides store: %w", err)
    }
    var overrides map[string]json.RawMessage
    if err := json.Unmarshal(raw, &overrides); err != nil {
        return fmt.Errorf("parse shell config overrides store %s: %w", s.path, err)
    }
    s.mu.Lock()
    s.overrides = overrides
    if s.overrides == nil {
        s.overrides = make(map[string]json.RawMessage)
    }
    n := len(s.overrides)
    s.mu.Unlock()
    slog.Info("shell config overrides loaded", "path", s.path, "keys", n)
    return nil
}

func (s *ConfigOverrides) Save() error {
    if s.path == "" {
        return nil
    }
    s.mu.RLock()
    raw, err := jsonMarshalIndent(s.overrides)
    s.mu.RUnlock()
    if err != nil {
        return fmt.Errorf("marshal shell config overrides: %w", err)
    }
    if err := os.MkdirAll(filepath.Dir(s.path), 0o755); err != nil {
        return fmt.Errorf("create shell config overrides dir: %w", err)
    }
    tmp := s.path + ".tmp"
    if err := os.WriteFile(tmp, raw, 0o600); err != nil {
        return fmt.Errorf("write shell config overrides temp: %w", err)
    }
    if err := os.Rename(tmp, s.path); err != nil {
        return fmt.Errorf("rename shell config overrides store: %w", err)
    }
    return nil
}

// Get returns the defaults + overrides projection for one key. Both are
// empty JSON objects when absent — the shell renders `{defaults:{},
// overrides:{}}` for an unconfigured key, not a 404.
func (s *ConfigOverrides) Get(key string) (defaults, overrides json.RawMessage) {
    s.mu.RLock()
    defer s.mu.RUnlock()
    defaults = json.RawMessage(`{}`)
    if s.defaults != nil {
        if d := s.defaults(key); len(d) > 0 {
            defaults = d
        }
    }
    overrides = json.RawMessage(`{}`)
    if o, ok := s.overrides[key]; ok && len(o) > 0 {
        overrides = o
    }
    return defaults, overrides
}

// Put stores one key's override JSON, effective immediately. The value must
// be a valid JSON object (top-level sections per the contract).
func (s *ConfigOverrides) Put(key string, raw json.RawMessage) error {
    if key == "" {
        return fmt.Errorf("config key is required")
    }
    var probe map[string]any
    if err := json.Unmarshal(raw, &probe); err != nil {
        return fmt.Errorf("override must be a JSON object: %w", err)
    }
    s.mu.Lock()
    s.overrides[key] = raw
    s.mu.Unlock()
    if err := s.Save(); err != nil {
        slog.Error("shell config overrides save failed", "path", s.path, "error", err)
        return err
    }
    slog.Info("shell config override saved", "key", key, "bytes", len(raw))
    return nil
}

// Keys lists the configured override keys (sorted) — ops helper.
func (s *ConfigOverrides) Keys() []string {
    s.mu.RLock()
    defer s.mu.RUnlock()
    keys := make([]string, 0, len(s.overrides))
    for k := range s.overrides {
        keys = append(keys, k)
    }
    sort.Strings(keys)
    return keys
}
