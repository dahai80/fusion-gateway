package memory

import (
    "log/slog"
    "sync"
    "time"
)

type QuotaStore struct {
    mu         sync.Mutex
    usage      map[string]float64
    dailyUsage map[string]float64
    dailyDate  map[string]string
    keys       *KeyStore
    nowFn      func() time.Time

    // A2 fix: per-key quota usage (usage/dailyUsage/dailyDate) is the
    // authoritative cumulative-budget source — Check reads q.usage, not the
    // key entry's QuotaUsed. Pre-fix these maps were pure in-memory, so every
    // restart zeroed per-key quota: a BudgetLimit key burned to the limit
    // reset to 0 on reload (billing bypass, same class as F2 for team quota).
    // Deduct mutates the maps on every billed request; persisting through
    // onMutate (full keys.json rewrite) would be write amplification, and
    // keys.json alone is insufficient anyway — BudgetLimit-only keys never
    // write k.QuotaUsed (the Deduct guard below), so their usage lives only in
    // q.usage. A debounced quota.json persists the three maps atomically every
    // quotaPersistDebounce, FlushKey drains on shutdown, and SeedUsage restores
    // them on load. Mirrors the F2 team-quota debounce. nil = memory-only.
    keyPersist      func()
    keyPersistTimer *time.Timer
    keyPersistMu    sync.Mutex
}

const keyPersistDebounce = 2 * time.Second

func NewQuotaStore(keys *KeyStore) *QuotaStore {
    return &QuotaStore{
        usage:      make(map[string]float64),
        dailyUsage: make(map[string]float64),
        dailyDate:  make(map[string]string),
        keys:       keys,
        nowFn:      time.Now,
    }
}

// SetKeyPersist installs the debounced per-key quota persistence callback
// (typically SaveQuota). Called once during store wiring; nil = memory-only.
func (q *QuotaStore) SetKeyPersist(fn func()) { q.keyPersist = fn }

// scheduleKeyPersist arms the debounce timer; coalesces a burst of Deduct
// calls into a single atomic quota.json write after quotaPersistDebounce.
func (q *QuotaStore) scheduleKeyPersist() {
    if q.keyPersist == nil {
        return
    }
    q.keyPersistMu.Lock()
    defer q.keyPersistMu.Unlock()
    if q.keyPersistTimer != nil {
        q.keyPersistTimer.Reset(keyPersistDebounce)
        return
    }
    q.keyPersistTimer = time.AfterFunc(keyPersistDebounce, func() {
        q.keyPersistMu.Lock()
        q.keyPersistTimer = nil
        q.keyPersistMu.Unlock()
        q.keyPersist()
    })
}

// FlushKey synchronously flushes any pending debounced per-key quota write so
// the last burst of Deduct before shutdown reaches disk. Called on graceful
// shutdown via the store.
func (q *QuotaStore) FlushKey() {
    q.keyPersistMu.Lock()
    if q.keyPersistTimer != nil {
        q.keyPersistTimer.Stop()
        q.keyPersistTimer = nil
    }
    q.keyPersistMu.Unlock()
    if q.keyPersist != nil {
        q.keyPersist()
    }
}

// SeedUsage restores per-key quota maps from persisted quota.json on startup,
// so Check/Deduct read correct post-restart usage instead of zeroed maps. Also
// syncs each key entry's QuotaUsed/DailyUsed/DailyDate from the authoritative
// maps so admin display is fresh before the first post-restart request.
func (q *QuotaStore) SeedUsage(usage, dailyUsage map[string]float64, dailyDate map[string]string) {
    q.mu.Lock()
    for k, v := range usage {
        q.usage[k] = v
    }
    for k, v := range dailyUsage {
        q.dailyUsage[k] = v
    }
    for k, v := range dailyDate {
        q.dailyDate[k] = v
    }
    q.mu.Unlock()
    // sync key entries for display consistency (best-effort; missing keys skip)
    for name, used := range usage {
        if k, err := q.keys.Get(name); err == nil {
            k.QuotaUsed = used
            if k.QuotaLimit > 0 {
                k.QuotaRemaining = k.QuotaLimit - used
            }
            if du, ok := dailyUsage[name]; ok {
                k.DailyUsed = du
            }
            if dd, ok := dailyDate[name]; ok {
                k.DailyDate = dd
            }
        }
    }
}

// SnapshotQuota returns copies of the three quota maps for atomic persistence.
// Called by Persister.SaveQuota under the persister's writer.
func (q *QuotaStore) SnapshotQuota() (usage, dailyUsage map[string]float64, dailyDate map[string]string) {
    q.mu.Lock()
    defer q.mu.Unlock()
    usage = make(map[string]float64, len(q.usage))
    for k, v := range q.usage {
        usage[k] = v
    }
    dailyUsage = make(map[string]float64, len(q.dailyUsage))
    for k, v := range q.dailyUsage {
        dailyUsage[k] = v
    }
    dailyDate = make(map[string]string, len(q.dailyDate))
    for k, v := range q.dailyDate {
        dailyDate[k] = v
    }
    return usage, dailyUsage, dailyDate
}

func (q *QuotaStore) today() string {
    return q.nowFn().Format("2006-01-02")
}

func (q *QuotaStore) rolloverLocked(keyName, today string) {
    if q.dailyDate[keyName] != today {
        q.dailyUsage[keyName] = 0
        q.dailyDate[keyName] = today
    }
}

func (q *QuotaStore) Check(keyName string) (used, limit float64, exceeded bool, err error) {
    q.mu.Lock()
    defer q.mu.Unlock()

    k, err := q.keys.Get(keyName)
    if err != nil {
        used = q.usage[keyName]
        return used, 0, false, err
    }

    limit = k.BudgetLimit
    if limit <= 0 {
        limit = k.QuotaLimit
    }
    used = q.usage[keyName]
    exceeded = limit > 0 && used >= limit
    if exceeded {
        slog.Warn("quota exceeded: cumulative budget",
            "key", keyName,
            "used", used,
            "limit", limit,
        )
        return used, limit, exceeded, nil
    }

    today := q.today()
    q.rolloverLocked(keyName, today)
    dailyLimit := k.DailyBudgetLimit
    dailyUsed := q.dailyUsage[keyName]
    k.DailyUsed = dailyUsed
    k.DailyDate = q.dailyDate[keyName]
    if dailyLimit > 0 && dailyUsed >= dailyLimit {
        exceeded = true
        slog.Warn("quota exceeded: daily budget",
            "key", keyName,
            "daily_used", dailyUsed,
            "daily_limit", dailyLimit,
            "date", today,
        )
    }
    return used, limit, exceeded, nil
}

func (q *QuotaStore) Deduct(keyName string, amount float64) error {
    q.mu.Lock()

    q.usage[keyName] += amount
    today := q.today()
    q.rolloverLocked(keyName, today)
    q.dailyUsage[keyName] += amount

    k, err := q.keys.Get(keyName)
    if err != nil {
        q.mu.Unlock()
        // A2: still persist the authoritative usage maps even for an unknown
        // key (the map is keyed by name regardless of the entry existing).
        q.scheduleKeyPersist()
        return nil
    }
    if k.QuotaLimit > 0 {
        k.QuotaUsed = q.usage[keyName]
        k.QuotaRemaining = k.QuotaLimit - k.QuotaUsed
    }
    k.DailyUsed = q.dailyUsage[keyName]
    k.DailyDate = q.dailyDate[keyName]
    q.mu.Unlock()
    // A2: debounced persist of the authoritative quota maps (outside the lock
    // to avoid holding q.mu across the timer bookkeeping).
    q.scheduleKeyPersist()
    return nil
}

func (q *QuotaStore) GetUsage(keyName string) float64 {
    q.mu.Lock()
    defer q.mu.Unlock()
    return q.usage[keyName]
}

func (q *QuotaStore) SetUsage(keyName string, amount float64) {
    q.mu.Lock()
    defer q.mu.Unlock()
    q.usage[keyName] = amount
}

func (q *QuotaStore) DailyUsage(keyName string) (used float64, date string) {
    q.mu.Lock()
    defer q.mu.Unlock()
    today := q.today()
    q.rolloverLocked(keyName, today)
    return q.dailyUsage[keyName], q.dailyDate[keyName]
}
