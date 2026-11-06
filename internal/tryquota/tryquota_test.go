package tryquota

import (
    "crypto/rand"
    "crypto/rsa"
    "encoding/base64"
    "encoding/json"
    "math/big"
    "net/http"
    "net/http/httptest"
    "os"
    "path/filepath"
    "sync"
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

// #526: Record must schedule a debounced save when a snapshot path is set —
// without it the usage rollup resets on every restart and state_path is dead
// config.
func TestRecordPersistsDebounced(t *testing.T) {
    old := saveDebounce
    saveDebounce = 30 * time.Millisecond
    defer func() { saveDebounce = old }()
    path := filepath.Join(t.TempDir(), "quota.json")
    now := time.Now()
    s := NewStore(path)
    s.Record("u1", 42, CallDetail{Model: "m"}, now)
    if _, err := os.Stat(path); !os.IsNotExist(err) {
        t.Fatalf("snapshot written before debounce elapsed, want no file")
    }
    time.Sleep(150 * time.Millisecond)
    s2 := NewStore(path)
    if got := s2.UsedToday("u1", now); got != 42 {
        t.Fatalf("reloaded UsedToday = %d, want 42", got)
    }
}

// #526: SetBudgetOverride participates in the same debounced persistence.
func TestSetBudgetOverridePersistsDebounced(t *testing.T) {
    old := saveDebounce
    saveDebounce = 30 * time.Millisecond
    defer func() { saveDebounce = old }()
    path := filepath.Join(t.TempDir(), "quota.json")
    s := NewStore(path)
    s.SetBudgetOverride("u1", 9000)
    time.Sleep(150 * time.Millisecond)
    s2 := NewStore(path)
    if b, ok := s2.BudgetOverride("u1"); !ok || b != 9000 {
        t.Fatalf("reloaded override = %d,%v want 9000,true", b, ok)
    }
}

// #526: Flush persists any pending debounced snapshot immediately (shutdown
// path) — no waiting for the debounce to elapse.
func TestFlushPersistsImmediately(t *testing.T) {
    old := saveDebounce
    saveDebounce = time.Hour
    defer func() { saveDebounce = old }()
    path := filepath.Join(t.TempDir(), "quota.json")
    now := time.Now()
    s := NewStore(path)
    s.Record("u1", 42, CallDetail{Model: "m"}, now)
    s.Flush()
    s2 := NewStore(path)
    if got := s2.UsedToday("u1", now); got != 42 {
        t.Fatalf("reloaded UsedToday after Flush = %d, want 42", got)
    }
}

// #526: Flush without a pending save is a no-op (path empty or timer nil).
func TestFlushNoPendingNoop(t *testing.T) {
    s := NewStore("")
    s.Flush()
    path := filepath.Join(t.TempDir(), "quota.json")
    s = NewStore(path)
    s.Flush()
    if _, err := os.Stat(path); !os.IsNotExist(err) {
        t.Fatalf("Flush with no pending save wrote a file")
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

// --- #524: RS256 / JWKS verification (deployed fusion-identity signs RS256) ---

// rsaJWK builds a JWK-style map for a public key (base64url, unpadded).
func rsaJWK(pub *rsa.PublicKey, kid string) map[string]interface{} {
    return map[string]interface{}{
        "kty": "RSA",
        "use": "sig",
        "alg": "RS256",
        "kid": kid,
        "n":   base64.RawURLEncoding.EncodeToString(pub.N.Bytes()),
        "e":   base64.RawURLEncoding.EncodeToString(big.NewInt(int64(pub.E)).Bytes()),
    }
}

// jwksServer serves a rotating JWKS document. Swap keys to simulate
// identity-side rotation and assert the verifier refreshes.
type jwksServer struct {
    mu   sync.Mutex
    keys []map[string]interface{}
    hits int
}

func (s *jwksServer) setKeys(keys ...map[string]interface{}) {
    s.mu.Lock()
    s.keys = keys
    s.mu.Unlock()
}

func (s *jwksServer) handler() http.HandlerFunc {
    return func(w http.ResponseWriter, r *http.Request) {
        s.mu.Lock()
        defer s.mu.Unlock()
        s.hits++
        w.Header().Set("Content-Type", "application/json")
        _ = json.NewEncoder(w).Encode(map[string]interface{}{"keys": s.keys})
    }
}

func testRSAKey(t *testing.T) *rsa.PrivateKey {
    t.Helper()
    key, err := rsa.GenerateKey(rand.Reader, 2048)
    if err != nil {
        t.Fatalf("rsa keygen: %v", err)
    }
    return key
}

func mintRS256(t *testing.T, key *rsa.PrivateKey, kid string, claims jwt.MapClaims) string {
    t.Helper()
    tok := jwt.NewWithClaims(jwt.SigningMethodRS256, claims)
    tok.Header["kid"] = kid
    str, err := tok.SignedString(key)
    if err != nil {
        t.Fatalf("mint RS256: %v", err)
    }
    return str
}

func TestRS256VerifierValid(t *testing.T) {
    now := time.Now()
    key := testRSAKey(t)
    srv := &jwksServer{}
    srv.setKeys(rsaJWK(&key.PublicKey, "kid-a"))
    httpSrv := httptest.NewServer(srv.handler())
    defer httpSrv.Close()

    v := NewRS256Verifier(httpSrv.URL)
    v.refetchCooldown = 0
    tok := mintRS256(t, key, "kid-a", jwt.MapClaims{"sub": "user-rs", "exp": now.Add(time.Hour).Unix()})
    sub, err := v.Verify(tok, now)
    if err != nil {
        t.Fatalf("RS256 Verify: %v", err)
    }
    if sub != "user-rs" {
        t.Fatalf("sub = %q, want user-rs", sub)
    }
}

func TestRS256VerifierExpired(t *testing.T) {
    now := time.Now()
    key := testRSAKey(t)
    srv := &jwksServer{}
    srv.setKeys(rsaJWK(&key.PublicKey, "kid-a"))
    httpSrv := httptest.NewServer(srv.handler())
    defer httpSrv.Close()

    v := NewRS256Verifier(httpSrv.URL)
    v.refetchCooldown = 0
    tok := mintRS256(t, key, "kid-a", jwt.MapClaims{"sub": "u", "exp": now.Add(-time.Minute).Unix()})
    if _, err := v.Verify(tok, now); err == nil {
        t.Fatalf("expired RS256 token must reject")
    }
}

func TestRS256VerifierGarbage(t *testing.T) {
    srv := &jwksServer{}
    httpSrv := httptest.NewServer(srv.handler())
    defer httpSrv.Close()

    v := NewRS256Verifier(httpSrv.URL)
    v.refetchCooldown = 0
    if _, err := v.Verify("not-a-jwt", time.Now()); err == nil {
        t.Fatalf("garbage token must reject")
    }
}

func TestRS256VerifierUnknownKid(t *testing.T) {
    now := time.Now()
    key := testRSAKey(t)
    srv := &jwksServer{}
    srv.setKeys(rsaJWK(&key.PublicKey, "kid-a"))
    httpSrv := httptest.NewServer(srv.handler())
    defer httpSrv.Close()

    v := NewRS256Verifier(httpSrv.URL)
    v.refetchCooldown = 0
    tok := mintRS256(t, key, "kid-other", jwt.MapClaims{"sub": "u", "exp": now.Add(time.Hour).Unix()})
    if _, err := v.Verify(tok, now); err == nil {
        t.Fatalf("unknown kid must reject")
    }
}

// Algorithm-confusion guard: an HS256 token must never verify in rs256 mode.
func TestRS256VerifierRejectsHS256Token(t *testing.T) {
    now := time.Now()
    srv := &jwksServer{}
    httpSrv := httptest.NewServer(srv.handler())
    defer httpSrv.Close()

    v := NewRS256Verifier(httpSrv.URL)
    v.refetchCooldown = 0
    hsTok := mint(t, "secret", jwt.MapClaims{"sub": "u", "exp": now.Add(time.Hour).Unix()})
    if _, err := v.Verify(hsTok, now); err == nil {
        t.Fatalf("HS256 token in rs256 mode must reject (alg confusion)")
    }
}

// Algorithm-confusion guard, reverse direction: an RS256 token must never
// verify against the HS256 path.
func TestHS256VerifierRejectsRS256Token(t *testing.T) {
    now := time.Now()
    key := testRSAKey(t)
    tok := mintRS256(t, key, "kid-a", jwt.MapClaims{"sub": "u", "exp": now.Add(time.Hour).Unix()})
    if _, err := VerifyToken(tok, "secret", now); err == nil {
        t.Fatalf("RS256 token in hs256 mode must reject (alg confusion)")
    }
}

// Key rotation: once the identity JWKS drops the old kid and serves a new
// one, tokens signed by the new key must verify without a restart.
func TestRS256VerifierRefreshOnRotation(t *testing.T) {
    now := time.Now()
    key1 := testRSAKey(t)
    key2 := testRSAKey(t)
    srv := &jwksServer{}
    srv.setKeys(rsaJWK(&key1.PublicKey, "kid-a"))
    httpSrv := httptest.NewServer(srv.handler())
    defer httpSrv.Close()

    v := NewRS256Verifier(httpSrv.URL)
    v.refetchCooldown = 0
    tok1 := mintRS256(t, key1, "kid-a", jwt.MapClaims{"sub": "u1", "exp": now.Add(time.Hour).Unix()})
    if _, err := v.Verify(tok1, now); err != nil {
        t.Fatalf("pre-rotation Verify: %v", err)
    }

    srv.setKeys(rsaJWK(&key2.PublicKey, "kid-b"))
    tok2 := mintRS256(t, key2, "kid-b", jwt.MapClaims{"sub": "u2", "exp": now.Add(time.Hour).Unix()})
    sub, err := v.Verify(tok2, now)
    if err != nil {
        t.Fatalf("post-rotation Verify: %v", err)
    }
    if sub != "u2" {
        t.Fatalf("sub = %q, want u2", sub)
    }
}

// DoS guard: repeated unknown-kid probes must not hammer the JWKS endpoint
// — fetches are rate-limited by the refetch cooldown.
func TestRS256VerifierRefetchThrottled(t *testing.T) {
    now := time.Now()
    key := testRSAKey(t)
    srv := &jwksServer{}
    srv.setKeys(rsaJWK(&key.PublicKey, "kid-a"))
    httpSrv := httptest.NewServer(srv.handler())
    defer httpSrv.Close()

    v := NewRS256Verifier(httpSrv.URL)
    tok := mintRS256(t, key, "kid-unknown", jwt.MapClaims{"sub": "u", "exp": now.Add(time.Hour).Unix()})
    for i := 0; i < 5; i++ {
        if _, err := v.Verify(tok, now); err == nil {
            t.Fatalf("unknown kid must reject")
        }
    }
    if srv.hits > 2 {
        t.Fatalf("JWKS fetched %d times for 5 throttled probes, want <= 2", srv.hits)
    }
}

func TestWindowKeyVariants(t *testing.T) {
    ts := time.Date(2026, 11, 6, 23, 45, 0, 0, time.Local)
    if got := WindowKey(ts, ""); got != "2026-11-06" {
        t.Fatalf("default window key = %q, want 2026-11-06", got)
    }
    if got := WindowKey(ts, "daily"); got != "2026-11-06" {
        t.Fatalf("daily window key = %q", got)
    }
    if got := WindowKey(ts, "monthly"); got != "2026-11" {
        t.Fatalf("monthly window key = %q, want 2026-11", got)
    }
    wk := WindowKey(ts, "weekly")
    if len(wk) != 8 || wk[4] != '-' || wk[5] != 'W' {
        t.Fatalf("weekly window key %q must look like 2026-W45", wk)
    }
    // 2026-11-06 is ISO week 45 (Friday).
    if wk != "2026-W45" {
        t.Fatalf("weekly window key = %q, want 2026-W45", wk)
    }
}

func TestRecordAccumulatesAcrossDaysInWeeklyWindow(t *testing.T) {
    s := NewStore("")
    s.SetWindow("weekly")
    mon := time.Date(2026, 11, 2, 10, 0, 0, 0, time.Local)
    fri := time.Date(2026, 11, 6, 10, 0, 0, 0, time.Local)
    s.Record("u1", 100, CallDetail{}, mon)
    s.Record("u1", 50, CallDetail{}, fri)
    if got := s.UsedToday("u1", fri); got != 150 {
        t.Fatalf("same ISO week must accumulate, got %d", got)
    }
    // Next Monday starts a fresh bucket.
    next := time.Date(2026, 11, 9, 10, 0, 0, 0, time.Local)
    if got := s.UsedToday("u1", next); got != 0 {
        t.Fatalf("new week must start empty, got %d", got)
    }
}

func TestMonthlyWindowResetsOnMonthRollover(t *testing.T) {
    s := NewStore("")
    s.SetWindow("monthly")
    s.Record("u1", 300, CallDetail{}, time.Date(2026, 10, 31, 23, 0, 0, 0, time.Local))
    nov := time.Date(2026, 11, 1, 0, 5, 0, 0, time.Local)
    if got := s.UsedToday("u1", nov); got != 0 {
        t.Fatalf("new month must start empty, got %d", got)
    }
}

func TestSetWindowSwitchBuckets(t *testing.T) {
    s := NewStore("")
    s.Record("u1", 100, CallDetail{}, time.Date(2026, 11, 6, 10, 0, 0, 0, time.Local))
    s.SetWindow("monthly")
    if got := s.UsedToday("u1", time.Date(2026, 11, 6, 10, 0, 0, 0, time.Local)); got != 0 {
        t.Fatalf("window switch must start a fresh bucket, got %d", got)
    }
    s.Record("u1", 40, CallDetail{}, time.Date(2026, 11, 6, 11, 0, 0, 0, time.Local))
    if got := s.UsedToday("u1", time.Date(2026, 11, 6, 12, 0, 0, 0, time.Local)); got != 40 {
        t.Fatalf("record after switch must land in new bucket, got %d", got)
    }
}

func TestMarkSoftAlertedOncePerWindow(t *testing.T) {
    s := NewStore("")
    now := time.Date(2026, 11, 6, 10, 0, 0, 0, time.Local)
    s.Record("u1", 100, CallDetail{}, now)
    if fired := s.MarkSoftAlerted("u1", now); !fired {
        t.Fatal("first mark must fire")
    }
    if fired := s.MarkSoftAlerted("u1", now); fired {
        t.Fatal("second mark in same window must not fire")
    }
    next := time.Date(2026, 11, 7, 10, 0, 0, 0, time.Local)
    if fired := s.MarkSoftAlerted("u1", next); !fired {
        t.Fatal("new window must allow the alert again")
    }
}

func TestUsageReportsSoftAlerted(t *testing.T) {
    s := NewStore("")
    now := time.Date(2026, 11, 6, 10, 0, 0, 0, time.Local)
    s.Record("alerted", 100, CallDetail{}, now)
    s.Record("quiet", 50, CallDetail{}, now)
    s.MarkSoftAlerted("alerted", now)
    rows := s.Usage(now, nil)
    bySub := map[string]UsageRow{}
    for _, r := range rows {
        bySub[r.Sub] = r
    }
    if !bySub["alerted"].SoftAlerted {
        t.Fatal("alerted row must carry soft_alerted")
    }
    if bySub["quiet"].SoftAlerted {
        t.Fatal("quiet row must not carry soft_alerted")
    }
}
