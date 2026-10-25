package insights

import (
    "encoding/json"
    "testing"
)

func TestEvalMigrateToLocal(t *testing.T) {
    rows := []modelSplitRow{
        // Fires: >10% cloud share, local P95 well under 0.7x cloud, clean local.
        {Model: "glm-5", Total: 100, CloudN: 20, LocalN: 80, LocalErr: 0,
            CloudP95: 2000, LocalP95: 800, LocalGen: 50},
        // No fire: cloud share 5%.
        {Model: "m-low-cloud", Total: 100, CloudN: 5, LocalN: 95,
            CloudP95: 2000, LocalP95: 100, LocalGen: 50},
        // No fire: local P95 1500 >= 0.7*2000.
        {Model: "m-slow-local", Total: 100, CloudN: 20, LocalN: 80,
            CloudP95: 2000, LocalP95: 1500, LocalGen: 50},
        // No fire: local error rate 5% > 2% guard (that is KEEP_CLOUD's row).
        {Model: "m-flaky-local", Total: 100, CloudN: 20, LocalN: 80, LocalErr: 4,
            CloudP95: 2000, LocalP95: 800, LocalGen: 50},
        // No fire: total under 50.
        {Model: "m-tiny", Total: 49, CloudN: 49, CloudP95: 2000, LocalP95: 100, LocalGen: 10},
        // No fire: local generation samples under 10.
        {Model: "m-few-gen", Total: 100, CloudN: 50, LocalN: 50,
            CloudP95: 2000, LocalP95: 100, LocalGen: 9},
    }
    got := evalMigrateToLocal(rows)
    if len(got) != 1 || got[0].Rule != RuleMigrateToLocal || got[0].Scope["model"] != "glm-5" {
        t.Fatalf("expected exactly glm-5 to fire, got %+v", got)
    }
    a := got[0].Action
    if a["type"] != "models_supply" || a["prefer"] != "fusion-mlx" {
        t.Fatalf("action must be models_supply prefer fusion-mlx, got %v", a)
    }
}

func TestEvalKeepCloud(t *testing.T) {
    rows := []modelSplitRow{
        // Fires: 50 local requests, 5% error rate.
        {Model: "m-flaky", LocalN: 50, LocalErr: 3},
        // No fire: error rate 1%.
        {Model: "m-ok", LocalN: 100, LocalErr: 1},
        // No fire: under 50 local requests.
        {Model: "m-small", LocalN: 49, LocalErr: 10},
    }
    got := evalKeepCloud(rows)
    if len(got) != 1 || got[0].Scope["model"] != "m-flaky" {
        t.Fatalf("expected exactly m-flaky to fire, got %+v", got)
    }
    if got[0].Action != nil {
        t.Fatalf("KEEP_CLOUD must be advisory (nil action), got %v", got[0].Action)
    }
}

func TestEvalTrimTokens(t *testing.T) {
    rows := []modelSplitRow{
        // Fires: 10% oversized.
        {Model: "m-bloated", Total: 100, TrimN: 10},
        // No fire: 1% oversized.
        {Model: "m-lean", Total: 100, TrimN: 1},
        // No fire: under 100 requests.
        {Model: "m-small", Total: 99, TrimN: 50},
    }
    got := evalTrimTokens(rows)
    if len(got) != 1 || got[0].Scope["model"] != "m-bloated" {
        t.Fatalf("expected exactly m-bloated to fire, got %+v", got)
    }
}

func TestEvalMigrateToCloud(t *testing.T) {
    rows := []hourErrRow{
        {Hour: 3, Total: 40, Errs: 4},   // 10%: fires
        {Hour: 14, Total: 40, Errs: 1},  // 2.5%: no fire
        {Hour: 15, Total: 19, Errs: 10}, // under 20 requests: no fire
    }
    got := evalMigrateToCloud(rows)
    if len(got) != 1 || got[0].Scope["hour"] != 3 {
        t.Fatalf("expected hour 3 to fire, got %+v", got)
    }
    if got[0].Scope["backend"] != "local" {
        t.Fatalf("scope backend must be local, got %v", got[0].Scope)
    }
}

func TestEvalDedupPrompt(t *testing.T) {
    rows := []promptDupeRow{
        {Hash: "aaa", Count: 4, Model: "glm-5"},
        {Hash: "bbb", Count: 3, Model: "glm-5"},
        {Hash: "ccc", Count: 100, Model: "other"},
    }
    got := evalDedupPrompt(rows)
    if len(got) != 2 {
        t.Fatalf("expected 2 firings (count>3), got %d", len(got))
    }
    if got[0].Scope["prompt_hash"] != "aaa" || got[1].Scope["prompt_hash"] != "ccc" {
        t.Fatalf("hashes mismatch: %+v", got)
    }
    if got[0].Action["type"] != "semantic_cache" {
        t.Fatalf("action must be semantic_cache, got %v", got[0].Action)
    }
}

func TestEvalPreloadModel(t *testing.T) {
    three := []localModelRow{{Model: "a"}, {Model: "b"}, {Model: "c"}}
    if got := evalPreloadModel(three, 0.5); len(got) != 1 {
        t.Fatalf("3 models + 50%% mem must fire, got %d", len(got))
    }
    if got := evalPreloadModel(three, 0.9); got != nil {
        t.Fatal("mem ratio 0.9 (no headroom) must not fire")
    }
    if got := evalPreloadModel(three[:2], 0.5); got != nil {
        t.Fatal("2 models must not fire")
    }
    if got := evalPreloadModel(three, 0); got != nil {
        t.Fatal("no hw samples (avg 0) must not fire")
    }
    if got := evalPreloadModel(nil, 0.5); got != nil {
        t.Fatal("no local traffic must not fire")
    }
}

func TestRuleDedupeScopeJSON(t *testing.T) {
    // The dedupe SQL matches on scope jsonb equality — the scope maps must
    // marshal deterministically for the same logical scope.
    a := findingDraft{Rule: RuleMigrateToLocal, Scope: map[string]any{"model": "x"}}
    b := findingDraft{Rule: RuleMigrateToLocal, Scope: map[string]any{"model": "x"}}
    ma, _ := json.Marshal(a.Scope)
    mb, _ := json.Marshal(b.Scope)
    if string(ma) != string(mb) {
        t.Fatalf("scope marshal not stable: %s vs %s", ma, mb)
    }
}
