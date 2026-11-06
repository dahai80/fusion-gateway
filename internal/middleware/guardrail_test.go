package middleware

import (
    "testing"

    "github.com/fusion-gateway/fusion-gateway/internal/config"
)

func TestGuardrailChainNilWhenEmpty(t *testing.T) {
    gc := NewGuardrailChain(nil, config.PIIConfig{})
    if gc != nil {
        t.Fatal("empty guardrails list must not activate the chain")
    }
    if deny, detected := gc.ScanText("email@example.com"); deny || detected != nil {
        t.Fatal("nil chain must be a no-op")
    }
}

func TestGuardrailChainAllDisabledNil(t *testing.T) {
    gc := NewGuardrailChain([]config.GuardrailConfig{
        {Name: "off", Type: "pii", Enabled: false},
    }, config.PIIConfig{Enabled: false})
    if gc != nil {
        t.Fatal("all-disabled list must not activate the chain")
    }
}

func TestGuardrailChainPiiLinkDenies(t *testing.T) {
    gc := NewGuardrailChain([]config.GuardrailConfig{
        {Name: "g-pii", Type: "pii", Action: "deny", Enabled: true},
    }, config.PIIConfig{Enabled: false, Patterns: []config.PIIPattern{
        {Name: "test_email", Regex: `[a-z]+@example\.com`},
    }})
    if gc.Len() != 1 {
        t.Fatalf("expected 1 active link, got %d", gc.Len())
    }
    deny, detected := gc.ScanText("contact user@example.com now")
    if !deny {
        t.Fatal("deny link must deny on match")
    }
    // The vendored SSOT patterns (e.g. email) also match — the custom
    // pattern must be among the detections, not necessarily first.
    found := false
    for _, d := range detected {
        if d == "test_email" {
            found = true
        }
    }
    if !found {
        t.Fatalf("custom pattern missing from detections %v", detected)
    }
    if deny2, _ := gc.ScanText("clean text"); deny2 {
        t.Fatal("clean text must pass")
    }
}

func TestGuardrailChainLogLinkPassesThrough(t *testing.T) {
    gc := NewGuardrailChain([]config.GuardrailConfig{
        {Name: "g-log", Type: "pii", Action: "log", Enabled: true},
    }, config.PIIConfig{Enabled: false, Patterns: []config.PIIPattern{
        {Name: "test_email", Regex: `[a-z]+@example\.com`},
    }})
    deny, detected := gc.ScanText("contact user@example.com now")
    if deny {
        t.Fatal("log link must not deny")
    }
    if len(detected) == 0 {
        t.Fatal("log link must still report detections")
    }
}

func TestGuardrailChainDenyWinsOverEarlierLinks(t *testing.T) {
    patterns := config.PIIConfig{Enabled: false, Patterns: []config.PIIPattern{
        {Name: "test_email", Regex: `[a-z]+@example\.com`},
    }}
    gc := NewGuardrailChain([]config.GuardrailConfig{
        {Name: "first-log", Type: "pii", Action: "log", Enabled: true},
        {Name: "second-deny", Type: "pii", Action: "deny", Enabled: true},
    }, patterns)
    deny, detected := gc.ScanText("user@example.com")
    if !deny {
        t.Fatal("deny link must deny even after a log link matched")
    }
    if len(detected) == 0 {
        t.Fatal("detections before the deny must be reported")
    }
}

func TestGuardrailChainDisabledLinkSkipped(t *testing.T) {
    gc := NewGuardrailChain([]config.GuardrailConfig{
        {Name: "off", Type: "pii", Action: "deny", Enabled: false},
        {Name: "on", Type: "pii", Action: "log", Enabled: true},
    }, config.PIIConfig{Enabled: false, Patterns: []config.PIIPattern{
        {Name: "test_email", Regex: `[a-z]+@example\.com`},
    }})
    if gc.Len() != 1 {
        t.Fatalf("disabled link must not be active, got %d", gc.Len())
    }
    deny, _ := gc.ScanText("user@example.com")
    if deny {
        t.Fatal("disabled deny link must not deny")
    }
}

func TestGuardrailChainLinkActionOverridesPiiAction(t *testing.T) {
    gc := NewGuardrailChain([]config.GuardrailConfig{
        {Name: "g", Type: "pii", Action: "log", Enabled: true},
    }, config.PIIConfig{Enabled: true, Action: "deny", Patterns: []config.PIIPattern{
        {Name: "test_email", Regex: `[a-z]+@example\.com`},
    }})
    deny, _ := gc.ScanText("user@example.com")
    if deny {
        t.Fatal("link action log must override pii.action deny")
    }
}

func TestGuardrailChainPiiLinkWorksWithPiiDisabled(t *testing.T) {
    // Chain mode: the pii link's presence is the explicit enablement —
    // pii.enabled=false must not silently neuter the chain.
    gc := NewGuardrailChain([]config.GuardrailConfig{
        {Name: "g", Type: "pii", Action: "deny", Enabled: true},
    }, config.PIIConfig{Enabled: false, Patterns: []config.PIIPattern{
        {Name: "test_email", Regex: `[a-z]+@example\.com`},
    }})
    deny, detected := gc.ScanText("user@example.com")
    if !deny || len(detected) == 0 {
        t.Fatal("pii link must work while pii.enabled is false")
    }
}

func TestGuardrailChainUnknownTypeSkipsWithWarn(t *testing.T) {
    // Defense in depth: Validate rejects unknown types, the chain skip+warns
    // instead of panicking if one ever lands.
    gc := NewGuardrailChain([]config.GuardrailConfig{
        {Name: "future", Type: "llama-guard", Action: "deny", Enabled: true},
    }, config.PIIConfig{})
    if gc == nil {
        t.Fatal("chain with unknown-type link must still construct")
    }
    if deny, _ := gc.ScanText("anything"); deny {
        t.Fatal("unknown-type link must be skipped, not deny")
    }
}
