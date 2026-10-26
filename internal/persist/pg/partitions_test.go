package pg

import (
    "testing"
    "time"
)

// partitionDay must recover the full 4-digit year from a daily partition
// name. Regression: the old code sliced relname by len(table+"_20"), which
// chopped the century off the year ("2025_01_15" -> "25_01_15"), failed
// time.Parse, and silently skipped every retention DROP.
func TestPartitionDay(t *testing.T) {
    cases := []struct {
        relname string
        want    time.Time
        ok      bool
    }{
        {"fg_request_logs_2025_01_15", time.Date(2025, 1, 15, 0, 0, 0, 0, time.UTC), true},
        {"fg_request_logs_2026_10_26", time.Date(2026, 10, 26, 0, 0, 0, 0, time.UTC), true},
        {"fg_request_logs_2026_01_01", time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC), true},
        {"fg_request_logs_2026_12_31", time.Date(2026, 12, 31, 0, 0, 0, 0, time.UTC), true},
        // non-daily names are skipped, not fatal
        {"fg_request_logs_default", time.Time{}, false},
        {"fg_request_logs", time.Time{}, false},
    }
    for _, c := range cases {
        got, ok := partitionDay("fg_request_logs", c.relname)
        if ok != c.ok {
            t.Fatalf("partitionDay(%q) ok = %v, want %v", c.relname, ok, c.ok)
        }
        if ok && !got.Equal(c.want) {
            t.Fatalf("partitionDay(%q) = %v, want %v", c.relname, got, c.want)
        }
    }
}
