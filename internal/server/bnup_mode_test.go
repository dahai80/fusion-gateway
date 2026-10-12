package server

import (
    "testing"

    "github.com/fusion-gateway/fusion-gateway/internal/adapter"
    "github.com/fusion-gateway/fusion-gateway/internal/config"
)

// #185: applyBnupMode remaps bnup-* alias to bnup_model + auto-attaches grammar.
func TestApplyBnupModeEnabled(t *testing.T) {
    s := newTestServer()
    s.cfg.Config.Routing.BnupMode = true
    s.cfg.Config.Routing.BnupModel = "Qwen2.5-32B-BNUP-Final"
    s.cfg.Config.Routing.BnupGrammar = "bnup-socratic"

    req := &adapter.ChatRequest{Model: "bnup-math"}
    s.applyBnupMode(req, false)

    if req.Model != "Qwen2.5-32B-BNUP-Final" {
        t.Fatalf("expected model=Qwen2.5-32B-BNUP-Final, got %q", req.Model)
    }
    if req.Grammar != "bnup-socratic" {
        t.Fatalf("expected grammar=bnup-socratic, got %q", req.Grammar)
    }
}

// #185: bnup_mode disabled (default) — no changes to request.
func TestApplyBnupModeDisabled(t *testing.T) {
    s := newTestServer()
    s.cfg.Config.Routing.BnupMode = false

    req := &adapter.ChatRequest{Model: "bnup-math"}
    s.applyBnupMode(req, false)

    if req.Model != "bnup-math" {
        t.Fatalf("expected model unchanged=bnup-math, got %q", req.Model)
    }
    if req.Grammar != "" {
        t.Fatalf("expected grammar empty, got %q", req.Grammar)
    }
}

// #185: non-bnup model is not affected even when bnup_mode=true.
func TestApplyBnupModeNonBnupModel(t *testing.T) {
    s := newTestServer()
    s.cfg.Config.Routing.BnupMode = true
    s.cfg.Config.Routing.BnupModel = "Qwen2.5-32B-BNUP-Final"
    s.cfg.Config.Routing.BnupGrammar = "bnup-socratic"

    req := &adapter.ChatRequest{Model: "mlx-community/Qwen3.8-27B-8bit"}
    s.applyBnupMode(req, false)

    if req.Model != "mlx-community/Qwen3.8-27B-8bit" {
        t.Fatalf("expected model unchanged, got %q", req.Model)
    }
    if req.Grammar != "" {
        t.Fatalf("expected grammar empty, got %q", req.Grammar)
    }
}

// #185: client-supplied grammar wins over auto-attach.
func TestApplyBnupModeClientGrammarWins(t *testing.T) {
    s := newTestServer()
    s.cfg.Config.Routing.BnupMode = true
    s.cfg.Config.Routing.BnupModel = "Qwen2.5-32B-BNUP-Final"
    s.cfg.Config.Routing.BnupGrammar = "bnup-socratic"

    req := &adapter.ChatRequest{Model: "bnup-math", Grammar: "custom-grammar"}
    s.applyBnupMode(req, false)

    if req.Model != "Qwen2.5-32B-BNUP-Final" {
        t.Fatalf("expected model=Qwen2.5-32B-BNUP-Final, got %q", req.Model)
    }
    if req.Grammar != "custom-grammar" {
        t.Fatalf("expected client grammar=custom-grammar, got %q", req.Grammar)
    }
}

// #185: bnup-* alias with different prefix suffix also remaps.
func TestApplyBnupModeDifferentAlias(t *testing.T) {
    s := newTestServer()
    s.cfg.Config.Routing.BnupMode = true
    s.cfg.Config.Routing.BnupModel = "Qwen2.5-32B-BNUP-Final"
    s.cfg.Config.Routing.BnupGrammar = "bnup-socratic"

    req := &adapter.ChatRequest{Model: "bnup-science"}
    s.applyBnupMode(req, false)

    if req.Model != "Qwen2.5-32B-BNUP-Final" {
        t.Fatalf("expected model=Qwen2.5-32B-BNUP-Final, got %q", req.Model)
    }
    if req.Grammar != "bnup-socratic" {
        t.Fatalf("expected grammar=bnup-socratic, got %q", req.Grammar)
    }
}

// #185: config defaults — BnupMode false, BnupModel/BnupGrammar populated.
func TestBnupModeConfigDefaults(t *testing.T) {
    cfg := config.DefaultConfig()
    if cfg.Routing.BnupMode {
        t.Fatal("expected BnupMode=false by default")
    }
    if cfg.Routing.BnupModel != "Qwen2.5-32B-BNUP-Final" {
        t.Fatalf("expected BnupModel=Qwen2.5-32B-BNUP-Final, got %q", cfg.Routing.BnupModel)
    }
    if cfg.Routing.BnupGrammar != "bnup-socratic" {
        t.Fatalf("expected BnupGrammar=bnup-socratic, got %q", cfg.Routing.BnupGrammar)
    }
}

// #185: BNUP model not loaded → fallback to default_model (no 404 crash).
func TestApplyBnupModeFallbackToDefault(t *testing.T) {
    s, srv := newTestServerWithMLX(t, true, []string{"qwen-7b"})
    defer srv.Close()
    s.cfg.Config.Routing.BnupMode = true
    s.cfg.Config.Routing.BnupModel = "Qwen2.5-32B-BNUP-Final"
    s.cfg.Config.Routing.BnupGrammar = "bnup-socratic"
    s.cfg.Config.Routing.DefaultModel = "qwen-7b"

    req := &adapter.ChatRequest{Model: "bnup-math"}
    s.applyBnupMode(req, false)

    if req.Model != "qwen-7b" {
        t.Fatalf("expected fallback to default model qwen-7b, got %q", req.Model)
    }
    if req.Grammar != "bnup-socratic" {
        t.Fatalf("expected grammar=bnup-socratic, got %q", req.Grammar)
    }
}

// #203: force=true bypasses the bnup_mode gate — /v1/solve-stream works
// out of the box without flipping the global default.
func TestApplyBnupModeForcedBypassesGate(t *testing.T) {
    s := newTestServer()
    s.cfg.Config.Routing.BnupMode = false // global default off
    s.cfg.Config.Routing.BnupModel = "Qwen2.5-32B-BNUP-Final"
    s.cfg.Config.Routing.BnupGrammar = "bnup-socratic"

    req := &adapter.ChatRequest{Model: "bnup-math"}
    s.applyBnupMode(req, true)

    if req.Model != "Qwen2.5-32B-BNUP-Final" {
        t.Fatalf("force=true should remap even when bnup_mode=false; got %q", req.Model)
    }
    if req.Grammar != "bnup-socratic" {
        t.Fatalf("expected grammar=bnup-socratic, got %q", req.Grammar)
    }
}

// #203: force=true with a non-bnup model sets the bnup alias before remap.
func TestApplyBnupModeForcedSetsAlias(t *testing.T) {
    s := newTestServer()
    s.cfg.Config.Routing.BnupMode = false
    s.cfg.Config.Routing.BnupModel = "Qwen2.5-32B-BNUP-Final"
    s.cfg.Config.Routing.BnupGrammar = "bnup-socratic"

    req := &adapter.ChatRequest{Model: "mlx-community/Qwen3.8-27B-8bit"}
    s.applyBnupMode(req, true)

    if req.Model != "Qwen2.5-32B-BNUP-Final" {
        t.Fatalf("force=true should set alias then remap; got %q", req.Model)
    }
    if req.Grammar != "bnup-socratic" {
        t.Fatalf("expected grammar=bnup-socratic, got %q", req.Grammar)
    }
}

// #203: force=true respects an existing bnup-* alias (no clobber).
func TestApplyBnupModeForcedRespectsBnupAlias(t *testing.T) {
    s := newTestServer()
    s.cfg.Config.Routing.BnupMode = false
    s.cfg.Config.Routing.BnupModel = "Qwen2.5-32B-BNUP-Final"
    s.cfg.Config.Routing.BnupGrammar = "bnup-socratic"

    req := &adapter.ChatRequest{Model: "bnup-science"}
    s.applyBnupMode(req, true)

    if req.Model != "Qwen2.5-32B-BNUP-Final" {
        t.Fatalf("force=true should remap existing bnup alias; got %q", req.Model)
    }
    if req.Grammar != "bnup-socratic" {
        t.Fatalf("expected grammar=bnup-socratic, got %q", req.Grammar)
    }
}

// #203: force=true respects client-supplied grammar (same as non-forced).
func TestApplyBnupModeForcedClientGrammarWins(t *testing.T) {
    s := newTestServer()
    s.cfg.Config.Routing.BnupMode = false
    s.cfg.Config.Routing.BnupModel = "Qwen2.5-32B-BNUP-Final"
    s.cfg.Config.Routing.BnupGrammar = "bnup-socratic"

    req := &adapter.ChatRequest{Model: "bnup-math", Grammar: "custom-grammar"}
    s.applyBnupMode(req, true)

    if req.Model != "Qwen2.5-32B-BNUP-Final" {
        t.Fatalf("expected model=Qwen2.5-32B-BNUP-Final, got %q", req.Model)
    }
    if req.Grammar != "custom-grammar" {
        t.Fatalf("expected client grammar=custom-grammar, got %q", req.Grammar)
    }
}
