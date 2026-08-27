package memory

import (
    "os"
    "path/filepath"
    "testing"

    "github.com/fusion-gateway/fusion-gateway/internal/config"
    "github.com/fusion-gateway/fusion-gateway/internal/store"
)

func newPersistTestStore(t *testing.T, dataDir string) *MemoryStore {
    t.Helper()
    ms := NewMemoryStore(100)
    if err := ms.EnablePersistence(dataDir); err != nil {
        t.Fatalf("EnablePersistence: %v", err)
    }
    return ms
}

func TestEnablePersistence_EmptyDataDir_NoOp(t *testing.T) {
    ms := NewMemoryStore(100)
    if err := ms.EnablePersistence(""); err != nil {
        t.Fatalf("empty data_dir should be no-op, got %v", err)
    }
    if ms.persist != nil {
        t.Fatal("persist should be nil when data_dir empty")
    }
}

func TestEnablePersistence_MissingDataDir_Created(t *testing.T) {
    dir := filepath.Join(t.TempDir(), "nested", "data")
    ms := newPersistTestStore(t, dir)
    if ms.persist == nil {
        t.Fatal("persist should be set when data_dir non-empty")
    }
    if _, err := os.Stat(dir); err != nil {
        t.Fatalf("data dir should exist: %v", err)
    }
}

func TestSaveOnMutate_KeyCreate(t *testing.T) {
    dir := t.TempDir()
    ms := newPersistTestStore(t, dir)

    key := &store.APIKeyEntry{
        Name:      "test-key-1",
        KeyPrefix: "sk-test1",
        KeyHash:   "hash-test-1",
        Status:    "active",
        QuotaLimit: 1000,
    }
    if err := ms.CreateKey(key); err != nil {
        t.Fatalf("CreateKey: %v", err)
    }

    data, err := os.ReadFile(filepath.Join(dir, "keys.json"))
    if err != nil {
        t.Fatalf("keys.json not written: %v", err)
    }
    if len(data) == 0 {
        t.Fatal("keys.json empty")
    }

    ms2 := newPersistTestStore(t, dir)
    got, err := ms2.GetKey("test-key-1")
    if err != nil {
        t.Fatalf("key not restored after reload: %v", err)
    }
    if got.KeyHash != "hash-test-1" {
        t.Fatalf("restored key hash mismatch: %s", got.KeyHash)
    }
    if got.QuotaRemaining != 1000 {
        t.Fatalf("QuotaRemaining not recomputed on load: %f", got.QuotaRemaining)
    }
}

func TestSaveOnMutate_KeyDelete(t *testing.T) {
    dir := t.TempDir()
    ms := newPersistTestStore(t, dir)

    key := &store.APIKeyEntry{Name: "del-key", KeyPrefix: "sk-del", KeyHash: "h-del"}
    if err := ms.CreateKey(key); err != nil {
        t.Fatal(err)
    }
    if err := ms.DeleteKey("del-key"); err != nil {
        t.Fatal(err)
    }

    ms2 := newPersistTestStore(t, dir)
    if _, err := ms2.GetKey("del-key"); err == nil {
        t.Fatal("deleted key should not be restored")
    }
}

func TestSaveOnMutate_KeyUpdate(t *testing.T) {
    dir := t.TempDir()
    ms := newPersistTestStore(t, dir)

    key := &store.APIKeyEntry{Name: "upd-key", KeyPrefix: "sk-upd", KeyHash: "h-upd", QuotaLimit: 500}
    if err := ms.CreateKey(key); err != nil {
        t.Fatal(err)
    }
    key.QuotaLimit = 2000
    if err := ms.UpdateKey(key); err != nil {
        t.Fatal(err)
    }

    ms2 := newPersistTestStore(t, dir)
    got, err := ms2.GetKey("upd-key")
    if err != nil {
        t.Fatal(err)
    }
    if got.QuotaLimit != 2000 {
        t.Fatalf("updated QuotaLimit not persisted: %f", got.QuotaLimit)
    }
}

func TestSaveOnMutate_Channel(t *testing.T) {
    dir := t.TempDir()
    ms := newPersistTestStore(t, dir)

    ch := &store.ChannelEntry{Name: "ch-1", Type: "openai-compatible", Provider: "openai", BaseURL: "http://x", Enabled: true}
    if err := ms.CreateChannel(ch); err != nil {
        t.Fatal(err)
    }

    ms2 := newPersistTestStore(t, dir)
    got, err := ms2.GetChannel("ch-1")
    if err != nil {
        t.Fatalf("channel not restored: %v", err)
    }
    if got.BaseURL != "http://x" {
        t.Fatalf("channel baseURL mismatch: %s", got.BaseURL)
    }

    if err := ms.DeleteChannel("ch-1"); err != nil {
        t.Fatal(err)
    }
    ms3 := newPersistTestStore(t, dir)
    if _, err := ms3.GetChannel("ch-1"); err == nil {
        t.Fatal("deleted channel should not be restored")
    }
}

func TestSaveOnMutate_Team(t *testing.T) {
    dir := t.TempDir()
    ms := newPersistTestStore(t, dir)

    team := &store.Team{ID: "team-x", Name: "Team X", OrgID: "default", QuotaLimit: 100}
    if err := ms.CreateTeam(team); err != nil {
        t.Fatal(err)
    }
    if err := ms.BindKeyToTeam("sk-bind", "team-x"); err != nil {
        t.Fatal(err)
    }

    ms2 := newPersistTestStore(t, dir)
    got, err := ms2.GetTeam("team-x")
    if err != nil {
        t.Fatalf("team not restored: %v", err)
    }
    if got.Name != "Team X" {
        t.Fatalf("team name mismatch: %s", got.Name)
    }
    bound, err := ms2.GetTeamByKey("sk-bind")
    if err != nil {
        t.Fatalf("key-team binding not restored: %v", err)
    }
    if bound.ID != "team-x" {
        t.Fatalf("bound team mismatch: %s", bound.ID)
    }
}

func TestSaveOnMutate_Org(t *testing.T) {
    dir := t.TempDir()
    ms := newPersistTestStore(t, dir)

    org := &store.Organization{ID: "org-1", Name: "Org One"}
    if err := ms.CreateOrg(org); err != nil {
        t.Fatal(err)
    }

    ms2 := newPersistTestStore(t, dir)
    got, err := ms2.GetOrg("org-1")
    if err != nil {
        t.Fatalf("org not restored: %v", err)
    }
    if got.Name != "Org One" {
        t.Fatalf("org name mismatch: %s", got.Name)
    }
}

func TestAddCost_NotFlushed(t *testing.T) {
    dir := t.TempDir()
    ms := newPersistTestStore(t, dir)

    if err := ms.AddTeamCost("default", 42.5); err != nil {
        t.Fatal(err)
    }

    teamsPath := filepath.Join(dir, "teams.json")
    _, err := os.Stat(teamsPath)
    if err == nil {
        t.Fatal("teams.json should NOT be written by AddCost (high-frequency excluded)")
    }
    if !os.IsNotExist(err) {
        t.Fatalf("unexpected stat error: %v", err)
    }
}

func TestTeamsLoad_ReplacesSeed(t *testing.T) {
    dir := t.TempDir()

    ms1 := NewMemoryStore(100)
    if err := ms1.EnablePersistence(dir); err != nil {
        t.Fatal(err)
    }
    if err := ms1.CreateTeam(&store.Team{ID: "persisted-team", Name: "P"}); err != nil {
        t.Fatal(err)
    }

    ms2 := NewMemoryStore(100)
    if err := ms2.EnablePersistence(dir); err != nil {
        t.Fatal(err)
    }
    if _, err := ms2.GetTeam("persisted-team"); err != nil {
        t.Fatalf("persisted team missing: %v", err)
    }
    if _, err := ms2.GetTeam("default"); err != nil {
        t.Fatalf("seeded default team should survive: %v", err)
    }
}

func TestTeamsLoad_KeepsSeedWhenNoFile(t *testing.T) {
    dir := t.TempDir()
    ms := newPersistTestStore(t, dir)
    if _, err := ms.GetTeam("default"); err != nil {
        t.Fatalf("default team should be seeded when no teams.json: %v", err)
    }
    if _, err := ms.GetOrg("default"); err != nil {
        t.Fatalf("default org should be seeded when no teams.json: %v", err)
    }
}

func TestCorruptFile_GracefulEmpty(t *testing.T) {
    dir := t.TempDir()
    if err := os.WriteFile(filepath.Join(dir, "keys.json"), []byte("{not valid json"), 0o600); err != nil {
        t.Fatal(err)
    }

    ms := NewMemoryStore(100)
    if err := ms.EnablePersistence(dir); err != nil {
        t.Fatalf("corrupt keys.json should not crash EnablePersistence: %v", err)
    }
    if _, err := ms.ListKeys(); err != nil {
        t.Fatalf("store should be usable after corrupt load: %v", err)
    }
}

// F6: a corrupt keys.json must be quarantined to <name>.corrupt.<ts> so the
// next SaveKeys() does not overwrite the only (corrupt) copy with the empty
// in-memory store — preserving the bytes for forensic recovery. The store
// continues empty (soft contract), but the original file no longer sits at
// the canonical path.
func TestCorruptFile_QuarantinedNotOverwritten(t *testing.T) {
    dir := t.TempDir()
    corruptPayload := []byte("{not valid json")
    if err := os.WriteFile(filepath.Join(dir, "keys.json"), corruptPayload, 0o600); err != nil {
        t.Fatal(err)
    }

    ms := NewMemoryStore(100)
    if err := ms.EnablePersistence(dir); err != nil {
        t.Fatalf("corrupt keys.json should not crash EnablePersistence: %v", err)
    }

    // canonical path no longer holds the corrupt file
    if _, err := os.Stat(filepath.Join(dir, "keys.json")); !os.IsNotExist(err) {
        t.Fatalf("corrupt keys.json should have been renamed away, stat err=%v", err)
    }
    // a quarantine copy exists and holds the original corrupt bytes
    matches, err := filepath.Glob(filepath.Join(dir, "keys.json.corrupt.*"))
    if err != nil {
        t.Fatal(err)
    }
    if len(matches) != 1 {
        t.Fatalf("expected exactly 1 quarantine file, got %d: %v", len(matches), matches)
    }
    got, err := os.ReadFile(matches[0])
    if err != nil {
        t.Fatal(err)
    }
    if string(got) != string(corruptPayload) {
        t.Fatalf("quarantine file content mismatch: want %q got %q", corruptPayload, got)
    }

    // subsequent SaveKeys() writes a fresh valid keys.json, not a silent overwrite
    if err := ms.CreateKey(&store.APIKeyEntry{Name: "recovery-key", Status: "active"}); err != nil {
        t.Fatalf("CreateKey after corrupt load: %v", err)
    }
    if err := ms.persist.SaveKeys(); err != nil {
        t.Fatalf("SaveKeys after corrupt load: %v", err)
    }
    fresh, err := os.ReadFile(filepath.Join(dir, "keys.json"))
    if err != nil {
        t.Fatal(err)
    }
    if string(fresh) == string(corruptPayload) {
        t.Fatal("SaveKeys overwrote the quarantine path instead of writing a fresh valid file")
    }
    // quarantine file still intact after save
    if _, err := os.Stat(matches[0]); err != nil {
        t.Fatalf("quarantine file should survive SaveKeys: %v", err)
    }
}

func TestAtomicWrite_NoCorrupt(t *testing.T) {
    dir := t.TempDir()
    p := NewPersister(dir, NewKeyStore(), NewChannelStore(), NewTeamsStore())
    payload := []byte(`{"teams":[],"orgs":[],"key_team":{}}`)
    if err := p.atomicWrite(filepath.Join(dir, "teams.json"), payload); err != nil {
        t.Fatal(err)
    }
    got, err := os.ReadFile(filepath.Join(dir, "teams.json"))
    if err != nil {
        t.Fatal(err)
    }
    if string(got) != string(payload) {
        t.Fatalf("atomic write content mismatch")
    }
}

func TestExpandPath_Tilde(t *testing.T) {
    home, err := os.UserHomeDir()
    if err != nil {
        t.Skip("no home dir")
    }
    cases := []struct {
        in, want string
    }{
        {"~/x/y", filepath.Join(home, "x", "y")},
        {"~", home},
        {"", ""},
        {"/abs/path", "/abs/path"},
        {"relative/path", "relative/path"},
    }
    for _, c := range cases {
        got := config.ExpandPath(c.in)
        if got != c.want {
            t.Errorf("ExpandPath(%q) = %q, want %q", c.in, got, c.want)
        }
    }
}

// A2: per-key quota usage must survive restart. Pre-fix, QuotaStore.usage was
// pure in-memory — a BudgetLimit key burned to the limit reset to 0 on reload
// (billing bypass, same class as F2 for team quota). Deduct persists the
// authoritative maps to quota.json (debounced); a fresh store loads + seeds
// them so Check reads the pre-restart usage. Covers BudgetLimit-only keys
// (QuotaLimit==0, so k.QuotaUsed is never written by Deduct — keys.json alone
// could never restore this).
func TestPerKeyQuota_SurvivesRestart(t *testing.T) {
    dir := t.TempDir()
    ms := newPersistTestStore(t, dir)

    // BudgetLimit-only key: QuotaLimit==0 so Deduct never writes k.QuotaUsed.
    // Cumulative usage lives only in QuotaStore.usage.
    key := &store.APIKeyEntry{Name: "budget-key", Status: "active", BudgetLimit: 100.0}
    if err := ms.CreateKey(key); err != nil {
        t.Fatal(err)
    }
    if err := ms.DeductQuota("budget-key", 42.0); err != nil {
        t.Fatal(err)
    }
    // flush the debounced quota.json write (shutdown path does this)
    ms.FlushQuota()

    quotaPath := filepath.Join(dir, "quota.json")
    if _, err := os.Stat(quotaPath); err != nil {
        t.Fatalf("quota.json not written after FlushQuota: %v", err)
    }

    // fresh store from the same data_dir: usage must be restored
    ms2 := newPersistTestStore(t, dir)
    used, limit, exceeded, err := ms2.CheckQuota("budget-key")
    if err != nil {
        t.Fatalf("CheckQuota after reload: %v", err)
    }
    if used != 42.0 {
        t.Fatalf("per-key quota not restored after restart: used=%f want 42.0", used)
    }
    if limit != 100.0 {
        t.Fatalf("cumulative budget limit mismatch: limit=%f want 100.0", limit)
    }
    if exceeded {
        t.Fatal("42/100 should not be exceeded after reload")
    }

    // deducting again accumulates on the restored baseline (not reset to 0)
    if err := ms2.DeductQuota("budget-key", 8.0); err != nil {
        t.Fatal(err)
    }
    ms2.FlushQuota()
    used2, _, _, _ := ms2.CheckQuota("budget-key")
    if used2 != 50.0 {
        t.Fatalf("post-reload deduct did not accumulate on restored baseline: used=%f want 50.0", used2)
    }
}

// A2: quota.json corruption is quarantined (not silently overwritten) and the
// store starts with zeroed usage rather than crashing — mirrors the F6 keys
// quarantine contract.
func TestQuotaCorrupt_QuarantinedZeroed(t *testing.T) {
    dir := t.TempDir()
    if err := os.WriteFile(filepath.Join(dir, "quota.json"), []byte("{not valid json"), 0o600); err != nil {
        t.Fatal(err)
    }

    ms := NewMemoryStore(100)
    if err := ms.EnablePersistence(dir); err != nil {
        t.Fatalf("corrupt quota.json should not crash EnablePersistence: %v", err)
    }
    // store usable with zeroed usage
    used, _, _, _ := ms.CheckQuota("any-key")
    if used != 0 {
        t.Fatalf("usage should be zeroed after corrupt load: got %f", used)
    }
    // corrupt file quarantined, canonical path cleared
    if _, err := os.Stat(filepath.Join(dir, "quota.json")); !os.IsNotExist(err) {
        t.Fatalf("corrupt quota.json should be renamed away, stat err=%v", err)
    }
    matches, err := filepath.Glob(filepath.Join(dir, "quota.json.corrupt.*"))
    if err != nil {
        t.Fatal(err)
    }
    if len(matches) != 1 {
        t.Fatalf("expected 1 quarantine file, got %d: %v", len(matches), matches)
    }
}
