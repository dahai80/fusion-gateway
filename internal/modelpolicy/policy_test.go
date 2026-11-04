package modelpolicy

import (
    "errors"
    "path/filepath"
    "testing"
)

// #486 AC: resolution order — module override > user preference
// (allowlist-constrained) > tenant default; the build-time fallback is the
// last link only when the whole chain is empty.
func TestResolve_ChainOrder(t *testing.T) {
    s := NewStore()
    if err := s.SetPolicy("tenant-a", Policy{
        DefaultModel:    "mlx/default",
        ModuleOverrides: map[string]string{"fusion-code": "mlx/coder"},
        UserAllowlist:   []string{"mlx/coder", "mlx/chat"},
    }); err != nil {
        t.Fatalf("set policy: %v", err)
    }
    if err := s.SetUserPreference("tenant-a", "user-1", "fusion-code", "mlx/chat"); err != nil {
        t.Fatalf("set pref: %v", err)
    }
    if err := s.SetUserPreference("tenant-a", "user-1", "fusion-doc", "mlx/chat"); err != nil {
        t.Fatalf("set pref: %v", err)
    }
    // Module override wins over the user preference.
    d := s.Resolve("tenant-a", "user-1", "fusion-code", "legacy/fallback", nil)
    if d.Model != "mlx/coder" || d.Source != SourceModuleOverride {
        t.Fatalf("module override must win, got model=%q source=%q", d.Model, d.Source)
    }
    // No override for the module: the user preference applies.
    d = s.Resolve("tenant-a", "user-1", "fusion-doc", "legacy/fallback", nil)
    if d.Model != "mlx/chat" || d.Source != SourceUserPreference {
        t.Fatalf("user preference must apply, got model=%q source=%q", d.Model, d.Source)
    }
    // Another user without a preference falls to the tenant default.
    d = s.Resolve("tenant-a", "user-2", "fusion-doc", "legacy/fallback", nil)
    if d.Model != "mlx/default" || d.Source != SourceTenantDefault {
        t.Fatalf("tenant default must apply, got model=%q source=%q", d.Model, d.Source)
    }
    // Empty chain falls to the build-time fallback.
    d = s.Resolve("tenant-b", "user-1", "fusion-doc", "legacy/fallback", nil)
    if d.Model != "legacy/fallback" || d.Source != SourceFallback {
        t.Fatalf("fallback must apply, got model=%q source=%q", d.Model, d.Source)
    }
    // Nothing anywhere: empty decision, no invented model.
    d = s.Resolve("tenant-b", "user-1", "fusion-doc", "", nil)
    if d.Model != "" || d.Source != SourceNone {
        t.Fatalf("empty chain must resolve to none, got model=%q source=%q", d.Model, d.Source)
    }
}

// #486 AC: user preference is allowlist-constrained — a write outside the
// admin allowlist is refused, and a preference recorded before the allowlist
// narrowed is ignored at resolution time.
func TestResolve_UserPreferenceAllowlist(t *testing.T) {
    s := NewStore()
    if err := s.SetUserPreference("tenant-a", "user-1", "fusion-doc", "mlx/chat"); err != nil {
        t.Fatalf("pref without allowlist must be free, got %v", err)
    }
    if err := s.SetPolicy("tenant-a", Policy{UserAllowlist: []string{"mlx/coder"}}); err != nil {
        t.Fatalf("set policy: %v", err)
    }
    if err := s.SetUserPreference("tenant-a", "user-1", "fusion-doc", "mlx/chat"); !errors.Is(err, ErrNotInAllowlist) {
        t.Fatalf("write outside allowlist must be refused, got %v", err)
    }
    // The pre-allowlist preference is stale: resolution must not honor it.
    d := s.Resolve("tenant-a", "user-1", "fusion-doc", "legacy/fallback", nil)
    if d.Source == SourceUserPreference {
        t.Fatalf("stale preference must be ignored, got source=%q model=%q", d.Source, d.Model)
    }
    if err := s.SetUserPreference("tenant-a", "user-1", "fusion-doc", "mlx/coder"); err != nil {
        t.Fatalf("write inside allowlist must pass, got %v", err)
    }
    if d := s.Resolve("tenant-a", "user-1", "fusion-doc", "", nil); d.Model != "mlx/coder" {
        t.Fatalf("fresh preference must apply, got %q", d.Model)
    }
}

// #486 AC: privacy_mode=local_only filters the candidate set to local
// models; the whole chain re-filters, it does not just block the winner.
func TestResolve_PrivacyModeFiltersToLocal(t *testing.T) {
    s := NewStore()
    isLocal := func(model string) bool {
        return model == "mlx/local" || model == "mlx/local2"
    }
    if err := s.SetPolicy("tenant-a", Policy{
        DefaultModel:    "cloud/big",
        ModuleOverrides: map[string]string{"fusion-doc": "cloud/doc"},
        PrivacyMode:     PrivacyModeLocalOnly,
    }); err != nil {
        t.Fatalf("set policy: %v", err)
    }
    if err := s.SetUserPreference("tenant-a", "user-1", "fusion-doc", "cloud/chat"); err != nil {
        t.Fatalf("set pref: %v", err)
    }
    // Module override is non-local: the chain walks down, not blocks.
    d := s.Resolve("tenant-a", "user-1", "fusion-doc", "cloud/legacy", isLocal)
    if d.Source != SourceNone || d.Model != "" || !d.PrivacyFiltered {
        t.Fatalf("all-non-local chain must resolve to none+filtered, got model=%q source=%q filtered=%v", d.Model, d.Source, d.PrivacyFiltered)
    }
    // Give the module a local user preference: it must be honored.
    if err := s.SetUserPreference("tenant-a", "user-1", "fusion-doc", "mlx/local"); err != nil {
        t.Fatalf("set pref: %v", err)
    }
    d = s.Resolve("tenant-a", "user-1", "fusion-doc", "cloud/legacy", isLocal)
    if d.Model != "mlx/local" || d.Source != SourceUserPreference || !d.PrivacyFiltered {
        t.Fatalf("local user preference must be picked under privacy, got model=%q source=%q filtered=%v", d.Model, d.Source, d.PrivacyFiltered)
    }
    // privacy_mode=all (default) does not filter.
    p := s.Policy("tenant-a")
    p.PrivacyMode = PrivacyModeAll
    if err := s.SetPolicy("tenant-a", p); err != nil {
        t.Fatalf("set policy: %v", err)
    }
    d = s.Resolve("tenant-a", "user-2", "fusion-doc", "cloud/legacy", isLocal)
    if d.Model != "cloud/doc" || d.Source != SourceModuleOverride || d.PrivacyFiltered {
        t.Fatalf("mode=all must not filter, got model=%q source=%q filtered=%v", d.Model, d.Source, d.PrivacyFiltered)
    }
}

// #486 AC: tenant isolation — tenant A default/overrides must not leak to
// tenant B.
func TestIsolation_TenantsDoNotLeak(t *testing.T) {
    s := NewStore()
    if err := s.SetPolicy("tenant-a", Policy{
        DefaultModel:    "mlx/a-only",
        ModuleOverrides: map[string]string{"fusion-doc": "mlx/a-doc"},
    }); err != nil {
        t.Fatalf("set policy: %v", err)
    }
    if p := s.Policy("tenant-b"); p.DefaultModel != "" || p.ModuleOverrides != nil {
        t.Fatalf("tenant B must see no tenant A policy, got %+v", p)
    }
    if d := s.Resolve("tenant-b", "user-1", "fusion-doc", "legacy/fallback", nil); d.Model != "legacy/fallback" {
        t.Fatalf("tenant B must not inherit tenant A resolution, got %q", d.Model)
    }
    // Preferences are tenant-scoped too: the same user id in tenant B has
    // no preference.
    if err := s.SetUserPreference("tenant-a", "user-1", "fusion-doc", "mlx/a-doc"); err != nil {
        t.Fatalf("set pref: %v", err)
    }
    if d := s.Resolve("tenant-b", "user-1", "fusion-doc", "", nil); d.Source != SourceNone {
        t.Fatalf("tenant B must not see tenant A user preference, got source=%q", d.Source)
    }
}

// #486 AC: policy validation — privacy_mode is an enum, model fields must
// be non-empty where present.
func TestSetPolicy_Validation(t *testing.T) {
    s := NewStore()
    if err := s.SetPolicy("tenant-a", Policy{PrivacyMode: "paranoid"}); !errors.Is(err, ErrInvalidPrivacyMode) {
        t.Fatalf("invalid privacy mode must be refused, got %v", err)
    }
    if err := s.SetPolicy("tenant-a", Policy{PrivacyMode: PrivacyModeLocalOnly}); err != nil {
        t.Fatalf("valid enum must pass, got %v", err)
    }
    if err := s.SetPolicy("tenant-a", Policy{ModuleOverrides: map[string]string{"fusion-doc": ""}}); !errors.Is(err, ErrInvalidPolicy) {
        t.Fatalf("empty override model must be refused, got %v", err)
    }
    if err := s.SetPolicy("tenant-a", Policy{ModuleOverrides: map[string]string{"": "mlx/x"}}); !errors.Is(err, ErrInvalidPolicy) {
        t.Fatalf("empty override module must be refused, got %v", err)
    }
    if err := s.SetUserPreference("tenant-a", "user-1", "fusion-doc", " "); !errors.Is(err, ErrInvalidPolicy) {
        t.Fatalf("blank preference model must be refused, got %v", err)
    }
    if err := s.SetUserPreference("tenant-a", "user-1", "", "mlx/x"); !errors.Is(err, ErrInvalidPolicy) {
        t.Fatalf("blank module must be refused, got %v", err)
    }
}

// #486 AC: PUT policy takes effect without restart — the same store
// instance serves the new resolution immediately (hot-apply), and the state
// round-trips through persistence for a restarted gateway.
func TestStore_HotApplyAndPersistRoundTrip(t *testing.T) {
    path := filepath.Join(t.TempDir(), "model_policy.json")
    s := NewStore()
    s.SetPersistence(path)
    if err := s.SetPolicy("tenant-a", Policy{DefaultModel: "mlx/v1"}); err != nil {
        t.Fatalf("set policy: %v", err)
    }
    // Hot-apply: immediate.
    if d := s.Resolve("tenant-a", "user-1", "fusion-doc", "", nil); d.Model != "mlx/v1" {
        t.Fatalf("hot-apply failed, got %q", d.Model)
    }
    if err := s.SetUserPreference("tenant-a", "user-1", "fusion-doc", "mlx/pref"); err != nil {
        t.Fatalf("set pref: %v", err)
    }
    s2 := NewStore()
    s2.SetPersistence(path)
    if err := s2.Load(); err != nil {
        t.Fatalf("load: %v", err)
    }
    d := s2.Resolve("tenant-a", "user-1", "fusion-doc", "", nil)
    if d.Model != "mlx/pref" || d.Source != SourceUserPreference {
        t.Fatalf("preference must survive the round-trip, got model=%q source=%q", d.Model, d.Source)
    }
    p := s2.Policy("tenant-a")
    if p.DefaultModel != "mlx/v1" {
        t.Fatalf("policy must survive the round-trip, got %+v", p)
    }
    // User preference deletion clears the link without touching the policy.
    s2.DeleteUserPreference("tenant-a", "user-1", "fusion-doc")
    if got := s2.UserPreference("tenant-a", "user-1", "fusion-doc"); got != "" {
        t.Fatalf("preference must be deleted, got %q", got)
    }
}
