package memory

// Teams store for v0.5.0 Task #69 (Team/Org/RBAC).
// Importers: internal/server/server.go admin handlers for /admin/teams, /admin/orgs.
// User instruction: "按照0.3.0, 0.4.0, 0.5.0顺序全面实施" — PLAN.md specifies
// "internal/store/memory/teams.go — Team/Org CRUD. Key association Team, cost aggregated by Team".
// Data: Team(id/name/org_id/quota/members/allowed_models/cost), Organization(id/name), TeamsStore(keyTeam map).

import (
    "fmt"
    "log/slog"
    "sync"
    "time"
)

type TeamMember struct {
    UserID string `json:"user_id"`
    Role   string `json:"role"`
}

type Team struct {
    ID              string       `json:"id"`
    Name            string       `json:"name"`
    OrgID           string       `json:"org_id,omitempty"`
    QuotaLimit      float64      `json:"quota_limit"`
    QuotaUsed       float64      `json:"quota_used"`
    Members         []TeamMember `json:"members"`
    AllowedModels   []string     `json:"allowed_models,omitempty"`
    CostAccumulated float64      `json:"cost_accumulated"`
    CreatedAt       time.Time    `json:"created_at"`
    UpdatedAt       time.Time    `json:"updated_at"`
}

type Organization struct {
    ID        string    `json:"id"`
    Name      string    `json:"name"`
    CreatedAt time.Time `json:"created_at"`
    UpdatedAt time.Time `json:"updated_at"`
}

type TeamsStore struct {
    mu      sync.RWMutex
    teams   map[string]*Team
    orgs    map[string]*Organization
    keyTeam map[string]string
}

func NewTeamsStore() *TeamsStore {
    s := &TeamsStore{
        teams:   make(map[string]*Team),
        orgs:    make(map[string]*Organization),
        keyTeam: make(map[string]string),
    }
    s.seedDefaults()
    return s
}

func (s *TeamsStore) seedDefaults() {
    now := time.Now()
    s.orgs["default"] = &Organization{
        ID: "default", Name: "Default Organization",
        CreatedAt: now, UpdatedAt: now,
    }
    s.teams["default"] = &Team{
        ID: "default", Name: "Default Team", OrgID: "default",
        QuotaLimit: 1000, AllowedModels: []string{"*"},
        Members:   []TeamMember{{UserID: "admin", Role: "admin"}},
        CreatedAt: now, UpdatedAt: now,
    }
    slog.Info("teams store seeded with default org and team")
}

func (s *TeamsStore) CreateTeam(team *Team) error {
    s.mu.Lock()
    defer s.mu.Unlock()
    if _, exists := s.teams[team.ID]; exists {
        return fmt.Errorf("team %s already exists", team.ID)
    }
    now := time.Now()
    team.CreatedAt = now
    team.UpdatedAt = now
    if team.Members == nil {
        team.Members = []TeamMember{}
    }
    s.teams[team.ID] = team
    slog.Info("team created", "id", team.ID, "name", team.Name)
    return nil
}

func (s *TeamsStore) GetTeam(id string) (*Team, error) {
    s.mu.RLock()
    defer s.mu.RUnlock()
    team, exists := s.teams[id]
    if !exists {
        return nil, fmt.Errorf("team %s not found", id)
    }
    return team, nil
}

func (s *TeamsStore) ListTeams() []*Team {
    s.mu.RLock()
    defer s.mu.RUnlock()
    result := make([]*Team, 0, len(s.teams))
    for _, t := range s.teams {
        result = append(result, t)
    }
    return result
}

func (s *TeamsStore) UpdateTeam(team *Team) error {
    s.mu.Lock()
    defer s.mu.Unlock()
    existing, exists := s.teams[team.ID]
    if !exists {
        return fmt.Errorf("team %s not found", team.ID)
    }
    team.CreatedAt = existing.CreatedAt
    team.UpdatedAt = time.Now()
    s.teams[team.ID] = team
    slog.Info("team updated", "id", team.ID)
    return nil
}

func (s *TeamsStore) DeleteTeam(id string) error {
    s.mu.Lock()
    defer s.mu.Unlock()
    if id == "default" {
        return fmt.Errorf("cannot delete default team")
    }
    if _, exists := s.teams[id]; !exists {
        return fmt.Errorf("team %s not found", id)
    }
    delete(s.teams, id)
    for key, tid := range s.keyTeam {
        if tid == id {
            delete(s.keyTeam, key)
        }
    }
    slog.Info("team deleted", "id", id)
    return nil
}

func (s *TeamsStore) CreateOrg(org *Organization) error {
    s.mu.Lock()
    defer s.mu.Unlock()
    if _, exists := s.orgs[org.ID]; exists {
        return fmt.Errorf("organization %s already exists", org.ID)
    }
    now := time.Now()
    org.CreatedAt = now
    org.UpdatedAt = now
    s.orgs[org.ID] = org
    slog.Info("org created", "id", org.ID, "name", org.Name)
    return nil
}

func (s *TeamsStore) GetOrg(id string) (*Organization, error) {
    s.mu.RLock()
    defer s.mu.RUnlock()
    org, exists := s.orgs[id]
    if !exists {
        return nil, fmt.Errorf("organization %s not found", id)
    }
    return org, nil
}

func (s *TeamsStore) ListOrgs() []*Organization {
    s.mu.RLock()
    defer s.mu.RUnlock()
    result := make([]*Organization, 0, len(s.orgs))
    for _, o := range s.orgs {
        result = append(result, o)
    }
    return result
}

func (s *TeamsStore) DeleteOrg(id string) error {
    s.mu.Lock()
    defer s.mu.Unlock()
    if id == "default" {
        return fmt.Errorf("cannot delete default organization")
    }
    if _, exists := s.orgs[id]; !exists {
        return fmt.Errorf("organization %s not found", id)
    }
    for _, t := range s.teams {
        if t.OrgID == id {
            return fmt.Errorf("organization %s still has teams", id)
        }
    }
    delete(s.orgs, id)
    slog.Info("org deleted", "id", id)
    return nil
}

func (s *TeamsStore) BindKeyToTeam(apiKey, teamID string) error {
    s.mu.Lock()
    defer s.mu.Unlock()
    if _, exists := s.teams[teamID]; !exists {
        return fmt.Errorf("team %s not found", teamID)
    }
    s.keyTeam[apiKey] = teamID
    slog.Info("key bound to team", "team", teamID)
    return nil
}

func (s *TeamsStore) GetTeamByKey(apiKey string) (*Team, error) {
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
    defer s.mu.Unlock()
    team, exists := s.teams[teamID]
    if !exists {
        return fmt.Errorf("team %s not found", teamID)
    }
    team.QuotaUsed += cost
    team.CostAccumulated += cost
    team.UpdatedAt = time.Now()
    slog.Debug("cost added to team", "team", teamID, "cost", cost, "total", team.CostAccumulated)
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
    defer s.mu.Unlock()
    team, exists := s.teams[teamID]
    if !exists {
        return fmt.Errorf("team %s not found", teamID)
    }
    for _, m := range team.Members {
        if m.UserID == userID {
            return fmt.Errorf("user %s already in team", userID)
        }
    }
    team.Members = append(team.Members, TeamMember{UserID: userID, Role: role})
    team.UpdatedAt = time.Now()
    slog.Info("member added to team", "team", teamID, "user", userID, "role", role)
    return nil
}

func (s *TeamsStore) RemoveMember(teamID, userID string) error {
    s.mu.Lock()
    defer s.mu.Unlock()
    team, exists := s.teams[teamID]
    if !exists {
        return fmt.Errorf("team %s not found", teamID)
    }
    for i, m := range team.Members {
        if m.UserID == userID {
            team.Members = append(team.Members[:i], team.Members[i+1:]...)
            team.UpdatedAt = time.Now()
            slog.Info("member removed from team", "team", teamID, "user", userID)
            return nil
        }
    }
    return fmt.Errorf("user %s not in team", userID)
}
