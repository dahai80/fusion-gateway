package memory

import (
    "testing"
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
    err := s.CreateTeam(&Team{ID: "default", Name: "dup"})
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
    err := s.CreateOrg(&Organization{ID: "org1", Name: "Org One"})
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
