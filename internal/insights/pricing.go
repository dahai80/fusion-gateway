// Read-time model pricing (#564): pricing.model_pricing maps model-name
// prefixes to USD-per-Mtok rates; the insights aggregations price token sums
// at query time, falling back to the stored per-request cost column for
// unmatched models. Pure functions over the config map — the Service keeps
// an atomic snapshot updated on startup and hot reload.
package insights

import (
    "log/slog"

    "github.com/fusion-gateway/fusion-gateway/internal/config"
)

// ModelPrice mirrors config.ModelPrice for the query layer.
type ModelPrice = config.ModelPrice

// pricingSnapshot is the atomic holder on Service (nil map = no pricing).
type pricingSnapshot struct {
    table map[string]ModelPrice
}

// priceFor returns the longest-prefix pricing entry for a model name. An
// empty table or unmatched model reports ok=false.
func priceFor(table map[string]ModelPrice, model string) (ModelPrice, bool) {
    best, bestLen := ModelPrice{}, -1
    for prefix, p := range table {
        if len(prefix) > bestLen && len(prefix) > 0 && len(model) >= len(prefix) && model[:len(prefix)] == prefix {
            best, bestLen = p, len(prefix)
        }
    }
    if bestLen < 0 {
        return ModelPrice{}, false
    }
    return best, true
}

// costFor prices one aggregate row: input/output token sums at the matched
// rates; the stored cost sum when no pricing entry matches.
func costFor(table map[string]ModelPrice, model string, inputTokens, outputTokens int64, storedCost float64) float64 {
    if p, ok := priceFor(table, model); ok {
        return float64(inputTokens)/1e6*p.InputPerMTok + float64(outputTokens)/1e6*p.OutputPerMTok
    }
    return storedCost
}

// SetPricing swaps the pricing table (startup + config hot reload). Nil
// clears it — all rows fall back to stored cost.
func (s *Service) SetPricing(table map[string]ModelPrice) {
    s.pricing.Store(&pricingSnapshot{table: table})
    if len(table) == 0 {
        slog.Info("insights pricing cleared: cost falls back to stored column")
        return
    }
    slog.Info("insights pricing applied", "models", len(table))
}

// pricingTable reads the current snapshot.
func (s *Service) pricingTable() map[string]ModelPrice {
    if snap := s.pricing.Load(); snap != nil {
        return snap.table
    }
    return nil
}
