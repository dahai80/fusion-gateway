package config

// #732: web_shell.link.ticket_routes validation — format ("METHOD /path",
// at most a trailing /* glob) and the admin/auth plane block (the ticket
// plane is execution-scoped, never an admin elevation).

import (
    "strings"
    "testing"
    "time"
)

func Test732_LinkTicketRoutesValidate(t *testing.T) {
    ok := ShellLinkConfig{
        Enabled: true, Credential: strings.Repeat("x", 32), TicketTTL: time.Minute,
        TicketRoutes: []string{"POST /shell/office-api/v1/*", "GET /shell/office-api/v1/docx/write"},
    }
    if err := validateLinkTicketRoutes(ok); err != nil {
        t.Fatalf("valid ticket_routes rejected: %v", err)
    }

    bad := []struct {
        name  string
        entry string
    }{
        {"missing path", "POST office/parse"},
        {"relative path", "POST shell/office"},
        {"double glob", "POST /office/**"},
        {"mid glob", "POST /office/*/write"},
        {"admin plane", "POST /admin/api/config/auth"},
        {"admin glob", "GET /admin/*"},
        {"admin subpath glob", "GET /admin/api/*"},
        {"auth plane", "GET /auth/api/code"},
        {"empty entry", ""},
        {"method only", "POST"},
    }
    for _, tc := range bad {
        link := ShellLinkConfig{Enabled: true, Credential: strings.Repeat("x", 32), TicketRoutes: []string{tc.entry}}
        if err := validateLinkTicketRoutes(link); err == nil {
            t.Fatalf("%s: invalid ticket_routes entry accepted: %q", tc.name, tc.entry)
        }
    }
}

// Test732_LinkCredentialFilePassesValidate is the regression test for the
// #732 config-guard quarantine: a credential_file-only link deployment used
// to fail validate (a 9-char "from-file" placeholder hit the 32-char gate),
// so ReloadWithRecovery quarantined the edited config.yaml and restored the
// last-good copy — the plane could never be enabled by config edit.
func Test732_LinkCredentialFilePassesValidate(t *testing.T) {
    cases := []struct {
        name    string
        link    ShellLinkConfig
        wantErr bool
    }{
        {"credential_file only", ShellLinkConfig{Enabled: true, CredentialFile: "~/.fusion-gateway/link-credential"}, false},
        {"inline credential", ShellLinkConfig{Enabled: true, Credential: strings.Repeat("x", 32)}, false},
        {"no credential at all", ShellLinkConfig{Enabled: true}, true},
        {"short inline credential", ShellLinkConfig{Enabled: true, Credential: "short"}, true},
        {"disabled plane unvalidated", ShellLinkConfig{Credential: "short"}, false},
    }
    for _, tc := range cases {
        if err := validateLinkCredential(tc.link); (err != nil) != tc.wantErr {
            t.Fatalf("%s: err=%v wantErr=%v", tc.name, err, tc.wantErr)
        }
    }
}
