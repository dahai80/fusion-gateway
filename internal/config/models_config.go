package config

import (
    "fmt"
    "log/slog"
    "os"
    "strings"
    "time"
)

// ModelsConfig is the models.* namespace (plan §13.3): the single
// user-facing place to express model intent — supply priority for bare
// model names, client-visible aliases, and the cross-supply escape hatch.
// Everything here is optional; an empty ModelsConfig preserves the
// historical routing-chain behavior exactly.
type ModelsConfig struct {
    // Supply maps a bare model name to an ordered list of backend names.
    // The list is the explicit resolution priority for a bare-name request:
    // the first eligible backend wins (engine slot P0.18, guarded by the
    // cloud_models allowlist and the local breaker). An entry absent from
    // Supply keeps the historical routing chain.
    Supply map[string][]string `mapstructure:"supply"`
    // Aliases maps a client-visible name to a target (bare or
    // "backend/model" qualified). Resolved before routing, max 3 hops to
    // bound alias chains; a cycle resolves to the last hop with a WARN.
    Aliases map[string]string `mapstructure:"aliases"`
    // AllowCrossSupply toggles the historical fallback when a bare model
    // has a Supply list but none of its entries are currently eligible:
    // true (default/nil) = fall through to the routing chain (local-first,
    // fallback, model_mapping); false = 503-style miss instead of letting
    // an unlisted backend serve the name. Pointer so YAML-omitted = true.
    AllowCrossSupply *bool `mapstructure:"allow_cross_supply"`
}

// allowCrossSupply resolves the pointer default (nil = true).
func (m ModelsConfig) allowCrossSupply() bool {
    return m.AllowCrossSupply == nil || *m.AllowCrossSupply
}

// ResolvedModel is the outcome of ResolveModel: what the client asked for,
// the bare model id the engine/upstream see, and (when the client used a
// "backend/model" qualified form) the backend the request is pinned to.
type ResolvedModel struct {
    // Requested is the raw client-supplied model string (post-trim).
    Requested string
    // Bare is the model id after alias expansion and qualified-prefix
    // strip. This is what routing and the upstream provider see.
    Bare string
    // Backend is the pinned backend name from a qualified id, or "" when
    // the request carries no provider pin.
    Backend string
    // AliasHops records the alias chain walked (diagnostics/logging).
    AliasHops []string
}

// Qualified returns "<backend>/<bare>" when pinned, else Bare.
func (r ResolvedModel) Qualified() string {
    if r.Backend == "" {
        return r.Bare
    }
    return r.Backend + "/" + r.Bare
}

// maxAliasHops bounds alias chain resolution: alias→alias→alias is legal,
// the 4th hop stops expansion (cycle or misconfiguration guard).
const maxAliasHops = 3

// ResolveModel expands models.aliases and splits a "backend/model"
// qualified id. Pure function over the snapshot so handlers stay thin and
// tests are trivial. A slash prefix only counts as a qualified id when the
// prefix is exactly a configured backend name — model ids that happen to
// contain "/" (e.g. "deepseek-ai/DeepSeek-V3") pass through untouched.
func ResolveModel(raw string, backends map[string]BackendConfig, models ModelsConfig) ResolvedModel {
    cur := strings.TrimSpace(raw)
    res := ResolvedModel{Requested: cur, Bare: cur}

    for hop := 0; hop < maxAliasHops; hop++ {
        target, ok := models.Aliases[cur]
        if !ok || strings.TrimSpace(target) == "" || target == cur {
            break
        }
        res.AliasHops = append(res.AliasHops, cur+"→"+target)
        cur = strings.TrimSpace(target)
        res.Bare = cur
    }
    if _, ok := models.Aliases[cur]; ok {
        slog.Warn("models.aliases chain exceeded max hops or is cyclic, stopping at last hop",
            "model", raw, "resolved", cur, "hops", res.AliasHops)
    }

    if idx := strings.Index(cur, "/"); idx > 0 && idx < len(cur)-1 {
        prefix := cur[:idx]
        if _, ok := backends[prefix]; ok {
            res.Backend = prefix
            res.Bare = cur[idx+1:]
        }
    }
    return res
}

// vendorDefault is the per-type builtin backend template: the base_url and
// timeout a backends.<name> entry inherits when it omits them, so a new
// cloud backend needs only type + api_key_env (plan §13.3).
type vendorDefault struct {
    BaseURL string
    Timeout time.Duration
}

// vendorDefaults is the builtin vendor table. Resource/region-specific
// providers (bedrock, foundry) are absent on purpose — their base_url is
// deployment-specific and must be written explicitly.
var vendorDefaults = map[string]vendorDefault{
    "anthropic":  {"https://api.anthropic.com", 60 * time.Second},
    "openai":     {"https://api.openai.com/v1", 60 * time.Second},
    "deepseek":   {"https://api.deepseek.com/v1", 60 * time.Second},
    "dashscope":  {"https://dashscope.aliyuncs.com/compatible-mode/v1", 60 * time.Second},
    "baichuan":   {"https://api.baichuan-ai.com/v1", 60 * time.Second},
    "moonshot":   {"https://api.moonshot.cn/v1", 60 * time.Second},
    "minimax":    {"https://api.minimax.chat/v1", 60 * time.Second},
    "hunyuan":    {"https://api.hunyuan.cloud.tencent.com/v1", 60 * time.Second},
    "qianfan":    {"https://aip.baidubce.com/rpc/2.0/ai_custom/v1/wenxinworkshop", 120 * time.Second},
    "glm":        {"https://api.z.ai", 60 * time.Second},
    "lingyiwanwu": {"https://api.lingyiwanwu.com/v1", 60 * time.Second},
    "siliconflow": {"https://api.siliconflow.cn/v1", 60 * time.Second},
    "openrouter": {"https://openrouter.ai/api/v1", 60 * time.Second},
}

// normalizeBackends applies post-unmarshal normalization to every backend
// entry: (1) api_key_env resolution — the plaintext api_key never needs to
// live in config.yaml; (2) vendor base_url/timeout fill for entries that
// omit them. Called from both Load and Reload before validate, so
// hot-reloaded configs get the same treatment. Env resolution happens at
// load time, matching bindSecretEnv semantics (a running process picks up
// env changes only on reload — documented behavior).
func normalizeBackends(cfg *Config) {
    for name, be := range cfg.Backends {
        if be.APIKeyEnv != "" && strings.TrimSpace(be.APIKey) == "" {
            if v := strings.TrimSpace(os.Getenv(be.APIKeyEnv)); v != "" {
                be.APIKey = v
                slog.Info("backend api_key resolved from env", "backend", name, "env", be.APIKeyEnv)
            } else {
                slog.Warn("backend api_key_env set but env var is empty", "backend", name, "env", be.APIKeyEnv)
            }
        } else if be.APIKeyEnv == "" && strings.TrimSpace(be.APIKey) != "" && !strings.HasPrefix(be.APIKey, "enc:") {
            slog.Warn("backends."+name+".api_key is deprecated, use api_key_env (plaintext keys in config.yaml)", "backend", name)
        }
        if be.BaseURL == "" {
            if vd, ok := vendorDefaults[strings.ToLower(be.Type)]; ok {
                be.BaseURL = vd.BaseURL
                slog.Info("backend base_url filled from vendor defaults", "backend", name, "type", be.Type, "base_url", be.BaseURL)
            }
        }
        if be.Timeout == 0 {
            if vd, ok := vendorDefaults[strings.ToLower(be.Type)]; ok {
                be.Timeout = vd.Timeout
            }
        }
        cfg.Backends[name] = be
    }
}

// validateModels checks the models.* namespace: alias cycles/empties,
// supply entries pointing at unknown backends. WARNs are preferred over
// hard errors — a stale alias after a backend rename should not brick the
// gateway; the miss resolves to the raw name and the routing chain.
func validateModels(cfg *Config) error {
    for alias, target := range cfg.Models.Aliases {
        if strings.TrimSpace(alias) == "" || strings.TrimSpace(target) == "" {
            return fmt.Errorf("models.aliases: empty alias or target (%q -> %q)", alias, target)
        }
    }
    for model, backends := range cfg.Models.Supply {
        if strings.TrimSpace(model) == "" {
            return fmt.Errorf("models.supply: empty model name")
        }
        for _, b := range backends {
            if _, ok := cfg.Backends[strings.TrimSpace(b)]; !ok {
                slog.Warn("models.supply entry references unknown backend (ignored at runtime)",
                    "model", model, "backend", b)
            }
        }
    }
    return nil
}
