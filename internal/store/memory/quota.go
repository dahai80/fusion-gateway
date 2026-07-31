package memory

import (
    "sync"
)

type QuotaStore struct {
    mu    sync.Mutex
    usage map[string]float64
    keys  *KeyStore
}

func NewQuotaStore(keys *KeyStore) *QuotaStore {
    return &QuotaStore{
        usage: make(map[string]float64),
        keys:  keys,
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
    return used, limit, exceeded, nil
}

func (q *QuotaStore) Deduct(keyName string, amount float64) error {
    q.mu.Lock()
    defer q.mu.Unlock()

    q.usage[keyName] += amount

    k, err := q.keys.Get(keyName)
    if err != nil {
        return nil
    }
    if k.QuotaLimit > 0 {
        k.QuotaUsed = q.usage[keyName]
        k.QuotaRemaining = k.QuotaLimit - k.QuotaUsed
    }
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
