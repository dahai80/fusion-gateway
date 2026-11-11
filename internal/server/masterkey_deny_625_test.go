package server

// #625: a rejected master key on a protected endpoint logged only
// "invalid master_key for protected endpoint" — no fingerprint of the
// presented credential and no pointer to the boot-time "auth.master_key
// resolved" line, so an operator could not tell a typo from the documented
// env-override (FG_MASTER_KEY / FUSION_ADMIN_KEY beats the config-file
// value, #611). The deny branch now logs a short non-secret fingerprint of
// the presented key plus that hint; diff it against the boot log to see
// WHICH credential the runtime expects.

import (
    "bytes"
    "log/slog"
    "net/http"
    "net/http/httptest"
    "strings"
    "testing"
)

func Test625_MasterKeyDeny_LogsPresentedFingerprint(t *testing.T) {
    var buf bytes.Buffer
    prev := slog.Default()
    slog.SetDefault(slog.New(slog.NewTextHandler(&buf, nil)))
    defer slog.SetDefault(prev)

    s := newTestServer()
    s.cfg.Config.Auth.MasterKey = "the-real-runtime-master-key"
    h := s.withMasterKey(func(w http.ResponseWriter, r *http.Request) {
        t.Fatal("wrong key must not pass")
    })
    req := httptest.NewRequest(http.MethodGet, "/metrics", nil)
    req.Header.Set("Authorization", "Bearer operator-presented-wrong-key")
    rec := httptest.NewRecorder()
    h(rec, req)

    if rec.Code != http.StatusUnauthorized {
        t.Fatalf("code = %d, want 401", rec.Code)
    }
    out := buf.String()
    if !strings.Contains(out, "invalid master_key for protected endpoint") {
        t.Fatalf("deny log missing: %s", out)
    }
    if !strings.Contains(out, "presented_fingerprint=") {
        t.Fatalf("deny log must carry the presented credential fingerprint: %s", out)
    }
    if !strings.Contains(out, "auth.master_key resolved") {
        t.Fatalf("deny log must point at the boot resolve line: %s", out)
    }
}
