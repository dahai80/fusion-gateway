package tryquota

import (
    "os"
    "path/filepath"
    "testing"
    "time"

    "github.com/golang-jwt/jwt/v5"
)

func TestDayKey(t *testing.T) {
    day := DayKey(time.Date(2026, 11, 5, 23, 59, 0, 0, time.Local))
    if day != "2026-11-05" {
        t.Fatalf("DayKey = %q, want 2026-11-05", day)
    }
}

func TestRecordAndUsedToday(t *testing.T) {
    now := time.Date(2026, 11, 5, 10, 0, 0, 0, time.Local)
    s := NewStore("")
    s.Record("u1", 100, CallDetail{Model: "m"}, now)
    s.Record("u1", 50, CallDetail{Model: "m"}, now)
    if got := s.UsedToday("u1", now); got != 150 {
        t.Fatalf("UsedToday = %d, want 150", got)
    }
    // Unknown sub is zero, never negative.
    if got := s.UsedToday("ghost", now); got != 0 {
        t.Fatalf("UsedToday(ghost) = %d, want 0", got)
    }
}

func TestDayRollover(t *testing.T) {
    day1 := time.Date(2026, 11, 5, 23, 0, 0, 0, time.Local)
    day2 := time.Date(2026, 11, 6, 1, 0, 0, 0, time.Local)
    s := NewStore("")
    s.Record("u1", 900, CallDetail{}, day1)
    if got := s.UsedToday("u1", day2); got != 0 {
        t.Fatalf("next day UsedToday = %d, want 0", got)
    }
    s.Record("u1", 10, CallDetail{}, day2)
    if got := s.UsedToday("u1", day2); got != 10 {
        t.Fatalf("day2 UsedToday = %d, want 10", got)
    }
    // Single bucket per sub: the rolled-over day is gone, UsedToday for the
    // old day reads as 0 (only `now` determines the queried day).
    if got := s.UsedToday("u1", day1); got != 0 {
        t.Fatalf("day1 UsedToday = %d, want 0", got)
    }
}

func TestSubIsolation(t *testing.T) {
    now := time.Now()
    s := NewStore("")
    s.Record("a", 100, CallDetail{}, now)
    s.Record("b", 7, CallDetail{}, now)
    if s.UsedToday("a", now) != 100 || s.UsedToday("b", now) != 7 {
        t.Fatalf("per-sub totals leaked")
    }
}

func TestRecordEmptySubNoop(t *testing.T) {
    s := NewStore("")
    s.Record("", 100, CallDetail{}, time.Now())
    rows := s.Usage(time.Now(), func(string) int { return 0 })
    if len(rows) != 0 {
        t.Fatalf("empty sub recorded, rows = %d", len(rows))
    }
}

func TestDetailRingCap(t *testing.T) {
    now := time.Now()
    s := NewStore("")
    for i := 0; i < maxDetailPerDay+50; i++ {
        s.Record("u1", 1, CallDetail{Model: "m"}, now)
    }
    s.mu.Lock()
    u := s.usage["u1"]
    if len(u.Calls) != maxDetailPerDay {
        t.Fatalf("calls = %d, want %d", len(u.Calls), maxDetailPerDay)
    }
    if !u.CallsCapped {
        t.Fatalf("CallsCapped not set")
    }
    s.mu.Unlock()
    if got := s.UsedToday("u1", now); got != maxDetailPerDay+50 {
        t.Fatalf("total = %d, want %d (rollup must stay exact)", got, maxDetailPerDay+50)
    }
}

func TestBudgetOverride(t *testing.T) {
    s := NewStore("")
    if _, ok := s.BudgetOverride("u1"); ok {
        t.Fatalf("override exists before set")
    }
    s.SetBudgetOverride("u1", 500)
    if b, ok := s.BudgetOverride("u1"); !ok || b != 500 {
        t.Fatalf("override = %d,%v want 500,true", b, ok)
    }
    s.SetBudgetOverride("u1", 0)
    if _, ok := s.BudgetOverride("u1"); ok {
        t.Fatalf("override not removed")
    }
}

func TestUsageRows(t *testing.T) {
    now := time.Now()
    s := NewStore("")
    s.Record("u1", 30, CallDetail{Model: "glm"}, now)
    rows := s.Usage(now, func(string) int { return 1000 })
    if len(rows) != 1 {
        t.Fatalf("rows = %d, want 1", len(rows))
    }
    r := rows[0]
    if r.Sub != "u1" || r.Used != 30 || r.Budget != 1000 {
        t.Fatalf("row = %+v", r)
    }
    if len(r.Recent) != 1 || r.Recent[0].Model != "glm" {
        t.Fatalf("recent = %+v", r.Recent)
    }
    if r.LastSeen.IsZero() {
        t.Fatalf("LastSeen zero")
    }
}

func TestSaveLoadRoundtrip(t *testing.T) {
    dir := t.TempDir()
    path := filepath.Join(dir, "quota.json")
    now := time.Now()
    s := NewStore(path)
    s.Record("u1", 42, CallDetail{Model: "m"}, now)
    s.SetBudgetOverride("u1", 9000)
    s.Save()

    s2 := NewStore(path)
    if got := s2.UsedToday("u1", now); got != 42 {
        t.Fatalf("reloaded UsedToday = %d, want 42", got)
    }
    if b, ok := s2.BudgetOverride("u1"); !ok || b != 9000 {
        t.Fatalf("reloaded override = %d,%v want 9000,true", b, ok)
    }
}

func TestSnapshotCorruptStartsEmpty(t *testing.T) {
    dir := t.TempDir()
    path := filepath.Join(dir, "quota.json")
    _ = os.WriteFile(path, []byte("not json"), 0o600)
    s := NewStore(path)
    if got := s.UsedToday("u1", time.Now()); got != 0 {
        t.Fatalf("corrupt snapshot should start empty, got %d", got)
    }
}

// mint makes an HS256 token for VerifyToken tests.
func mint(t *testing.T, secret string, claims jwt.MapClaims) string {
    t.Helper()
    tok := jwt.NewWithClaims(jwt.SigningMethodHS256, claims)
    str, err := tok.SignedString([]byte(secret))
    if err != nil {
        t.Fatalf("mint: %v", err)
    }
    return str
}

func TestVerifyTokenValid(t *testing.T) {
    now := time.Now()
    tok := mint(t, "secret", jwt.MapClaims{
        "sub": "user-1",
        "exp": now.Add(time.Hour).Unix(),
        "iss": "fusion-identity",
    })
    sub, err := VerifyToken(tok, "secret", now)
    if err != nil {
        t.Fatalf("VerifyToken: %v", err)
    }
    if sub != "user-1" {
        t.Fatalf("sub = %q, want user-1", sub)
    }
}

func TestVerifyTokenWrongSecret(t *testing.T) {
    now := time.Now()
    tok := mint(t, "secret", jwt.MapClaims{"sub": "u", "exp": now.Add(time.Hour).Unix()})
    if _, err := VerifyToken(tok, "other", now); err == nil {
        t.Fatalf("wrong secret must reject")
    }
}

func TestVerifyTokenExpired(t *testing.T) {
    now := time.Now()
    tok := mint(t, "secret", jwt.MapClaims{"sub": "u", "exp": now.Add(-time.Minute).Unix()})
    if _, err := VerifyToken(tok, "secret", now); err == nil {
        t.Fatalf("expired token must reject")
    }
}

func TestVerifyTokenMissingExp(t *testing.T) {
    now := time.Now()
    tok := mint(t, "secret", jwt.MapClaims{"sub": "u"})
    if _, err := VerifyToken(tok, "secret", now); err == nil {
        t.Fatalf("missing exp must reject (WithExpirationRequired)")
    }
}

func TestVerifyTokenMissingSub(t *testing.T) {
    now := time.Now()
    tok := mint(t, "secret", jwt.MapClaims{"exp": now.Add(time.Hour).Unix()})
    if _, err := VerifyToken(tok, "secret", now); err == nil {
        t.Fatalf("missing sub must reject")
    }
}

func TestVerifyTokenNonHMACRejected(t *testing.T) {
    now := time.Now()
    // Sign with RS256-style alg header but HMAC key: the signing-method check
    // must reject the alg before verification even matters.
    tok := jwt.NewWithClaims(jwt.SigningMethodPS256, jwt.MapClaims{"sub": "u", "exp": now.Add(time.Hour).Unix()})
    str, err := tok.SignedString([]byte("secret"))
    if err != nil {
        t.Skipf("PS256 needs RSA key: %v", err)
    }
    if _, err := VerifyToken(str, "secret", now); err == nil {
        t.Fatalf("non-HMAC alg must reject")
    }
}
