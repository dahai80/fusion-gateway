package server

import (
    "encoding/json"
    "net/http"
    "net/http/httptest"
    "strings"
    "testing"
    "time"

    "github.com/fusion-gateway/fusion-gateway/internal/config"
)

// Issue #592: the shell login plane gains a server-side CAPTCHA contract for
// fusion-web-shell #139 — an unauthenticated SVG challenge endpoint plus a
// pre-credential verification gate on POST /api/v1/shell/session.

func captchaTestServer(t *testing.T, enabled bool) *Server {
    t.Helper()
    s := newShellTestServer(t, false)
    s.cfg.Config.Shell.Captcha = config.ShellCaptchaConfig{Enabled: enabled, TTL: 5 * time.Minute}
    s.setupShell()
    return s
}

func challengeGet(t *testing.T, s *Server) (int, map[string]any, http.Header) {
    t.Helper()
    req := httptest.NewRequest(http.MethodGet, "/api/v1/shell/session/captcha", nil)
    w := httptest.NewRecorder()
    s.handleShellCaptchaChallenge(w, req)
    var resp map[string]any
    _ = json.Unmarshal(w.Body.Bytes(), &resp)
    return w.Code, resp, w.Header()
}

func loginWithCaptcha(s *Server, username, password, captchaID, captchaCode string) (int, map[string]any) {
    body, _ := json.Marshal(map[string]string{
        "username": username, "password": password,
        "captcha_id": captchaID, "captcha_code": captchaCode,
    })
    req := httptest.NewRequest(http.MethodPost, "/api/v1/shell/session", strings.NewReader(string(body)))
    w := httptest.NewRecorder()
    s.handleShellSessionIssue(w, req)
    var resp map[string]any
    _ = json.Unmarshal(w.Body.Bytes(), &resp)
    return w.Code, resp
}

func TestShellCaptcha_ChallengeEndpointUnauthenticatedNoStore(t *testing.T) {
    s := captchaTestServer(t, true)
    code, resp, hdr := challengeGet(t, s)
    if code != http.StatusOK {
        t.Fatalf("challenge endpoint must answer 200 unauthenticated, got %d: %v", code, resp)
    }
    if hdr.Get("Cache-Control") != "no-store" {
        t.Fatalf("challenge response must be Cache-Control: no-store, got %q", hdr.Get("Cache-Control"))
    }
    id, _ := resp["captcha_id"].(string)
    if len(id) < 16 {
        t.Fatalf("captcha_id missing or too short: %v", resp["captcha_id"])
    }
    img, _ := resp["image"].(string)
    if !strings.HasPrefix(img, "data:image/svg+xml;base64,") {
        t.Fatalf("image must be a base64 SVG data URI, got prefix %q", img[:min(40, len(img))])
    }
    if ttl, _ := resp["ttl_seconds"].(float64); ttl != 300 {
        t.Fatalf("ttl_seconds = %v, want 300 (config TTL)", resp["ttl_seconds"])
    }
}

func TestShellCaptcha_MissingChallengeRejectedBeforeCredentials(t *testing.T) {
    s := captchaTestServer(t, true)
    // CORRECT password, no captcha fields: must NOT reach credential
    // verification — a 401 invalid_credentials would prove the gate is after
    // the check; captcha_missing proves it runs first.
    code, resp := loginWithCaptcha(s, "admin", "s3cret", "", "")
    if code != http.StatusBadRequest || resp["code"] != "captcha_missing" {
        t.Fatalf("missing challenge must be 400 captcha_missing BEFORE credential check, got %d %v", code, resp)
    }
}

func TestShellCaptcha_MismatchRejectedBeforeCredentials(t *testing.T) {
    s := captchaTestServer(t, true)
    _, ch, _ := challengeGet(t, s)
    id, _ := ch["captcha_id"].(string)
    code, resp := loginWithCaptcha(s, "admin", "s3cret", id, "WRONG1")
    if code != http.StatusBadRequest || resp["code"] != "captcha_mismatch" {
        t.Fatalf("wrong code with correct password must be 400 captcha_mismatch, got %d %v", code, resp)
    }
}

func TestShellCaptcha_BurnedChallengeReplayRejected(t *testing.T) {
    s := captchaTestServer(t, true)
    _, ch, _ := challengeGet(t, s)
    id, _ := ch["captcha_id"].(string)
    // First attempt consumes (burns) the challenge whatever the outcome…
    code, _ := loginWithCaptcha(s, "admin", "s3cret", id, "WRONG1")
    if code != http.StatusBadRequest {
        t.Fatalf("first attempt must be 400, got %d", code)
    }
    // …a replay of the SAME id (even with the right code now known) is dead.
    code, resp := loginWithCaptcha(s, "admin", "s3cret", id, "WRONG1")
    if code != http.StatusBadRequest || resp["code"] != "captcha_expired" {
        t.Fatalf("burned challenge replay must be 400 captcha_expired, got %d %v", code, resp)
    }
}

func TestShellCaptcha_ExpiredChallengeRejected(t *testing.T) {
    s := captchaTestServer(t, true)
    _, ch, _ := challengeGet(t, s)
    id, _ := ch["captcha_id"].(string)
    s.shellCaptcha.nowFn = func() time.Time { return time.Now().Add(6 * time.Minute) }
    code, resp := loginWithCaptcha(s, "admin", "s3cret", id, "WRONG1")
    if code != http.StatusBadRequest || resp["code"] != "captcha_expired" {
        t.Fatalf("expired challenge must be 400 captcha_expired, got %d %v", code, resp)
    }
}

func TestShellCaptcha_CorrectChallengeLogsIn(t *testing.T) {
    s := captchaTestServer(t, true)
    _, ch, _ := challengeGet(t, s)
    id, _ := ch["captcha_id"].(string)
    s.shellCaptcha.mu.Lock()
    code := s.shellCaptcha.challenges[id].code
    s.shellCaptcha.mu.Unlock()
    loginCode, resp := loginWithCaptcha(s, "admin", "s3cret", id, code)
    if loginCode != http.StatusOK {
        t.Fatalf("correct challenge + correct password must log in, got %d: %v", loginCode, resp)
    }
}

func TestShellCaptcha_DisabledPlaneUnchanged(t *testing.T) {
    s := captchaTestServer(t, false)
    if code, resp := loginShell(t, s, "admin", "s3cret"); code != http.StatusOK {
        t.Fatalf("captcha disabled must not gate login, got %d: %v", code, resp)
    }
}

func TestShellCaptcha_ChallengePrunedBounded(t *testing.T) {
    s := captchaTestServer(t, true)
    for i := 0; i < 500; i++ {
        challengeGet(t, s)
    }
    s.shellCaptcha.nowFn = func() time.Time { return time.Now().Add(6 * time.Minute) }
    challengeGet(t, s)
    s.shellCaptcha.mu.Lock()
    n := len(s.shellCaptcha.challenges)
    s.shellCaptcha.mu.Unlock()
    if n > 1 {
        t.Fatalf("expired challenges must be pruned, %d remain", n)
    }
}
