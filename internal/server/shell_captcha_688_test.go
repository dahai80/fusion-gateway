package server

import (
    "encoding/base64"
    "encoding/json"
    "net/http/httptest"
    "strings"
    "testing"
    "time"
)

// #688: the rendered SVG must not be deterministically decodable. The
// pre-hardening renderer produced fixed-geometry strokes (straight
// horizontal run-length segments quantized to originY 12.0 / rowStep 3.2 /
// cell 3.0 under a rotate-only transform), which template matching solved
// 100% of the time. These tests pin the structural entropy: per-challenge
// seeds, curved strokes, shifted glyph origins.

func svg688(t *testing.T, code string) string {
    t.Helper()
    uri := renderSVG(code)
    const prefix = "data:image/svg+xml;base64,"
    if !strings.HasPrefix(uri, prefix) {
        t.Fatalf("renderSVG must return a base64 data URI, got prefix %.40s", uri)
    }
    raw, err := base64.StdEncoding.DecodeString(strings.TrimPrefix(uri, prefix))
    if err != nil {
        t.Fatalf("decode svg: %v", err)
    }
    return string(raw)
}

func Test688_RenderSVGPerChallengeEntropy(t *testing.T) {
    a := svg688(t, "A1b2C")
    b := svg688(t, "A1b2C")
    if a == b {
        t.Fatal("same code rendered identical SVG — no per-challenge entropy")
    }
    if got := strings.Count(a, "<g transform="); got != 5 {
        t.Fatalf("expected one glyph group per char, got %d", got)
    }
}

func Test688_NoStraightRunLengthStrokes(t *testing.T) {
    // The decoder that beat the old renderer parsed "M x y L x y" run
    // segments inside rotate-only <g> groups. Neither shape may return.
    svg := svg688(t, "H8X2M")
    if strings.Contains(svg, ` L`) {
        t.Fatal("straight run-length stroke found — legacy decoder geometry still present")
    }
    for _, g := range strings.Split(svg, "<g") {
        if !strings.Contains(g, "transform=") {
            continue
        }
        frag := strings.SplitN(g, `transform="`, 2)[1]
        if strings.HasPrefix(frag, "rotate(") {
            t.Fatal("rotate-only glyph transform found — fixed origin recoverable")
        }
    }
    if strings.Contains(strings.ToLower(svg), "<text") {
        t.Fatal("text node in captcha SVG (#614 regression)")
    }
}

func Test688_TestModeGate(t *testing.T) {
    t.Setenv("FUSION_CAPTCHA_TESTMODE", "")
    t.Setenv("FUSION_ENV", "")
    if captchaTestModeEnabled() {
        t.Fatal("test mode must default off")
    }
    t.Setenv("FUSION_CAPTCHA_TESTMODE", "1")
    if !captchaTestModeEnabled() {
        t.Fatal("explicit opt-in must enable test mode outside production")
    }
    t.Setenv("FUSION_ENV", "production")
    if captchaTestModeEnabled() {
        t.Fatal("production must refuse the test path even when opted in")
    }
}

func Test688_TestCodeOnlyWhenTestMode(t *testing.T) {
    st := newShellCaptchaStore(time.Minute)
    st.testMode = true
    rec := httptest.NewRecorder()
    serveCaptchaChallenge(rec, st)
    var body map[string]any
    _ = json.Unmarshal(rec.Body.Bytes(), &body)
    tc, ok := body["test_code"].(string)
    if !ok || len(tc) != shellCaptchaLength {
        t.Fatalf("test mode response must carry the challenge code, got %v", body["test_code"])
    }
    // The leaked code must actually verify: it is the sanctioned path.
    if res := st.verify(body["captcha_id"].(string), tc); res != "" {
        t.Fatalf("test_code failed verification: %s", res)
    }

    // Without test mode the code must not appear anywhere in the response.
    st2 := newShellCaptchaStore(time.Minute)
    rec2 := httptest.NewRecorder()
    serveCaptchaChallenge(rec2, st2)
    var body2 map[string]any
    _ = json.Unmarshal(rec2.Body.Bytes(), &body2)
    if _, leaked := body2["test_code"]; leaked {
        t.Fatal("non-test response must not carry test_code")
    }
}
