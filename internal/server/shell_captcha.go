package server

import (
    "crypto/rand"
    "encoding/json"
    "encoding/base64"
    "fmt"
    "log/slog"
    "math/big"
    "net/http"
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
    // #604: compare case-insensitively — the mixed-case charset rendered as
    // rotated SVG glyphs makes case unreadable, so a correct reading fails
    // ~50% per ambiguous char under an exact match. Entropy loss is
    // negligible for a single-use 5-minute challenge behind the #587 lockout.
    if !strings.EqualFold(ch.code, code) {
        return "captcha_mismatch"
    }
    return ""
}

// captchaGlyphs (#614): 5x7 dot-matrix bitmaps for the challenge charset.
// The SVG must carry NO text nodes — a <text>-based captcha was extracted
// with one string match on the decoded markup. Glyphs render as stroke
// paths only. Lowercase code characters draw their uppercase glyph: the
// verify path is EqualFold (#604), so displayed case is cosmetic, and the
// glyph table halves. Charset rows: 5 columns, 'X' = stroke cell.
var captchaGlyphs = map[rune][7]string{
    '2': {".XXX.", "X...X", "....X", "...X.", "..X..", ".X...", "XXXXX"},
    '3': {"XXXXX", "....X", "...X.", "..X..", "...X.", "....X", "XXXXX"},
    '4': {"...X.", "..XX.", ".X.X.", "X..X.", "XXXXX", "...X.", "...X."},
    '5': {"XXXXX", "X....", "XXXX.", "....X", "....X", "X...X", ".XXX."},
    '6': {"..XX.", ".X...", "X....", "XXXX.", "X...X", "X...X", ".XXX."},
    '7': {"XXXXX", "....X", "...X.", "..X..", ".X...", ".X...", ".X..."},
    '8': {".XXX.", "X...X", "X...X", ".XXX.", "X...X", "X...X", ".XXX."},
    '9': {".XXX.", "X...X", "X...X", ".XXXX", "....X", "...X.", ".XX.."},
    'A': {".XXX.", "X...X", "X...X", "XXXXX", "X...X", "X...X", "X...X"},
    'B': {"XXXX.", "X...X", "X...X", "XXXX.", "X...X", "X...X", "XXXX."},
    'C': {".XXXX", "X....", "X....", "X....", "X....", "X....", ".XXXX"},
    'D': {"XXXX.", "X...X", "X...X", "X...X", "X...X", "X...X", "XXXX."},
    'E': {"XXXXX", "X....", "X....", "XXXX.", "X....", "X....", "XXXXX"},
    'F': {"XXXXX", "X....", "X....", "XXXX.", "X....", "X....", "X...."},
    'G': {".XXX.", "X...X", "X....", "X.XXX", "X...X", "X...X", ".XXX."},
    'H': {"X...X", "X...X", "X...X", "XXXXX", "X...X", "X...X", "X...X"},
    'J': {"....X", "....X", "....X", "....X", "X...X", "X...X", ".XXX."},
    'K': {"X...X", "X..X.", "X.X..", "XX...", "X.X..", "X..X.", "X...X"},
    'M': {"X...X", "XX.XX", "X.X.X", "X.X.X", "X...X", "X...X", "X...X"},
    'N': {"X...X", "XX..X", "X.X.X", "X..XX", "X...X", "X...X", "X...X"},
    'P': {"XXXX.", "X...X", "X...X", "XXXX.", "X....", "X....", "X...."},
    'Q': {".XXX.", "X...X", "X...X", "X...X", "X.X.X", "X..X.", ".XX.X"},
    'R': {"XXXX.", "X...X", "X...X", "XXXX.", "X.X..", "X..X.", "X...X"},
    'S': {".XXXX", "X....", "X....", ".XXX.", "....X", "....X", "XXXX."},
    'T': {"XXXXX", "..X..", "..X..", "..X..", "..X..", "..X..", "..X.."},
    'U': {"X...X", "X...X", "X...X", "X...X", "X...X", "X...X", ".XXX."},
    'V': {"X...X", "X...X", "X...X", "X...X", "X...X", ".X.X.", "..X.."},
    'W': {"X...X", "X...X", "X...X", "X.X.X", "X.X.X", "XX.XX", "X...X"},
    'X': {"X...X", "X...X", ".X.X.", "..X..", ".X.X.", "X...X", "X...X"},
    'Y': {"X...X", "X...X", ".X.X.", "..X..", "..X..", "..X..", "..X.."},
    'Z': {"XXXXX", "....X", "...X.", "..X..", ".X...", "X....", "XXXXX"},
}

// captchaGlyphFor maps a code character to its glyph: lowercase draws the
// uppercase shape (verify is EqualFold, #604).
func captchaGlyphFor(ch rune) ([7]string, bool) {
    upper := []rune(strings.ToUpper(string(ch)))[0]
    g, ok := captchaGlyphs[upper]
    return g, ok
}

// renderSVG draws the challenge as a self-contained SVG document: the code
// as per-character jittered dot-matrix stroke paths on a fixed light
// background plus noise strokes against trivial OCR. NO text nodes, NO
// character data — the decoded markup must not carry a machine-recoverable
// code (#614: the <text>-based rendering was extractable with one string
// match). Returns the base64 data URI the shell frontend drops straight
// into an <img src>.
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
    const (
        cell = 3.0        // glyph cell size in SVG units
        rowStep = 3.2     // slight vertical stretch for readability
        originY = 12.0
    )
    for i, ch := range code {
        glyph, ok := captchaGlyphFor(ch)
        if !ok {
            // Charset guarantees a glyph exists; a miss renders a bar so a
            // future charset edit fails visibly instead of dropping a char.
            glyph = [7]string{"X...X", "X...X", "X...X", "X...X", "X...X", "X...X", "X...X"}
        }
        gx := float64(14+i*24) + float64(jitter(5))
        rot := jitter(30) - 15
        cx := gx + 7.5
        cy := originY + 10.5
        sb.WriteString(fmt.Sprintf(`<g transform="rotate(%d %.1f %.1f)">`, rot, cx, cy))
        // Horizontal run-length strokes: contiguous cells in a row join into
        // one path segment, so verticals stay crisp without per-cell dots.
        for r, row := range glyph {
            c := 0
            for c < len(row) {
                if row[c] != 'X' {
                    c++
                    continue
                }
                start := c
                for c < len(row) && row[c] == 'X' {
                    c++
                }
                x0 := gx + float64(start)*cell
                x1 := gx + float64(c-1)*cell
                y := originY + float64(r)*rowStep
                sb.WriteString(fmt.Sprintf(
                    `<path d="M%.1f %.1f L%.1f %.1f" stroke="#334155" stroke-width="2.2" stroke-linecap="round" fill="none"/>`,
                    x0, y, x1, y))
            }
        }
        sb.WriteString(`</g>`)
    }
    sb.WriteString(`</svg>`)
    return "data:image/svg+xml;base64," + base64.StdEncoding.EncodeToString([]byte(sb.String()))
}

// serveCaptchaChallenge writes the shared single-use challenge shape (shell
// login #592, portal #603). Nil store = plane disabled: a stable
// {"enabled":false} the frontends probe and degrade on. Always no-store —
// a cached challenge is a burned challenge.
func serveCaptchaChallenge(w http.ResponseWriter, st *shellCaptchaStore) {
    w.Header().Set("Cache-Control", "no-store")
    if st == nil {
        w.Header().Set("Content-Type", "application/json")
        _ = json.NewEncoder(w).Encode(map[string]any{"enabled": false})
        return
    }
    id, code := st.issue()
    w.Header().Set("Content-Type", "application/json")
    _ = json.NewEncoder(w).Encode(map[string]any{
        "captcha_id":   id,
        "image":        renderSVG(code),
        "ttl_seconds":  int(st.ttl.Seconds()),
        "enabled":      true,
    })
}
