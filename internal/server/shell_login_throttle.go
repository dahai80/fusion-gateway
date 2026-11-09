package server

import (
    "log/slog"
    "net"
    "net/http"
    "strings"
    "sync"
    "time"
)

// Issue #587: POST /api/v1/shell/session is the gateway's only public
// authentication oracle (the shell plane is meant to be public behind nginx)
// and had no rate limiting, no lockout, no backoff — a single source could
// guess passwords indefinitely. This is the gateway-native inner belt (the
// outer belt is the reverse proxy's limit_req, deploy-side): a failed-login
// throttle that locks the source after shellLoginMaxFails failures inside
// the window, keyed BOTH by source IP (best-effort behind a trusted proxy —
// X-Forwarded-For first hop, else RemoteAddr; header-spoofable) and by
// username (not spoofable — catches single-target guessing no matter how the
// source address rotates). A successful login clears the source's counters.

const (
    shellLoginMaxFails   = 5
    shellLoginFailWindow = 5 * time.Minute
    shellLoginLockout    = 60 * time.Second
)

type shellLoginFail struct {
    count       int
    first       time.Time
    lockedUntil time.Time
}

type shellLoginThrottle struct {
    mu    sync.Mutex
    fails map[string]*shellLoginFail
    nowFn func() time.Time
}

func newShellLoginThrottle() *shellLoginThrottle {
    return &shellLoginThrottle{fails: map[string]*shellLoginFail{}, nowFn: time.Now}
}

func (t *shellLoginThrottle) now() time.Time {
    if t.nowFn != nil {
        return t.nowFn()
    }
    return time.Now()
}

// allowed reports whether both keys (ip and user) are outside lockout.
func (t *shellLoginThrottle) allowed(keys ...string) (bool, time.Duration) {
    t.mu.Lock()
    defer t.mu.Unlock()
    now := t.now()
    var wait time.Duration
    for _, k := range keys {
        f, ok := t.fails[k]
        if !ok {
            continue
        }
        if f.lockedUntil.After(now) {
            d := f.lockedUntil.Sub(now)
            if d > wait {
                wait = d
            }
        }
    }
    return wait == 0, wait
}

// recordFailure bumps both counters; the source locks when either reaches
// shellLoginMaxFails within the window.
func (t *shellLoginThrottle) recordFailure(ip, username string) {
    t.mu.Lock()
    defer t.mu.Unlock()
    now := t.now()
    locked := false
    for _, k := range []string{"ip:" + ip, "user:" + username} {
        f, ok := t.fails[k]
        if !ok || now.Sub(f.first) > shellLoginFailWindow {
            f = &shellLoginFail{first: now}
            t.fails[k] = f
        }
        f.count++
        if f.count >= shellLoginMaxFails {
            f.lockedUntil = now.Add(shellLoginLockout)
            locked = true
        }
    }
    if locked {
        slog.Warn("shell login lockout engaged: repeated failed logins", "ip", ip, "username", username,
            "max_fails", shellLoginMaxFails, "lockout", shellLoginLockout.String())
    }
    t.pruneLocked(now)
}

// recordSuccess clears both counters so a legitimate user's occasional typo
// never accumulates into a lockout across sessions.
func (t *shellLoginThrottle) recordSuccess(ip, username string) {
    t.mu.Lock()
    defer t.mu.Unlock()
    delete(t.fails, "ip:"+ip)
    delete(t.fails, "user:"+username)
}

// pruneLocked drops entries whose window and lockout have both elapsed so
// the map cannot grow without bound under distributed guessing.
func (t *shellLoginThrottle) pruneLocked(now time.Time) {
    for k, f := range t.fails {
        if now.Sub(f.first) > shellLoginFailWindow && !f.lockedUntil.After(now) {
            delete(t.fails, k)
        }
    }
}

// loginClientIP is the throttle's best-effort source identity. Behind the
// production reverse proxy RemoteAddr is the proxy itself, so the first
// X-Forwarded-For hop wins when present (spoofable — the per-username key
// carries the real weight; this key only adds an outer ring).
func loginClientIP(r *http.Request) string {
    if xff := r.Header.Get("X-Forwarded-For"); xff != "" {
        if idx := strings.Index(xff, ","); idx >= 0 {
            return strings.TrimSpace(xff[:idx])
        }
        return strings.TrimSpace(xff)
    }
    host, _, err := net.SplitHostPort(r.RemoteAddr)
    if err != nil {
        return r.RemoteAddr
    }
    return host
}
