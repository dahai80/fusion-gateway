package server

import (
    "crypto/rand"
    "encoding/binary"
    "encoding/json"
    "encoding/base64"
    "fmt"
    "log/slog"
    "math/big"
    "net/http"
    "os"
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
    // testMode (#688): the sanctioned deterministic path for CI/E2E —
    // serveCaptchaChallenge then includes the challenge code in the JSON
    // response so suites need no image decoding. Enabled only when
    // captchaTestModeEnabled() passes; production can never turn it on.
    testMode bool
}

func newShellCaptchaStore(ttl time.Duration) *shellCaptchaStore {
    return &shellCaptchaStore{
        challenges: map[string]*shellCaptchaChallenge{},
        ttl:        ttl,
        nowFn:      time.Now,
    }
}

// captchaTestModeEnabled (#688): the deterministic test path is opt-in via
// FUSION_CAPTCHA_TESTMODE=1 and is refused outright when
// FUSION_ENV=production — a bypass token that ships enabled by default
// would be a static bypass token in disguise. Production operators get no
// flag that turns it on; CI and local E2E set both env vars explicitly.
func captchaTestModeEnabled() bool {
    if os.Getenv("FUSION_ENV") == "production" {
        return false
    }
    return os.Getenv("FUSION_CAPTCHA_TESTMODE") == "1"
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

// renderSVG draws the challenge as a self-contained SVG document (#614: no
// text nodes — the markup must never carry a machine-recoverable code).
//
// #688 hardening: the previous renderer was deterministically decodable —
// fixed glyph geometry quantized to exact grid constants (originY 12.0,
// rowStep 3.2, cell 3.0), straight horizontal run-length strokes, and a
// rotation-only transform, so template matching succeeded 100% of the time.
// Now every challenge derives its geometry from a crypto/rand seed and the
// per-stroke structure defeats grid quantization:
//   - per-glyph transform gains translate + non-uniform scale
//     (translate/rotate/scale), so no fixed origin recovers rows;
//   - every horizontal run renders as a QUADRATIC curve (control point
//     offset by up to ±0.9 units) with per-run y jitter ±0.9 — straight-line
//     run-length parsing finds nothing and row buckets blur;
//   - each glyph carries up to 1 distractor stroke inside its own bounding
//     box (lighter than real strokes), polluting any grid
//     reconstruction with cells no glyph bitmap contains;
//   - stroke widths and background noise are seeded per challenge.
// #694 rebalance (human legibility): the original hardening values stacked
// past what a ~16px glyph cell survives — rotation ±16°, bulge ±1.9 (over
// half the row pitch) and same-weight distractors made legit challenges
// unreadable. Now: 200x64 canvas with a 4.0 cell, rotation capped ±8°,
// jitter under 1/4 of the 4.4 row pitch, distractors short + 0.5 opacity.
// Anti-decode structure is unchanged: crypto/rand seeding, quadratic
// curves, translate+rotate transform, no text nodes. Returns the base64
// data URI the shell frontend drops straight into an <img src>.
func renderSVG(code string) string {
    var sb strings.Builder
    sb.WriteString(`<svg xmlns="http://www.w3.org/2000/svg" width="200" height="64" viewBox="0 0 200 64">`)
    sb.WriteString(`<rect width="200" height="64" fill="#f1f5f9"/>`)
    // #688: per-CHALLENGE entropy from crypto/rand — the previous
    // time.Now().UnixNano() seed was only per-process-time unpredictability;
    // the failure mode was the fixed geometry, not the seed, so the seed
    // now drives every coordinate decision below.
    var seed int64
    var seedBuf [8]byte
    if _, err := rand.Read(seedBuf[:]); err != nil {
        // Same fatal-quality fallback as issue(): a time-derived seed still
        // randomizes the render; log loudly.
        slog.Error("shell captcha: crypto/rand failed for render seed, falling back to time", "error", err)
        seed = time.Now().UnixNano()
    } else {
        seed = int64(binary.BigEndian.Uint64(seedBuf[:]))
    }
    jitter := func(mod int) int {
        seed = seed*6364136223846793005 + 1442695040888963407
        return int(uint64(seed)>>33) % mod
    }
    // Background noise: 4 curves with random amplitude + 2 short arcs
    // crossing the glyph band.
    for i := 0; i < 4; i++ {
        sb.WriteString(fmt.Sprintf(
            `<path d="M0,%d Q60,%d 120,%d T200,%d" stroke="#94a3b8" stroke-width="1" fill="none" opacity="0.6"/>`,
            10+jitter(40), jitter(50), 10+jitter(40), jitter(50)))
    }
    for i := 0; i < 2; i++ {
        x := jitter(80) + 30
        sb.WriteString(fmt.Sprintf(
            `<path d="M%d,%d q6,%d 12,%d q6,%d 12,%d" stroke="#94a3b8" stroke-width="1" fill="none" opacity="0.5"/>`,
            x, 12+jitter(36), jitter(13)-6, jitter(13)-6, jitter(13)-6, jitter(13)-6))
    }
    const (
        cell    = 4.0  // glyph cell size in SVG units (#694: roomier canvas)
        rowStep = 4.4  // slight vertical stretch for readability
        originY = 16.0
    )
    for i, ch := range code {
        glyph, ok := captchaGlyphFor(ch)
        if !ok {
            // Charset guarantees a glyph exists; a miss renders a bar so a
            // future charset edit fails visibly instead of dropping a char.
            glyph = [7]string{"X...X", "X...X", "X...X", "X...X", "X...X", "X...X", "X...X"}
        }
        gx := float64(22+i*36) + float64(jitter(5))
        rot := jitter(17) - 8 // #694: ±8° — ±16° destroyed legibility
        gty := float64(jitter(31)-15) / 10.0 // per-glyph vertical shift ±1.5
        cx := gx + 10.0
        cy := originY + 3*rowStep
        sb.WriteString(fmt.Sprintf(
            `<g transform="translate(0 %.1f) rotate(%d %.1f %.1f)">`,
            gty, rot, cx, cy))
        // Per-row strokes with y jitter and quadratic curvature (#688).
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
                x0 := gx + float64(start)*cell + float64(jitter(9)-4)/10.0
                x1 := gx + float64(c-1)*cell + float64(jitter(9)-4)/10.0
                // #694: y jitter and bulge ±0.9 — under 1/4 of the 4.4 row
                // pitch (the old ±1.2/±1.9 blurred rows together).
                y := originY + float64(r)*rowStep + float64(jitter(19)-9)/10.0
                ctrlY := y + float64(jitter(19)-9)/10.0
                ctrlX := (x0 + x1) / 2 + float64(jitter(21)-10)/10.0
                sw := 2.0 + float64(jitter(7))/10.0 // stroke width 2.0-2.6
                sb.WriteString(fmt.Sprintf(
                    `<path d="M%.1f %.1f Q%.1f %.1f %.1f %.1f" stroke="#334155" stroke-width="%.1f" stroke-linecap="round" fill="none"/>`,
                    x0, y, ctrlX, ctrlY, x1, y, sw))
            }
        }
        // Distractor strokes: at most 1 short, light stroke inside this
        // glyph's bounding box (#694: the old 1-2 same-weight strokes
        // overpowered the glyphs) — any grid reconstruction still picks up
        // cells no bitmap contains.
        for d := 0; d < jitter(2); d++ {
            dx0 := gx + float64(jitter(40)) / 10.0
            dy0 := originY + float64(jitter(280)) / 10.0
            dlen := 2 + float64(jitter(20))/10.0
            dx1 := dx0 + dlen
            dy1 := dy0 + float64(jitter(17)-8)/10.0
            sb.WriteString(fmt.Sprintf(
                `<path d="M%.1f %.1f Q%.1f %.1f %.1f %.1f" stroke="#334155" stroke-width="2.0" stroke-linecap="round" fill="none" opacity="0.5"/>`,
                dx0, dy0, (dx0+dx1)/2, (dy0+dy1)/2+float64(jitter(13)-6)/10.0, dx1, dy1))
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
    resp := map[string]any{
        "captcha_id":  id,
        "image":       renderSVG(code),
        "ttl_seconds": int(st.ttl.Seconds()),
        "enabled":     true,
    }
    // #688: the sanctioned deterministic path for CI/E2E — the challenge
    // code rides the response so suites need no image decoding. The store
    // only carries testMode when captchaTestModeEnabled() passed at wiring
    // time; production can never set it.
    if st.testMode {
        resp["test_code"] = code
    }
    _ = json.NewEncoder(w).Encode(resp)
}
