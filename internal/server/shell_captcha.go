package server

import (
    "crypto/rand"
    "encoding/base64"
    "fmt"
    "log/slog"
    "math/big"
    "strings"
    "sync"
    "time"
)

// Issue #592: server-side CAPTCHA for the shell login plane (contract for
// fusion-web-shell #139). Challenges are single-use: ANY verification
// attempt (pass or fail) burns the challenge, so a replay — even with the
// correct code — is dead. The store is bounded by pruning expired entries on
// every issuance. No external CAPTCHA service: the image is server-rendered
// SVG with an unambiguous charset (no 0/O/1/I/l).

const (
    shellCaptchaLength = 5
    // Unambiguous charset: no 0/O (confusable) and no 1/I/l (confusable).
    shellCaptchaCharset = "23456789ABCDEFGHJKMNPQRSTUVWXYZabcdefghjkmnpqrstuvwxyz"
)

type shellCaptchaChallenge struct {
    code    string
    expires time.Time
    burned  bool
}

type shellCaptchaStore struct {
    mu         sync.Mutex
    challenges map[string]*shellCaptchaChallenge
    ttl        time.Duration
    nowFn      func() time.Time
}

func newShellCaptchaStore(ttl time.Duration) *shellCaptchaStore {
    return &shellCaptchaStore{
        challenges: map[string]*shellCaptchaChallenge{},
        ttl:        ttl,
        nowFn:      time.Now,
    }
}

func (c *shellCaptchaStore) now() time.Time {
    if c.nowFn != nil {
        return c.nowFn()
    }
    return time.Now()
}

// issue mints a fresh challenge and prunes expired entries so the map stays
// bounded even under challenge-farming.
func (c *shellCaptchaStore) issue() (string, string) {
    c.mu.Lock()
    defer c.mu.Unlock()
    now := c.now()
    for id, ch := range c.challenges {
        if !ch.expires.After(now) {
            delete(c.challenges, id)
        }
    }
    idBytes := make([]byte, 16)
    if _, err := rand.Read(idBytes); err != nil {
        // crypto/rand failure is fatal-quality; panic is unacceptable in the
        // request path — fall back to a time-derived id (still unpredictable
        // enough for a 5-minute single-use challenge, and logged loudly).
        slog.Error("shell captcha: crypto/rand failed, falling back to time-derived id", "error", err)
        idBytes = []byte(fmt.Sprintf("%d", now.UnixNano()))
    }
    id := base64.RawURLEncoding.EncodeToString(idBytes)
    var sb strings.Builder
    for i := 0; i < shellCaptchaLength; i++ {
        n, err := rand.Int(rand.Reader, big.NewInt(int64(len(shellCaptchaCharset))))
        if err != nil {
            n = big.NewInt(int64(now.UnixNano()+int64(i)) % int64(len(shellCaptchaCharset)))
        }
        sb.WriteByte(shellCaptchaCharset[n.Int64()])
    }
    code := sb.String()
    c.challenges[id] = &shellCaptchaChallenge{code: code, expires: now.Add(c.ttl)}
    return id, code
}

// verify consumes the challenge (single-use, pass or fail) and returns a
// sentinel error string: "" = valid, "captcha_missing", "captcha_expired"
// (unknown, expired, or burned), "captcha_mismatch".
func (c *shellCaptchaStore) verify(id, code string) string {
    if id == "" || code == "" {
        return "captcha_missing"
    }
    c.mu.Lock()
    defer c.mu.Unlock()
    ch, ok := c.challenges[id]
    if !ok {
        return "captcha_expired"
    }
    // Single-use: burn before judging so a parallel replay also loses.
    delete(c.challenges, id)
    if !ch.expires.After(c.now()) {
        return "captcha_expired"
    }
    if ch.code != code {
        return "captcha_mismatch"
    }
    return ""
}

// renderSVG draws the challenge as a self-contained SVG document: the code
// as per-character jittered text on a fixed light background (readable on
// any UI theme) plus noise strokes against trivial OCR. Returns the base64
// data URI the shell frontend drops straight into an <img src>.
func renderSVG(code string) string {
    var sb strings.Builder
    sb.WriteString(`<svg xmlns="http://www.w3.org/2000/svg" width="140" height="48" viewBox="0 0 140 48">`)
    sb.WriteString(`<rect width="140" height="48" fill="#f1f5f9"/>`)
    seed := time.Now().UnixNano()
    jitter := func(mod int) int {
        seed = seed*6364136223846793005 + 1442695040888963407
        return int(uint64(seed)>>33) % mod
    }
    for i := 0; i < 3; i++ {
        sb.WriteString(fmt.Sprintf(
            `<path d="M0,%d Q35,%d 70,%d T140,%d" stroke="#94a3b8" stroke-width="1" fill="none" opacity="0.6"/>`,
            8+jitter(30), jitter(40), 8+jitter(30), jitter(40)))
    }
    for i, ch := range code {
        x := 16 + i*24 + jitter(5)
        y := 32 + jitter(7)
        rot := jitter(30) - 15
        sb.WriteString(fmt.Sprintf(
            `<text x="%d" y="%d" font-family="monospace" font-size="24" font-weight="bold" fill="#334155" transform="rotate(%d %d %d)">%s</text>`,
            x, y, rot, x, y, escapeSVGText(string(ch))))
    }
    sb.WriteString(`</svg>`)
    return "data:image/svg+xml;base64," + base64.StdEncoding.EncodeToString([]byte(sb.String()))
}

func escapeSVGText(s string) string {
    r := strings.NewReplacer("&", "&amp;", "<", "&lt;", ">", "&gt;", `"`, "&#34;", "'", "&#39;")
    return r.Replace(s)
}
