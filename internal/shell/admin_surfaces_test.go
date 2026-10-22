package shell

import (
    "encoding/json"
    "os"
    "path/filepath"
    "testing"
)

func TestAdapterVault_PutListResolve(t *testing.T) {
    dir := t.TempDir()
    v, err := NewAdapterVault(filepath.Join(dir, "vault.json"), "master-key")
    if err != nil {
        t.Fatalf("vault: %v", err)
    }
    if err := v.Put("wechat", map[string]string{"access_token": "super-secret-token-9999"}, 5.5, 120, true); err != nil {
        t.Fatalf("put: %v", err)
    }
    views := v.List()
    if len(views) != 1 || views[0].Platform != "wechat" {
        t.Fatalf("views = %+v", views)
    }
    if !views[0].Configured || !views[0].Enabled {
        t.Fatalf("configured/enabled = %+v", views[0])
    }
    masked := views[0].CredentialsMasked["access_token"]
    if masked == "super-secret-token-9999" || masked == "" {
        t.Fatalf("mask leak or empty: %q", masked)
    }
    // Plaintext must never hit the store file.
    raw, _ := os.ReadFile(filepath.Join(dir, "vault.json"))
    if contains(string(raw), "super-secret-token-9999") {
        t.Fatal("plaintext credential persisted to the vault store")
    }
    // Round-trip through a fresh instance.
    v2, err := NewAdapterVault(filepath.Join(dir, "vault.json"), "master-key")
    if err != nil {
        t.Fatalf("vault2: %v", err)
    }
    if err := v2.Load(); err != nil {
        t.Fatalf("load: %v", err)
    }
    creds, ok := v2.Resolve("wechat")
    if !ok || creds["access_token"] != "super-secret-token-9999" {
        t.Fatalf("resolve = %+v ok=%v", creds, ok)
    }
    got, reason := v2.Test("wechat")
    if !got || reason != "" {
        t.Fatalf("test = %v %q", got, reason)
    }
}

func TestAdapterVault_TestFailures(t *testing.T) {
    v, _ := NewAdapterVault("", "master-key")
    if ok, reason := v.Test("nope"); ok || reason == "" {
        t.Fatalf("unconfigured test = %v %q", ok, reason)
    }
    if err := v.Put("p", nil, 0, 0, false); err != nil {
        t.Fatalf("put: %v", err)
    }
    if ok, reason := v.Test("p"); ok || reason != "adapter disabled" {
        t.Fatalf("disabled test = %v %q", ok, reason)
    }
    if err := v.Put("p", nil, 0, 0, true); err != nil {
        t.Fatalf("put: %v", err)
    }
    if ok, reason := v.Test("p"); ok || reason != "no credentials stored" {
        t.Fatalf("empty-cred test = %v %q", ok, reason)
    }
}

func TestAdapterVault_PartialUpdateKeepsCredentials(t *testing.T) {
    v, _ := NewAdapterVault("", "master-key")
    if err := v.Put("p", map[string]string{"k": "v"}, 1, 0, true); err != nil {
        t.Fatalf("put: %v", err)
    }
    // Rate-limit-only edit: empty credentials map must not wipe the secret.
    if err := v.Put("p", map[string]string{}, 9, 0, true); err != nil {
        t.Fatalf("put2: %v", err)
    }
    creds, ok := v.Resolve("p")
    if !ok || creds["k"] != "v" {
        t.Fatalf("credential wiped by partial update: %+v", creds)
    }
}

func TestConnectorStore_PutValidateTest(t *testing.T) {
    dir := t.TempDir()
    v, _ := NewAdapterVault("", "master-key")
    if err := v.Put("vault-plat", map[string]string{"token": "abcd"}, 0, 0, true); err != nil {
        t.Fatalf("vault put: %v", err)
    }
    s := NewConnectorStore(filepath.Join(dir, "conn.json"), v)
    problems, err := s.Put(ConnectorDoc{ID: "c1", Kind: "http", Operations: []string{"fetch"}, CredentialRef: "vault-plat", Enabled: true})
    if err != nil || len(problems) != 0 {
        t.Fatalf("put = %v %v", problems, err)
    }
    ok, reason, _ := s.Test("c1", "")
    if !ok || reason != "" {
        t.Fatalf("test = %v %q", ok, reason)
    }
    // Unknown operation.
    if ok, _, _ := s.Test("c1", "nope"); ok {
        t.Fatal("unknown operation must fail test")
    }
    // Unresolvable credential_ref + missing kind/ops surface as problems.
    if _, err := s.Put(ConnectorDoc{ID: "c2", CredentialRef: "ghost"}); err != nil {
        t.Fatalf("put c2: %v", err)
    }
    views := s.List()
    var c2 *ConnectorView
    for i := range views {
        if views[i].ID == "c2" {
            c2 = &views[i]
        }
    }
    if c2 == nil || len(c2.Problems) < 3 {
        t.Fatalf("c2 problems = %+v", c2)
    }
    if ok, _, _ := s.Test("c2", ""); ok {
        t.Fatal("c2 must fail test")
    }
    // Round-trip.
    s2 := NewConnectorStore(filepath.Join(dir, "conn.json"), v)
    if err := s2.Load(); err != nil {
        t.Fatalf("load: %v", err)
    }
    if _, ok := s2.Get("c1"); !ok {
        t.Fatal("c1 lost on reload")
    }
}

func TestConfigOverrides_PutGetImmediate(t *testing.T) {
    dir := t.TempDir()
    s := NewConfigOverrides(filepath.Join(dir, "ovr.json"))
    defaults, overrides := s.Get("pricing_policies")
    if string(defaults) != "{}" || string(overrides) != "{}" {
        t.Fatalf("unconfigured get = %s %s", defaults, overrides)
    }
    raw := json.RawMessage(`{"currency":"CNY"}`)
    if err := s.Put("pricing_policies", raw); err != nil {
        t.Fatalf("put: %v", err)
    }
    _, overrides = s.Get("pricing_policies")
    if string(overrides) != `{"currency":"CNY"}` {
        t.Fatalf("override not effective immediately: %s", overrides)
    }
    if err := s.Put("bad", json.RawMessage(`[1,2]`)); err == nil {
        t.Fatal("non-object override must be rejected")
    }
    // Round-trip.
    s2 := NewConfigOverrides(filepath.Join(dir, "ovr.json"))
    if err := s2.Load(); err != nil {
        t.Fatalf("load: %v", err)
    }
    _, overrides = s2.Get("pricing_policies")
    var roundTrip map[string]string
    if err := json.Unmarshal(overrides, &roundTrip); err != nil || roundTrip["currency"] != "CNY" {
        t.Fatalf("override lost on reload: %s (%v)", overrides, err)
    }
}

func TestAuditLog_ChainAndTamper(t *testing.T) {
    dir := t.TempDir()
    path := filepath.Join(dir, "audit.json")
    a := NewAuditLog(path)
    for i := 0; i < 3; i++ {
        if err := a.Record("u1", "admin", AuditClassConfig, "PUT", "/x", AuditOutcomeOk, int64(i)); err != nil {
            t.Fatalf("record: %v", err)
        }
    }
    views := a.Entries("", "")
    if len(views) != 3 {
        t.Fatalf("entries = %d", len(views))
    }
    for _, v := range views {
        if !v.IntegrityOK {
            t.Fatalf("fresh chain must verify: %+v", v)
        }
    }
    // Newest first.
    if views[0].Seq != 2 {
        t.Fatalf("order = %+v", views[0])
    }
    // Filters.
    if got := a.Entries("u1", AuditClassAuth); len(got) != 0 {
        t.Fatalf("class filter = %d", len(got))
    }
    // Tamper with the middle entry on disk, reload, tail must flag.
    raw, _ := os.ReadFile(path)
    var entries []AuditEntry
    _ = json.Unmarshal(raw, &entries)
    entries[0].Path = "/tampered"
    tampered, _ := json.Marshal(entries)
    if err := os.WriteFile(path, tampered, 0o600); err != nil {
        t.Fatalf("write: %v", err)
    }
    b := NewAuditLog(path)
    if err := b.Load(); err != nil {
        t.Fatalf("load: %v", err)
    }
    views = b.Entries("", "")
    if views[2].IntegrityOK {
        t.Fatal("tampered head entry must flag integrity_ok=false")
    }
    if views[1].IntegrityOK || views[0].IntegrityOK {
        t.Fatal("entries after the break must flag integrity_ok=false")
    }
}

func TestRoleStore_ListCreateDelete(t *testing.T) {
    dir := t.TempDir()
    s := NewRoleStore(filepath.Join(dir, "roles.json"))
    views := s.List(func(role string) int {
        if role == "admin" {
            return 2
        }
        return 0
    })
    if len(views) != 3 || views[0].Name != "admin" || !views[0].Builtin || views[0].UserCount != 2 {
        t.Fatalf("builtin roles = %+v", views)
    }
    if err := s.Create("approver", " approvals", []string{AuditClassApproval, AuditClassRead}); err != nil {
        t.Fatalf("create: %v", err)
    }
    if !s.Exists("approver") {
        t.Fatal("custom role must resolve")
    }
    if err := s.Create("admin", "", []string{AuditClassRead}); err == nil || err.Error() == "" {
        t.Fatal("builtin collision must be rejected")
    } else if err.Error() == "" || !contains(err.Error(), "built-in") {
        t.Fatalf("builtin collision error = %v", err)
    }
    if err := s.Create("bad", "", []string{"fly"}); err == nil {
        t.Fatal("off-vocabulary write class must be rejected")
    }
    // Delete: in-use rejected with reason, then succeeds when unassigned.
    if err := s.Delete("approver", func(role string) int { return 1 }); err == nil {
        t.Fatal("in-use role delete must be rejected")
    } else if !contains(err.Error(), "assigned") {
        t.Fatalf("in-use error = %v", err)
    }
    if err := s.Delete("approver", func(role string) int { return 0 }); err != nil {
        t.Fatalf("delete: %v", err)
    }
    if s.Exists("approver") {
        t.Fatal("role must be gone")
    }
}

func TestMenuPermissions_PutGetValidate(t *testing.T) {
    dir := t.TempDir()
    m := NewMenuPermissions(filepath.Join(dir, "menus.json"))
    def, over := m.Get([]MenuItem{{ID: "fusion-mlx", Label: "MLX", Href: "/fusion-mlx", Roles: []string{"admin"}}})
    if len(def) != 1 || len(over) != 0 {
        t.Fatalf("get = %+v %+v", def, over)
    }
    err := m.Put([]MenuItem{{ID: "m1", Label: "M1", Roles: []string{"ghost-role"}}}, func(string) bool { return false })
    if err == nil {
        t.Fatal("unknown role must be rejected")
    }
    err = m.Put([]MenuItem{
        {ID: "m1", Label: "M1", Roles: []string{"admin"}},
        {ID: "m1", Label: "Dup", Roles: []string{"admin"}},
    }, func(string) bool { return true })
    if err == nil {
        t.Fatal("duplicate id must be rejected")
    }
    if err := m.Put([]MenuItem{{ID: "m1", Label: "M1", Href: "/m1", Roles: []string{"admin", "auditor"}}}, func(n string) bool { return n == "admin" || n == "auditor" }); err != nil {
        t.Fatalf("put: %v", err)
    }
    // Round-trip.
    m2 := NewMenuPermissions(filepath.Join(dir, "menus.json"))
    if err := m2.Load(); err != nil {
        t.Fatalf("load: %v", err)
    }
    _, over = m2.Get(nil)
    if len(over) != 1 || over[0].ID != "m1" || len(over[0].Roles) != 2 {
        t.Fatalf("override lost on reload: %+v", over)
    }
}
