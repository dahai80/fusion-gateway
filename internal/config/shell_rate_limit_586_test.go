package config

import "testing"

// shell586Cfg returns a config that satisfies the pre-existing web_shell
// mandatory checks (origin allowlist, session signing) so each test isolates
// exactly the rate-limit rule under test.
func shell586Cfg() Config {
    cfg := DefaultConfig()
    cfg.Shell.Enabled = true
    cfg.Shell.EntryOriginAllowlist = []string{"http://127.0.0.1:11432"}
    cfg.Auth.MasterKey = "test-master-key-586-sufficiently-long"
    return cfg
}

// #586: enabling the shell usage gate without an RPM cap must fail closed —
// an "unlimited by typo" default is exactly the footgun the issue reports
// (rpm: 0 silently meant unlimited on the key plane).
func TestValidate_ShellRateLimitEnabledRequiresRPM(t *testing.T) {
    cfg := shell586Cfg()
    cfg.Shell.RateLimit = ShellRateLimitConfig{Enabled: true, RPM: 0}
    err := validate(&cfg)
    if err == nil {
        t.Fatalf("rate_limit.enabled with rpm=0 must be rejected (fail closed, #586)")
    }
    want := "web_shell.rate_limit.enabled is true but web_shell.rate_limit.rpm is 0"
    if err.Error()[:len(want)] != want {
        t.Fatalf("error = %q, want prefix %q", err.Error(), want)
    }

    cfg.Shell.RateLimit.RPM = 30
    if err := validate(&cfg); err != nil {
        t.Fatalf("positive rpm must validate: %v", err)
    }
}

func TestValidate_ShellRateLimitNegativeRejected(t *testing.T) {
    cfg := shell586Cfg()
    cfg.Shell.RateLimit = ShellRateLimitConfig{Enabled: true, RPM: 10, TPM: -1}
    if err := validate(&cfg); err == nil {
        t.Fatalf("negative tpm must be rejected")
    }
}

func TestValidate_ShellRateLimitDisabledUnlimitedOK(t *testing.T) {
    // Disabled gate: zero values stay legal (back-compat — shells that never
    // asked for the gate keep booting).
    cfg := shell586Cfg()
    cfg.Shell.RateLimit = ShellRateLimitConfig{}
    if err := validate(&cfg); err != nil {
        t.Fatalf("disabled rate limit must not require rpm: %v", err)
    }
}
