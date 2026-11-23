package admin

import (
    "net/http"
    "sort"
    "strings"

    "github.com/fusion-gateway/fusion-gateway/internal/config"
)

// Issue #709: read-only model routing surface for the admin console.
//
// The operator demand-engine workflow edits model routing from UIs, not
// config files. The WRITABLE half of that story already exists —
// GET/PUT /admin/api/config/auth exposes api_keys[].allowed_models, gated
// by admin.config_mutation_lock (#662) and audited (auditSensitiveChanges).
// What was missing is visibility: models.supply routes, the cloud_models
// allowlist, and a per-key allowlist summary in one place, plus a model
// catalog that the allowlist editor can validate entries against.

// modelsConfigResponse is the GET /admin/api/config/models payload.
type modelsConfigResponse struct {
    Supply           map[string][]string `json:"supply"`
    Aliases          map[string]string   `json:"aliases"`
    AllowCrossSupply *bool               `json:"allow_cross_supply"`
    CloudModels      []string            `json:"cloud_models"`
    Keys             []keyAllowlistEntry `json:"keys"`
    // Catalog maps every model name the config knows about to the sources
    // that mention it ("backend:<name>", "models.supply", "auth key:<name>").
    // Wildcard allowlist entries are validated syntactically, not against
    // the catalog — a catalog miss is a warning, never a rejection.
    Catalog       map[string][]string `json:"catalog"`
    MutationLock  bool                `json:"mutation_lock"`
    ConfigVersion uint64              `json:"config_version"`
}

type keyAllowlistEntry struct {
    Name            string   `json:"name"`
    AllowedModels   []string `json:"allowed_models"`
    AllowedBackends []string `json:"allowed_backends"`
}

// modelCatalog builds the config-known model name set: every backend's
// models list, every models.supply key, and the cloud_models entries.
// Pure function over the snapshot — no engine/local-set state (admin
// handlers are engine-free by design; live local models already surface
// through /v1/models).
func modelCatalog(cfg *config.Config) (map[string][]string, []string) {
    catalog := map[string][]string{}
    addSource := func(model, source string) {
        model = strings.TrimSpace(model)
        if model == "" {
            return
        }
        for _, s := range catalog[model] {
            if s == source {
                return
            }
        }
        catalog[model] = append(catalog[model], source)
    }
    names := make([]string, 0, len(cfg.Backends))
    for name := range cfg.Backends {
        names = append(names, name)
    }
    sort.Strings(names)
    for _, name := range names {
        be := cfg.Backends[name]
        for _, m := range be.Models {
            addSource(m, "backend:"+name)
        }
    }
    for model := range cfg.Models.Supply {
        addSource(model, "models.supply")
    }
    for _, m := range cfg.Routing.CloudModels {
        addSource(m, "cloud_models")
    }
    return catalog, names
}

// validateAllowedModels checks the entries an operator typed into the
// allowlist editor and returns human-readable warnings. Non-blocking by
// design: patterns with a trailing "*" match by prefix and cannot be
// resolved against the catalog, and an exact name may legitimately precede
// the model's registration — the warning tells the operator, the write
// still lands. Malformed entries (empty, stray "*") always warn.
func validateAllowedModels(entries []string, catalog map[string][]string) []string {
    var warnings []string
    for _, e := range entries {
        trimmed := strings.TrimSpace(e)
        switch {
        case trimmed == "":
            warnings = append(warnings, "empty allowed_models entry (drop it or fill in a model name)")
        case strings.Contains(trimmed, "*") && !strings.HasSuffix(trimmed, "*"):
            warnings = append(warnings, trimmed+": wildcard patterns must use a trailing * (prefix match)")
        case !strings.Contains(trimmed, "*"):
            if _, ok := catalog[trimmed]; !ok {
                warnings = append(warnings, trimmed+": not in the configured model catalog (backend models lists, models.supply, cloud_models) — verify the spelling or the model registration")
            }
        }
    }
    return warnings
}

func (h *Handler) handleModelsConfig(w http.ResponseWriter, r *http.Request) {
    if r.Method != http.MethodGet {
        writeError(w, http.StatusMethodNotAllowed, "method not allowed")
        return
    }
    snap := config.GetSnapshot()
    cfg := &snap.Config
    catalog, _ := modelCatalog(cfg)

    keys := make([]keyAllowlistEntry, 0, len(cfg.Auth.APIKeys))
    for _, k := range cfg.Auth.APIKeys {
        keys = append(keys, keyAllowlistEntry{
            Name:            k.Name,
            AllowedModels:   k.AllowedModels,
            AllowedBackends: k.AllowedBackends,
        })
    }

    allowCross := cfg.Models.AllowCrossSupply == nil || *cfg.Models.AllowCrossSupply
    supply := make(map[string][]string, len(cfg.Models.Supply))
    for model, backends := range cfg.Models.Supply {
        supply[model] = backends
    }
    aliases := make(map[string]string, len(cfg.Models.Aliases))
    for alias, target := range cfg.Models.Aliases {
        aliases[alias] = target
    }

    writeJSON(w, http.StatusOK, modelsConfigResponse{
        Supply:           supply,
        Aliases:          aliases,
        AllowCrossSupply: &allowCross,
        CloudModels:      cfg.Routing.CloudModels,
        Keys:             keys,
        Catalog:          catalog,
        MutationLock:     configMutationLocked(),
        ConfigVersion:    snap.Version,
    })
}

// #709: the AuthConfig PUT response now carries allowlist warnings so the
// editor can surface them inline. This helper keeps handleAuthConfig thin.
func allowedModelsWarnings(apiKeys []authKeyResponse) []string {
    snap := config.GetSnapshot()
    catalog, _ := modelCatalog(&snap.Config)
    var warnings []string
    for _, k := range apiKeys {
        for _, w := range validateAllowedModels(k.AllowedModels, catalog) {
            warnings = append(warnings, "key "+k.Name+": "+w)
        }
    }
    return warnings
}
