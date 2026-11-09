package config

import (
    "strings"
    "testing"
)

// Issue #594: rpm:0 / tpm:0 silently means UNLIMITED on the auth.api_keys
// plane. Copy-pasting an admin-style entry to mint a "low-privilege" key
// inherits the unlimited default, so a dedicated key is only low-privilege
// if someone remembers numeric caps. Non-admin entries with a 0 cap now
// fail gateway startup unless the operator sets unlimited: true — an
// explicit, greppable decision. The admin entry keeps the back-compat WARN.

func key594Cfg(entries []AuthKeyConfig) Config {
    cfg := DefaultConfig()
    cfg.Auth.Enabled = true
    cfg.Auth.APIKeys = entries
    return cfg
}

func TestValidate_NonAdminZeroRPMRejected(t *testing.T) {
    cfg := key594Cfg([]AuthKeyConfig{
        {Key: "fk-office", Name: "office-standalone", RPM: 0, TPM: 20000},
    })
    err := validate(&cfg)
    if err == nil {
        t.Fatalf("non-admin entry with rpm=0 must fail startup (#594)")
    }
    for _, want := range []string{"office-standalone", "unlimited: true", "rpm"} {
        if !contains(err.Error(), want) {
            t.Fatalf("error %q must name the entry and the fix (%q missing)", err.Error(), want)
        }
    }
}

func TestValidate_NonAdminZeroTPMRejected(t *testing.T) {
    cfg := key594Cfg([]AuthKeyConfig{
        {Key: "fk-office", Name: "office-standalone", RPM: 30, TPM: 0},
    })
    err := validate(&cfg)
    if err == nil {
        t.Fatalf("non-admin entry with tpm=0 must fail startup (#594)")
    }
    if !contains(err.Error(), "tpm") {
        t.Fatalf("error %q must name the tpm dimension", err.Error())
    }
}

func TestValidate_UnlimitedOptInPasses(t *testing.T) {
    cfg := key594Cfg([]AuthKeyConfig{
        {Key: "fk-office", Name: "office-standalone", RPM: 0, TPM: 0, Unlimited: true},
    })
    if err := validate(&cfg); err != nil {
        t.Fatalf("explicit unlimited: true must validate (#594): %v", err)
    }
}

func TestValidate_AdminEntryUnchanged(t *testing.T) {
    cfg := key594Cfg([]AuthKeyConfig{
        {Key: "fk-admin", Name: "admin", RPM: 0, TPM: 0},
    })
    if err := validate(&cfg); err != nil {
        t.Fatalf("admin entry must keep back-compat unlimited semantics: %v", err)
    }
}

func TestValidate_FullyCappedKeyPasses(t *testing.T) {
    cfg := key594Cfg([]AuthKeyConfig{
        {Key: "fk-demo", Name: "demo", RPM: 60, TPM: 100000},
        {Key: "fk-office", Name: "office-standalone", RPM: 30, TPM: 20000},
    })
    if err := validate(&cfg); err != nil {
        t.Fatalf("numeric-capped keys must validate: %v", err)
    }
}

func TestValidate_AuthDisabledKeyPlaneUnaffected(t *testing.T) {
    cfg := key594Cfg([]AuthKeyConfig{
        {Key: "fk-office", Name: "office-standalone", RPM: 0, TPM: 0},
    })
    cfg.Auth.Enabled = false
    if err := validate(&cfg); err != nil {
        t.Fatalf("auth disabled = key plane not enforced; unlimited keys must pass: %v", err)
    }
}

func contains(s, sub string) bool {
    return strings.Contains(s, sub)
}
