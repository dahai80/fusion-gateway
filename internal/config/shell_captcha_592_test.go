package config

import "testing"

// #592: an enabled captcha gate with no challenge lifetime must fail closed
// — a zero TTL would expire every challenge instantly and brick the login
// plane while looking "enabled".
func TestValidate_ShellCaptchaEnabledRequiresTTL(t *testing.T) {
    cfg := shell586Cfg()
    cfg.Shell.Captcha = ShellCaptchaConfig{Enabled: true, TTL: 0}
    err := validate(&cfg)
    if err == nil {
        t.Fatalf("captcha.enabled with ttl=0 must be rejected (fail closed, #592)")
    }
    want := "web_shell.captcha.enabled is true but web_shell.captcha.ttl is"
    if err.Error()[:len(want)] != want {
        t.Fatalf("error = %q, want prefix %q", err.Error(), want)
    }

    cfg.Shell.Captcha.TTL = 300000000000
    if err := validate(&cfg); err != nil {
        t.Fatalf("positive ttl must validate: %v", err)
    }
}

func TestValidate_ShellCaptchaNegativeTTLRejected(t *testing.T) {
    cfg := shell586Cfg()
    cfg.Shell.Captcha = ShellCaptchaConfig{Enabled: false, TTL: -1}
    if err := validate(&cfg); err == nil {
        t.Fatalf("negative ttl must be rejected")
    }
}

func TestValidate_ShellCaptchaDisabledUnlimitedOK(t *testing.T) {
    cfg := shell586Cfg()
    cfg.Shell.Captcha = ShellCaptchaConfig{Enabled: false, TTL: 0}
    if err := validate(&cfg); err != nil {
        t.Fatalf("disabled captcha with zero ttl must validate (gate off): %v", err)
    }
}
