package middleware

import (
    "testing"

    "github.com/fusion-gateway/fusion-gateway/internal/config"
)

func TestNewPIIChecker_ReturnsNilWhenDisabled(t *testing.T) {
    t.Parallel()
    cfg := config.PIIConfig{Enabled: false}
    checker := NewPIIChecker(cfg)
    if checker != nil {
        t.Fatal("expected nil when PII is disabled")
    }
}

func TestScanText_DetectsEmail(t *testing.T) {
    t.Parallel()
    cfg := config.PIIConfig{Enabled: true, Action: "deny"}
    mw := NewPIIMiddleware(cfg)
    deny, types := mw.ScanText("contact me at user@example.com please")
    if !deny {
        t.Error("expected deny=true for email with action=deny")
    }
    found := false
    for _, tp := range types {
        if tp == "email" {
            found = true
            break
        }
    }
    if !found {
        t.Errorf("expected 'email' in detected types, got %v", types)
    }
}

func TestScanText_DetectsPhoneCN(t *testing.T) {
    t.Parallel()
    cfg := config.PIIConfig{Enabled: true, Action: "deny"}
    mw := NewPIIMiddleware(cfg)
    deny, types := mw.ScanText("my number is 13912345678 call me")
    if !deny {
        t.Error("expected deny=true for phone_cn with action=deny")
    }
    found := false
    for _, tp := range types {
        if tp == "phone_cn" {
            found = true
            break
        }
    }
    if !found {
        t.Errorf("expected 'phone_cn' in detected types, got %v", types)
    }
}

func TestScanText_ReturnsFalseForCleanText(t *testing.T) {
    t.Parallel()
    cfg := config.PIIConfig{Enabled: true, Action: "deny"}
    mw := NewPIIMiddleware(cfg)
    deny, types := mw.ScanText("hello world, this is a clean message")
    if deny {
        t.Error("expected deny=false for clean text")
    }
    if len(types) != 0 {
        t.Errorf("expected no detected types, got %v", types)
    }
}

func TestScanText_DenyActionReturnsTrue(t *testing.T) {
    t.Parallel()
    cfg := config.PIIConfig{Enabled: true, Action: "deny"}
    mw := NewPIIMiddleware(cfg)
    deny, _ := mw.ScanText("email: test@foo.com")
    if !deny {
        t.Error("expected deny=true when action=deny and PII found")
    }
}

func TestScanText_LogActionReturnsFalseButStillDetects(t *testing.T) {
    t.Parallel()
    cfg := config.PIIConfig{Enabled: true, Action: "log"}
    mw := NewPIIMiddleware(cfg)
    deny, types := mw.ScanText("email: test@foo.com")
    if deny {
        t.Error("expected deny=false when action=log")
    }
    if len(types) == 0 {
        t.Error("expected detected types even when action=log")
    }
    found := false
    for _, tp := range types {
        if tp == "email" {
            found = true
            break
        }
    }
    if !found {
        t.Errorf("expected 'email' in detected types, got %v", types)
    }
}

func TestScanText_CustomPattern(t *testing.T) {
    t.Parallel()
    cfg := config.PIIConfig{
        Enabled: true,
        Action:  "deny",
        Patterns: []config.PIIPattern{
            {Name: "emp_id", Regex: `EMP-\d{5}`},
        },
    }
    mw := NewPIIMiddleware(cfg)
    deny, types := mw.ScanText("employee EMP-12345 is here")
    if !deny {
        t.Error("expected deny=true for custom pattern match")
    }
    found := false
    for _, tp := range types {
        if tp == "emp_id" {
            found = true
            break
        }
    }
    if !found {
        t.Errorf("expected 'emp_id' in detected types, got %v", types)
    }
}

func TestScanText_NilCheckerReturnsFalse(t *testing.T) {
    t.Parallel()
    cfg := config.PIIConfig{Enabled: false}
    mw := NewPIIMiddleware(cfg)
    deny, types := mw.ScanText("user@example.com 13912345678")
    if deny {
        t.Error("expected deny=false when checker is nil")
    }
    if types != nil {
        t.Errorf("expected nil types when checker is nil, got %v", types)
    }
}
