package insights

import (
    "context"
    "os"
    "testing"
    "time"

    "github.com/fusion-gateway/fusion-gateway/internal/config"
)

// requireTestDSN skips unless FUSION_GW_INSIGHTS_TEST_DSN points at a
// PostgreSQL database carrying the persistence schema. CI has no PG, so these
// run locally/deploy-side — they exist because unit tests cannot catch a
// broken SQL statement (the compare query's missing `AS requests` alias
// shipped to production and only surfaced as a 400 at runtime).
func requireTestDSN(t *testing.T) *Service {
    t.Helper()
    dsn := os.Getenv("FUSION_GW_INSIGHTS_TEST_DSN")
    if dsn == "" {
        t.Skip("set FUSION_GW_INSIGHTS_TEST_DSN to run live SQL checks")
    }
    svc, err := New(config.PersistenceConfig{}, dsn)
    if err != nil {
        t.Fatalf("connect: %v", err)
    }
    t.Cleanup(svc.Close)
    ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
    defer cancel()
    if err := svc.Ping(ctx); err != nil {
        t.Fatalf("ping: %v", err)
    }
    return svc
}

func TestSQL_LatencyAgainstLiveDB(t *testing.T) {
    svc := requireTestDSN(t)
    ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
    defer cancel()
    resp, err := svc.Latency(ctx, "", "", "hour", "", "", "")
    if err != nil {
        t.Fatalf("latency query: %v", err)
    }
    t.Logf("latency buckets=%d", len(resp.Series))
}

func TestSQL_CompareAgainstLiveDB(t *testing.T) {
    svc := requireTestDSN(t)
    ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
    defer cancel()
    resp, err := svc.Compare(ctx, "", "", "")
    if err != nil {
        t.Fatalf("compare query: %v", err)
    }
    t.Logf("compare rows=%d", len(resp.Rows))
}

func TestSQL_SearchAgainstLiveDB(t *testing.T) {
    svc := requireTestDSN(t)
    ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
    defer cancel()
    sq := SearchQuery{Q: "", Limit: 5}
    resp, err := svc.Search(ctx, sq)
    if err != nil {
        t.Fatalf("search query: %v", err)
    }
    t.Logf("search total=%d returned=%d", resp.Total, len(resp.Rows))
    if len(resp.Rows) > 0 {
        if _, err := svc.Payload(ctx, resp.Rows[0].ID); err != nil {
            t.Fatalf("payload query: %v", err)
        }
    }
}
