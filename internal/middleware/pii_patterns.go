package middleware

// #128: PII redaction pattern SSOT, vendored from fusion-guard fg-redact
// (crates/fg-redact/src/lib.rs Redactor::new defs). Guard's redaction patterns
// are hardcoded-only in fg-redact and never serialized via any IPC dump
// (guard.rules.dump carries authorization rules, not redaction patterns), so
// there is no runtime-fetch surface to consume — the patterns are vendored
// here as the single source on the gateway side. A drift test (pii_patterns_test.go)
// pins the count == 15 + the canonical name set so a guard-side add/remove is
// caught. When guard lands a guard.redact.patterns.dump contract (issue filed),
// the pii.guard_fetch flag can flip gateway to fetch+cache instead of vendoring.
//
// Regex strings are copied VERBATIM from lib.rs; Go regexp is RE2-compatible
// with the Rust regex crate (same syntax, no lookaround). Four patterns carry
// a Rust validator (regex crate has no lookaround) — ported to Go validators
// with signature func(content string, start, end int) bool, byte-indexed to
// match regexp FindStringIndex. Group-1-capture patterns (oauth_bearer,
// conn_string, password, secret_kv, env_kv, netrc, api_key-sub) keep guard
// semantics: the scan reports the pattern name on a match; the value (group 1
// when present) is the redaction target — out of scope for this scan-only
// checker, which reports detected names only (matches ScanText's contract).
//
// Order is priority order from lib.rs (long credentials + overlapping digit
// patterns precede id_number so a 40-char AWS secret or 13-19 digit card is
// not swallowed by the 17-digit id_number). First-accept wins on overlap.

import (
    "regexp"
    "strings"
)

// ValidatorFn mirrors guard's validator signature: (content, start, end) where
// start/end are byte offsets into content of the candidate match. Returns true
// when the candidate is a real secret/PII (e.g. Luhn check, octet ≤255), false
// to reject (false positive). nil = no validator (regex match alone suffices).
type ValidatorFn func(content string, start, end int) bool

// PatternDef mirrors guard's (name, pattern, optional_validator) tuple.
type PatternDef struct {
    Name      string
    Regex     string
    Validator ValidatorFn
}

// guardRedactPatterns is the vendored SSOT, in guard's priority order. Do NOT
// add/remove/reorder without aligning fg-redact — the drift test pins count +
// name set. Each entry is compiled once into a *regexp.Regexp at PIIChecker
// construction (pii.go loadFromSSOT).
var guardRedactPatterns = []PatternDef{
    {"private_key", `-----BEGIN [A-Z ]+PRIVATE KEY-----[\s\S]*?-----END [A-Z ]+PRIVATE KEY-----|ssh-(?:rsa|ed25519|ecdsa) [A-Za-z0-9+/=]+|"(?:d|p|q|k|n)":\s*"[^"]+"`, nil},
    {"jwt", `eyJ[A-Za-z0-9_-]+\.eyJ[A-Za-z0-9_-]+\.[A-Za-z0-9_-]+`, nil},
    {"oauth_bearer", `(?i)bearer\s+([A-Za-z0-9_\-\.=~:/+]+)`, nil},
    {"api_key", `(?i)(sk-[A-Za-z0-9]{20,}|sk-ant-[A-Za-z0-9_-]{20,}|AKIA[0-9A-Z]{16}|AIza[0-9A-Za-z_-]{35}|gh[pousr]_[A-Za-z0-9]{36}|glpat-[A-Za-z0-9_-]{20}|xox[baprs]-[A-Za-z0-9-]{10,}|sk_live_[A-Za-z0-9]{24,}|sk_test_[A-Za-z0-9]{24,}|ya29\.[A-Za-z0-9_\-]{20,}|api[_-]?key\s*[:=]\s*(\S+))`, nil},
    {"conn_string", `(?i)(?:postgres(?:ql)?|mongodb(?:\+srv)?|redis|mysql|amqp|ftp|sftp)://[^/\s:@]+:([^@/\s]+)@`, nil},
    {"password", `(?i)(?:password|passwd|pwd)\s*["']?\s*[:=]\s*["']?([^\s"']+)`, nil},
    {"secret_kv", `(?i)(?:secret|token|access[_-]?token)["']?\s*[:=]\s*["']?([^\s"']{6,})`, nil},
    {"env_kv", `(?m)^[A-Z][A-Z0-9_]{2,}=(\S+)`, nil},
    {"netrc", `(?i)password\s+(\S+)`, nil},
    {"email", `[a-zA-Z0-9._%+-]+@[a-zA-Z0-9.-]+\.[a-zA-Z]{2,}`, nil},
    {"ipv4", `\b(?:[0-9]{1,3}\.){3}[0-9]{1,3}\b`, validIPv4},
    {"aws_secret", `[A-Za-z0-9/+=]{40}`, validAWSSecret},
    {"credit_card", `\d{13,19}`, validLuhn},
    {"phone", `1[3-9]\d{9}`, validPhone},
    {"id_number", `\d{17}[\dXx]`, nil},
}

// validIPv4 (port of guard valid_ipv4): each octet ≤255 + boundary non-digit
// and non-dot (regex has no lookaround; boundary checked in code so 999.1.1.1
// is rejected and an ipv4 that is a sub-segment of a longer digit/dot run is
// rejected).
func validIPv4(content string, start, end int) bool {
    b := content
    if start > 0 {
        prev := b[start-1]
        if isAsciiDigit(prev) || prev == '.' {
            return false
        }
    }
    if end < len(b) {
        nxt := b[end]
        if isAsciiDigit(nxt) || nxt == '.' {
            return false
        }
    }
    for _, seg := range strings.Split(b[start:end], ".") {
        if len(seg) == 0 {
            return false
        }
        n := 0
        for _, c := range []byte(seg) {
            if !isAsciiDigit(c) {
                return false
            }
            n = n*10 + int(c-'0')
            if n > 255 {
                return false
            }
        }
    }
    return true
}

// validAWSSecret (port of guard valid_aws_secret): 40-char base64 charset +
// ≥6 distinct chars (rejects "aaaa…"/"////…" static strings) + boundary
// non-base64 (so the 40 chars are not a sub-segment of a longer run).
func validAWSSecret(content string, start, end int) bool {
    b := content
    isBase64 := func(c byte) bool {
        return isAsciiAlnum(c) || c == '/' || c == '+' || c == '='
    }
    if start > 0 && isBase64(b[start-1]) {
        return false
    }
    if end < len(b) && isBase64(b[end]) {
        return false
    }
    s := b[start:end]
    if len(s) != 40 {
        return false
    }
    distinct := make(map[byte]struct{}, 40)
    for i := 0; i < len(s); i++ {
        distinct[s[i]] = struct{}{}
    }
    return len(distinct) >= 6
}

// validLuhn (port of guard valid_luhn): boundary non-digit (so the 13-19 digits
// are not a sub-segment of a longer digit run, preventing swallow of
// id_number/phone) + Luhn checksum (confirm real card number, not arbitrary
// digit string — lowers false positive since 13-19 digits appear in non-payment
// contexts).
func validLuhn(content string, start, end int) bool {
    b := content
    if start > 0 && isAsciiDigit(b[start-1]) {
        return false
    }
    if end < len(b) && isAsciiDigit(b[end]) {
        return false
    }
    s := b[start:end]
    var digits []int
    for i := 0; i < len(s); i++ {
        c := s[i]
        if isAsciiDigit(c) {
            digits = append(digits, int(c-'0'))
        }
    }
    if len(digits) < 13 || len(digits) > 19 {
        return false
    }
    sum := 0
    double := false
    for i := len(digits) - 1; i >= 0; i-- {
        d := digits[i]
        if double {
            d *= 2
            if d > 9 {
                d -= 9
            }
        }
        sum += d
        double = !double
    }
    return sum%10 == 0
}

// validPhone (port of guard valid_phone): boundary non-digit, preventing
// swallow of a longer digit run (id_number sub-segment).
func validPhone(content string, start, end int) bool {
    b := content
    if start > 0 && isAsciiDigit(b[start-1]) {
        return false
    }
    if end < len(b) && isAsciiDigit(b[end]) {
        return false
    }
    return true
}

func isAsciiDigit(c byte) bool { return c >= '0' && c <= '9' }

func isAsciiAlnum(c byte) bool {
    return isAsciiDigit(c) || (c >= 'a' && c <= 'z') || (c >= 'A' && c <= 'Z')
}

// compileGuardPatterns compiles the SSOT into a []piiPattern (compiled regex +
// validator), preserving guard's priority order. Returns an error slice for any
// pattern that fails to compile (should never happen for the static set, but
// fail-closed: a broken pattern is logged + skipped, never panics). The
// ValidatorFn is threaded onto the piiPattern so a candidate match can be
// re-checked (matches guard collect_spans semantics: a candidate whose
// validator returns false is skipped).
func compileGuardPatterns() ([]piiPattern, []error) {
    out := make([]piiPattern, 0, len(guardRedactPatterns))
    var errs []error
    for _, def := range guardRedactPatterns {
        re, err := regexp.Compile(def.Regex)
        if err != nil {
            errs = append(errs, err)
            continue
        }
        out = append(out, piiPattern{name: def.Name, regex: re, validator: def.Validator})
    }
    return out, errs
}
