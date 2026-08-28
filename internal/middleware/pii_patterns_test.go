package middleware

// #128 drift test: pins the vendored guard SSOT so an add/remove/rename on
// the guard side (fg-redact/src/lib.rs Redactor::new defs) is caught here
// before gateway ships a divergent pattern set. Count == 15 AND the name set
// == the 15 canonical names. A hash-of-regex would be brittle across
// regex-syntax tweaks; name-set + count is the durable guard.

import (
    "sort"
    "strings"
    "testing"
)

// canonicalGuardRedactNames is the 15-name set from fg-redact/src/lib.rs
// Redactor::new defs, in priority order. Single source of truth for the drift
// test — if guard adds/removes a pattern, update BOTH this list and
// guardRedactPatterns in lockstep (the test then enforces the alignment).
var canonicalGuardRedactNames = []string{
    "private_key",
    "jwt",
    "oauth_bearer",
    "api_key",
    "conn_string",
    "password",
    "secret_kv",
    "env_kv",
    "netrc",
    "email",
    "ipv4",
    "aws_secret",
    "credit_card",
    "phone",
    "id_number",
}

func TestPIIPatterns_CountIs15(t *testing.T) {
    if got, want := len(guardRedactPatterns), 15; got != want {
        t.Fatalf("drift: guardRedactPatterns count = %d, want %d (guard fg-redact has 15 defs)", got, want)
    }
}

func TestPIIPatterns_NameSetMatchesGuard(t *testing.T) {
    if len(guardRedactPatterns) != len(canonicalGuardRedactNames) {
        t.Fatalf("drift: guardRedactPatterns (%d) vs canonical list (%d) length mismatch — update both in lockstep",
            len(guardRedactPatterns), len(canonicalGuardRedactNames))
    }
    got := make([]string, len(guardRedactPatterns))
    for i, p := range guardRedactPatterns {
        got[i] = p.Name
    }
    want := append([]string(nil), canonicalGuardRedactNames...)
    sort.Strings(got)
    sort.Strings(want)
    for i := range got {
        if got[i] != want[i] {
            t.Fatalf("drift: pattern name mismatch at sorted[%d]: got %q want %q — guard fg-redact name set changed", i, got[i], want[i])
        }
    }
}

func TestPIIPatterns_PriorityOrderMatchesGuard(t *testing.T) {
    // Order matters (lib.rs comment: long credentials precede id_number so a
    // 40-char AWS secret / 13-19 digit card is not swallowed by the 17-digit
    // id_number). Assert the vendored order == guard's literal defs order.
    for i, want := range canonicalGuardRedactNames {
        if guardRedactPatterns[i].Name != want {
            t.Fatalf("drift: priority order broken at [%d]: got %q want %q — order must match guard fg-redact defs (first-accept wins on overlap)",
                i, guardRedactPatterns[i].Name, want)
        }
    }
}

func TestPIIPatterns_AllCompile(t *testing.T) {
    // Fail-closed: every vendored regex must compile under Go RE2. A pattern
    // that fails compiles is skipped at runtime (compileGuardPatterns returns
    // the error), but the SSOT must ship all-compileable — a compile failure
    // means the vendored string drifted from valid RE2.
    compiled, errs := compileGuardPatterns()
    if len(errs) > 0 {
        msgs := make([]string, len(errs))
        for i, e := range errs {
            msgs[i] = e.Error()
        }
        t.Fatalf("drift: %d guard SSOT pattern(s) failed to compile: %s", len(errs), strings.Join(msgs, "; "))
    }
    if len(compiled) != 15 {
        t.Fatalf("compiled %d patterns, want 15", len(compiled))
    }
}

func TestPIIPatterns_FourValidatorsAttached(t *testing.T) {
    // The 4 guard patterns that carry a Rust validator (regex crate has no
    // lookaround) must carry a Go validator here: ipv4, aws_secret,
    // credit_card, phone. A nil validator on these = silent false-positive
    // regression (999.1.1.1, non-Luhn digit runs, swallowed digit runs).
    wantValidators := map[string]bool{
        "ipv4":        true,
        "aws_secret":  true,
        "credit_card": true,
        "phone":       true,
    }
    for _, p := range guardRedactPatterns {
        if wantValidators[p.Name] && p.Validator == nil {
            t.Fatalf("drift: pattern %q must carry a Go validator (guard has one — regex has no lookaround)", p.Name)
        }
    }
    // Conversely, the other 11 must NOT carry one (guard has none for them).
    for _, p := range guardRedactPatterns {
        if !wantValidators[p.Name] && p.Validator != nil {
            t.Fatalf("drift: pattern %q has a validator but guard has none for it", p.Name)
        }
    }
}
