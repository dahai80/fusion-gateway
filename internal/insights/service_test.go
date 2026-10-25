package insights

import (
    "encoding/json"
    "testing"
    "time"
)

func TestWindowBounds_DefaultsAndLimits(t *testing.T) {
    from, to, err := windowBounds("", "")
    if err != nil {
        t.Fatalf("empty bounds must default: %v", err)
    }
    if d := to.Sub(from); d != 24*time.Hour {
        t.Fatalf("default window = %v, want 24h", d)
    }

    if _, _, err := windowBounds("not-a-time", ""); err == nil {
        t.Fatal("malformed from must error")
    }
    if _, _, err := windowBounds("", "not-a-time"); err == nil {
        t.Fatal("malformed to must error")
    }

    now := time.Now().UTC()
    if _, _, err := windowBounds(now.Format(time.RFC3339), now.Format(time.RFC3339)); err == nil {
        t.Fatal("to == from must error")
    }
    far := now.Add(-50 * 24 * time.Hour)
    if _, _, err := windowBounds(far.Format(time.RFC3339), now.Format(time.RFC3339)); err == nil {
        t.Fatal("window > 31 days must error")
    }
    // just under 31 days is allowed
    edge := now.Add(-31*24*time.Hour + time.Minute)
    if _, _, err := windowBounds(edge.Format(time.RFC3339), now.Format(time.RFC3339)); err != nil {
        t.Fatalf("31-day window must pass: %v", err)
    }
}

func TestGranularity(t *testing.T) {
    for in, want := range map[string]string{"": "hour", "hour": "hour", "day": "day", "minute": "minute"} {
        got, err := granularity(in)
        if err != nil || got != want {
            t.Fatalf("granularity(%q) = %q, %v; want %q", in, got, err, want)
        }
    }
    if _, err := granularity("week"); err == nil {
        t.Fatal("invalid granularity must error")
    }
}

func TestSearchQuery_JSONRoundTrip(t *testing.T) {
    raw := `{"from":"2026-10-25T00:00:00Z","q":"connection reset","is_stream":true,"status_code":502,"limit":100,"min_duration_ms":250.5}`
    var sq SearchQuery
    if err := json.Unmarshal([]byte(raw), &sq); err != nil {
        t.Fatalf("unmarshal: %v", err)
    }
    if sq.Q != "connection reset" || sq.StatusCode != 502 || sq.Limit != 100 {
        t.Fatalf("fields lost: %+v", sq)
    }
    if sq.IsStream == nil || !*sq.IsStream {
        t.Fatal("is_stream=true must decode to non-nil true pointer")
    }
    if sq.IsSuccess != nil {
        t.Fatal("absent is_success must stay nil")
    }
    if sq.MinDurationMS != 250.5 {
        t.Fatalf("min_duration_ms = %v", sq.MinDurationMS)
    }
}
