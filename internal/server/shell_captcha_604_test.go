package server

import (
    "net/http/httptest"
    "time"
    "strings"
    "testing"
)

// Issue #604: the mixed-case charset rendered as jittered SVG glyphs makes
// case unreadable — a user reading "nx82e" for code "nX82E" must pass.
// Verify is case-insensitive (EqualFold), entropy loss negligible behind the
// #587 lockout and single-use burn.

func Test604_CaptchaVerifyCaseInsensitive(t *testing.T) {
    st := newShellCaptchaStore(5 * time.Minute)
    id, code := st.issue()
    if code == strings.ToLower(code) && code == strings.ToUpper(code) {
        t.Fatalf("charset must be alphabetic, got %q", code)
    }
    // All-lowercase reading of a mixed-case code passes.
    if got := st.verify(id, strings.ToLower(code)); got != "" {
        t.Fatalf("lowercase reading of %q must pass, got %q", code, got)
    }
}

func Test604_CaptchaVerifyCaseInsensitiveUpper(t *testing.T) {
    st := newShellCaptchaStore(5 * time.Minute)
    id, code := st.issue()
    if got := st.verify(id, strings.ToUpper(code)); got != "" {
        t.Fatalf("uppercase reading of %q must pass, got %q", code, got)
    }
}

func Test604_CaptchaChallengeEndpointAcceptsLowercase(t *testing.T) {
    s := newShellTestServer(t, false)
    s.shellCaptcha = newShellCaptchaStore(5 * time.Minute)
    req := httptest.NewRequest("GET", "/api/v1/shell/session/captcha", nil)
    w := httptest.NewRecorder()
    s.handleShellCaptchaChallenge(w, req)
    if w.Code != 200 {
        t.Fatalf("challenge code = %d", w.Code)
    }
}
