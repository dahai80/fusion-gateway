package middleware

import (
    "log/slog"
    "net/http"
    "regexp"
    "strings"
    "sync"
    "time"

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

    action := normalizePIIAction(cfg.Action)

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

// normalizePIIAction maps a configured action onto the three the checker can
// actually carry out. Previously an unrecognized value (the live config used
// "block") fell through ScanText's switch to log-only while the per-request
// WARN still printed action=block — the operator believed requests were being
// blocked when nothing was. Unknown values now say so once, at boot.
func normalizePIIAction(raw string) string {
    action := strings.ToLower(strings.TrimSpace(raw))
    switch action {
    case "":
        return "log"
    case "log", "deny":
        return action
    case "mask":
        slog.Warn("pii.action=mask is accepted but masking is not implemented — detections are logged only and request text is forwarded unchanged")
        return action
    default:
        slog.Warn("pii.action is not a recognized value, falling back to log-only (no request will be blocked)",
            "configured", raw,
            "valid", "log, deny, mask",
        )
        return "log"
    }
}

// Detection is logged at most once per piiLogInterval. With the guard SSOT
// patterns active almost every coding request carries an email or an IPv4
// literal, so an unthrottled WARN fired on every single turn of a Claude Code
// session and buried the lines that matter.
const piiLogInterval = 60 * time.Second

var (
    piiLogMu       sync.Mutex
    piiLogLast     time.Time
    piiLogSuppress int
)

func logPIIDetection(action string, detected []string) {
    piiLogMu.Lock()
    now := time.Now()
    if now.Sub(piiLogLast) < piiLogInterval {
        piiLogSuppress++
        piiLogMu.Unlock()
        return
    }
    suppressed := piiLogSuppress
    piiLogSuppress = 0
    piiLogLast = now
    piiLogMu.Unlock()

    // Message states what the code does with the request, not just the config
    // token: log and mask both forward the text unchanged.
    if action == "deny" {
        slog.Warn("PII detected in request, request rejected",
            "types", strings.Join(detected, ","),
            "action", action,
            "suppressed_since_last", suppressed,
        )
        return
    }
    slog.Warn("PII detected in request, forwarded unchanged",
        "types", strings.Join(detected, ","),
        "action", action,
        "suppressed_since_last", suppressed,
    )
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
        logPIIDetection(pm.checker.action, detected)
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

// guardScan holds the lazily compiled guard SSOT patterns for persistence
// stamping (G6): contains_pii must be marked regardless of the pii config —
// PIIConfig.Enabled gates request handling (deny/mask), not evidence.
var (
    guardScanOnce     sync.Once
    guardScanPatterns []piiPattern
)

// ScanGuardPII reports whether the text matches any guard SSOT pattern.
// Pure detection, no action semantics; used by the persistence layer to
// stamp contains_pii on log rows.
func ScanGuardPII(text string) bool {
    guardScanOnce.Do(func() {
        patterns, errs := compileGuardPatterns()
        for _, err := range errs {
            slog.Warn("PII guard SSOT pattern compile failed", "error", err)
        }
        guardScanPatterns = patterns
    })
    if len(text) == 0 {
        return false
    }
    for _, p := range guardScanPatterns {
        if p.validator == nil {
            if p.regex.MatchString(text) {
                return true
            }
            continue
        }
        // Same N5 semantics as ScanText: a validated pattern only counts
        // when some candidate passes its validator.
        for _, loc := range p.regex.FindAllStringIndex(text, -1) {
            if p.validator(text, loc[0], loc[1]) {
                return true
            }
        }
    }
    return false
}
