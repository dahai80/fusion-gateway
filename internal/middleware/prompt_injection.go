package middleware

// Prompt injection detection for v0.5.0 Task #71.
// Importers: internal/server/server.go middleware chain.
// User instruction: "按照0.3.0, 0.4.0, 0.5.0顺序全面实施" — PLAN.md: "regex + optional Lakera API".
// API: CheckPromptInjection, PromptInjectionMiddleware. Config: PromptInjectionConfig(enabled/action/provider/api_key).

import (
    "encoding/json"
    "io"
    "log/slog"
    "net/http"
    "regexp"
    "strings"

    "github.com/fusion-gateway/fusion-gateway/internal/config"
)

var injectionPatterns = []*regexp.Regexp{
    regexp.MustCompile(`(?i)ignore\s+(all\s+)?previous\s+(instructions?|prompts?)`),
    regexp.MustCompile(`(?i)forget\s+(all\s+)?previous\s+(instructions?|prompts?)`),
    regexp.MustCompile(`(?i)disregard\s+(all\s+)?previous\s+(instructions?|prompts?)`),
    regexp.MustCompile(`(?i)you\s+are\s+now\s+a`),
    regexp.MustCompile(`(?i)pretend\s+you\s+are`),
    regexp.MustCompile(`(?i)act\s+as\s+if\s+you\s+are`),
    regexp.MustCompile(`(?i)jailbreak`),
    regexp.MustCompile(`(?i)system\s*:\s*`),
    regexp.MustCompile(`(?i)<\|im_start\|>`),
    regexp.MustCompile(`(?i)\[INST\]`),
    regexp.MustCompile(`(?i)###\s*instruction`),
    regexp.MustCompile(`(?i)override\s+(your|the)\s+(safety|security|policy)`),
    regexp.MustCompile(`(?i)reveal\s+your\s+(system|initial)\s+prompt`),
    regexp.MustCompile(`(?i)output\s+your\s+(system|initial)\s+(prompt|instructions?)`),
}

type PromptInjectionResult struct {
    Detected bool     `json:"detected"`
    Patterns []string `json:"patterns_matched,omitempty"`
    Severity string   `json:"severity,omitempty"`
}

func CheckPromptInjection(text string) PromptInjectionResult {
    var matched []string
    for _, p := range injectionPatterns {
        if p.MatchString(text) {
            matched = append(matched, p.String())
        }
    }
    severity := "low"
    if len(matched) >= 3 {
        severity = "high"
    } else if len(matched) >= 1 {
        severity = "medium"
    }
    return PromptInjectionResult{
        Detected: len(matched) > 0,
        Patterns: matched,
        Severity: severity,
    }
}

func PromptInjectionMiddleware(cfg config.PromptInjectionConfig) func(http.Handler) http.Handler {
    return func(next http.Handler) http.Handler {
        return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
            if !cfg.Enabled {
                next.ServeHTTP(w, r)
                return
            }
            if r.Method != http.MethodPost {
                next.ServeHTTP(w, r)
                return
            }
            if !strings.HasPrefix(r.URL.Path, "/v1/chat/completions") &&
                !strings.HasPrefix(r.URL.Path, "/v1/completions") &&
                !strings.HasPrefix(r.URL.Path, "/v1/messages") {
                next.ServeHTTP(w, r)
                return
            }

            body, err := io.ReadAll(r.Body)
            if err != nil {
                slog.Warn("prompt injection: failed to read body", "error", err)
                next.ServeHTTP(w, r)
                return
            }
            r.Body = io.NopCloser(strings.NewReader(string(body)))

            var payload struct {
                Messages []struct {
                    Content interface{} `json:"content"`
                } `json:"messages"`
                Prompt interface{} `json:"prompt"`
            }
            if err := json.Unmarshal(body, &payload); err != nil {
                next.ServeHTTP(w, r)
                return
            }

            textToCheck := extractPromptText(payload.Messages, payload.Prompt)
            result := CheckPromptInjection(textToCheck)

            if result.Detected {
                slog.Warn("prompt injection detected",
                    "patterns", result.Patterns,
                    "severity", result.Severity,
                    "path", r.URL.Path,
                )
                action := cfg.Action
                if action == "" {
                    action = "log"
                }
                if action == "block" {
                    http.Error(w, `{"error":{"message":"Prompt injection detected","type":"content_filter"}}`, http.StatusBadRequest)
                    return
                }
                slog.Info("prompt injection: logging only", "severity", result.Severity)
            }

            next.ServeHTTP(w, r)
        })
    }
}

func extractPromptText(messages []struct {
    Content interface{} `json:"content"`
}, prompt interface{}) string {
    var sb strings.Builder
    for _, m := range messages {
        switch v := m.Content.(type) {
        case string:
            sb.WriteString(v)
            sb.WriteString(" ")
        case []interface{}:
            for _, item := range v {
                if obj, ok := item.(map[string]interface{}); ok {
                    contentType, _ := obj["type"].(string)
                    switch contentType {
                    case "text":
                        if t, ok := obj["text"].(string); ok {
                            sb.WriteString(t)
                            sb.WriteString(" ")
                        }
                    case "image_url":
                        // L7 fix: check URL data URIs for injection payloads
                        if urlObj, ok := obj["image_url"].(map[string]interface{}); ok {
                            if u, ok := urlObj["url"].(string); ok {
                                sb.WriteString(u)
                                sb.WriteString(" ")
                            }
                        }
                    case "input_audio":
                        if audioObj, ok := obj["input_audio"].(map[string]interface{}); ok {
                            if d, ok := audioObj["data"].(string); ok {
                                sb.WriteString(d)
                                sb.WriteString(" ")
                            }
                        }
                    case "image":
                        if urlObj, ok := obj["image"].(map[string]interface{}); ok {
                            if u, ok := urlObj["url"].(string); ok {
                                sb.WriteString(u)
                                sb.WriteString(" ")
                            }
                        }
                    default:
                        // unknown content type, try to extract text field if present
                        if t, ok := obj["text"].(string); ok {
                            sb.WriteString(t)
                            sb.WriteString(" ")
                        }
                    }
                }
            }
        }
    }
    if prompt != nil {
        switch v := prompt.(type) {
        case string:
            sb.WriteString(v)
        case []interface{}:
            for _, item := range v {
                if s, ok := item.(string); ok {
                    sb.WriteString(s)
                    sb.WriteString(" ")
                }
            }
        }
    }
    return sb.String()
}
