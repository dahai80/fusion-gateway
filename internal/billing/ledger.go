package billing

// Package billing implements the #633 prepaid-bucket balance plane: a per-key
// token balance, provisioned via the admin billing API (identity order plane
// pending upstream), enforced live in the request hot path, and deducted at
// request finalize with the REAL token counts. Balances are in TOKENS; the
// list price (config billing.list_price_per_1k_tokens_usd) is applied for
// reporting (billable USD) by the cost tracker, not here — one knob per plane.

import (
    "encoding/json"
    "log/slog"
    "os"
    "path/filepath"
    "sync"
    "time"
)

// entry is one prepaid key's balance state. OrderRef records the last
// identity-issued order that funded this balance (traceability once the
// identity order plane lands; admin top-ups pass a free-form reference).
type entry struct {
    BalanceTokens float64   `json:"balance_tokens"`
    TotalTopUp    float64   `json:"total_topup_tokens"`
    TotalUsed     float64   `json:"total_used_tokens"`
    LastOrderRef  string    `json:"last_order_ref,omitempty"`
    UpdatedAt     time.Time `json:"updated_at"`
}

// Ledger holds per-key prepaid balances. In-memory map guarded by an RWMutex
// (hot-path Balance is a plain map read), persisted to <data_dir>/billing_balances.json
// through a debounced writer so a crash loses at most a few seconds of
// accounting. Nil ledger = billing plane off (all callers nil-check).
type Ledger struct {
    mu       sync.RWMutex
    balances map[string]*entry
    path     string
    // dirty + lastFlush implement the debounce: flush at most once per second.
    dirty     bool
    lastFlush time.Time
}

func NewLedger(dataDir string) *Ledger {
    l := &Ledger{balances: make(map[string]*entry)}
    if dataDir != "" {
        l.path = filepath.Join(dataDir, "billing_balances.json")
        if b, err := os.ReadFile(l.path); err == nil {
            if err := json.Unmarshal(b, &l.balances); err != nil {
                slog.Error("billing balances file unreadable, starting empty (billing plane degrades to no prepaid enforcement)",
                    "path", l.path, "error", err)
                l.balances = make(map[string]*entry)
            } else {
                slog.Info("billing balances loaded", "path", l.path, "keys", len(l.balances))
            }
        }
    }
    return l
}

// HasBalance reports whether the key is prepaid (has a ledger entry). Keys
// without an entry are NOT balance-enforced (postpaid/config-key plane).
func (l *Ledger) HasBalance(keyName string) bool {
    if l == nil {
        return false
    }
    l.mu.RLock()
    defer l.mu.RUnlock()
    return keyName != "" && l.balances[keyName] != nil
}

// Balance returns the remaining token balance (0 when unknown).
func (l *Ledger) Balance(keyName string) float64 {
    if l == nil {
        return 0
    }
    l.mu.RLock()
    defer l.mu.RUnlock()
    if e := l.balances[keyName]; e != nil {
        return e.BalanceTokens
    }
    return 0
}

// TopUp funds (or creates) a key's prepaid balance. tokens must be > 0.
// Returns the new balance.
func (l *Ledger) TopUp(keyName string, tokens float64, orderRef string) (float64, error) {
    if keyName == "" {
        return 0, ErrUnknownKey
    }
    if tokens <= 0 {
        return 0, ErrInvalidAmount
    }
    l.mu.Lock()
    e := l.balances[keyName]
    if e == nil {
        e = &entry{}
        l.balances[keyName] = e
    }
    e.BalanceTokens += tokens
    e.TotalTopUp += tokens
    if orderRef != "" {
        e.LastOrderRef = orderRef
    }
    e.UpdatedAt = time.Now()
    l.dirty = true
    slog.Info("billing balance topped up",
        "key", keyName, "tokens", tokens, "balance", e.BalanceTokens, "order_ref", orderRef)
    l.mu.Unlock()
    l.flushIfDirty()
    return e.BalanceTokens, nil
}

// Deduct records token consumption against a prepaid balance. Balance may go
// slightly negative on concurrent in-flight requests (pre-check admission is
// optimistic); the next request is then rejected — documented v1 trade-off,
// no reservation protocol.
func (l *Ledger) Deduct(keyName string, tokens float64) {
    if l == nil || keyName == "" || tokens <= 0 {
        return
    }
    l.mu.Lock()
    e := l.balances[keyName]
    if e == nil {
        l.mu.Unlock()
        return
    }
    e.BalanceTokens -= tokens
    e.TotalUsed += tokens
    e.UpdatedAt = time.Now()
    l.dirty = true
    l.mu.Unlock()
    l.flushIfDirty()
}

// Snapshot returns a copy of all entries for the admin summary endpoint.
func (l *Ledger) Snapshot() map[string]map[string]any {
    if l == nil {
        return nil
    }
    l.mu.RLock()
    defer l.mu.RUnlock()
    out := make(map[string]map[string]any, len(l.balances))
    for k, e := range l.balances {
        out[k] = map[string]any{
            "balance_tokens":   e.BalanceTokens,
            "total_topup":      e.TotalTopUp,
            "total_used":       e.TotalUsed,
            "last_order_ref":   e.LastOrderRef,
            "updated_at":       e.UpdatedAt.Format(time.RFC3339),
        }
    }
    return out
}

// Flush writes balances to disk synchronously (shutdown path).
func (l *Ledger) Flush() {
    if l == nil || l.path == "" {
        return
    }
    l.mu.Lock()
    l.dirty = false
    b, err := json.MarshalIndent(l.balances, "", "    ")
    l.mu.Unlock()
    if err != nil {
        slog.Error("billing balances marshal failed", "error", err)
        return
    }
    tmp := l.path + ".tmp"
    if err := os.WriteFile(tmp, b, 0600); err != nil {
        slog.Error("billing balances persist failed (tmp write)", "path", tmp, "error", err)
        return
    }
    if err := os.Rename(tmp, l.path); err != nil {
        slog.Error("billing balances persist failed (rename)", "path", l.path, "error", err)
    }
}

// flushIfDirty is the debounced persist (1s floor between writes). Called
// from the admin TopUp path (rare), so the write is synchronous — no
// goroutine, no races with process shutdown or test cleanup.
func (l *Ledger) flushIfDirty() {
    if l.path == "" {
        return
    }
    if time.Since(l.lastFlush) < time.Second {
        return
    }
    l.lastFlush = time.Now()
    l.Flush()
}

var (
    ErrUnknownKey    = ledgerError("key name required")
    ErrInvalidAmount = ledgerError("tokens must be positive")
)

type ledgerError string

func (e ledgerError) Error() string { return string(e) }
