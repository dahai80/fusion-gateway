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
    name      string
    regex     *regexp.Regexp
    validator ValidatorFn
}

func NewPIIChecker(cfg config.PIIConfig) *PIIChecker {
    if !cfg.Enabled {
        return nil
    }

    action := cfg.Action
    if action == "" {
        action = "log"
    }

    // #128: load the 15 vendored guard SSOT patterns (priority order) as the
    // base set, replacing the prior 6 hardcoded builtins that had drifted from
    // guard (gateway-only ssn/phone_us; guard-only jwt/oauth_bearer/api_key/
    // conn_string/password/secret_kv/env_kv/netrc/aws_secret/private_key/
    // id_number). See pii_patterns.go for the SSOT + drift test.
    patterns, errs := compileGuardPatterns()
    for _, err := range errs {
        slog.Warn("PII guard SSOT pattern compile failed", "error", err)
    }

    // User-supplied patterns are appended AFTER the SSOT (override/extend
    // hook preserved): a user pattern with the same Name appends a second
    // entry (both run), a new Name extends detection. Validators on user
    // patterns are nil (regex match alone).
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
        // #128: a pattern with a validator (ipv4/aws_secret/credit_card/phone)
        // must re-check each candidate with the guard-ported validator — a
        // candidate the validator rejects (e.g. 999.1.1.1 for ipv4, non-Luhn
        // digit run for credit_card) is a false positive and skipped, matching
        // guard collect_spans semantics. Patterns without a validator: a plain
        // regex match suffices (MatchString fast-path).
        if p.validator == nil {
            if p.regex.MatchString(text) {
                detected = append(detected, p.name)
            }
            continue
        }
        // N5 (audit NICE): the prior FindStringIndex returned only the FIRST
        // candidate. A text with a leading false positive (validator rejects)
        // followed by a real match (validator accepts) was NOT detected — the
        // scan stopped at the first candidate, saw it rejected, and missed
        // the real one later in the text. FindAllStringIndex iterates every
        // candidate so a real match anywhere in the text is caught. Append
        // the name on the FIRST validator-accepting candidate (one name per
        // pattern, matching the detected-names contract — not one per match).
        locs := p.regex.FindAllStringIndex(text, -1)
        for _, loc := range locs {
            if p.validator(text, loc[0], loc[1]) {
                detected = append(detected, p.name)
                break
            }
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
