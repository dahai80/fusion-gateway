package cost

import "strings"

type ModelPricing struct {
    PromptPricePer1M     float64
    CompletionPricePer1M float64
}

var defaultPricing = map[string]ModelPricing{
    "gpt-4":                  {30.0, 60.0},
    "gpt-4-turbo":            {10.0, 30.0},
    "gpt-4o":                 {2.5, 10.0},
    "gpt-4o-mini":            {0.15, 0.60},
    "gpt-3.5-turbo":          {0.50, 1.50},
    "claude-3-opus":          {15.0, 75.0},
    "claude-3-sonnet":        {3.0, 15.0},
    "claude-3-haiku":         {0.25, 1.25},
    "claude-3.5-sonnet":      {3.0, 15.0},
    "claude-3.5-haiku":       {0.80, 4.0},
    "deepseek-chat":          {0.14, 0.28},
    "deepseek-reasoner":      {0.55, 2.19},
    "text-embedding-3-small": {0.02, 0.0},
    "text-embedding-3-large": {0.13, 0.0},
    "text-embedding-ada-002": {0.10, 0.0},
}

func CalculateCost(model string, promptTokens, completionTokens int) float64 {
    pricing, ok := lookupPricing(model)
    if !ok {
        return 0.0
    }

    promptCost := float64(promptTokens) / 1_000_000.0 * pricing.PromptPricePer1M
    completionCost := float64(completionTokens) / 1_000_000.0 * pricing.CompletionPricePer1M
    return promptCost + completionCost
}

func lookupPricing(model string) (ModelPricing, bool) {
    if p, ok := defaultPricing[model]; ok {
        return p, true
    }

    var bestKey string
    var bestPricing ModelPricing
    for key, pricing := range defaultPricing {
        if strings.HasPrefix(model, key) && len(key) > len(bestKey) {
            bestKey = key
            bestPricing = pricing
        }
    }

    if bestKey != "" {
        return bestPricing, true
    }

    return ModelPricing{}, false
}
