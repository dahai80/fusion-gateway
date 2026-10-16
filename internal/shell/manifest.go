package shell

import (
    "encoding/json"
    "fmt"
    "log/slog"
    "net/url"
    "os"
    "path/filepath"
    "strings"
    "sync"
    "time"

    "github.com/fusion-gateway/fusion-gateway/internal/config"
)

// Status of a manifest entry as surfaced by the list API. online = a dynamic
// registration with a fresh heartbeat; unreachable = a dynamic registration
// whose heartbeat expired (menu greyed, NOT deleted — PRD §5.2); offline =
// static fallback only (module never registered this process lifetime).
type Status string

const (
    StatusOnline      Status = "online"
    StatusUnreachable Status = "unreachable"
    StatusOffline     Status = "offline"
)

// ManifestView is what GET /api/v1/shell/manifests returns: the manifest
// fields plus the derived liveness status.
type ManifestView struct {
    config.ShellManifest
    Status         Status    `json:"status"`
    LastHeartbeat  time.Time `json:"last_heartbeat,omitempty"`
    Registered     bool      `json:"registered"`
    // Unreachable is the host-menu flag (PRD §5.2 greyed-not-removed): any
    // status below online — stale heartbeat (unreachable) or never
    // registered (offline static fallback).
    Unreachable bool `json:"unreachable"`
}

type entry struct {
    manifest      config.ShellManifest
    lastHeartbeat time.Time
    dynamic       bool
}

// persistedManifest is the on-disk shape of a dynamic registration
// (store.data_dir/shell_manifests.json, #230). Only dynamic registrations
// persist — static fallback entries live in config.yaml by definition.
type persistedManifest struct {
    config.ShellManifest
    LastHeartbeat time.Time `json:"last_heartbeat"`
}

// Registry holds dynamic registrations plus the static fallback list.
// Idempotent upsert by id (PRD §5.2): a re-registration overwrites the
// manifest fields and resets the heartbeat, never duplicates.
type Registry struct {
    mu              sync.RWMutex
    dynamic         map[string]*entry
    staticFallback  []config.ShellManifest
    heartbeatExpiry time.Duration
    allowlist       []string
    // lastHealthy records the most recent successful health probe per
    // manifest id (health.go poller). A static fallback entry with a fresh
    // probe is surfaced online even though no dynamic registration exists.
    lastHealthy map[string]time.Time
    // path is the persistence file for dynamic registrations ("" = memory
    // only). When set, Upsert persists and Load restores on boot (#230);
    // the loaded lastHeartbeat is kept as-is, so a stale entry comes back
    // unreachable and the heartbeat/health-poll offline marking still
    // applies — a restart never resurrects a dead module to online.
    path string
}

func NewRegistry(staticFallback []config.ShellManifest, allowlist []string, heartbeatExpiry time.Duration) *Registry {
    if heartbeatExpiry <= 0 {
        heartbeatExpiry = 30 * time.Second
    }
    return &Registry{
        dynamic:         make(map[string]*entry),
        staticFallback:  staticFallback,
        heartbeatExpiry: heartbeatExpiry,
        allowlist:       allowlist,
        lastHealthy:     make(map[string]time.Time),
    }
}

// Upsert registers or refreshes a manifest. Validation: required fields, id
// shape (fusion-*), device/category vocabulary, and the entry origin
// whitelist (A7 — reject 403 at registration, not at load time).
func (r *Registry) Upsert(m config.ShellManifest) error {
    if err := validateManifest(m); err != nil {
        return err
    }
    if !strings.HasPrefix(m.ID, "fusion-") {
        return fmt.Errorf("manifest id %q must use the fusion-<name> prefix", m.ID)
    }
    if err := r.checkEntryAllowlist(m.Entry); err != nil {
        return err
    }

    r.mu.Lock()
    _, exists := r.dynamic[m.ID]
    r.dynamic[m.ID] = &entry{
        manifest:      m,
        lastHeartbeat: time.Now(),
        dynamic:       true,
    }
    if exists {
        slog.Info("shell manifest re-registered (idempotent upsert)", "id", m.ID, "entry", m.Entry)
    } else {
        slog.Info("shell manifest registered", "id", m.ID, "entry", m.Entry, "category", m.Category)
    }
    r.mu.Unlock()
    // Persist the dynamic registration after releasing the lock (#230).
    // A failed write must not fail the registration itself — log loudly;
    // the next upsert retries the write.
    if err := r.save(); err != nil {
        slog.Error("shell manifest registry save failed", "path", r.path, "error", err)
    }
    return nil
}

// SetPersistence points the registry at a store file (must be called before
// Load). Empty path keeps the registry memory-only.
func (r *Registry) SetPersistence(path string) {
    r.mu.Lock()
    defer r.mu.Unlock()
    r.path = path
}

// Load restores dynamic registrations from the store file (#230). A missing
// file is not an error (fresh install). Each entry is re-validated against
// the CURRENT manifest rules and entry allowlist — a stale store entry that
// no longer validates (config allowlist tightened, bad shape) is skipped
// with a warning, never fatal. Load must be called before any Upsert.
func (r *Registry) Load() error {
    r.mu.RLock()
    path := r.path
    r.mu.RUnlock()
    if path == "" {
        return nil
    }
    raw, err := os.ReadFile(path)
    if os.IsNotExist(err) {
        return nil
    }
    if err != nil {
        return fmt.Errorf("read shell manifest store: %w", err)
    }
    var persisted []persistedManifest
    if err := json.Unmarshal(raw, &persisted); err != nil {
        return fmt.Errorf("parse shell manifest store %s: %w", path, err)
    }
    r.mu.Lock()
    for i := range persisted {
        p := persisted[i]
        m := p.ShellManifest
        if err := r.validateForStoreLocked(m); err != nil {
            slog.Warn("shell manifest store entry skipped (no longer valid)", "id", m.ID, "error", err)
            continue
        }
        r.dynamic[m.ID] = &entry{
            manifest:      m,
            lastHeartbeat: p.LastHeartbeat,
            dynamic:       true,
        }
    }
    n := len(r.dynamic)
    r.mu.Unlock()
    slog.Info("shell manifest store loaded", "path", path, "manifests", n)
    return nil
}

// validateForStoreLocked applies the same gates as Upsert. Called with r.mu
// held (Lock or RLock) — it reads allowlist only.
func (r *Registry) validateForStoreLocked(m config.ShellManifest) error {
    if err := validateManifest(m); err != nil {
        return err
    }
    if !strings.HasPrefix(m.ID, "fusion-") {
        return fmt.Errorf("manifest id %q must use the fusion-<name> prefix", m.ID)
    }
    return r.checkEntryAllowlist(m.Entry)
}

// save snapshots the dynamic registrations and writes them atomically
// (temp file + rename, 0600). Heartbeat-only refreshes are NOT persisted —
// a restart drops at most the last heartbeat interval of liveness, and the
// next keepalive re-marks the module online.
func (r *Registry) save() error {
    r.mu.RLock()
    path := r.path
    if path == "" {
        r.mu.RUnlock()
        return nil
    }
    persisted := make([]persistedManifest, 0, len(r.dynamic))
    for _, e := range r.dynamic {
        persisted = append(persisted, persistedManifest{
            ShellManifest: e.manifest,
            LastHeartbeat: e.lastHeartbeat,
        })
    }
    r.mu.RUnlock()
    if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
        return fmt.Errorf("create shell manifest store dir: %w", err)
    }
    raw, err := json.MarshalIndent(persisted, "", "    ")
    if err != nil {
        return fmt.Errorf("marshal shell manifests: %w", err)
    }
    tmp := path + ".tmp"
    if err := os.WriteFile(tmp, raw, 0o600); err != nil {
        return fmt.Errorf("write shell manifest temp: %w", err)
    }
    if err := os.Rename(tmp, path); err != nil {
        return fmt.Errorf("rename shell manifest store: %w", err)
    }
    return nil
}

// Heartbeat refreshes the liveness clock of one dynamic manifest.
func (r *Registry) Heartbeat(id string) error {
    r.mu.Lock()
    defer r.mu.Unlock()
    e, ok := r.dynamic[id]
    if !ok {
        return fmt.Errorf("manifest %q not registered", id)
    }
    e.lastHeartbeat = time.Now()
    return nil
}

// List returns static fallback entries plus dynamic registrations, each with
// its derived status. Static entries whose id has a dynamic registration are
// suppressed (the dynamic one wins). Stale dynamic entries stay listed as
// unreachable — never deleted.
func (r *Registry) List() []ManifestView {
    now := time.Now()
    r.mu.RLock()
    defer r.mu.RUnlock()

    views := make([]ManifestView, 0, len(r.dynamic)+len(r.staticFallback))
    for _, e := range r.dynamic {
        status := statusOf(e, now, r.heartbeatExpiry)
        views = append(views, ManifestView{
            ShellManifest: e.manifest,
            Status:        status,
            LastHeartbeat: e.lastHeartbeat,
            Registered:    true,
            Unreachable:   status != StatusOnline,
        })
    }
    for _, m := range r.staticFallback {
        if _, live := r.dynamic[m.ID]; live {
            continue
        }
        status := StatusOffline
        if t, ok := r.lastHealthy[m.ID]; ok && now.Sub(t) <= r.heartbeatExpiry {
            status = StatusOnline
        }
        views = append(views, ManifestView{
            ShellManifest: m,
            Status:        status,
            Registered:    false,
            Unreachable:   status != StatusOnline,
        })
    }
    return views
}

// RecordHealth stores a health-probe observation for one manifest id. A
// failed probe clears the observation so a dead module goes offline on the
// next list render (A5 recovery path: probe succeeds again -> online).
func (r *Registry) RecordHealth(id string, healthy bool) {
    r.mu.Lock()
    defer r.mu.Unlock()
    if healthy {
        r.lastHealthy[id] = time.Now()
    } else {
        delete(r.lastHealthy, id)
    }
}

func statusOf(e *entry, now time.Time, expiry time.Duration) Status {
    if now.Sub(e.lastHeartbeat) <= expiry {
        return StatusOnline
    }
    return StatusUnreachable
}

// checkEntryAllowlist enforces the entry origin whitelist (A7). Gateway-hosted
// relative entries (#223, PRD §5.1 prod convention) start with "/" and are
// always same-origin — allowed without an origin check, but path traversal
// ("..") is rejected. An empty allowlist means unrestricted — but
// config.Validate rejects shell.enabled with an empty list, so empty here
// only occurs in tests.
func (r *Registry) checkEntryAllowlist(entryURL string) error {
    if strings.HasPrefix(entryURL, "/") {
        if strings.Contains(entryURL, "..") {
            return fmt.Errorf("manifest entry %q must not contain path traversal", entryURL)
        }
        return nil
    }
    if len(r.allowlist) == 0 {
        return nil
    }
    u, err := url.Parse(entryURL)
    if err != nil || u.Scheme == "" || u.Host == "" {
        return fmt.Errorf("manifest entry %q is not a valid absolute URL or /shell/<id>/ relative path", entryURL)
    }
    origin := fmt.Sprintf("%s://%s", u.Scheme, u.Host)
    for _, allowed := range r.allowlist {
        if allowed == origin {
            return nil
        }
    }
    return fmt.Errorf("manifest entry origin %q is not in shell.entry_origin_allowlist", origin)
}

func validateManifest(m config.ShellManifest) error {
    if m.ID == "" {
        return fmt.Errorf("manifest id is required")
    }
    if m.Title == "" {
        return fmt.Errorf("manifest %q: title is required", m.ID)
    }
    if m.Path == "" {
        return fmt.Errorf("manifest %q: path is required", m.ID)
    }
    if m.Entry == "" {
        return fmt.Errorf("manifest %q: entry is required", m.ID)
    }
    if len(m.Devices) == 0 {
        return fmt.Errorf("manifest %q: devices is required", m.ID)
    }
    switch m.Category {
    case "platform", "domain", "infra", "dev":
    default:
        return fmt.Errorf("manifest %q: category %q must be platform|domain|infra|dev", m.ID, m.Category)
    }
    return nil
}
