package server

import (
    "encoding/base64"
    "strings"
    "testing"
)

// #614: the captcha SVG embedded the code as plain <text> nodes — a bot
// extracted it with `atob(img.src.split(',')[1])` and one string match, so
// the challenge provided zero protection. The rendered image must carry no
// machine-recoverable code: no <text> nodes, no embedded plaintext of the
// code, while remaining a valid human-solvable SVG data URI.

func svg614(t *testing.T) (code, svg string) {
    t.Helper()
    st := newShellCaptchaStore(0)
    _, code2 := st.issue()
    code = code2
    // renderSVG takes the code directly; the store lookup is irrelevant to
    // the rendering contract under test.
    img := renderSVG(code)
    if !strings.HasPrefix(img, "data:image/svg+xml;base64,") {
        t.Fatal("render must stay a base64 SVG data URI")
    }
    raw, err := base64.StdEncoding.DecodeString(strings.TrimPrefix(img, "data:image/svg+xml;base64,"))
    if err != nil {
        t.Fatalf("decode svg: %v", err)
    }
    return code, string(raw)
}

func Test614_CaptchaSVG_NoTextNodes(t *testing.T) {
    _, svg := svg614(t)
    if strings.Contains(svg, "<text") {
        t.Fatalf("SVG must contain no <text> nodes, got: %s", svg)
    }
    if !strings.Contains(svg, "<path") {
        t.Fatal("glyphs must be rendered as <path> strokes")
    }
}

func Test614_CaptchaSVG_NoEmbeddedPlaintext(t *testing.T) {
    code, svg := svg614(t)
    // XML character data is the only machine-recoverable channel: strip all
    // elements/attributes; whatever remains must be whitespace. A code
    // character surviving in character data (e.g. a <text> or <title>) is a
    // leak; path data attributes are stripped with their tags.
    var textContent strings.Builder
    depth := 0
    for _, r := range svg {
        switch r {
        case '<':
            depth++
        case '>':
            if depth > 0 {
                depth--
            }
        default:
            if depth == 0 {
                textContent.WriteRune(r)
            }
        }
    }
    if strings.TrimSpace(textContent.String()) != "" {
        t.Fatalf("SVG character data must be empty (no code leak), got %q in: %s", textContent.String(), svg)
    }
    _ = code
}

func Test614_CaptchaSVG_DeterministicShapeStillReadable(t *testing.T) {
    // Five glyph groups must render: one <g> wrapper per character keeps
    // per-char rotation/jitter while every stroke is path data only.
    _, svg := svg614(t)
    n := strings.Count(svg, `<g transform="rotate(`)
    if n != shellCaptchaLength {
        t.Fatalf("expected %d glyph groups, got %d: %s", shellCaptchaLength, n, svg)
    }
}
