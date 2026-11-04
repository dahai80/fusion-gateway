// Package modelpolicy implements the #486 central model-policy plane: a
// tenant-scoped four-layer preference chain
//
//	module override > user preference (allowlist-constrained)
//	    > tenant default > build-time fallback (legacy)
//
// with an optional privacy_mode pinning resolution to local-backend models.
// The store is deliberately independent of config hot-reload: PUT mutates
// in-memory state and persists to JSON, so a policy change applies to the
// next resolution without a gateway restart.
package modelpolicy

import (
    "encoding/json"
    "errors"
    "fmt"
    "log/slog"
    "os"
    "path/filepath"
    "strings"
    "sync"
)

// Privacy modes (#486): "all" resolves the chain unfiltered; "local_only"
// filters the candidate set to local-backend models (mlx / model-hub).
const (
    PrivacyModeAll       = "all"
    PrivacyModeLocalOnly = "local_only"
)

// Resolution sources, surfaced by the preference endpoint so callers can
// tell which chain layer won.
const (
    SourceNone           = ""
    SourceModuleOverride = "module_override"
    SourceUserPreference = "user_preference"
    SourceTenantDefault  = "tenant_default"
    SourceFallback       = "fallback"
)

var (
    ErrInvalidPrivacyMode = errors.New("invalid privacy_mode: must be \"all\" or \"local_only\"")
    ErrInvalidPolicy      = errors.New("invalid policy: model and module ids must be non-empty")
    ErrNotInAllowlist     = errors.New("model not in the tenant user_allowlist")
    ErrNoPolicy           = errors.New("no policy for tenant")
)

// Policy is the admin-managed per-tenant configuration (#486 scope 1).
type Policy struct {
    DefaultModel    string            `json:"default_model,omitempty"`
    ModuleOverrides map[string]string `json:"module_overrides,omitempty"`
    // UserAllowlist lists the models users may select as their preference.
    // Empty = unrestricted (any model is selectable).
    UserAllowlist []string `json:"user_allowlist,omitempty"`
    PrivacyMode   string   `json:"privacy_mode,omitempty"`
}

// Decision is a resolved model plus provenance.
type Decision struct {
    Model  string `json:"model"`
    Source string `json:"source"`
    // PrivacyFiltered is true when privacy_mode=local_only narrowed the
    // candidate set (a non-local candidate was skipped or the resolution
    // ended empty because nothing local was available).
    PrivacyFiltered bool `json:"privacy_filtered"`
}

// userPrefKey identifies one user preference slot: preferences are scoped
// per (tenant, user) and keyed by module id, so the same user in two
// tenants never shares a choice (AC: tenant isolation).
type userPrefKey struct {
    tenant string
    user   string
}

// Store holds per-tenant policies and per-(tenant,user,module) preference
// links. Reads take the RLock; writes mutate + persist outside the state
// lock (saveMu serializes the temp-file rename, mirroring the shell
// registry pattern) so hot-apply never blocks on disk I/O.
type Store struct {
    mu       sync.RWMutex
    saveMu   sync.Mutex
    policies map[string]*Policy
    prefs    map[userPrefKey]map[string]string
    path     string
}

func NewStore() *Store {
    return &Store{
        policies: map[string]*Policy{},
        prefs:    map[userPrefKey]map[string]string{},
    }
}

// SetPersistence enables JSON persistence (#486 hot-apply without losing
// state on restart). Empty path = memory only.
func (s *Store) SetPersistence(path string) {
    s.mu.Lock()
    s.path = path
    s.mu.Unlock()
}

// Policy returns the tenant's policy; a tenant with no stored policy gets
// the zero Policy (privacy_mode empty behaves as "all").
func (s *Store) Policy(tenant string) Policy {
    s.mu.RLock()
    defer s.mu.RUnlock()
    if p, ok := s.policies[tenant]; ok && p != nil {
        return *p
    }
    return Policy{}
}

// SetPolicy validates and stores the tenant policy. Hot-applies: the next
// Resolve call sees it. The "" tenant is valid — it is the isolated default
// namespace for principals with no tenant binding (#499: an admin JWT
// principal has no team binding, so a PUT would otherwise 400 forever).
func (s *Store) SetPolicy(tenant string, p Policy) error {
    switch p.PrivacyMode {
    case "", PrivacyModeAll, PrivacyModeLocalOnly:
    default:
        return ErrInvalidPrivacyMode
    }
    for module, model := range p.ModuleOverrides {
        if strings.TrimSpace(module) == "" || strings.TrimSpace(model) == "" {
            return fmt.Errorf("%w: module_overrides entries must be non-empty", ErrInvalidPolicy)
        }
    }
    for _, m := range p.UserAllowlist {
        if strings.TrimSpace(m) == "" {
            return fmt.Errorf("%w: user_allowlist entries must be non-empty", ErrInvalidPolicy)
        }
    }
    if p.DefaultModel != "" && strings.TrimSpace(p.DefaultModel) == "" {
        return fmt.Errorf("%w: default_model must be non-empty", ErrInvalidPolicy)
    }
    s.mu.Lock()
    s.policies[tenant] = &p
    s.mu.Unlock()
    slog.Info("model policy updated", "tenant", tenant, "default_model", p.DefaultModel,
        "overrides", len(p.ModuleOverrides), "allowlist", len(p.UserAllowlist), "privacy_mode", p.PrivacyMode)
    s.save()
    return nil
}

// SetUserPreference records the user's own model choice for a module. The
// tenant allowlist constrains the write; a preference recorded before the
// allowlist narrowed is ignored at resolution time (defense in depth).
func (s *Store) SetUserPreference(tenant, user, module, model string) error {
    if strings.TrimSpace(module) == "" || strings.TrimSpace(model) == "" || strings.TrimSpace(user) == "" {
        return fmt.Errorf("%w: user, module and model are required", ErrInvalidPolicy)
    }
    s.mu.RLock()
    p := s.policies[tenant]
    allow := p != nil && len(p.UserAllowlist) > 0
    if allow {
        allowed := false
        for _, m := range p.UserAllowlist {
            if m == model {
                allowed = true
                break
            }
        }
        if !allowed {
            s.mu.RUnlock()
            return ErrNotInAllowlist
        }
    }
    s.mu.RUnlock()
    s.mu.Lock()
    key := userPrefKey{tenant: tenant, user: user}
    if s.prefs[key] == nil {
        s.prefs[key] = map[string]string{}
    }
    s.prefs[key][module] = model
    s.mu.Unlock()
    slog.Info("model user preference set", "tenant", tenant, "user", user, "module", module, "model", model)
    s.save()
    return nil
}

// UserPreference returns the stored raw preference ("" = none).
func (s *Store) UserPreference(tenant, user, module string) string {
    s.mu.RLock()
    defer s.mu.RUnlock()
    return s.prefs[userPrefKey{tenant: tenant, user: user}][module]
}

// DeleteUserPreference clears one preference link.
func (s *Store) DeleteUserPreference(tenant, user, module string) {
    s.mu.Lock()
    key := userPrefKey{tenant: tenant, user: user}
    if mods, ok := s.prefs[key]; ok {
        delete(mods, module)
        if len(mods) == 0 {
            delete(s.prefs, key)
        }
    }
    s.mu.Unlock()
    slog.Info("model user preference deleted", "tenant", tenant, "user", user, "module", module)
    s.save()
}

// Resolve walks the #486 chain for (tenant, user, module). fallback is the
// legacy build-time model (routing.default_model); isLocal, when non-nil,
// reports whether a model runs on a local backend and is only consulted
// when the tenant pins privacy_mode=local_only.
func (s *Store) Resolve(tenant, user, module, fallback string, isLocal func(string) bool) Decision {
    s.mu.RLock()
    p := s.policies[tenant]
    var policyCopy Policy
    if p != nil {
        policyCopy = *p
    }
    pref := s.prefs[userPrefKey{tenant: tenant, user: user}][module]
    s.mu.RUnlock()

    localOnly := policyCopy.PrivacyMode == PrivacyModeLocalOnly && isLocal != nil
    d := Decision{PrivacyFiltered: localOnly}

    // User preferences are allowlist-constrained at write time; re-check at
    // resolution time so an allowlist narrowing deprecates stale prefs.
    prefOK := pref != "" && module != ""
    if prefOK && len(policyCopy.UserAllowlist) > 0 {
        prefOK = false
        for _, m := range policyCopy.UserAllowlist {
            if m == pref {
                prefOK = true
                break
            }
        }
    }

    candidates := []struct {
        model  string
        source string
        active bool
    }{
        {policyCopy.ModuleOverrides[module], SourceModuleOverride, module != ""},
        {pref, SourceUserPreference, prefOK},
        {policyCopy.DefaultModel, SourceTenantDefault, true},
        {fallback, SourceFallback, true},
    }
    for _, c := range candidates {
        if !c.active || c.model == "" {
            continue
        }
        if localOnly && !isLocal(c.model) {
            continue
        }
        d.Model = c.model
        d.Source = c.source
        return d
    }
    return d
}

// persistedStore is the on-disk shape (store.data_dir/model_policy.json).
type persistedStore struct {
    Policies map[string]*Policy                      `json:"policies"`
    Prefs    map[string]map[string]map[string]string `json:"preferences"`
}

// Load restores persisted state. Corrupt file = start empty (the next
// SetPolicy rewrite heals it), mirroring the shell store degradation rules.
func (s *Store) Load() error {
    s.mu.RLock()
    path := s.path
    s.mu.RUnlock()
    if path == "" {
        return nil
    }
    raw, err := os.ReadFile(path)
    if err != nil {
        if os.IsNotExist(err) {
            return nil
        }
        return fmt.Errorf("model policy load: %w", err)
    }
    var ps persistedStore
    if err := json.Unmarshal(raw, &ps); err != nil {
        return fmt.Errorf("model policy load: %w", err)
    }
    s.mu.Lock()
    if ps.Policies != nil {
        s.policies = ps.Policies
    }
    if ps.Prefs != nil {
        prefs := map[userPrefKey]map[string]string{}
        for tenant, users := range ps.Prefs {
            for user, mods := range users {
                prefs[userPrefKey{tenant: tenant, user: user}] = mods
            }
        }
        s.prefs = prefs
    }
    s.mu.Unlock()
    slog.Info("model policy store loaded", "path", path, "tenants", len(ps.Policies))
    return nil
}

// save persists outside the state lock; two writers serialize on saveMu.
func (s *Store) save() {
    s.mu.RLock()
    path := s.path
    if path == "" {
        s.mu.RUnlock()
        return
    }
    ps := persistedStore{
        Policies: s.policies,
        Prefs:    map[string]map[string]map[string]string{},
    }
    for key, mods := range s.prefs {
        if ps.Prefs[key.tenant] == nil {
            ps.Prefs[key.tenant] = map[string]map[string]string{}
        }
        ps.Prefs[key.tenant][key.user] = mods
    }
    s.mu.RUnlock()

    s.saveMu.Lock()
    defer s.saveMu.Unlock()
    raw, err := json.MarshalIndent(ps, "", "    ")
    if err != nil {
        slog.Error("model policy save marshal failed", "error", err)
        return
    }
    tmp := path + ".tmp"
    if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
        slog.Error("model policy save mkdir failed", "error", err)
        return
    }
    if err := os.WriteFile(tmp, raw, 0o600); err != nil {
        slog.Error("model policy save write failed", "error", err)
        return
    }
    if err := os.Rename(tmp, path); err != nil {
        slog.Error("model policy save rename failed", "error", err)
        return
    }
}
