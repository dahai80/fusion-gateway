package server

// #488 product catalog merge view: GET /v1/catalog joins three sources —
// the built-in static catalog (what exists), the shell manifest registry
// (what is registered/running), and supervisor health (installed/stopped).
// Degrade-by-design: each source's absence is a visible degraded flag,
// never a 5xx and never error-spam (the #490 core-profile boot contract).
//
// Three-state derivation per product: running / installed_stopped /
// not_installed (supervisor Unhealthy surfaces as installed_unhealthy — a
// superset state the shell UI may render as degraded-running). Modules
// registered in the manifest registry but absent from the static catalog
// are appended as dynamic products so the view never hides a live module.

import (
    "context"
    "encoding/json"
    "log/slog"
    "net/http"
    "sort"
    "sync"
    "time"

    "github.com/fusion-gateway/fusion-gateway/internal/catalog"
)

// Product states surfaced to the shell three-state view.
const (
    CatalogStateRunning           = "running"
    CatalogStateInstalledStopped  = "installed_stopped"
    CatalogStateInstalledUnhealthy = "installed_unhealthy"
    CatalogStateNotInstalled      = "not_installed"
)

// catalogProduct is one merged view row.
type catalogProduct struct {
    catalog.Entry
    // State is the three-state derivation; Empty for a pure catalog row
    // when both live sources are degraded (state unknown, not fabricated).
    State string `json:"state,omitempty"`
    // Registered/Reachable reflect the manifest registry dimension.
    Registered bool `json:"registered"`
    Reachable  bool `json:"reachable"`
    // InCatalog is false for dynamic rows (registered module absent from
    // the static catalog).
    InCatalog bool `json:"in_catalog"`
}

// catalogResponse is the merged payload; Sources records per-source health
// so absence is visible, never a 5xx.
type catalogResponse struct {
    Sources    map[string]any   `json:"sources"`
    Products   []catalogProduct `json:"products"`
    GeneratedAt string          `json:"generated_at"`
}

// supervisorSnapshot is the cached supervisor status fetch holder.
type supervisorSnapshot struct {
    mu   sync.Mutex
    data supervisorStatusData
}

// supervisorStatusData is the copyable payload of a supervisor status
// fetch (the cache holder carries the mutex separately).
type supervisorStatusData struct {
    fetchedAt time.Time
    services  map[string]string // name → state
    available bool
    reason    string
}

// supervisorStatus returns the cached service state map, refreshing from
// the supervisor read-only face when the TTL expired. Unreachable
// supervisor = available=false + reason; never an error to the caller.
func (s *Server) supervisorStatus(ctx context.Context) supervisorStatusData {
    snap := s.snapshot()
    url := "http://127.0.0.1:9100/api/v1/status"
    ttl := 30 * time.Second
    if snap != nil {
        if u := snap.Config.Catalog.SupervisorStatusURL; u != "" {
            url = u
        }
        if t := snap.Config.Catalog.StatusTTL; t > 0 {
            ttl = t
        }
    }
    s.catalogSup.mu.Lock()
    defer s.catalogSup.mu.Unlock()
    if s.catalogSup.data.services != nil && time.Since(s.catalogSup.data.fetchedAt) < ttl {
        return s.catalogSup.data
    }
    services := map[string]string{}
    available := false
    reason := ""
    client := &http.Client{Timeout: 2 * time.Second}
    req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
    if err == nil {
        var resp *http.Response
        resp, err = client.Do(req)
        if err == nil {
            defer resp.Body.Close()
            if resp.StatusCode == http.StatusOK {
                var body struct {
                    Services []struct {
                        Name  string `json:"name"`
                        State string `json:"state"`
                    } `json:"services"`
                }
                if err := json.NewDecoder(resp.Body).Decode(&body); err == nil {
                    for _, svc := range body.Services {
                        services[svc.Name] = svc.State
                    }
                    available = true
                } else {
                    reason = "supervisor status decode failed"
                }
            } else {
                reason = "supervisor status http " + resp.Status
            }
        }
    }
    if err != nil {
        reason = "supervisor unreachable: " + err.Error()
    }
    if !available && reason == "" {
        reason = "supervisor status unavailable"
    }
    s.catalogSup.data = supervisorStatusData{
        services:  services,
        available: available,
        reason:    reason,
        fetchedAt: time.Now(),
    }
    slog.Debug("supervisor status fetched", "available", available, "services", len(services), "reason", reason)
    return s.catalogSup.data
}

// handleCatalog serves GET /v1/catalog (#488).
func (s *Server) handleCatalog(w http.ResponseWriter, r *http.Request) {
    if r.Method != http.MethodGet {
        w.Header().Set("Allow", "GET")
        writeJSON(w, http.StatusMethodNotAllowed, map[string]string{"error": "method not allowed"})
        return
    }
    entries, err := catalog.All()
    catalogOK := err == nil
    if err != nil {
        // Degrade visibly, never fail the endpoint (#490: at most warn-level).
        slog.Warn("catalog source degraded", "error", err)
        entries = nil
    }
    type row struct {
        product catalogProduct
    }
    rows := map[string]*row{}
    order := make([]string, 0, len(entries)+8)
    addRow := func(p catalogProduct) {
        if _, ok := rows[p.ID]; !ok {
            order = append(order, p.ID)
        }
        rows[p.ID] = &row{product: p}
    }
    for _, e := range entries {
        addRow(catalogProduct{Entry: e, InCatalog: true})
    }
    // Manifest registry dimension: registered + reachable.
    manifestsOK := false
    if s.shellRegistry != nil {
        manifestsOK = true
        for _, v := range s.shellRegistry.List() {
            existing, ok := rows[v.ID]
            if !ok {
                addRow(catalogProduct{Entry: catalog.Entry{
                    ID: v.ID, TitleEn: v.Title, TitleCn: v.Title, Category: "dynamic",
                }, InCatalog: false})
                existing = rows[v.ID]
            }
            existing.product.Registered = true
            existing.product.Reachable = !v.Unreachable
        }
    }
    // Supervisor dimension: installed/stopped state.
    sup := s.supervisorStatus(r.Context())
    products := make([]catalogProduct, 0, len(order))
    for _, id := range order {
        p := rows[id].product
        state, ok := sup.services[p.ID]
        switch {
        case ok && state == "Healthy":
            p.State = CatalogStateRunning
        case ok && state == "Stopped":
            p.State = CatalogStateInstalledStopped
        case ok:
            p.State = CatalogStateInstalledUnhealthy
        case p.Registered && p.Reachable:
            p.State = CatalogStateRunning
        case p.Registered:
            p.State = CatalogStateInstalledStopped
        case sup.available:
            // Supervisor is live and does not know this product — it is
            // genuinely not installed. With the supervisor degraded the
            // state stays empty (unknown, not fabricated).
            p.State = CatalogStateNotInstalled
        }
        products = append(products, p)
    }
    sort.Slice(products, func(a, b int) bool {
        if products[a].Category != products[b].Category {
            return products[a].Category < products[b].Category
        }
        return products[a].ID < products[b].ID
    })
    writeJSON(w, http.StatusOK, catalogResponse{
        Sources: map[string]any{
            "catalog":          catalogOK,
            "manifests":        manifestsOK,
            "supervisor":       sup.available,
            "supervisor_reason": sup.reason,
        },
        Products:    products,
        GeneratedAt: time.Now().UTC().Format(time.RFC3339),
    })
}
