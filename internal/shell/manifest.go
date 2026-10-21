package shell

import (
    "encoding/json"
    "errors"
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
    Status        Status    `json:"status"`
    LastHeartbeat time.Time `json:"last_heartbeat,omitempty"`
    Registered    bool      `json:"registered"`
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
    // builtins are gateway-owned manifest ids (the auto-registered
    // fusion-admin, #216). They are dynamic registrations in every other
    // respect, but NOT API-owned: Remove rejects them like static entries
    // (#253) — a restart re-registers them, yet the process-lifetime menu
    // loss is a config-plane change, not an API one.
    builtins map[string]struct{}
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
        builtins:        make(map[string]struct{}),
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
    if err := validateUpstreamURL(m); err != nil {
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
    if err := validateUpstreamURL(m); err != nil {
        return err
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

// Sentinel errors surfaced by Remove, mapped to HTTP statuses by the delete
// handler (#253): unknown id → 404, config-owned static manifest → 409.
var (
    ErrManifestNotFound = errors.New("shell manifest not registered")
    ErrManifestStatic   = errors.New("static manifest is config-owned and cannot be deleted via the API")
)

// Remove drops a dynamic registration and persists the store file (#253) —
// the API-level counterpart of Upsert, so test registrations can be cleaned
// up without editing shell_manifests.json by hand. Static fallback entries
// (cfg.Shell.Manifests) are NOT API-owned: they would keep rendering offline
// from config even after a dynamic delete, so removing them is rejected
// (409) rather than silently returning 200 for an entry that survives.
// Removing an unknown id is a 404 condition (not idempotent-200): the shell
// host should distinguish "already gone" from "deleted now".
// MarkBuiltin records a gateway-owned manifest id (auto-registered at
// startup). Builtins are protected from API deletion like static entries.
func (r *Registry) MarkBuiltin(id string) {
    r.mu.Lock()
    r.builtins[id] = struct{}{}
    r.mu.Unlock()
}

func (r *Registry) Remove(id string) error {
    r.mu.Lock()
    if _, isBuiltin := r.builtins[id]; isBuiltin {
        r.mu.Unlock()
        return ErrManifestStatic
    }
    _, isDynamic := r.dynamic[id]
    if !isDynamic {
        for _, m := range r.staticFallback {
            if m.ID == id {
                r.mu.Unlock()
                return ErrManifestStatic
            }
        }
        r.mu.Unlock()
        return ErrManifestNotFound
    }
    delete(r.dynamic, id)
    r.mu.Unlock()
    // Persist after releasing the lock (same pattern as Upsert): a failed
    // write must not fail the delete — log loudly, the next mutation retries.
    if err := r.save(); err != nil {
        slog.Error("shell manifest registry save failed", "path", r.path, "error", err)
    }
    slog.Info("shell manifest removed", "id", id)
    return nil
}

// List returns static fallback entries plus dynamic registrations, each with
// its derived status. Static entries whose id has a dynamic registration are
// suppressed (the dynamic one wins). Stale dynamic entries stay listed as
// unreachable — never deleted. APIKey is always zeroed (#232): the list is
// session-visible, and the upstream credential must never reach the client.
func (r *Registry) List() []ManifestView {
    now := time.Now()
    r.mu.RLock()
    defer r.mu.RUnlock()

    views := make([]ManifestView, 0, len(r.dynamic)+len(r.staticFallback))
    for _, e := range r.dynamic {
        status := statusOf(e, now, r.heartbeatExpiry)
        m := e.manifest
        m.APIKey = ""
        views = append(views, ManifestView{
            ShellManifest: m,
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
        m.APIKey = ""
        views = append(views, ManifestView{
            ShellManifest: m,
            Status:        status,
            Registered:    false,
            Unreachable:   status != StatusOnline,
        })
    }
    return views
}

// UpstreamFor resolves the /shell/<id>/ proxy target from a dynamic
// registration's own routing fields (#232). ok=false when the manifest did
// not carry an upstream (static-config-only module). Secrets stay inside the
// gateway — only the proxy consumes this.
func (r *Registry) UpstreamFor(id string) (string, string, bool) {
    r.mu.RLock()
    defer r.mu.RUnlock()
    e, ok := r.dynamic[id]
    if !ok || e.manifest.Upstream == "" {
        return "", "", false
    }
    return e.manifest.Upstream, e.manifest.APIKey, true
}

// ExtRouteFor reports the dynamic registration's own upstream + credential
// for one FULL gateway request path (#256). The static config may pin a
// module to a different upstream than the one the module self-registered
// (engine vs web bridge) — the endpoints the module declares in its ext
// fields (searchProviders/notifications endpoints, #250) live only on the
// self-registered upstream, so the proxy routes those paths there while
// everything else keeps the static upstream. Pages are deliberately NOT
// matched: they are SPA deep links (${entry}#${route}), not API paths.
// ok=false keeps the static upstream.
func (r *Registry) ExtRouteFor(id, path string) (string, string, bool) {
    r.mu.RLock()
    defer r.mu.RUnlock()
    e, ok := r.dynamic[id]
    if !ok || e.manifest.Upstream == "" {
        return "", "", false
    }
    // A declared endpoint may be gateway-absolute (/shell/<id>/api/...) or
    // module-relative (/api/...): match the full path AND the
    // proxy-relative form so both registration styles work.
    rel := "/" + strings.TrimPrefix(strings.TrimPrefix(path, "/shell/"+id+"/"), "/")
    if !manifestHasExtEndpoint(&e.manifest, path) && !manifestHasExtEndpoint(&e.manifest, rel) {
        return "", "", false
    }
    return e.manifest.Upstream, e.manifest.APIKey, true
}

// manifestHasExtEndpoint reports whether path matches a declared ext
// endpoint: exact, or a segment-boundary prefix (a search API may serve
// sub-resources under its endpoint root). Endpoints are validated
// gateway-absolute ("/...", no traversal) at registration; both
// gateway-absolute (/shell/<id>/api/...) and module-relative (/api/...)
// declaration styles are handled by ExtRouteFor normalizing the path.
func manifestHasExtEndpoint(m *config.ShellManifest, path string) bool {
    if path == "" {
        return false
    }
    for _, p := range m.SearchProviders {
        if extEndpointMatches(p.Endpoint, path) {
            return true
        }
    }
    for _, n := range m.Notifications {
        if extEndpointMatches(n.Endpoint, path) {
            return true
        }
    }
    return false
}

func extEndpointMatches(endpoint, path string) bool {
    if endpoint == "" {
        return false
    }
    return path == endpoint || strings.HasPrefix(path, endpoint+"/")
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

// validateUpstreamURL gates the optional #232 dynamic routing fields: an
// upstream, when carried, must be an absolute http(s) URL (the proxy dials
// it directly).
func validateUpstreamURL(m config.ShellManifest) error {
    if m.Upstream == "" {
        return nil
    }
    u, err := url.Parse(m.Upstream)
    if err != nil || (u.Scheme != "http" && u.Scheme != "https") || u.Host == "" {
        return fmt.Errorf("manifest %q upstream %q must be an absolute http(s) URL", m.ID, m.Upstream)
    }
    return nil
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
    return validateManifestExt(m)
}

// maxShellExtEntries bounds each schema v1 ext array (#250) so a module
// cannot inflate the manifest list payload; the shell host caps at the
// same value on load.
const maxShellExtEntries = 32

// validateManifestExt gates the schema v1 ext fields (#250): endpoints must
// be gateway-absolute /... (the shell dials them same-origin), page routes
// are module-relative paths, and notification kinds use the host's
// vocabulary. Shape errors are rejected at registration, not silently
// dropped at shell load.
func validateManifestExt(m config.ShellManifest) error {
    if len(m.SearchProviders) > maxShellExtEntries {
        return fmt.Errorf("manifest %q: search_providers exceeds %d entries", m.ID, maxShellExtEntries)
    }
    for _, p := range m.SearchProviders {
        if p.ID == "" || p.Label == "" {
            return fmt.Errorf("manifest %q: search provider requires id and label", m.ID)
        }
        if err := validateGatewayAbsolutePath(p.Endpoint); err != nil {
            return fmt.Errorf("manifest %q: search provider %q endpoint: %w", m.ID, p.ID, err)
        }
    }
    if len(m.Pages) > maxShellExtEntries {
        return fmt.Errorf("manifest %q: pages exceeds %d entries", m.ID, maxShellExtEntries)
    }
    for _, p := range m.Pages {
        if p.Title == "" {
            return fmt.Errorf("manifest %q: page requires title", m.ID)
        }
        if err := validateGatewayAbsolutePath(p.Route); err != nil {
            return fmt.Errorf("manifest %q: page route %q: %w", m.ID, p.Route, err)
        }
        if strings.Contains(p.Route, "#") {
            return fmt.Errorf("manifest %q: page route %q must not contain '#'", m.ID, p.Route)
        }
    }
    if len(m.Notifications) > maxShellExtEntries {
        return fmt.Errorf("manifest %q: notifications exceeds %d entries", m.ID, maxShellExtEntries)
    }
    for _, n := range m.Notifications {
        if n.ID == "" || n.Label == "" {
            return fmt.Errorf("manifest %q: notification requires id and label", m.ID)
        }
        switch n.Kind {
        case "approvals", "runs", "engine":
        default:
            return fmt.Errorf("manifest %q: notification kind %q must be approvals|runs|engine", m.ID, n.Kind)
        }
        if err := validateGatewayAbsolutePath(n.Endpoint); err != nil {
            return fmt.Errorf("manifest %q: notification %q endpoint: %w", m.ID, n.ID, err)
        }
    }
    return nil
}

// validateGatewayAbsolutePath enforces the gateway-absolute path vocabulary
// shared with manifest entries: starts with "/", no traversal.
func validateGatewayAbsolutePath(p string) error {
    if !strings.HasPrefix(p, "/") {
        return fmt.Errorf("%q must be a gateway-absolute /... path", p)
    }
    if strings.Contains(p, "..") {
        return fmt.Errorf("%q must not contain path traversal", p)
    }
    return nil
}
