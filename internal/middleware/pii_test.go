package middleware

import (
    "log/slog"
    "net/http"
    "net/http/httptest"
    "testing"

    "github.com/fusion-gateway/fusion-gateway/internal/config"
)

func TestNewPIIChecker_Disabled(t *testing.T) {
    slog.Info("test NewPIIChecker_Disabled")
    checker := NewPIIChecker(config.PIIConfig{Enabled: false})
    if checker != nil {
        t.Error("expected nil for disabled PII")
    }
}

func TestNewPIIChecker_Enabled(t *testing.T) {
    slog.Info("test NewPIIChecker_Enabled")
    checker := NewPIIChecker(config.PIIConfig{Enabled: true})
    if checker == nil {
        t.Fatal("expected non-nil checker")
    }
}

func TestNewPIIChecker_DefaultAction(t *testing.T) {
    slog.Info("test NewPIIChecker_DefaultAction")
    checker := NewPIIChecker(config.PIIConfig{Enabled: true})
    if checker.action != "log" {
        t.Errorf("expected log, got %s", checker.action)
    }
}

func TestNewPIIChecker_CustomAction(t *testing.T) {
    slog.Info("test NewPIIChecker_CustomAction")
    checker := NewPIIChecker(config.PIIConfig{Enabled: true, Action: "deny"})
    if checker.action != "deny" {
        t.Errorf("expected deny, got %s", checker.action)
    }
}

func TestNewPIIChecker_CustomPatterns(t *testing.T) {
    slog.Info("test NewPIIChecker_CustomPatterns")
    checker := NewPIIChecker(config.PIIConfig{
        Enabled: true,
        Patterns: []config.PIIPattern{
            {Name: "custom", Regex: `test\d+`},
        },
    })
    if checker == nil {
        t.Fatal("expected checker")
    }
}

func TestNewPIIChecker_InvalidPattern(t *testing.T) {
    slog.Info("test NewPIIChecker_InvalidPattern")
    checker := NewPIIChecker(config.PIIConfig{
        Enabled: true,
        Patterns: []config.PIIPattern{
            {Name: "bad", Regex: `[invalid`},
        },
    })
    if checker == nil {
        t.Fatal("expected checker even with invalid pattern")
    }
}

func TestPIIMiddleware_Handler_NilChecker(t *testing.T) {
    slog.Info("test PIIMiddleware_Handler_NilChecker")
    pm := NewPIIMiddleware(config.PIIConfig{Enabled: false})
    var called bool
    handler := pm.Handler(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
        called = true
        w.WriteHeader(http.StatusOK)
    }))
    req := httptest.NewRequest(http.MethodPost, "/v1/chat/completions", nil)
    rec := httptest.NewRecorder()
    handler.ServeHTTP(rec, req)
    if !called {
        t.Error("next handler should be called when checker is nil")
    }
}

func TestPIIMiddleware_Handler_Enabled(t *testing.T) {
    slog.Info("test PIIMiddleware_Handler_Enabled")
    pm := NewPIIMiddleware(config.PIIConfig{Enabled: true})
    var called bool
    handler := pm.Handler(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
        called = true
        w.WriteHeader(http.StatusOK)
    }))
    req := httptest.NewRequest(http.MethodPost, "/v1/chat/completions", nil)
    rec := httptest.NewRecorder()
    handler.ServeHTTP(rec, req)
    if !called {
        t.Error("next handler should be called")
    }
}

func TestPIIMiddleware_ScanText_NilChecker(t *testing.T) {
    slog.Info("test PIIMiddleware_ScanText_NilChecker")
    pm := NewPIIMiddleware(config.PIIConfig{Enabled: false})
    detected, types := pm.ScanText("user@example.com")
    if detected {
        t.Error("should not detect with nil checker")
    }
    if types != nil {
        t.Error("should return nil types with nil checker")
    }
}

func TestPIIMiddleware_ScanText_NoPII(t *testing.T) {
    slog.Info("test PIIMiddleware_ScanText_NoPII")
    pm := NewPIIMiddleware(config.PIIConfig{Enabled: true})
    detected, types := pm.ScanText("hello world")
    if detected {
        t.Error("should not detect PII in clean text")
    }
    if types != nil {
        t.Errorf("expected nil types, got %v", types)
    }
}

func TestPIIMiddleware_ScanText_Email(t *testing.T) {
    slog.Info("test PIIMiddleware_ScanText_Email")
    pm := NewPIIMiddleware(config.PIIConfig{Enabled: true})
    _, types := pm.ScanText("contact user@example.com for info")
    if len(types) == 0 {
        t.Error("should detect email")
    }
    found := false
    for _, t := range types {
        if t == "email" {
            found = true
        }
    }
    if !found {
        t.Error("expected email type in results")
    }
}

func TestPIIMiddleware_ScanText_Deny(t *testing.T) {
    slog.Info("test PIIMiddleware_ScanText_Deny")
    pm := NewPIIMiddleware(config.PIIConfig{Enabled: true, Action: "deny"})
    detected, _ := pm.ScanText("user@example.com")
    if !detected {
        t.Error("should detect and return true for deny action")
    }
}

func TestPIIMiddleware_ScanText_Mask(t *testing.T) {
    slog.Info("test PIIMiddleware_ScanText_Mask")
    pm := NewPIIMiddleware(config.PIIConfig{Enabled: true, Action: "mask"})
    detected, _ := pm.ScanText("user@example.com")
    if detected {
        t.Error("mask action should return false for detected")
    }
}

func TestPIIMiddleware_ScanText_PhoneCN(t *testing.T) {
    slog.Info("test PIIMiddleware_ScanText_PhoneCN")
    pm := NewPIIMiddleware(config.PIIConfig{Enabled: true})
    _, types := pm.ScanText("call 13912345678 for info")
    found := false
    for _, t := range types {
        if t == "phone" {
            found = true
        }
    }
    if !found {
        t.Errorf("expected guard-aligned phone type, got %v", types)
    }
}

func TestPIIMiddleware_ScanText_IPv4(t *testing.T) {
    slog.Info("test PIIMiddleware_ScanText_IPv4")
    pm := NewPIIMiddleware(config.PIIConfig{Enabled: true})
    _, types := pm.ScanText("server at 192.168.1.1 is down")
    found := false
    for _, t := range types {
        if t == "ipv4" {
            found = true
        }
    }
    if !found {
        t.Errorf("expected guard-aligned ipv4 type, got %v", types)
    }
}

// TestPIIMiddleware_ScanText_ValidatorSecondCandidate (N5): a pattern with a
// validator must scan EVERY candidate, not just the first. "999.1.1.1" is a
// regex match for ipv4 but validIPv4 rejects it (octet >255). Before N5 the
// scan stopped at the first candidate (FindStringIndex) → saw it rejected →
// never reached the real "10.0.0.1" later in the text → false negative. With
// FindAllStringIndex the second candidate is checked and detected.
func TestPIIMiddleware_ScanText_ValidatorSecondCandidate(t *testing.T) {
    slog.Info("test PIIMiddleware_ScanText_ValidatorSecondCandidate")
    pm := NewPIIMiddleware(config.PIIConfig{Enabled: true})
    _, types := pm.ScanText("bad 999.1.1.1 then real 10.0.0.1 here")
    found := false
    for _, tn := range types {
        if tn == "ipv4" {
            found = true
        }
    }
    if !found {
        t.Errorf("N5: validator must scan past a rejected first candidate; expected ipv4 for 10.0.0.1, got %v", types)
    }
}

func TestPIIMiddleware_Handler_WithPII_Deny(t *testing.T) {
    slog.Info("test PIIMiddleware_Handler_WithPII_Deny")
    pm := NewPIIMiddleware(config.PIIConfig{Enabled: true, Action: "deny"})
    called := false
    handler := pm.Handler(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
        called = true
        w.WriteHeader(http.StatusOK)
    }))
    req := httptest.NewRequest(http.MethodPost, "/v1/chat/completions", nil)
    rec := httptest.NewRecorder()
    handler.ServeHTTP(rec, req)
    if !called {
        t.Error("Handler should always call next — PII Handler is passthrough")
    }
}

func TestPIIMiddleware_Handler_WithPII_Log(t *testing.T) {
    slog.Info("test PIIMiddleware_Handler_WithPII_Log")
    pm := NewPIIMiddleware(config.PIIConfig{Enabled: true, Action: "log"})
    called := false
    handler := pm.Handler(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
        called = true
        w.WriteHeader(http.StatusOK)
    }))
    req := httptest.NewRequest(http.MethodPost, "/v1/chat/completions", nil)
    rec := httptest.NewRecorder()
    handler.ServeHTTP(rec, req)
    if !called {
        t.Error("Handler should call next regardless of action")
    }
}

func TestPIIMiddleware_ScanText_CreditCard(t *testing.T) {
    slog.Info("test PIIMiddleware_ScanText_CreditCard")
    pm := NewPIIMiddleware(config.PIIConfig{Enabled: true})
    // #128: guard credit_card = \d{13,19} + Luhn validator. Guard does not
    // strip separators, so a contiguous 16-digit Luhn-valid card is detected
    // (4111 4111 4111 4111 is a canonical Luhn-valid test PAN). The prior
    // gateway-only pattern matched hyphen-split groups; guard-aligned SSOT
    // matches the bare contiguous run + validates Luhn.
    _, types := pm.ScanText("card number 4111111111111111 on file")
    found := false
    for _, t := range types {
        if t == "credit_card" {
            found = true
        }
    }
    if !found {
        t.Errorf("expected credit_card type, got %v", types)
    }
}

func TestPIIMiddleware_ScanText_CreditCard_RejectsNonLuhn(t *testing.T) {
    slog.Info("test PIIMiddleware_ScanText_CreditCard_RejectsNonLuhn")
    pm := NewPIIMiddleware(config.PIIConfig{Enabled: true})
    // 13 digits, NOT Luhn-valid → validator rejects (guard valid_luhn). A bare
    // digit run that is not a real card number must not false-positive.
    _, types := pm.ScanText("order id 1234567890123 on file")
    for _, tn := range types {
        if tn == "credit_card" {
            t.Fatalf("non-Luhn digit run must NOT be flagged credit_card (guard valid_luhn rejects), got %v", types)
        }
    }
}

func TestPIIMiddleware_ScanText_SSN(t *testing.T) {
    slog.Info("test PIIMiddleware_ScanText_SSN")
    pm := NewPIIMiddleware(config.PIIConfig{Enabled: true})
    // #128: ssn was a gateway-only pattern NOT in guard fg-redact; removed to
    // align to the SSOT. Assert it is no longer detected (operator who needs
    // ssn detection adds it via pii.patterns user-supplied set).
    _, types := pm.ScanText("SSN is 123-45-6789")
    for _, tn := range types {
        if tn == "ssn" {
            t.Errorf("ssn is not in the guard SSOT and must not be detected; got %v", types)
        }
    }
}

func TestPIIMiddleware_ScanText_PhoneUS(t *testing.T) {
    slog.Info("test PIIMiddleware_ScanText_PhoneUS")
    pm := NewPIIMiddleware(config.PIIConfig{Enabled: true})
    // #128: phone_us was gateway-only, not in guard; removed. Guard phone =
    // 1[3-9]\d{9} (CN, leading 1, 11 digits) — 555-123-4567 (10 digits, leads
    // with 5) does not match. Assert phone_us is no longer detected.
    _, types := pm.ScanText("call 555-123-4567 for info")
    for _, tn := range types {
        if tn == "phone_us" {
            t.Errorf("phone_us is not in the guard SSOT and must not be detected; got %v", types)
        }
    }
}
