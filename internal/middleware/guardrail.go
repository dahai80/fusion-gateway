package middleware

// G4: pluggable guardrail chain (insight/fusion-gateway-vs-litellm-insight-1106.md).
// Before G4 the only guardrail was the vendored PII checker, hardcoded as a
// single scan point (scanPIIOrDeny). The chain makes the governance surface
// config-driven: an ordered list of links (guardrails: in config.yaml), each
// with a type (today: "pii", backed by the same SSOT pattern set), an action
// (log / deny / mask), and an enable flag. The vendor PII single-point
// behavior is preserved verbatim when the list is empty — the chain only
// activates when the operator explicitly configures it.

import (
    "log/slog"

    "github.com/fusion-gateway/fusion-gateway/internal/config"
)

// GuardrailLink is one resolved chain entry.
type GuardrailLink struct {
    Name   string
    Type   string
    Action string
}

// GuardrailChain scans text through ordered links. Scan semantics match the
// legacy PII middleware: deny short-circuits, log/mask collect and continue.
type GuardrailChain struct {
    links []GuardrailLink
    // piiChecker backs every type=pii link (patterns compiled once; the
    // pattern set is the vendored guard SSOT + pii.patterns extensions).
    piiChecker *PIIChecker
}

// NewGuardrailChain builds the chain from config. A nil/empty list returns
// nil — callers fall back to the legacy single-point PII middleware.
func NewGuardrailChain(links []config.GuardrailConfig, piiCfg config.PIIConfig) *GuardrailChain {
    if len(links) == 0 {
        return nil
    }
    chain := &GuardrailChain{}
    for _, l := range links {
        if !l.Enabled {
            continue
        }
        action := l.Action
        if action == "" {
            action = piiCfg.Action
        }
        action = normalizePIIAction(action)
        chain.links = append(chain.links, GuardrailLink{Name: l.Name, Type: l.Type, Action: action})
    }
    if len(chain.links) == 0 {
        return nil
    }
    for _, l := range chain.links {
        if l.Type == "pii" {
            // Chain mode: the pii link's presence is the explicit enablement
            // — patterns load even when pii.enabled is false (the legacy
            // flag gates the standalone single-point behavior, not the chain).
            on := piiCfg
            on.Enabled = true
            chain.piiChecker = NewPIIChecker(on)
            break
        }
    }
    return chain
}

// Len reports the active link count (0 when nil).
func (gc *GuardrailChain) Len() int {
    if gc == nil {
        return 0
    }
    return len(gc.links)
}

// ScanText runs the chain. Returns (deny, detected): deny=true when a
// deny-action link matched — the caller must short-circuit with a 400.
// detected accumulates matched type names across links for observability.
func (gc *GuardrailChain) ScanText(text string) (bool, []string) {
    if gc == nil {
        return false, nil
    }
    var detected []string
    deny := false
    for _, l := range gc.links {
        var matched []string
        switch l.Type {
        case "pii":
            matched = gc.piiChecker.Detect(text)
        default:
            // Unknown types are rejected at config.Validate; this branch is
            // defense in depth (fail-closed would break the request path —
            // skip + WARN keeps availability while surfacing the config bug).
            slog.Warn("guardrail: unknown link type, skipping", "name", l.Name, "type", l.Type)
            continue
        }
        if len(matched) == 0 {
            continue
        }
        detected = append(detected, matched...)
        if l.Action == "deny" {
            logPIIDetection("deny", matched)
            slog.Warn("guardrail link denied request", "name", l.Name, "type", l.Type, "types", matched)
            return true, detected
        }
        logPIIDetection(l.Action, matched)
    }
    return deny, detected
}
