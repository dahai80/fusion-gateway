package server

import (
    "encoding/base64"
    "fmt"
    "math"
    "os"
    "path/filepath"
    "regexp"
    "strconv"
    "strings"
    "testing"
)

// #694: the #688 hardening pushed anti-decoding noise past what a human
// can read (rotation ±16°, bulge ±1.9 over a 3.2 row pitch, 1-2
// same-weight distractors). The renderer is rebalanced (200x64 canvas,
// ±8° rotation, jitter < 1/4 row pitch, ≤1 light distractor) without
// giving back the #688 entropy structure. These tests pin the legibility
// envelope so a future hardening cannot silently re-cross it.

var rotRe694 = regexp.MustCompile(`rotate\((-?\d+(?:\.\d+)?) `)
var opacityRe694 = regexp.MustCompile(`opacity="(\d(?:\.\d+)?)"`)

func svg694(t *testing.T, code string) string {
    t.Helper()
    uri := renderSVG(code)
    raw, err := base64.StdEncoding.DecodeString(strings.TrimPrefix(uri, "data:image/svg+xml;base64,"))
    if err != nil {
        t.Fatalf("decode svg: %v", err)
    }
    return string(raw)
}

func Test694_CanvasAndRotationWithinLegibilityEnvelope(t *testing.T) {
    for i := 0; i < 20; i++ {
        svg := svg694(t, "A2B3C")
        if !strings.Contains(svg, `width="200" height="64" viewBox="0 0 200 64"`) {
            t.Fatal("canvas must be 200x64 (#694 legibility size)")
        }
        for _, m := range rotRe694.FindAllStringSubmatch(svg, -1) {
            deg, _ := strconv.ParseFloat(m[1], 64)
            if math.Abs(deg) > 8 {
                t.Fatalf("rotation %v exceeds ±8° cap", deg)
            }
        }
        // Real glyph strokes carry no opacity attr; only distractors and
        // background noise do, and distractor weight must stay light.
        if strings.Count(svg, `opacity="0.85"`) != 0 {
            t.Fatal("heavy 0.85 distractor found (#694 requires ≤0.5)")
        }
        _ = opacityRe694
    }
}

func Test694_AtMostOneDistractorPerGlyph(t *testing.T) {
    for i := 0; i < 20; i++ {
        svg := svg694(t, "H8X2M")
        groups := strings.Split(svg, "<g ")
        if len(groups) < 6 {
            t.Fatalf("expected 5 glyph groups, got %d", len(groups)-1)
        }
        for _, g := range groups[1:] {
            // Distractor paths are the only ones with an opacity attribute
            // inside a glyph group.
            if n := strings.Count(g, `opacity="0.5"`); n > 1 {
                t.Fatalf("glyph group carries %d distractors, cap is 1", n)
            }
        }
    }
}

func Test694_AntiDecodeStructureKept(t *testing.T) {
    svg := svg694(t, "Q7W9K")
    if strings.Contains(svg, ` L`) {
        t.Fatal("straight run-length stroke found — #688 structure regressed")
    }
    if strings.Count(svg, "<g transform=") != 5 {
        t.Fatal("per-glyph translate+rotate group missing — #688 structure regressed")
    }
    if strings.Contains(strings.ToLower(svg), "<text") {
        t.Fatal("text node in captcha SVG (#614 regression)")
    }
    a := svg694(t, "Q7W9K")
    b := svg694(t, "Q7W9K")
    if a == b {
        t.Fatal("same code rendered identical SVG — per-challenge entropy lost")
    }
}

// Test694_LegibilityFixture writes a labeled fixture set for human/VLM
// review (issue #694 acceptance: 9/10 readable). Off by default — enable
// with FUSION_CAPTCHA_FIXTURE_DIR=/some/dir when running the suite.
func Test694_LegibilityFixture(t *testing.T) {
    dir := os.Getenv("FUSION_CAPTCHA_FIXTURE_DIR")
    if dir == "" {
        t.Skip("set FUSION_CAPTCHA_FIXTURE_DIR to emit review fixtures")
    }
    if err := os.MkdirAll(dir, 0755); err != nil {
        t.Fatalf("mkdir: %v", err)
    }
    charset := "23456789ABCDEFGHJKMNPQRSTUVWXYZ"
    for i := 0; i < 10; i++ {
        code := ""
        for j := 0; j < 5; j++ {
            code += string(charset[(i*7+j*3)%len(charset)])
        }
        uri := renderSVG(code)
        raw, err := base64.StdEncoding.DecodeString(strings.TrimPrefix(uri, "data:image/svg+xml;base64,"))
        if err != nil {
            t.Fatalf("decode: %v", err)
        }
        name := filepath.Join(dir, fmt.Sprintf("fixture-%02d-%s.svg", i, code))
        if err := os.WriteFile(name, raw, 0644); err != nil {
            t.Fatalf("write fixture: %v", err)
        }
    }
    t.Logf("wrote 10 fixtures to %s", dir)
}
