package tryquota

// #514: per-user daily token quota metering for the cloud-try real-product
// experience plan. Every product LLM call through the gateway carries a
// fusion-identity JWT; the meter keys usage by the JWT `sub`, pre-checks the
// request's estimated tokens against the user's daily budget, and records
// real usage (upstream usage.total_tokens, tokenizer fallback) at finalize.
// Soft-limit semantics: only NEW requests are rejected — in-flight requests
// finish and are accounted normally.

import (
    "crypto/rsa"
    "encoding/base64"
    "encoding/json"
    "fmt"
    "log/slog"
    "math"
    "math/big"
    "net/http"
    "os"
    "path/filepath"
    "sync"
    "time"

    "github.com/fusion-gateway/fusion-gateway/internal/httpx"
    "github.com/golang-jwt/jwt/v5"
)

// DayKey is the metering day bucket (local date, YYYY-MM-DD). Day rollover
// is derived from the record timestamp — no timers, a new day simply starts
// a fresh bucket on the next request.
func DayKey(t time.Time) string {
    return t.Format("2006-01-02")
}

// WindowKey (G3) generalizes the bucket key to the configured reset window:
// daily (default, = DayKey), weekly (ISO year-week, 2026-W45), monthly
// (2026-11). Rollover stays derived from the record timestamp — a new window
// simply starts a fresh bucket on the next request, no timers.
func WindowKey(t time.Time, window string) string {
    switch window {
    case "weekly":
        y, w := t.ISOWeek()
        return fmt.Sprintf("%d-W%02d", y, w)
    case "monthly":
        return t.Format("2006-01")
    default:
        return DayKey(t)
    }
}

// CallDetail is one accounted call (per-call detail rollup row).
type CallDetail struct {
    Time    time.Time `json:"time"`
    Model   string    `json:"model"`
    Backend string    `json:"backend"`
    Product string    `json:"product"`
    Tokens  int       `json:"tokens"`
}

// DayUse is one user's rollup for one metering window (day / ISO week /
// month — the field name predates G3 windows and the JSON key stays "day"
// for snapshot-file compatibility).
type dayUse struct {
    Day    string       `json:"day"`
    Total  int          `json:"total"`
    Calls  []CallDetail `json:"calls,omitempty"`
    // CallsCapped marks that per-call detail rows were evicted (the detail
    // ring is bounded; the Total rollup stays exact).
    CallsCapped bool `json:"calls_capped,omitempty"`
    // SoftAlerted (G3): the soft-limit webhook fired for this window —
    // once per window, not once per request.
    SoftAlerted bool `json:"soft_alerted,omitempty"`
}

const maxDetailPerDay = 200

// Store holds per-sub usage rollups + per-user budget overrides. Safe for
// concurrent use; a single mutex is fine at product-call rates.
type Store struct {
    mu        sync.Mutex
    usage     map[string]*dayUse
    overrides map[string]int // sub → daily budget override (0 = remove)
    window    string         // reset window: "" / daily / weekly / monthly
    path      string         // optional JSON snapshot path ("" = memory only)
    saveTimer *time.Timer    // pending debounced save (nil = none armed)
    writeMu   sync.Mutex     // serializes snapshot writes (Flush may race the timer)
}

// saveDebounce coalesces bursty Record calls into one snapshot write. A var
// (not const) so tests can shorten it.
var saveDebounce = 2 * time.Second

// scheduleSave arms the debounced snapshot write. Callers must hold s.mu.
// #526: without this the state_path snapshot was never written and the
// usage rollup reset on every restart.
func (s *Store) scheduleSave() {
    if s.path == "" {
        return
    }
    if s.saveTimer != nil {
        return
    }
    s.saveTimer = time.AfterFunc(saveDebounce, func() {
        s.mu.Lock()
        s.saveTimer = nil
        s.mu.Unlock()
        s.Save()
    })
}

// Flush persists any pending debounced snapshot immediately (shutdown path).
// No-op when nothing is pending or no path is configured.
func (s *Store) Flush() {
    s.mu.Lock()
    t := s.saveTimer
    s.saveTimer = nil
    s.mu.Unlock()
    if t != nil {
        t.Stop()
        s.Save()
    }
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

// Save persists the usage snapshot (debounced via scheduleSave; Flush forces
// a pending save). Best-effort — a failed write logs and keeps serving.
func (s *Store) Save() {
    if s.path == "" {
        return
    }
    // Serialize the write: Flush can race the debounce timer's final Save.
    s.writeMu.Lock()
    defer s.writeMu.Unlock()
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

// SetWindow (G3) switches the reset window ("" / daily / weekly / monthly).
// A switch starts fresh buckets — the old rollup keys stay in the snapshot
// until evicted, and the change is logged for the operator. Called at
// startup and from the reload path.
func (s *Store) SetWindow(window string) {
    switch window {
    case "", "daily", "weekly", "monthly":
    default:
        slog.Warn("try-quota: unknown reset window, keeping current", "window", window)
        return
    }
    s.mu.Lock()
    changed := s.window != window
    s.window = window
    s.mu.Unlock()
    if changed {
        slog.Info("try-quota: reset window set", "window", WindowKey(time.Now(), window))
    }
}

// UsedToday returns the user's accounted total for the current metering
// window of `now` (day / week / month per the configured window — the name
// predates G3 windows).
func (s *Store) UsedToday(sub string, now time.Time) int {
    s.mu.Lock()
    defer s.mu.Unlock()
    u, ok := s.usage[sub]
    if !ok || u.Day != WindowKey(now, s.window) {
        return 0
    }
    return u.Total
}

// MarkSoftAlerted (G3) records that the soft-limit alert fired for sub in
// the window of `now`. Returns true when this call is the one that armed it
// (once per window); false when an alert already fired. The check-and-set is
// atomic under the store mutex, so concurrent requests crossing the soft
// threshold together fire exactly one webhook.
func (s *Store) MarkSoftAlerted(sub string, now time.Time) bool {
    s.mu.Lock()
    defer s.mu.Unlock()
    key := WindowKey(now, s.window)
    u, ok := s.usage[sub]
    if !ok || u.Day != key {
        // No rollup yet (the gate marks before Record lands) — create the
        // bucket so the once-per-window flag has a home.
        u = &dayUse{Day: key}
        s.usage[sub] = u
    }
    if u.SoftAlerted {
        return false
    }
    u.SoftAlerted = true
    s.scheduleSave()
    return true
}

// Record accounts `tokens` for sub in the metering window of `now`, rolling
// the bucket forward when the window key changed. The store keeps ONE bucket
// per sub — a rollover discards the previous window's rollup (the quota gate
// only reads the current window). The per-call detail ring is bounded
// (maxDetailPerDay); the rollup Total stays exact.
func (s *Store) Record(sub string, tokens int, detail CallDetail, now time.Time) {
    if sub == "" {
        return
    }
    if detail.Time.IsZero() {
        detail.Time = now
    }
    s.mu.Lock()
    key := WindowKey(now, s.window)
    u, ok := s.usage[sub]
    if !ok || u.Day != key {
        u = &dayUse{Day: key}
        s.usage[sub] = u
    }
    u.Total += tokens
    if len(u.Calls) >= maxDetailPerDay {
        u.Calls = u.Calls[1:]
        u.CallsCapped = true
    }
    u.Calls = append(u.Calls, detail)
    s.scheduleSave()
    s.mu.Unlock()
}

// SetBudgetOverride pins a per-user daily budget (0 removes the override).
func (s *Store) SetBudgetOverride(sub string, budget int) {
    s.mu.Lock()
    defer s.mu.Unlock()
    if budget <= 0 {
        delete(s.overrides, sub)
        s.scheduleSave()
        return
    }
    s.overrides[sub] = budget
    s.scheduleSave()
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

// --- #524: identity-token verifiers (HS256 shared-secret and RS256 JWKS) ---

// Verifier validates a fusion-identity access token and returns the `sub`
// claim (the metering key). Each verifier accepts exactly one signing
// algorithm — never both — so a token can never cross modes (alg-confusion
// guard).
type Verifier interface {
    // Algorithm reports the accepted JOSE alg ("hs256" / "rs256").
    Algorithm() string
    // Verify returns the token's `sub`, or an error for any
    // parse/signature/claims failure.
    Verify(tokenStr string, now time.Time) (string, error)
}

// verifyWithKeyfunc is the shared parse+claims core: exp required, method
// pinned to the verifier's algorithm, non-empty sub mandatory.
func verifyWithKeyfunc(tokenStr string, now time.Time, alg string, kf jwt.Keyfunc) (string, error) {
    claims := jwt.MapClaims{}
    _, err := jwt.ParseWithClaims(tokenStr, claims, kf,
        jwt.WithTimeFunc(func() time.Time { return now }),
        jwt.WithExpirationRequired(),
        jwt.WithValidMethods([]string{alg}))
    if err != nil {
        return "", err
    }
    sub, _ := claims["sub"].(string)
    if sub == "" {
        return "", jwt.ErrTokenInvalidClaims
    }
    return sub, nil
}

// HS256Verifier verifies shared-secret identity tokens (test/local profile).
type HS256Verifier struct {
    secret string
}

func NewHS256Verifier(secret string) *HS256Verifier {
    return &HS256Verifier{secret: secret}
}

func (v *HS256Verifier) Algorithm() string { return "HS256" }

func (v *HS256Verifier) Verify(tokenStr string, now time.Time) (string, error) {
    return verifyWithKeyfunc(tokenStr, now, "HS256", func(t *jwt.Token) (interface{}, error) {
        return []byte(v.secret), nil
    })
}

// VerifyToken validates a fusion-identity HS256 access token and returns the
// `sub` claim (the metering key). Any parse/validation failure is a reject.
// Kept for compatibility; see Verifier for the algorithm-dispatched forms.
func VerifyToken(tokenStr, secret string, now time.Time) (string, error) {
    return NewHS256Verifier(secret).Verify(tokenStr, now)
}

// jwksRefreshInterval re-fetches the cached key set even when the requested
// kid is known (stale-refresh).
const jwksRefreshInterval = 15 * time.Minute

// RS256Verifier verifies RSA-signed identity tokens against the
// fusion-identity JWKS (identity_url + /.well-known/jwks.json — the same
// source the #523 shell-SSO exchange trusts). Keys are cached in memory and
// refreshed when stale or when a token carries an unknown kid (rotation).
type RS256Verifier struct {
    jwksURL string
    client  *http.Client
    // refetchCooldown is the minimum gap between JWKS fetches when the kid
    // is unknown — garbage-kid probes must not hammer the identity service.
    refetchCooldown time.Duration

    mu        sync.Mutex
    keys      map[string]*rsa.PublicKey
    fetchedAt time.Time
}

func NewRS256Verifier(jwksURL string) *RS256Verifier {
    return &RS256Verifier{
        jwksURL:         jwksURL,
        client:          &http.Client{Timeout: 10 * time.Second},
        refetchCooldown: 5 * time.Second,
        keys:            map[string]*rsa.PublicKey{},
    }
}

func (v *RS256Verifier) Algorithm() string { return "RS256" }

func (v *RS256Verifier) Verify(tokenStr string, now time.Time) (string, error) {
    return verifyWithKeyfunc(tokenStr, now, "RS256", func(t *jwt.Token) (interface{}, error) {
        kid, _ := t.Header["kid"].(string)
        return v.key(kid)
    })
}

// key resolves a kid to a public key, refreshing the cached JWKS when the
// entry is missing or stale. A refresh failure serves the stale set when it
// still contains the kid (identity briefly unreachable must not lock out
// known users).
func (v *RS256Verifier) key(kid string) (interface{}, error) {
    v.mu.Lock()
    key, ok := v.keys[kid]
    age := time.Since(v.fetchedAt)
    cooldown := v.refetchCooldown
    v.mu.Unlock()

    if ok && age < jwksRefreshInterval {
        return key, nil
    }
    if !ok && age < cooldown {
        return nil, fmt.Errorf("unknown kid %q (refetch throttled)", kid)
    }
    if err := v.fetch(); err != nil {
        if ok {
            slog.Warn("try-quota: JWKS refresh failed, serving stale key", "kid", kid, "error", err)
            return key, nil
        }
        return nil, fmt.Errorf("fetch jwks: %w", err)
    }

    v.mu.Lock()
    defer v.mu.Unlock()
    if key, ok := v.keys[kid]; ok {
        return key, nil
    }
    return nil, fmt.Errorf("unknown kid %q after jwks refresh", kid)
}

// fetch pulls and parses the JWKS document. Mirrors the middleware OIDC
// fetcher: bounded 10 MiB read, RSA keys only.
func (v *RS256Verifier) fetch() error {
    resp, err := v.client.Get(v.jwksURL)
    if err != nil {
        return err
    }
    defer resp.Body.Close()
    if resp.StatusCode != http.StatusOK {
        return fmt.Errorf("jwks status %d", resp.StatusCode)
    }

    var doc struct {
        Keys []struct {
            Kid string `json:"kid"`
            Kty string `json:"kty"`
            Use string `json:"use"`
            N   string `json:"n"`
            E   string `json:"e"`
        } `json:"keys"`
    }
    if err := json.NewDecoder(httpx.LimitResponseReader(resp.Body)).Decode(&doc); err != nil {
        return fmt.Errorf("decode jwks: %w", err)
    }

    keys := make(map[string]*rsa.PublicKey, len(doc.Keys))
    for _, jwk := range doc.Keys {
        if jwk.Kty != "RSA" {
            continue
        }
        // "use" is optional per RFC 7517; only reject an explicit non-sig use.
        if jwk.Use != "" && jwk.Use != "sig" {
            continue
        }
        pub, err := jwkToRSAPublicKey(jwk.N, jwk.E)
        if err != nil {
            slog.Warn("try-quota: skip jwk", "kid", jwk.Kid, "error", err)
            continue
        }
        keys[jwk.Kid] = pub
    }

    v.mu.Lock()
    v.keys = keys
    v.fetchedAt = time.Now()
    v.mu.Unlock()
    slog.Info("try-quota: jwks fetched", "url", v.jwksURL, "key_count", len(keys))
    return nil
}

// jwkToRSAPublicKey decodes an RSA JWK (base64url, unpadded; exponent is
// big-endian, possibly short).
func jwkToRSAPublicKey(nStr, eStr string) (*rsa.PublicKey, error) {
    nBytes, err := base64.RawURLEncoding.DecodeString(nStr)
    if err != nil {
        return nil, fmt.Errorf("decode n: %w", err)
    }
    eBytes, err := base64.RawURLEncoding.DecodeString(eStr)
    if err != nil {
        return nil, fmt.Errorf("decode e: %w", err)
    }
    if len(nBytes) == 0 || len(eBytes) == 0 || len(eBytes) > 8 {
        return nil, fmt.Errorf("invalid jwk modulus/exponent sizes")
    }
    n := new(big.Int).SetBytes(nBytes)
    e := new(big.Int).SetBytes(eBytes)
    if !e.IsInt64() || e.Int64() <= 1 || e.Int64() > math.MaxInt32 {
        return nil, fmt.Errorf("invalid jwk exponent")
    }
    return &rsa.PublicKey{N: n, E: int(e.Int64())}, nil
}
