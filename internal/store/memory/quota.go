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
}

func NewQuotaStore(keys *KeyStore) *QuotaStore {
    return &QuotaStore{
        usage:      make(map[string]float64),
        dailyUsage: make(map[string]float64),
        dailyDate:  make(map[string]string),
        keys:       keys,
        nowFn:      time.Now,
    }
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
    defer q.mu.Unlock()

    q.usage[keyName] += amount
    today := q.today()
    q.rolloverLocked(keyName, today)
    q.dailyUsage[keyName] += amount

    k, err := q.keys.Get(keyName)
    if err != nil {
        return nil
    }
    if k.QuotaLimit > 0 {
        k.QuotaUsed = q.usage[keyName]
        k.QuotaRemaining = k.QuotaLimit - k.QuotaUsed
    }
    k.DailyUsed = q.dailyUsage[keyName]
    k.DailyDate = q.dailyDate[keyName]
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
