package shell

import (
    "fmt"
    "log/slog"
    "net/url"
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
}

type entry struct {
    manifest      config.ShellManifest
    lastHeartbeat time.Time
    dynamic       bool
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
    defer r.mu.Unlock()
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
        views = append(views, ManifestView{
            ShellManifest: e.manifest,
            Status:        statusOf(e, now, r.heartbeatExpiry),
            LastHeartbeat: e.lastHeartbeat,
            Registered:    true,
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

// checkEntryAllowlist enforces the entry origin whitelist (A7). An empty
// allowlist means unrestricted — but config.Validate rejects shell.enabled
// with an empty list, so empty here only occurs in tests.
func (r *Registry) checkEntryAllowlist(entryURL string) error {
    if len(r.allowlist) == 0 {
        return nil
    }
    u, err := url.Parse(entryURL)
    if err != nil || u.Scheme == "" || u.Host == "" {
        return fmt.Errorf("manifest entry %q is not a valid absolute URL", entryURL)
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
