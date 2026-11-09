package server

import (
    "net/http"
    "net/http/httptest"
    "strings"
    "testing"
    "time"
)

// Issue #587: POST /api/v1/shell/session is the gateway's only public
// authentication oracle and had no rate limiting, no lockout, no backoff —
// 10 consecutive bad-password logins got ten clean 401s. A failed-login
// throttle now locks the source (per-IP AND per-username) after 5 failures
// and answers 429 with the shell error envelope until the lockout elapses.

func TestShellLoginThrottle_LocksAfterFiveFailures(t *testing.T) {
    s := newShellTestServer(t, false)
    for i := 0; i < 5; i++ {
        code, _ := loginShell(t, s, "admin", "wrong-password")
        if code != http.StatusUnauthorized {
            t.Fatalf("failure %d must stay 401, got %d", i+1, code)
        }
    }
    code, resp := loginShell(t, s, "admin", "s3cret")
    if code != http.StatusTooManyRequests {
        t.Fatalf("6th attempt (even with the CORRECT password) must be locked out with 429 (#587), got %d", code)
    }
    if resp["code"] != "rate_limited" {
        t.Fatalf("429 envelope code = %v, want rate_limited", resp["code"])
    }
    if retryable, ok := resp["retryable"].(bool); !ok || !retryable {
        t.Fatalf("429 envelope retryable = %v, want true", resp["retryable"])
    }
}

func TestShellLoginThrottle_SuccessResetsCounter(t *testing.T) {
    s := newShellTestServer(t, false)
    for i := 0; i < 4; i++ {
        loginShell(t, s, "admin", "wrong-password")
    }
    if code, _ := loginShell(t, s, "admin", "s3cret"); code != http.StatusOK {
        t.Fatalf("4 failures must not lock yet, got %d", code)
    }
    // Counter reset by the success: 4 more failures stay under the cap.
    for i := 0; i < 4; i++ {
        if code, _ := loginShell(t, s, "admin", "wrong-password"); code != http.StatusUnauthorized {
            t.Fatalf("post-reset failure %d must be 401, got %d", i+1, code)
        }
    }
    if code, _ := loginShell(t, s, "admin", "s3cret"); code != http.StatusOK {
        t.Fatalf("success after reset + 4 failures must work, got %d", code)
    }
}

func TestShellLoginThrottle_UsernameIsolation(t *testing.T) {
    s := newShellTestServer(t, false)
    for i := 0; i < 5; i++ {
        loginShell(t, s, "admin", "wrong-password")
    }
    // admin is locked; alice logging in from a DIFFERENT source (XFF hop,
    // what the trusted proxy supplies in production) must not be locked by
    // admin's failures — one targeted account cannot lock out other clients.
    body := `{"username":"alice","password":"alicepw"}`
    req := httptest.NewRequest(http.MethodPost, "/api/v1/shell/session", strings.NewReader(body))
    req.Header.Set("X-Forwarded-For", "203.0.113.9")
    w := httptest.NewRecorder()
    s.handleShellSessionIssue(w, req)
    if w.Code != http.StatusOK {
        t.Fatalf("unrelated source must not be locked by admin's failures, got %d: %s", w.Code, w.Body.String())
    }
}

func TestShellLoginThrottle_LockoutExpires(t *testing.T) {
    s := newShellTestServer(t, false)
    s.shellLoginThrottle.nowFn = func() time.Time { return time.Now().Add(2 * time.Minute) }
    for i := 0; i < 5; i++ {
        loginShell(t, s, "admin", "wrong-password")
    }
    if code, _ := loginShell(t, s, "admin", "s3cret"); code != http.StatusTooManyRequests {
        t.Fatalf("locked out now, got %d", code)
    }
    // Advance the fake clock past the 60s lockout: the source is allowed again.
    s.shellLoginThrottle.nowFn = func() time.Time { return time.Now().Add(3 * time.Minute) }
    if code, _ := loginShell(t, s, "admin", "s3cret"); code != http.StatusOK {
        t.Fatalf("lockout must expire and allow login again, got %d", code)
    }
}
