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

func TestQuota_EI5_ReclaimKey_DropsQuotaMaps(t *testing.T) {
    t.Parallel()
    t.Log("EI5: ReclaimKey must drop a key's usage/dailyUsage/dailyDate entries (was unbounded growth)")
    k := &store.APIKeyEntry{Name: "reclaim-key", Status: "active", BudgetLimit: 100.0, DailyBudgetLimit: 10.0}
    q := newQuotaStoreWithKey(t, k)

    if err := q.Deduct("reclaim-key", 7.5); err != nil {
        t.Fatalf("deduct: %v", err)
    }
    if got := q.GetUsage("reclaim-key"); got != 7.5 {
        t.Fatalf("usage before reclaim: got %f want 7.5", got)
    }
    if used, _ := q.DailyUsage("reclaim-key"); used != 7.5 {
        t.Fatalf("daily usage before reclaim: got %f want 7.5", used)
    }

    q.ReclaimKey("reclaim-key")

    if got := q.GetUsage("reclaim-key"); got != 0 {
        t.Errorf("EI5: usage after reclaim should be 0, got %f (dead entry never reclaimed)", got)
    }
    // Read the daily maps via SnapshotQuota (the persist path) rather than
    // DailyUsage — DailyUsage's rolloverLocked side-effect would re-seed an
    // empty entry for a reclaimed key, masking whether reclaim worked. In
    // production a deleted key never reaches Deduct/Check (auth fails), so the
    // authoritative "is the dead entry gone?" signal is what gets persisted.
    usage, dailyUsage, dailyDate := q.SnapshotQuota()
    if _, ok := usage["reclaim-key"]; ok {
        t.Errorf("EI5: SnapshotQuota still carries reclaimed usage entry (persist grows unbounded)")
    }
    if _, ok := dailyUsage["reclaim-key"]; ok {
        t.Errorf("EI5: SnapshotQuota still carries reclaimed dailyUsage entry")
    }
    if _, ok := dailyDate["reclaim-key"]; ok {
        t.Errorf("EI5: SnapshotQuota still carries reclaimed dailyDate entry")
    }
}

func TestQuota_EI5_DeleteKey_CascadesReclaim(t *testing.T) {
    t.Parallel()
    t.Log("EI5: MemoryStore.DeleteKey must cascade into quota.ReclaimKey (was: key gone, quota entries orphaned forever)")
    ks := NewKeyStore()
    q := NewQuotaStore(ks)
    m := &MemoryStore{keys: ks, quota: q}

    k := &store.APIKeyEntry{Name: "del-cascade-key", Status: "active", BudgetLimit: 50.0}
    if err := m.CreateKey(k); err != nil {
        t.Fatalf("create key: %v", err)
    }
    if err := q.Deduct("del-cascade-key", 12.0); err != nil {
        t.Fatalf("deduct: %v", err)
    }
    if got := q.GetUsage("del-cascade-key"); got != 12.0 {
        t.Fatalf("usage before delete: got %f want 12.0", got)
    }

    if err := m.DeleteKey("del-cascade-key"); err != nil {
        t.Fatalf("delete key: %v", err)
    }

    if got := q.GetUsage("del-cascade-key"); got != 0 {
        t.Fatalf("EI5: quota usage after DeleteKey should be reclaimed to 0, got %f (DeleteKey did not cascade reclaim)", got)
    }
    if _, err := m.GetKey("del-cascade-key"); err == nil {
        t.Fatalf("key entry should be gone after DeleteKey")
    }
}
