package memory

import (
    "testing"
    "time"

    "github.com/fusion-gateway/fusion-gateway/internal/store"
)

func newQuotaStoreWithKey(t *testing.T, k *store.APIKeyEntry) *QuotaStore {
    t.Helper()
    ks := NewKeyStore()
    if err := ks.Create(k); err != nil {
        t.Fatalf("create key: %v", err)
    }
    return NewQuotaStore(ks)
}

func TestQuota_DailyLimit_TripsAndResets(t *testing.T) {
    t.Parallel()
    k := &store.APIKeyEntry{Name: "daily-key", Status: "active", DailyBudgetLimit: 1.0}
    q := newQuotaStoreWithKey(t, k)

    if _, _, exceeded, err := q.Check("daily-key"); err != nil || exceeded {
        t.Fatalf("initial check should not be exceeded: exceeded=%v err=%v", exceeded, err)
    }
    if err := q.Deduct("daily-key", 0.6); err != nil {
        t.Fatalf("deduct 0.6: %v", err)
    }
    if _, _, exceeded, _ := q.Check("daily-key"); exceeded {
        t.Fatalf("after 0.6 (limit 1.0) should not be exceeded")
    }
    if err := q.Deduct("daily-key", 0.5); err != nil {
        t.Fatalf("deduct 0.5: %v", err)
    }
    used, _, exceeded, _ := q.Check("daily-key")
    if !exceeded {
        t.Fatalf("after 1.1 (limit 1.0) should be exceeded, used=%f", used)
    }

    // simulate next day: rollover zeros daily usage
    next := time.Now().Add(26 * time.Hour)
    q.nowFn = func() time.Time { return next }
    if _, _, exceeded, _ := q.Check("daily-key"); exceeded {
        t.Fatalf("after day rollover daily usage should reset, not exceeded")
    }
}

func TestQuota_DailyLimit_Disabled(t *testing.T) {
    t.Parallel()
    k := &store.APIKeyEntry{Name: "nodaily-key", Status: "active", BudgetLimit: 100.0}
    q := newQuotaStoreWithKey(t, k)

    if err := q.Deduct("nodaily-key", 5.0); err != nil {
        t.Fatalf("deduct: %v", err)
    }
    if _, _, exceeded, _ := q.Check("nodaily-key"); exceeded {
        t.Fatalf("daily disabled + cumulative far from limit should not be exceeded")
    }
}

func TestQuota_BothCaps_DailyTripsFirst(t *testing.T) {
    t.Parallel()
    k := &store.APIKeyEntry{Name: "both-key", Status: "active", BudgetLimit: 100.0, DailyBudgetLimit: 2.0}
    q := newQuotaStoreWithKey(t, k)

    if err := q.Deduct("both-key", 2.5); err != nil {
        t.Fatalf("deduct: %v", err)
    }
    used, _, exceeded, _ := q.Check("both-key")
    if !exceeded {
        t.Fatalf("daily cap (2.0) should trip before cumulative (100.0), used=%f", used)
    }
}

func TestQuota_DailyLimit_SyncsEntry(t *testing.T) {
    t.Parallel()
    k := &store.APIKeyEntry{Name: "sync-key", Status: "active", DailyBudgetLimit: 5.0}
    q := newQuotaStoreWithKey(t, k)

    if err := q.Deduct("sync-key", 1.5); err != nil {
        t.Fatalf("deduct: %v", err)
    }
    _, _, _, _ = q.Check("sync-key")
    live, err := q.keys.Get("sync-key")
    if err != nil {
        t.Fatalf("get key: %v", err)
    }
    if live.DailyUsed != 1.5 {
        t.Fatalf("entry DailyUsed should sync to 1.5, got %f", live.DailyUsed)
    }
    if live.DailyDate == "" {
        t.Fatalf("entry DailyDate should be populated")
    }
}
