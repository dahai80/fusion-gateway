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
    // Chinese LLM providers (CNY/1M tokens, approximate)
    "qwen-turbo":            {0.30, 0.60},
    "qwen-plus":             {0.80, 2.00},
    "qwen-max":              {2.00, 6.00},
    "qwen-long":             {0.50, 2.00},
    "moonshot-v1":           {1.20, 1.20},
    "glm-4":                 {1.50, 1.50},
    "glm-4-flash":           {0.10, 0.10},
    "glm-4-plus":            {5.00, 5.00},
    "glm-4-long":            {1.00, 1.00},
    "MiniMax-Text-01":       {1.00, 1.00},
    "abab6.5s-chat":         {0.40, 0.40},
    "abab6.5-chat":          {0.60, 0.60},
    "Baichuan4":             {1.20, 1.20},
    "Baichuan3-Turbo":       {0.40, 0.40},
    "hunyuan-lite":          {0.075, 0.075},
    "hunyuan-standard":      {0.80, 0.80},
    "hunyuan-pro":           {3.00, 3.00},
    "hunyuan-turbo":         {1.20, 1.20},
    "step-1":                {0.80, 0.80},
    "step-2":                {1.60, 1.60},
    "yi-lightning":          {0.10, 0.10},
    "yi-large":              {2.00, 2.00},
    "yi-medium":             {0.50, 0.50},
    "yi-spark":              {0.10, 0.10},
    "text-embedding-v3":     {0.70, 0.0},
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
    // Custom pricing takes priority
    if globalCustomPricing != nil {
        if p, ok := globalCustomPricing.Lookup(model); ok {
            return p, true
        }
    }

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
