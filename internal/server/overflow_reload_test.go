package server

import (
    "testing"
    "time"

    "github.com/fusion-gateway/fusion-gateway/internal/config"
)

// Issue #541: a reload that disables overflow (or zeroes the cooldown) must
// clear the armed cooldown window — otherwise the stale state keeps diverting
// local traffic to cloud while the operator believes overflow is off, and the
// window resurrects diversion when overflow is re-enabled later.
func TestRebuildMiddlewareChain_DisabledOverflowClearsCooldown(t *testing.T) {
    s := newTestServer()
    s.overflow.armCooldown(time.Now(), 2*time.Minute)
    if !s.overflow.cooldownActive(time.Now()) {
        t.Fatal("precondition: cooldown should be armed")
    }

    disabled := &config.ConfigSnapshot{Config: config.DefaultConfig()}
    disabled.Config.Routing.Fallback.Overflow.Enabled = false
    s.RebuildMiddlewareChain(disabled)
    if s.overflow.cooldownActive(time.Now()) {
        t.Fatal("cooldown must be cleared when overflow is disabled by reload")
    }

    s.overflow.armCooldown(time.Now(), 2*time.Minute)
    zeroed := &config.ConfigSnapshot{Config: config.DefaultConfig()}
    zeroed.Config.Routing.Fallback.Overflow.Enabled = true
    s.RebuildMiddlewareChain(zeroed)
    if s.overflow.cooldownActive(time.Now()) {
        t.Fatal("cooldown must be cleared when the reload zeroes the cooldown")
    }
}
