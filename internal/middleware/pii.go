package middleware

import (
    "log/slog"
    "net/http"
    "regexp"
    "strings"

    "github.com/fusion-gateway/fusion-gateway/internal/config"
)

type PIIChecker struct {
    patterns []piiPattern
    action   string
}

type piiPattern struct {
    name  string
    regex *regexp.Regexp
}

func NewPIIChecker(cfg config.PIIConfig) *PIIChecker {
    if !cfg.Enabled {
        return nil
    }

    action := cfg.Action
    if action == "" {
        action = "log"
    }

    patterns := make([]piiPattern, 0)
    builtins := map[string]string{
        "email":       `[a-zA-Z0-9._%+\-]+@[a-zA-Z0-9.\-]+\.[a-zA-Z]{2,}`,
        "phone_cn":    `1[3-9]\d{9}`,
        "phone_us":    `\b\d{3}[-.]?\d{3}[-.]?\d{4}\b`,
        "credit_card": `\b\d{4}[-\s]?\d{4}[-\s]?\d{4}[-\s]?\d{4}\b`,
        "ssn":         `\b\d{3}-\d{2}-\d{4}\b`,
        "ip_v4":       `\b\d{1,3}\.\d{1,3}\.\d{1,3}\.\d{1,3}\b`,
    }

    for name, re := range builtins {
        if compiled, err := regexp.Compile(re); err == nil {
            patterns = append(patterns, piiPattern{name: name, regex: compiled})
        }
    }

    for _, p := range cfg.Patterns {
        if compiled, err := regexp.Compile(p.Regex); err == nil {
            patterns = append(patterns, piiPattern{name: p.Name, regex: compiled})
        } else {
            slog.Warn("PII pattern compile failed", "name", p.Name, "error", err)
        }
    }

    return &PIIChecker{patterns: patterns, action: action}
}

type PIIMiddleware struct {
    checker *PIIChecker
}

func NewPIIMiddleware(cfg config.PIIConfig) *PIIMiddleware {
    return &PIIMiddleware{checker: NewPIIChecker(cfg)}
}

func (pm *PIIMiddleware) Handler(next http.Handler) http.Handler {
    return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
        if pm.checker == nil {
            next.ServeHTTP(w, r)
            return
        }

        next.ServeHTTP(w, r)
    })
}

func (pm *PIIMiddleware) ScanText(text string) (bool, []string) {
    if pm.checker == nil {
        return false, nil
    }

    var detected []string
    for _, p := range pm.checker.patterns {
        if p.regex.MatchString(text) {
            detected = append(detected, p.name)
        }
    }

    if len(detected) > 0 {
        slog.Warn("PII detected in request",
            "types", strings.Join(detected, ","),
            "action", pm.checker.action,
        )
        switch pm.checker.action {
        case "deny":
            return true, detected
        case "mask":
            return false, detected
        default:
            return false, detected
        }
    }

    return false, nil
}
