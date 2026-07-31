package memory

import (
    "time"

    "github.com/fusion-gateway/fusion-gateway/internal/store"
)

type MemoryStore struct {
    logs      *LogStore
    keys      *KeyStore
    channels  *ChannelStore
    analytics *AnalyticsStore
    dashboard *DashboardStore
    quota     *QuotaStore
}

func NewMemoryStore(logMaxLen int) *MemoryStore {
    ls := NewLogStore(logMaxLen)
    ks := NewKeyStore()
    cs := NewChannelStore()
    return &MemoryStore{
        logs:      ls,
        keys:      ks,
        channels:  cs,
        analytics: NewAnalyticsStore(ls),
        dashboard: NewDashboardStore(ls),
        quota:     NewQuotaStore(ks),
    }
}

func (m *MemoryStore) AppendLog(log *store.RequestLog) error {
    return m.logs.Append(log)
}

func (m *MemoryStore) QueryLogs(filter store.LogFilter) ([]*store.RequestLog, int, error) {
    return m.logs.Query(filter)
}

func (m *MemoryStore) GetLog(id string) (*store.RequestLog, error) {
    return m.logs.Get(id)
}

func (m *MemoryStore) ExportLogs(filter store.LogFilter, format string) ([]byte, error) {
    return m.logs.Export(filter, format)
}

func (m *MemoryStore) ListKeys() ([]*store.APIKeyEntry, error) {
    return m.keys.List()
}

func (m *MemoryStore) GetKey(name string) (*store.APIKeyEntry, error) {
    return m.keys.Get(name)
}

func (m *MemoryStore) CreateKey(key *store.APIKeyEntry) error {
    return m.keys.Create(key)
}

func (m *MemoryStore) UpdateKey(key *store.APIKeyEntry) error {
    return m.keys.Update(key)
}

func (m *MemoryStore) DeleteKey(name string) error {
    return m.keys.Delete(name)
}

func (m *MemoryStore) ListChannels() ([]*store.ChannelEntry, error) {
    return m.channels.List()
}

func (m *MemoryStore) GetChannel(name string) (*store.ChannelEntry, error) {
    return m.channels.Get(name)
}

func (m *MemoryStore) CreateChannel(ch *store.ChannelEntry) error {
    return m.channels.Create(ch)
}

func (m *MemoryStore) UpdateChannel(ch *store.ChannelEntry) error {
    return m.channels.Update(ch)
}

func (m *MemoryStore) DeleteChannel(name string) error {
    return m.channels.Delete(name)
}

func (m *MemoryStore) GetTokenStats(from, to time.Time, groupBy string) ([]*store.TokenStat, error) {
    return m.analytics.GetTokenStats(from, to, groupBy)
}

func (m *MemoryStore) GetCostStats(from, to time.Time, groupBy string) ([]*store.CostStat, error) {
    return m.analytics.GetCostStats(from, to, groupBy)
}

func (m *MemoryStore) GetModelStats(from, to time.Time) ([]*store.ModelStat, error) {
    return m.analytics.GetModelStats(from, to)
}

func (m *MemoryStore) GetLatencyStats(from, to time.Time) ([]*store.LatencyStat, error) {
    return m.analytics.GetLatencyStats(from, to)
}

func (m *MemoryStore) GetErrorStats(from, to time.Time) ([]*store.ErrorStat, error) {
    return m.analytics.GetErrorStats(from, to)
}

func (m *MemoryStore) GetDashboardOverview() (*store.DashboardOverview, error) {
    return m.dashboard.Overview()
}

func (m *MemoryStore) CheckQuota(keyName string) (used, limit float64, exceeded bool, err error) {
    return m.quota.Check(keyName)
}

func (m *MemoryStore) DeductQuota(keyName string, amount float64) error {
    return m.quota.Deduct(keyName, amount)
}

func (m *MemoryStore) KeyStore() *KeyStore         { return m.keys }
func (m *MemoryStore) ChannelStore() *ChannelStore { return m.channels }
func (m *MemoryStore) QuotaStore() *QuotaStore     { return m.quota }
