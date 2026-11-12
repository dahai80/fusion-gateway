package billing

// #633: prepaid-bucket balance ledger — top-up, live enforcement read, and
// deduction with debounced disk persistence.

import (
    "encoding/json"
    "os"
    "path/filepath"
    "testing"
    "time"
)

func Test633_LedgerTopUpDeductBalance(t *testing.T) {
    l := NewLedger(t.TempDir())
    if l.HasBalance("k1") {
        t.Fatal("unknown key must not be prepaid")
    }
    bal, err := l.TopUp("k1", 1000, "order-1")
    if err != nil || bal != 1000 {
        t.Fatalf("topup = %v, %v", bal, err)
    }
    if !l.HasBalance("k1") || l.Balance("k1") != 1000 {
        t.Fatalf("balance after topup = %v", l.Balance("k1"))
    }
    l.Deduct("k1", 400)
    if l.Balance("k1") != 600 {
        t.Fatalf("balance after deduct = %v, want 600", l.Balance("k1"))
    }
    // Deduct on a non-prepaid key is a no-op (does NOT create an entry).
    l.Deduct("ghost", 10)
    if l.HasBalance("ghost") {
        t.Fatal("deduct must not create entries")
    }
    // Zero/negative top-up rejected.
    if _, err := l.TopUp("k1", 0, ""); err != ErrInvalidAmount {
        t.Fatalf("zero topup err = %v", err)
    }
    if _, err := l.TopUp("", 10, ""); err != ErrUnknownKey {
        t.Fatalf("empty key err = %v", err)
    }
}

func Test633_LedgerPersistsAcrossReopen(t *testing.T) {
    dir := t.TempDir()
    l := NewLedger(dir)
    if _, err := l.TopUp("k1", 2000, "order-2"); err != nil {
        t.Fatal(err)
    }
    l.Flush()
    l2 := NewLedger(dir)
    if l2.Balance("k1") != 2000 {
        t.Fatalf("balance after reopen = %v, want 2000", l2.Balance("k1"))
    }
    snap := l2.Snapshot()
    e, ok := snap["k1"]
    if !ok || e["last_order_ref"] != "order-2" {
        t.Fatalf("snapshot missing order ref: %v", e)
    }
}

func Test633_LedgerUnreadableFileStartsEmpty(t *testing.T) {
    dir := t.TempDir()
    p := filepath.Join(dir, "billing_balances.json")
    if err := os.WriteFile(p, []byte("{not json"), 0600); err != nil {
        t.Fatal(err)
    }
    l := NewLedger(dir)
    if l.HasBalance("anything") {
        t.Fatal("corrupt balances file must degrade to empty, not crash")
    }
    // And a write must heal the file.
    _, err := l.TopUp("k", 5, "")
    if err != nil {
        t.Fatal(err)
    }
    l.Flush()
    var m map[string]entry
    b, _ := os.ReadFile(p)
    if json.Unmarshal(b, &m) != nil || len(m) != 1 {
        t.Fatalf("balances file not healed: %s", b)
    }
}

func Test633_DebouncedFlushWritesWithinBudget(t *testing.T) {
    dir := t.TempDir()
    l := NewLedger(dir)
    if _, err := l.TopUp("k1", 7, ""); err != nil {
        t.Fatal(err)
    }
    deadline := time.Now().Add(3 * time.Second)
    for time.Now().Before(deadline) {
        if _, err := os.Stat(filepath.Join(dir, "billing_balances.json")); err == nil {
            return
        }
        time.Sleep(20 * time.Millisecond)
    }
    t.Fatal("debounced flush never wrote the balances file")
}

func Test633_LedgerExpandsTildeDataDir(t *testing.T) {
    // Regression: a literal `~/...` data_dir must be expanded, or every
    // persist fails against a path Go's os package cannot resolve.
    home, _ := os.UserHomeDir()
    if err := os.MkdirAll(filepath.Join(home, ".fusion-gateway-test-633"), 0700); err != nil {
        t.Fatal(err)
    }
    l := NewLedger("~/.fusion-gateway-test-633")
    if _, err := l.TopUp("k1", 5, ""); err != nil {
        t.Fatal(err)
    }
    if l.path == "" || l.path[0] == '~' {
        t.Fatalf("ledger path not expanded: %q", l.path)
    }
    if _, err := os.Stat(l.path); err != nil {
        t.Fatalf("persist file missing after flush: %v", err)
    }
    os.RemoveAll(filepath.Dir(l.path))
}
