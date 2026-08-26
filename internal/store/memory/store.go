package memory

import (
    "log/slog"
    "time"

    "github.com/fusion-gateway/fusion-gateway/internal/config"
    "github.com/fusion-gateway/fusion-gateway/internal/store"
)

type MemoryStore struct {
    logs      *LogStore
    keys      *KeyStore
    channels  *ChannelStore
    analytics *AnalyticsStore
    dashboard *DashboardStore
    quota     *QuotaStore
    profit    *ProfitStore
    // A3 fix: sub-stores for teams/orgs/batch/cost
    teams  *TeamsStore
    batch  *BatchSubStore
    cost   *CostSubStore
    persist *Persister
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
        profit:    NewProfitStore(ls),
        teams:     NewTeamsStore(),
        cost:      NewCostSubStore(10000),
    }
}

func NewMemoryStoreWithConfig(logMaxLen int, batchCfg config.BatchConfig) *MemoryStore {
    m := NewMemoryStore(logMaxLen)
    maxBatch := batchCfg.MaxBatchSize
    if maxBatch <= 0 {
        maxBatch = 100
    }
    m.batch = NewBatchSubStore(maxBatch)
    return m
}

func (m *MemoryStore) EnablePersistence(dataDir string) error {
    if dataDir == "" {
        return nil
    }
    dir := config.ExpandPath(dataDir)
    if dir == "" {
        return nil
    }
    p := NewPersister(dir, m.keys, m.channels, m.teams)
    if err := p.Load(); err != nil {
        return err
    }
    m.keys.SetOnMutate(func() {
        if err := p.SaveKeys(); err != nil {
            slog.Error("persist: save keys failed", "error", err)
        }
    })
    m.channels.SetOnMutate(func() {
        if err := p.SaveChannels(); err != nil {
            slog.Error("persist: save channels failed", "error", err)
        }
    })
    m.teams.SetOnMutate(func() {
        if err := p.SaveTeams(); err != nil {
            slog.Error("persist: save teams failed", "error", err)
        }
    })
    // F2/P1: high-frequency AddCost persists through a debounced SaveTeams so
    // quota/cost survive restart (F2 billing bypass) without a full-JSON rewrite
    // per billed request (P1 write amplification). Same SaveTeams writer, just
    // coalesced. FlushQuota is wired into the store shutdown path.
    m.teams.SetQuotaPersist(func() {
        if err := p.SaveTeams(); err != nil {
            slog.Error("persist: save teams (quota) failed", "error", err)
        }
    })
    m.persist = p
    slog.Info("store persistence enabled", "data_dir", dir)
    return nil
}

// FlushQuota synchronously flushes any pending debounced team quota/cost write
// so the last burst of AddCost before shutdown reaches disk (F2). Called from
// Server.Shutdown via a type assertion on the Store interface.
func (m *MemoryStore) FlushQuota() {
    if m.teams != nil {
        m.teams.FlushQuota()
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

func (m *MemoryStore) DistinctLogFilters() (*store.LogFilters, error) {
    return m.logs.DistinctFilters(), nil
}

func (m *MemoryStore) ListKeys() ([]*store.APIKeyEntry, error) {
    return m.keys.List()
}

func (m *MemoryStore) GetKey(name string) (*store.APIKeyEntry, error) {
    return m.keys.Get(name)
}

func (m *MemoryStore) GetKeyByHash(hash string) (*store.APIKeyEntry, error) {
    return m.keys.GetByHash(hash)
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

func (m *MemoryStore) GetKeyProfitStats(from, to time.Time) ([]*store.KeyProfitStat, error) {
    return m.profit.GetKeyProfitStats(from, to)
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
func (m *MemoryStore) TeamsStore() *TeamsStore     { return m.teams }

// A3 fix: teams/orgs/batch/cost Store interface implementations

func (m *MemoryStore) CreateTeam(team *store.Team) error {
    return m.teams.CreateTeam(team)
}
func (m *MemoryStore) GetTeam(id string) (*store.Team, error) {
    return m.teams.GetTeam(id)
}
func (m *MemoryStore) ListTeams() ([]*store.Team, error) {
    return m.teams.ListTeams(), nil
}
func (m *MemoryStore) UpdateTeam(team *store.Team) error {
    return m.teams.UpdateTeam(team)
}
func (m *MemoryStore) DeleteTeam(id string) error {
    return m.teams.DeleteTeam(id)
}
func (m *MemoryStore) BindKeyToTeam(apiKey, teamID string) error {
    return m.teams.BindKeyToTeam(apiKey, teamID)
}
func (m *MemoryStore) GetTeamByKey(apiKey string) (*store.Team, error) {
    return m.teams.GetTeamByKey(apiKey)
}
func (m *MemoryStore) AddTeamCost(teamID string, cost float64) error {
    return m.teams.AddCost(teamID, cost)
}
func (m *MemoryStore) CheckTeamQuota(teamID string) (limit, used float64, ok bool, err error) {
    l, u, o := m.teams.CheckQuota(teamID)
    return l, u, o, nil
}
func (m *MemoryStore) AddTeamMember(teamID, userID, role string) error {
    return m.teams.AddMember(teamID, userID, role)
}
func (m *MemoryStore) RemoveTeamMember(teamID, userID string) error {
    return m.teams.RemoveMember(teamID, userID)
}

func (m *MemoryStore) CreateOrg(org *store.Organization) error {
    return m.teams.CreateOrg(org)
}
func (m *MemoryStore) GetOrg(id string) (*store.Organization, error) {
    return m.teams.GetOrg(id)
}
func (m *MemoryStore) ListOrgs() ([]*store.Organization, error) {
    return m.teams.ListOrgs(), nil
}
func (m *MemoryStore) DeleteOrg(id string) error {
    return m.teams.DeleteOrg(id)
}

func (m *MemoryStore) CreateBatch(requests []store.BatchRequest, endpoint, window string) (*store.Batch, error) {
    if m.batch == nil {
        return nil, nil
    }
    return m.batch.Create(requests, endpoint, window)
}
func (m *MemoryStore) GetBatch(id string) (*store.Batch, error) {
    if m.batch == nil {
        return nil, nil
    }
    return m.batch.Get(id)
}
func (m *MemoryStore) ListBatches() ([]*store.Batch, error) {
    if m.batch == nil {
        return nil, nil
    }
    return m.batch.List(), nil
}
func (m *MemoryStore) CancelBatch(id string) (*store.Batch, error) {
    if m.batch == nil {
        return nil, nil
    }
    return m.batch.Cancel(id)
}
func (m *MemoryStore) UpdateBatch(batch *store.Batch) error {
    if m.batch == nil {
        return nil
    }
    return m.batch.Update(batch)
}

func (m *MemoryStore) RecordUsage(keyName, backend, model string, promptTokens, completionTokens int, costUSD float64) error {
    m.cost.Record(keyName, backend, model, promptTokens, completionTokens, costUSD)
    return nil
}
func (m *MemoryStore) GetCostSummary(keyName string) (*store.CostSummary, error) {
    return m.cost.Summary(keyName), nil
}
func (m *MemoryStore) GetCostSummaryAll() (*store.CostSummary, error) {
    return m.cost.Summary(""), nil
}
