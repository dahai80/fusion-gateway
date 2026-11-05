package tryquota

// #514: per-user daily token quota metering for the cloud-try real-product
// experience plan. Every product LLM call through the gateway carries a
// fusion-identity JWT; the meter keys usage by the JWT `sub`, pre-checks the
// request's estimated tokens against the user's daily budget, and records
// real usage (upstream usage.total_tokens, tokenizer fallback) at finalize.
// Soft-limit semantics: only NEW requests are rejected — in-flight requests
// finish and are accounted normally.

import (
    "encoding/json"
    "log/slog"
    "os"
    "path/filepath"
    "sync"
    "time"

    "github.com/golang-jwt/jwt/v5"
)

// DayKey is the metering day bucket (local date, YYYY-MM-DD). Day rollover
// is derived from the record timestamp — no timers, a new day simply starts
// a fresh bucket on the next request.
func DayKey(t time.Time) string {
    return t.Format("2006-01-02")
}

// CallDetail is one accounted call (per-call detail rollup row).
type CallDetail struct {
    Time    time.Time `json:"time"`
    Model   string    `json:"model"`
    Backend string    `json:"backend"`
    Product string    `json:"product"`
    Tokens  int       `json:"tokens"`
}

// DayUse is one user's rollup for one day.
type dayUse struct {
    Day    string       `json:"day"`
    Total  int          `json:"total"`
    Calls  []CallDetail `json:"calls,omitempty"`
    // CallsCapped marks that per-call detail rows were evicted (the detail
    // ring is bounded; the Total rollup stays exact).
    CallsCapped bool `json:"calls_capped,omitempty"`
}

const maxDetailPerDay = 200

// Store holds per-sub daily usage + per-user budget overrides. Safe for
// concurrent use; a single mutex is fine at product-call rates.
type Store struct {
    mu        sync.Mutex
    usage     map[string]*dayUse
    overrides map[string]int // sub → daily budget override (0 = remove)
    path      string         // optional JSON snapshot path ("" = memory only)
}

func NewStore(path string) *Store {
    s := &Store{
        usage:     map[string]*dayUse{},
        overrides: map[string]int{},
        path:      path,
    }
    if path != "" {
        s.load()
    }
    return s
}

// load restores the usage snapshot from disk (best-effort; a missing or
// corrupt file just starts empty).
func (s *Store) load() {
    data, err := os.ReadFile(s.path)
    if err != nil {
        return
    }
    var snap struct {
        Usage     map[string]*dayUse `json:"usage"`
        Overrides map[string]int     `json:"overrides"`
    }
    if err := json.Unmarshal(data, &snap); err != nil {
        slog.Warn("try-quota snapshot load failed", "path", s.path, "error", err)
        return
    }
    if snap.Usage != nil {
        s.usage = snap.Usage
    }
    if snap.Overrides != nil {
        s.overrides = snap.Overrides
    }
    slog.Info("try-quota snapshot loaded", "path", s.path, "users", len(s.usage))
}

// Save persists the usage snapshot (call after accounting; debounced by the
// caller). Best-effort — a failed write logs and keeps serving.
func (s *Store) Save() {
    if s.path == "" {
        return
    }
    s.mu.Lock()
    snap := struct {
        Usage     map[string]*dayUse `json:"usage"`
        Overrides map[string]int     `json:"overrides"`
    }{s.usage, s.overrides}
    s.mu.Unlock()
    data, err := json.Marshal(snap)
    if err != nil {
        return
    }
    if err := os.MkdirAll(filepath.Dir(s.path), 0o755); err != nil {
        slog.Warn("try-quota snapshot mkdir failed", "error", err)
        return
    }
    if err := os.WriteFile(s.path, data, 0o600); err != nil {
        slog.Warn("try-quota snapshot write failed", "path", s.path, "error", err)
    }
}

// UsedToday returns the user's accounted total for the day of `now`.
func (s *Store) UsedToday(sub string, now time.Time) int {
    s.mu.Lock()
    defer s.mu.Unlock()
    u, ok := s.usage[sub]
    if !ok || u.Day != DayKey(now) {
        return 0
    }
    return u.Total
}

// Record accounts `tokens` for sub on the day of `now`, rolling the day
// bucket forward when the date changed. The store keeps ONE bucket per sub —
// a rollover discards the previous day's rollup (the quota gate only reads
// today). The per-call detail ring is bounded (maxDetailPerDay); the rollup
// Total stays exact.
func (s *Store) Record(sub string, tokens int, detail CallDetail, now time.Time) {
    if sub == "" {
        return
    }
    day := DayKey(now)
    if detail.Time.IsZero() {
        detail.Time = now
    }
    s.mu.Lock()
    u, ok := s.usage[sub]
    if !ok || u.Day != day {
        u = &dayUse{Day: day}
        s.usage[sub] = u
    }
    u.Total += tokens
    if len(u.Calls) >= maxDetailPerDay {
        u.Calls = u.Calls[1:]
        u.CallsCapped = true
    }
    u.Calls = append(u.Calls, detail)
    s.mu.Unlock()
}

// SetBudgetOverride pins a per-user daily budget (0 removes the override).
func (s *Store) SetBudgetOverride(sub string, budget int) {
    s.mu.Lock()
    defer s.mu.Unlock()
    if budget <= 0 {
        delete(s.overrides, sub)
        return
    }
    s.overrides[sub] = budget
}

// BudgetOverride returns the user's pinned budget override, if any.
func (s *Store) BudgetOverride(sub string) (int, bool) {
    s.mu.Lock()
    defer s.mu.Unlock()
    b, ok := s.overrides[sub]
    return b, ok
}

// UsageRow is one admin-facing usage summary row.
type UsageRow struct {
    Sub       string       `json:"sub"`
    Day       string       `json:"day"`
    Used      int          `json:"used"`
    Budget    int          `json:"budget"`
    Override  int          `json:"override,omitempty"`
    Recent    []CallDetail `json:"recent,omitempty"`
    LastSeen  time.Time    `json:"last_seen,omitempty"`
}

// Usage lists one summary row per tracked sub. A stale previous-day bucket
// (rollover not yet hit) reports as-is with no Recent detail; it does not
// count against today. budgetFn supplies the effective budget per sub and is
// invoked AFTER the store lock is released — a budgetFn that calls back into
// the Store (e.g. BudgetOverride) would otherwise self-deadlock.
func (s *Store) Usage(now time.Time, budgetFn func(sub string) int) []UsageRow {
    day := DayKey(now)
    s.mu.Lock()
    rows := make([]UsageRow, 0, len(s.usage))
    for sub, u := range s.usage {
        row := UsageRow{
            Sub:  sub,
            Day:  u.Day,
            Used: u.Total,
        }
        if ov, ok := s.overrides[sub]; ok {
            row.Override = ov
        }
        if u.Day == day {
            row.Recent = append([]CallDetail(nil), u.Calls...)
            if len(u.Calls) > 0 {
                row.LastSeen = u.Calls[len(u.Calls)-1].Time
            }
        }
        rows = append(rows, row)
    }
    s.mu.Unlock()
    if budgetFn != nil {
        for i := range rows {
            rows[i].Budget = budgetFn(rows[i].Sub)
        }
    }
    return rows
}

// VerifyToken validates a fusion-identity HS256 access token and returns the
// `sub` claim (the metering key). Any parse/validation failure is a reject.
func VerifyToken(tokenStr, secret string, now time.Time) (string, error) {
    claims := jwt.MapClaims{}
    _, err := jwt.ParseWithClaims(tokenStr, claims, func(t *jwt.Token) (interface{}, error) {
        if _, ok := t.Method.(*jwt.SigningMethodHMAC); !ok {
            return nil, jwt.ErrSignatureInvalid
        }
        return []byte(secret), nil
    }, jwt.WithTimeFunc(func() time.Time { return now }), jwt.WithExpirationRequired())
    if err != nil {
        return "", err
    }
    sub, _ := claims["sub"].(string)
    if sub == "" {
        return "", jwt.ErrTokenInvalidClaims
    }
    return sub, nil
}
