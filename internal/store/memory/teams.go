package memory

import (
    "fmt"
    "log/slog"
    "sync"
    "time"

    "github.com/fusion-gateway/fusion-gateway/internal/store"
)

type TeamsStore struct {
    mu       sync.RWMutex
    teams    map[string]*store.Team
    orgs     map[string]*store.Organization
    keyTeam  map[string]string
    onMutate func()

    // F2/P1 fix: quota/cost mutations (AddCost) are high-frequency per-billed-
    // request writes. Persisting them through onMutate (full teams.json rewrite
    // per request) is both a write-amplification footgun (P1) and, if skipped
    // entirely, a billing-bypass-on-restart bug (F2 — the original code skipped
    // persistence so QuotaUsed reset to the last metadata-mutation snapshot on
    // every restart). A debounced quota-persist callback coalesces bursts into a
    // single atomic write every quotaPersistDebounce, guarantees the final
    // mutation flushes via FlushQuota, and never touches the metadata onMutate
    // path. Set via SetQuotaPersist by the store wiring.
    quotaPersist      func()
    quotaPersistTimer *time.Timer
    quotaPersistMu    sync.Mutex
}

const quotaPersistDebounce = 2 * time.Second

func NewTeamsStore() *TeamsStore {
    s := &TeamsStore{
        teams:   make(map[string]*store.Team),
        orgs:    make(map[string]*store.Organization),
        keyTeam: make(map[string]string),
    }
    s.seedDefaults()
    return s
}

func (s *TeamsStore) SetOnMutate(fn func()) { s.onMutate = fn }

// SetQuotaPersist installs the debounced quota/cost persistence callback
// (typically SaveTeams). Called once during store wiring; nil = memory-only
// (persistence disabled), and AddCost falls back to fireOnMutate so the quota
// is still persisted on the metadata path rather than silently dropped.
func (s *TeamsStore) SetQuotaPersist(fn func()) { s.quotaPersist = fn }

// scheduleQuotaPersist arms the debounce timer; the first AddCost starts it,
// subsequent calls reset it, and the final write fires once after the burst.
func (s *TeamsStore) scheduleQuotaPersist() {
    if s.quotaPersist == nil {
        // No debounced persister configured (memory-only / pre-wiring): persist
        // via the metadata path so quota is NOT silently lost on restart.
        s.fireOnMutate()
        return
    }
    s.quotaPersistMu.Lock()
    defer s.quotaPersistMu.Unlock()
    if s.quotaPersistTimer != nil {
        s.quotaPersistTimer.Reset(quotaPersistDebounce)
        return
    }
    s.quotaPersistTimer = time.AfterFunc(quotaPersistDebounce, func() {
        s.quotaPersistMu.Lock()
        s.quotaPersistTimer = nil
        s.quotaPersistMu.Unlock()
        s.quotaPersist()
    })
}

// FlushQuota synchronously flushes any pending debounced quota write. Called on
// graceful shutdown so the last burst of AddCost mutations reaches disk.
func (s *TeamsStore) FlushQuota() {
    s.quotaPersistMu.Lock()
    if s.quotaPersistTimer != nil {
        s.quotaPersistTimer.Stop()
        s.quotaPersistTimer = nil
    }
    s.quotaPersistMu.Unlock()
    if s.quotaPersist != nil {
        s.quotaPersist()
    }
}

func (s *TeamsStore) fireOnMutate() {
    if s.onMutate != nil {
        s.onMutate()
    }
}

func (s *TeamsStore) seedDefaults() {
    now := time.Now()
    s.orgs["default"] = &store.Organization{
        ID: "default", Name: "Default Organization",
        CreatedAt: now, UpdatedAt: now,
    }
    s.teams["default"] = &store.Team{
        ID: "default", Name: "Default Team", OrgID: "default",
        QuotaLimit: 1000, AllowedModels: []string{"*"},
        Members:   []store.TeamMember{{UserID: "admin", Role: "admin"}},
        CreatedAt: now, UpdatedAt: now,
    }
    slog.Info("teams store seeded with default org and team")
}

func (s *TeamsStore) CreateTeam(team *store.Team) error {
    s.mu.Lock()
    if _, exists := s.teams[team.ID]; exists {
        s.mu.Unlock()
        return fmt.Errorf("team %s already exists", team.ID)
    }
    now := time.Now()
    team.CreatedAt = now
    team.UpdatedAt = now
    if team.Members == nil {
        team.Members = []store.TeamMember{}
    }
    s.teams[team.ID] = team
    s.mu.Unlock()
    slog.Info("team created", "id", team.ID, "name", team.Name)
    s.fireOnMutate()
    return nil
}

func (s *TeamsStore) GetTeam(id string) (*store.Team, error) {
    s.mu.RLock()
    defer s.mu.RUnlock()
    team, exists := s.teams[id]
    if !exists {
        return nil, fmt.Errorf("team %s not found", id)
    }
    return team, nil
}

func (s *TeamsStore) ListTeams() []*store.Team {
    s.mu.RLock()
    defer s.mu.RUnlock()
    result := make([]*store.Team, 0, len(s.teams))
    for _, t := range s.teams {
        result = append(result, t)
    }
    return result
}

func (s *TeamsStore) UpdateTeam(team *store.Team) error {
    s.mu.Lock()
    existing, exists := s.teams[team.ID]
    if !exists {
        s.mu.Unlock()
        return fmt.Errorf("team %s not found", team.ID)
    }
    team.CreatedAt = existing.CreatedAt
    team.UpdatedAt = time.Now()
    s.teams[team.ID] = team
    s.mu.Unlock()
    slog.Info("team updated", "id", team.ID)
    s.fireOnMutate()
    return nil
}

func (s *TeamsStore) DeleteTeam(id string) error {
    s.mu.Lock()
    if id == "default" {
        s.mu.Unlock()
        return fmt.Errorf("cannot delete default team")
    }
    if _, exists := s.teams[id]; !exists {
        s.mu.Unlock()
        return fmt.Errorf("team %s not found", id)
    }
    delete(s.teams, id)
    for key, tid := range s.keyTeam {
        if tid == id {
            delete(s.keyTeam, key)
        }
    }
    s.mu.Unlock()
    slog.Info("team deleted", "id", id)
    s.fireOnMutate()
    return nil
}

func (s *TeamsStore) CreateOrg(org *store.Organization) error {
    s.mu.Lock()
    if _, exists := s.orgs[org.ID]; exists {
        s.mu.Unlock()
        return fmt.Errorf("organization %s already exists", org.ID)
    }
    now := time.Now()
    org.CreatedAt = now
    org.UpdatedAt = now
    s.orgs[org.ID] = org
    s.mu.Unlock()
    slog.Info("org created", "id", org.ID, "name", org.Name)
    s.fireOnMutate()
    return nil
}

func (s *TeamsStore) GetOrg(id string) (*store.Organization, error) {
    s.mu.RLock()
    defer s.mu.RUnlock()
    org, exists := s.orgs[id]
    if !exists {
        return nil, fmt.Errorf("organization %s not found", id)
    }
    return org, nil
}

func (s *TeamsStore) ListOrgs() []*store.Organization {
    s.mu.RLock()
    defer s.mu.RUnlock()
    result := make([]*store.Organization, 0, len(s.orgs))
    for _, o := range s.orgs {
        result = append(result, o)
    }
    return result
}

func (s *TeamsStore) DeleteOrg(id string) error {
    s.mu.Lock()
    if id == "default" {
        s.mu.Unlock()
        return fmt.Errorf("cannot delete default organization")
    }
    if _, exists := s.orgs[id]; !exists {
        s.mu.Unlock()
        return fmt.Errorf("organization %s not found", id)
    }
    for _, t := range s.teams {
        if t.OrgID == id {
            s.mu.Unlock()
            return fmt.Errorf("organization %s still has teams", id)
        }
    }
    delete(s.orgs, id)
    s.mu.Unlock()
    slog.Info("org deleted", "id", id)
    s.fireOnMutate()
    return nil
}

func (s *TeamsStore) BindKeyToTeam(apiKey, teamID string) error {
    s.mu.Lock()
    if _, exists := s.teams[teamID]; !exists {
        s.mu.Unlock()
        return fmt.Errorf("team %s not found", teamID)
    }
    s.keyTeam[apiKey] = teamID
    s.mu.Unlock()
    slog.Info("key bound to team", "team", teamID)
    s.fireOnMutate()
    return nil
}

func (s *TeamsStore) GetTeamByKey(apiKey string) (*store.Team, error) {
    s.mu.RLock()
    defer s.mu.RUnlock()
    teamID, exists := s.keyTeam[apiKey]
    if !exists {
        return nil, fmt.Errorf("no team for key")
    }
    team, exists := s.teams[teamID]
    if !exists {
        return nil, fmt.Errorf("team %s not found", teamID)
    }
    return team, nil
}

func (s *TeamsStore) AddCost(teamID string, cost float64) error {
    s.mu.Lock()
    team, exists := s.teams[teamID]
    if !exists {
        s.mu.Unlock()
        return fmt.Errorf("team %s not found", teamID)
    }
    team.QuotaUsed += cost
    team.CostAccumulated += cost
    team.UpdatedAt = time.Now()
    slog.Debug("cost added to team", "team", teamID, "cost", cost, "total", team.CostAccumulated)
    s.mu.Unlock()
    // F2/P1: persist quota via the debounced path (not the per-request full
    // metadata rewrite). See SetQuotaPersist / FlushQuota. Lock released before
    // the persist callback so the debounced SaveTeams write does not contend
    // with the write lock (matches the fireOnMutate-after-unlock convention
    // used by CreateTeam/UpdateTeam/etc).
    s.scheduleQuotaPersist()
    return nil
}

func (s *TeamsStore) CheckQuota(teamID string) (float64, float64, bool) {
    s.mu.RLock()
    defer s.mu.RUnlock()
    team, exists := s.teams[teamID]
    if !exists {
        return 0, 0, false
    }
    return team.QuotaLimit, team.QuotaUsed, team.QuotaUsed < team.QuotaLimit
}

func (s *TeamsStore) AddMember(teamID, userID, role string) error {
    s.mu.Lock()
    team, exists := s.teams[teamID]
    if !exists {
        s.mu.Unlock()
        return fmt.Errorf("team %s not found", teamID)
    }
    for _, m := range team.Members {
        if m.UserID == userID {
            s.mu.Unlock()
            return fmt.Errorf("user %s already in team", userID)
        }
    }
    team.Members = append(team.Members, store.TeamMember{UserID: userID, Role: role})
    team.UpdatedAt = time.Now()
    s.mu.Unlock()
    slog.Info("member added to team", "team", teamID, "user", userID, "role", role)
    s.fireOnMutate()
    return nil
}

func (s *TeamsStore) RemoveMember(teamID, userID string) error {
    s.mu.Lock()
    team, exists := s.teams[teamID]
    if !exists {
        s.mu.Unlock()
        return fmt.Errorf("team %s not found", teamID)
    }
    for i, m := range team.Members {
        if m.UserID == userID {
            team.Members = append(team.Members[:i], team.Members[i+1:]...)
            team.UpdatedAt = time.Now()
            s.mu.Unlock()
            slog.Info("member removed from team", "team", teamID, "user", userID)
            s.fireOnMutate()
            return nil
        }
    }
    s.mu.Unlock()
    return fmt.Errorf("user %s not in team", userID)
}
