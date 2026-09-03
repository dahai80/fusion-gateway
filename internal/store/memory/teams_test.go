package memory

import (
    "testing"

    "github.com/fusion-gateway/fusion-gateway/internal/store"
)

func TestTeamsStore_CreateAndGet(t *testing.T) {
    s := NewTeamsStore()
    team, err := s.GetTeam("default")
    if err != nil {
        t.Fatal(err)
    }
    if team.Name != "Default Team" {
        t.Fatalf("expected Default Team, got %s", team.Name)
    }
}

func TestTeamsStore_CreateDuplicate(t *testing.T) {
    s := NewTeamsStore()
    err := s.CreateTeam(&store.Team{ID: "default", Name: "dup"})
    if err == nil {
        t.Fatal("should fail on duplicate")
    }
}

func TestTeamsStore_DeleteDefault(t *testing.T) {
    s := NewTeamsStore()
    err := s.DeleteTeam("default")
    if err == nil {
        t.Fatal("should not delete default team")
    }
}

func TestTeamsStore_ListTeams(t *testing.T) {
    s := NewTeamsStore()
    teams := s.ListTeams()
    if len(teams) < 1 {
        t.Fatal("should have at least default team")
    }
}

func TestTeamsStore_BindKeyAndGet(t *testing.T) {
    s := NewTeamsStore()
    err := s.BindKeyToTeam("sk-test123", "default")
    if err != nil {
        t.Fatal(err)
    }
    team, err := s.GetTeamByKey("sk-test123")
    if err != nil {
        t.Fatal(err)
    }
    if team.ID != "default" {
        t.Fatalf("expected default, got %s", team.ID)
    }
}

func TestTeamsStore_AddMember(t *testing.T) {
    s := NewTeamsStore()
    err := s.AddMember("default", "user1", "editor")
    if err != nil {
        t.Fatal(err)
    }
    team, _ := s.GetTeam("default")
    if len(team.Members) < 2 {
        t.Fatal("should have at least 2 members")
    }
}

func TestTeamsStore_CostTracking(t *testing.T) {
    s := NewTeamsStore()
    err := s.AddCost("default", 1.5)
    if err != nil {
        t.Fatal(err)
    }
    limit, used, ok := s.CheckQuota("default")
    if !ok {
        t.Fatal("should have quota remaining")
    }
    if used != 1.5 {
        t.Fatalf("expected 1.5 used, got %f", used)
    }
    if limit != 1000 {
        t.Fatalf("expected 1000 limit, got %f", limit)
    }
}

func TestTeamsStore_OrgCRUD(t *testing.T) {
    s := NewTeamsStore()
    err := s.CreateOrg(&store.Organization{ID: "org1", Name: "Org One"})
    if err != nil {
        t.Fatal(err)
    }
    org, err := s.GetOrg("org1")
    if err != nil {
        t.Fatal(err)
    }
    if org.Name != "Org One" {
        t.Fatalf("expected Org One, got %s", org.Name)
    }
    orgs := s.ListOrgs()
    if len(orgs) < 2 {
        t.Fatal("should have at least 2 orgs")
    }
}

func TestTeamsStore_DeleteOrgWithTeams(t *testing.T) {
    s := NewTeamsStore()
    err := s.DeleteOrg("default")
    if err == nil {
        t.Fatal("should not delete org with teams")
    }
}

// #159: per-tenant daily quota cap trips when daily spend reaches the limit
// while the cumulative cap still has room, then rolls over (resets) on a new
// calendar day — no background sweep, implicit rollover in AddCost.
func TestTeamsStore_DailyQuotaTripAndReset(t *testing.T) {
    s := NewTeamsStore()
    team := &store.Team{
        ID: "dailyteam", Name: "Daily Team",
        QuotaLimit: 1000, DailyQuotaLimit: 5,
    }
    if err := s.CreateTeam(team); err != nil {
        t.Fatal(err)
    }
    // spend up to but not over the daily cap
    if err := s.AddCost("dailyteam", 4.5); err != nil {
        t.Fatal(err)
    }
    if _, _, ok := s.CheckQuota("dailyteam"); !ok {
        t.Fatal("daily cap should not trip below limit")
    }
    // cross the daily cap — cumulative (5.1) still far below 1000
    if err := s.AddCost("dailyteam", 0.6); err != nil {
        t.Fatal(err)
    }
    if _, _, ok := s.CheckQuota("dailyteam"); ok {
        t.Fatal("daily cap should trip at limit")
    }
    // simulate a new day: flip the stored date so AddCost rolls the counter
    tm, _ := s.GetTeam("dailyteam")
    tm.DailyQuotaDate = "2020-01-01"
    tm.DailyQuotaUsed = 0
    s.UpdateTeam(tm)
    if err := s.AddCost("dailyteam", 1.0); err != nil {
        t.Fatal(err)
    }
    if _, _, ok := s.CheckQuota("dailyteam"); !ok {
        t.Fatal("daily cap should reset after rollover")
    }
}

// #159: AddCost rolls the daily counter when the calendar day advances, so a
// check (with no new cost) after midnight still reads fresh usage.
func TestTeamsStore_DailyQuotaRollOnCheck(t *testing.T) {
    s := NewTeamsStore()
    team := &store.Team{
        ID: "rollteam", Name: "Roll Team",
        QuotaLimit: 1000, DailyQuotaLimit: 1,
    }
    s.CreateTeam(team)
    s.AddCost("rollteam", 0.9)
    // force the stored date stale so CheckQuota's rollover branch fires
    tm, _ := s.GetTeam("rollteam")
    tm.DailyQuotaDate = "2020-01-01"
    s.UpdateTeam(tm)
    if _, _, ok := s.CheckQuota("rollteam"); !ok {
        t.Fatal("stale daily counter should roll to fresh on check")
    }
}
