package memory

import (
    "encoding/json"
    "fmt"
    "log/slog"
    "os"
    "path/filepath"
    "time"

    "github.com/fusion-gateway/fusion-gateway/internal/store"
)

type Persister struct {
    dataDir string
    keys    *KeyStore
    chans   *ChannelStore
    teams   *TeamsStore
    quota   *QuotaStore
}

func NewPersister(dataDir string, ks *KeyStore, cs *ChannelStore, ts *TeamsStore) *Persister {
    return &Persister{
        dataDir: dataDir,
        keys:    ks,
        chans:   cs,
        teams:   ts,
    }
}

func (p *Persister) Load() error {
    if err := os.MkdirAll(p.dataDir, 0o700); err != nil {
        slog.Error("persist: mkdir data_dir failed", "dir", p.dataDir, "error", err)
        return err
    }
    if err := p.loadKeys(); err != nil {
        slog.Error("persist: load keys.json failed, starting with empty key store", "error", err)
    }
    if err := p.loadChannels(); err != nil {
        slog.Error("persist: load channels.json failed, starting with empty channel store", "error", err)
    }
    if err := p.loadTeams(); err != nil {
        slog.Error("persist: load teams.json failed, keeping seeded defaults", "error", err)
    }
    if err := p.loadQuota(); err != nil {
        slog.Error("persist: load quota.json failed, starting with zeroed per-key usage", "error", err)
    }
    return nil
}

// quarantine renames a corrupt persistence file to <path>.corrupt.<unix-ts>
// so (a) the operator can recover the data, and (b) the next Save*() does NOT
// silently overwrite the only copy. The original load already failed, so the
// store continues empty — but the corrupted bytes are preserved on disk for
// forensic inspection. F6: without this, a truncated keys.json write or a
// half-rename left an unparseable file that the next SaveKeys() would
// overwrite with the now-empty in-memory store, destroying recovery evidence
// and leaving all auth 401 with no clue why.
func (p *Persister) quarantine(path string, loadErr error) {
    q := fmt.Sprintf("%s.corrupt.%d", path, time.Now().Unix())
    if err := os.Rename(path, q); err != nil {
        slog.Error("persist: failed to quarantine corrupt file, original left in place",
            "path", path, "quarantine", q, "rename_error", err, "load_error", loadErr)
        return
    }
    slog.Error("persist: quarantined corrupt persistence file for forensic recovery",
        "original", path, "quarantine", q, "load_error", loadErr)
}

func (p *Persister) SaveKeys() error {
    if p.dataDir == "" {
        return nil
    }
    p.keys.mu.RLock()
    entries := make([]*store.APIKeyEntry, 0, len(p.keys.keys))
    for _, k := range p.keys.keys {
        entries = append(entries, k)
    }
    p.keys.mu.RUnlock()
    data, err := json.MarshalIndent(entries, "", "  ")
    if err != nil {
        return err
    }
    return p.atomicWrite(filepath.Join(p.dataDir, "keys.json"), data)
}

func (p *Persister) SaveChannels() error {
    if p.dataDir == "" {
        return nil
    }
    p.chans.mu.RLock()
    entries := make([]*store.ChannelEntry, 0, len(p.chans.channels))
    for _, c := range p.chans.channels {
        entries = append(entries, c)
    }
    p.chans.mu.RUnlock()
    data, err := json.MarshalIndent(entries, "", "  ")
    if err != nil {
        return err
    }
    return p.atomicWrite(filepath.Join(p.dataDir, "channels.json"), data)
}

func (p *Persister) SaveTeams() error {
    if p.dataDir == "" {
        return nil
    }
    p.teams.mu.RLock()
    teams := make([]*store.Team, 0, len(p.teams.teams))
    for _, t := range p.teams.teams {
        teams = append(teams, t)
    }
    orgs := make([]*store.Organization, 0, len(p.teams.orgs))
    for _, o := range p.teams.orgs {
        orgs = append(orgs, o)
    }
    keyTeam := make(map[string]string, len(p.teams.keyTeam))
    for k, v := range p.teams.keyTeam {
        keyTeam[k] = v
    }
    p.teams.mu.RUnlock()
    payload := struct {
        Teams   []*store.Team         `json:"teams"`
        Orgs    []*store.Organization `json:"orgs"`
        KeyTeam map[string]string     `json:"key_team"`
    }{
        Teams:   teams,
        Orgs:    orgs,
        KeyTeam: keyTeam,
    }
    data, err := json.MarshalIndent(payload, "", "  ")
    if err != nil {
        return err
    }
    return p.atomicWrite(filepath.Join(p.dataDir, "teams.json"), data)
}

func (p *Persister) loadKeys() error {
    path := filepath.Join(p.dataDir, "keys.json")
    data, err := os.ReadFile(path)
    if err != nil {
        if os.IsNotExist(err) {
            return nil
        }
        return err
    }
    var entries []*store.APIKeyEntry
    if err := json.Unmarshal(data, &entries); err != nil {
        p.quarantine(path, err)
        return err
    }
    p.keys.mu.Lock()
    for _, k := range entries {
        k.QuotaRemaining = k.QuotaLimit - k.QuotaUsed
        p.keys.keys[k.Name] = k
        if k.KeyHash != "" {
            p.keys.byHash[k.KeyHash] = k.Name
        }
    }
    p.keys.mu.Unlock()
    slog.Info("persist: loaded keys from disk", "count", len(entries), "dir", p.dataDir)
    return nil
}

func (p *Persister) loadChannels() error {
    path := filepath.Join(p.dataDir, "channels.json")
    data, err := os.ReadFile(path)
    if err != nil {
        if os.IsNotExist(err) {
            return nil
        }
        return err
    }
    var entries []*store.ChannelEntry
    if err := json.Unmarshal(data, &entries); err != nil {
        p.quarantine(path, err)
        return err
    }
    p.chans.mu.Lock()
    for _, c := range entries {
        p.chans.channels[c.Name] = c
    }
    p.chans.mu.Unlock()
    slog.Info("persist: loaded channels from disk", "count", len(entries), "dir", p.dataDir)
    return nil
}

func (p *Persister) loadTeams() error {
    path := filepath.Join(p.dataDir, "teams.json")
    data, err := os.ReadFile(path)
    if err != nil {
        if os.IsNotExist(err) {
            return nil
        }
        return err
    }
    var payload struct {
        Teams   []*store.Team         `json:"teams"`
        Orgs    []*store.Organization `json:"orgs"`
        KeyTeam map[string]string     `json:"key_team"`
    }
    if err := json.Unmarshal(data, &payload); err != nil {
        p.quarantine(path, err)
        return err
    }
    p.teams.mu.Lock()
    if payload.Teams != nil {
        p.teams.teams = make(map[string]*store.Team, len(payload.Teams))
        for _, t := range payload.Teams {
            p.teams.teams[t.ID] = t
        }
    }
    if payload.Orgs != nil {
        p.teams.orgs = make(map[string]*store.Organization, len(payload.Orgs))
        for _, o := range payload.Orgs {
            p.teams.orgs[o.ID] = o
        }
    }
    if payload.KeyTeam != nil {
        p.teams.keyTeam = make(map[string]string, len(payload.KeyTeam))
        for k, v := range payload.KeyTeam {
            p.teams.keyTeam[k] = v
        }
    }
    p.teams.mu.Unlock()
    slog.Info("persist: loaded teams from disk", "teams", len(payload.Teams), "orgs", len(payload.Orgs), "dir", p.dataDir)
    return nil
}

// SaveQuota atomically writes the authoritative per-key quota maps
// (usage/dailyUsage/dailyDate) to quota.json. A2: these maps are the source of
// truth for cumulative-budget enforcement (Check reads q.usage, not the key
// entry), and BudgetLimit-only keys never write k.QuotaUsed, so keys.json
// alone cannot restore usage. A dedicated file + the QuotaStore debounce avoid
// per-request write amplification.
func (p *Persister) SaveQuota() error {
    if p.dataDir == "" || p.quota == nil {
        return nil
    }
    usage, dailyUsage, dailyDate := p.quota.SnapshotQuota()
    payload := struct {
        Usage      map[string]float64 `json:"usage"`
        DailyUsage map[string]float64 `json:"daily_usage"`
        DailyDate  map[string]string  `json:"daily_date"`
    }{
        Usage:      usage,
        DailyUsage: dailyUsage,
        DailyDate:  dailyDate,
    }
    data, err := json.MarshalIndent(payload, "", "  ")
    if err != nil {
        return err
    }
    return p.atomicWrite(filepath.Join(p.dataDir, "quota.json"), data)
}

func (p *Persister) loadQuota() error {
    path := filepath.Join(p.dataDir, "quota.json")
    data, err := os.ReadFile(path)
    if err != nil {
        if os.IsNotExist(err) {
            return nil
        }
        return err
    }
    var payload struct {
        Usage      map[string]float64 `json:"usage"`
        DailyUsage map[string]float64 `json:"daily_usage"`
        DailyDate  map[string]string  `json:"daily_date"`
    }
    if err := json.Unmarshal(data, &payload); err != nil {
        p.quarantine(path, err)
        return err
    }
    if p.quota != nil {
        p.quota.SeedUsage(payload.Usage, payload.DailyUsage, payload.DailyDate)
    }
    slog.Info("persist: loaded quota from disk",
        "usage_keys", len(payload.Usage), "dir", p.dataDir)
    return nil
}

func (p *Persister) atomicWrite(path string, data []byte) error {
    dir := filepath.Dir(path)
    tmp, err := os.CreateTemp(dir, ".tmp-*")
    if err != nil {
        return err
    }
    tmpName := tmp.Name()
    defer func() {
        if err != nil {
            os.Remove(tmpName)
        }
    }()
    if _, werr := tmp.Write(data); werr != nil {
        tmp.Close()
        err = werr
        return err
    }
    if cerr := tmp.Close(); cerr != nil {
        err = cerr
        return err
    }
    if serr := os.Chmod(tmpName, 0o600); serr != nil {
        err = serr
        return err
    }
    if rerr := os.Rename(tmpName, path); rerr != nil {
        err = rerr
        return err
    }
    return nil
}
