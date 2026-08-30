package jitter

import (
    "math"
    "testing"
    "time"
)

// TestH5_Duration_StaysWithinBand: every perturbation must land in
// [base*(1-f), base*(1+f)]. Revert Duration to `return base` (no jitter) and
// the within-band-with-margin check fails — every sample equals base, so the
// span is 0 and the assertion that the samples are not all identical trips.
func TestH5_Duration_StaysWithinBand(t *testing.T) {
    const base = 10 * time.Second
    minD := time.Duration(float64(base) * (1 - jitterFactor))
    maxD := time.Duration(float64(base) * (1 + jitterFactor))
    seen := map[time.Duration]struct{}{}
    for i := 0; i < 200; i++ {
        d := Duration(base)
        if d < minD || d > maxD {
            t.Fatalf("H5: Duration(%s)=%s outside band [%s,%s]", base, d, minD, maxD)
        }
        seen[d] = struct{}{}
    }
    if len(seen) < 10 {
        t.Fatalf("H5: Duration produced only %d distinct values over 200 calls — jitter missing (locked to fixed value)", len(seen))
    }
}

// TestH5_Duration_MeanStaysAtBase: jitter must not drift the average — a
// persistently-skewed generator would systematically slow or speed the tick.
func TestH5_Duration_MeanStaysAtBase(t *testing.T) {
    const base = 1 * time.Second
    const n = 1000
    var sum time.Duration
    for i := 0; i < n; i++ {
        sum += Duration(base)
    }
    mean := sum / n
    drift := math.Abs(float64(mean-base)) / float64(base)
    if drift > 0.03 {
        t.Fatalf("H5: mean=%s drifts %.1f%% from base=%s — jitter is biased, not zero-mean", mean, drift*100, base)
    }
}

// TestH5_Duration_FloorRespected: a node needing >interval to recover must
// never be polled faster than base*(1-f). The floor is the hard lower bound.
func TestH5_Duration_FloorRespected(t *testing.T) {
    const base = 5 * time.Second
    floor := time.Duration(float64(base) * (1 - jitterFactor))
    for i := 0; i < 500; i++ {
        if d := Duration(base); d < floor {
            t.Fatalf("H5: Duration(%s)=%s below floor %s — polls faster than configured", base, d, floor)
        }
    }
}

// TestH5_Duration_ZeroBaseNoOp: a zero/negative base is a config gap; return
// zero so callers' "if interval == 0 { interval = default }" guard still wins.
func TestH5_Duration_ZeroBaseNoOp(t *testing.T) {
    if d := Duration(0); d != 0 {
        t.Fatalf("H5: Duration(0)=%s, want 0", d)
    }
    if d := Duration(-time.Second); d != 0 {
        t.Fatalf("H5: Duration(-1s)=%s, want 0", d)
    }
}

// TestH5_After_ZeroBaseFiresImmediately: a zero/negative base short-circuits to
// an already-ready buffered channel so a caller's select fires at once — no
// caller should block forever on a misconfigured zero interval.
func TestH5_After_ZeroBaseFiresImmediately(t *testing.T) {
    for _, base := range []time.Duration{0, -time.Second} {
        start := time.Now()
        ch := After(base)
        select {
        case <-ch:
            if elapsed := time.Since(start); elapsed > 50*time.Millisecond {
                t.Fatalf("H5: After(%s) fired after %s, want immediate (buffered ready chan)", base, elapsed)
            }
        case <-time.After(time.Second):
            t.Fatalf("H5: After(%s) never fired — zero-base short-circuit missing", base)
        }
    }
}

// TestH5_After_PositiveBaseFiresWithinBand: a positive base fires a real
// time.After at a jittered duration that must land in [base*(1-f), base*(1+f)].
// Confirms After wires Duration into a live timer (not a no-op).
func TestH5_After_PositiveBaseFiresWithinBand(t *testing.T) {
    const base = 100 * time.Millisecond
    floor := time.Duration(float64(base) * (1 - jitterFactor))
    ceil := time.Duration(float64(base) * (1 + jitterFactor))
    start := time.Now()
    ch := After(base)
    select {
    case fired := <-ch:
        elapsed := time.Since(start)
        if elapsed < floor {
            t.Fatalf("H5: After(%s) fired after %s, below floor %s", base, elapsed, floor)
        }
        if elapsed > ceil+50*time.Millisecond {
            t.Fatalf("H5: After(%s) fired after %s, above ceil %s (allowing scheduler slack)", base, elapsed, ceil)
        }
        // time.After returns time.Now() on the channel; the received value is a
        // real timestamp, not the zero value — confirms a live timer.
        if fired.IsZero() {
            t.Fatal("H5: After returned a zero time, want a live timer timestamp")
        }
    case <-time.After(2 * time.Second):
        t.Fatalf("H5: After(%s) never fired", base)
    }
}
