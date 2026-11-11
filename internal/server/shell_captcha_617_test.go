package server

import (
    "encoding/json"
    "net/http"
    "net/http/httptest"
    "strings"
    "testing"
)

// #617: web_shell.captcha.enabled is captured once at boot — a config
// reload that flips it never reaches the challenge endpoints or the login
// gates, so operators must restart to change the setting. The enabled flag
// must be consulted live (config snapshot) on every challenge/login; the
// store is always built so a boot-off → reload-on transition works too.

func swapCaptcha617(t *testing.T, s *Server, enabled bool) {
    t.Helper()
    cfg2 := *s.cfg
    cfg2.Config.Shell.Captcha.Enabled = enabled
    s.cfgMu.Lock()
    s.cfg = &cfg2
    s.cfgMu.Unlock()
}

func getCaptcha617(t *testing.T, h http.Handler, path string) (bool, int) {
    t.Helper()
    req := httptest.NewRequest(http.MethodGet, path, nil)
    rec := httptest.NewRecorder()
    h.ServeHTTP(rec, req)
    if rec.Code != http.StatusOK {
        return false, rec.Code
    }
    var body struct {
        Enabled bool `json:"enabled"`
    }
    _ = json.Unmarshal(rec.Body.Bytes(), &body)
    return body.Enabled, rec.Code
}

func Test617_CaptchaDisableViaReload(t *testing.T) {
    s := newShellTestServer(t, false)
    s.cfg.Config.Shell.Captcha.Enabled = true
    s.cfg.Config.AuthPortal.Enabled = true
    s.setupShell()
    mux := http.NewServeMux()
    s.registerShellRoutes(mux)
    s.registerAuthPortalRoutes(mux)

    if enabled, code := getCaptcha617(t, mux, "/api/v1/shell/session/captcha"); code != 200 || !enabled {
        t.Fatalf("pre-reload shell captcha must be enabled, got %d enabled=%v", code, enabled)
    }
    if enabled, code := getCaptcha617(t, mux, "/auth/api/captcha"); code != 200 || !enabled {
        t.Fatalf("pre-reload portal captcha must be enabled, got %d enabled=%v", code, enabled)
    }

    // Reload path: pointer swap under cfgMu, same as RebuildMiddlewareChain.
    swapCaptcha617(t, s, false)

    if enabled, code := getCaptcha617(t, mux, "/api/v1/shell/session/captcha"); code != 200 || enabled {
        t.Fatalf("post-reload shell captcha must answer disabled, got %d enabled=%v", code, enabled)
    }
    if enabled, code := getCaptcha617(t, mux, "/auth/api/captcha"); code != 200 || enabled {
        t.Fatalf("post-reload portal captcha must answer disabled, got %d enabled=%v", code, enabled)
    }

    // The shell login gate must skip the challenge entirely after the
    // reload — captcha_* rejection means the gate is still armed.
    issue := postJSON613(t, mux, "/api/v1/shell/session",
        `{"username":"admin","password":"s3cret"}`)
    if issue.Code != http.StatusOK {
        t.Fatalf("login after disable must not require captcha: %d %s", issue.Code, issue.Body.String())
    }
}

func Test617_CaptchaEnableViaReload_BootOff(t *testing.T) {
    s := newShellTestServer(t, false)
    s.cfg.Config.Shell.Captcha.Enabled = false
    s.cfg.Config.AuthPortal.Enabled = true
    s.setupShell()
    mux := http.NewServeMux()
    s.registerShellRoutes(mux)
    s.registerAuthPortalRoutes(mux)

    if enabled, code := getCaptcha617(t, mux, "/auth/api/captcha"); code != 200 || enabled {
        t.Fatalf("boot-off captcha must answer disabled, got %d enabled=%v", code, enabled)
    }

    swapCaptcha617(t, s, true)

    if enabled, code := getCaptcha617(t, mux, "/auth/api/captcha"); code != 200 || !enabled {
        t.Fatalf("post-reload captcha must be armed (store always built), got %d enabled=%v", code, enabled)
    }
    // Gate armed: a login without a valid challenge is rejected.
    issue := postJSON613(t, mux, "/api/v1/shell/session",
        `{"username":"admin","password":"s3cret"}`)
    if issue.Code != http.StatusBadRequest || !strings.Contains(issue.Body.String(), "captcha") {
        t.Fatalf("login after enable must require captcha: %d %s", issue.Code, issue.Body.String())
    }
}
