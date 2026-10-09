package adapter

import (
    "encoding/json"
    "testing"
)

// #184: Grammar field is serialized to JSON when set, omitted when empty.
func TestChatRequestGrammarSerialization(t *testing.T) {
    t.Run("grammar set", func(t *testing.T) {
        req := ChatRequest{
            Model:   "bnup-math",
            Grammar: "bnup-socratic",
        }
        data, err := json.Marshal(req)
        if err != nil {
            t.Fatalf("marshal: %v", err)
        }
        var m map[string]interface{}
        if err := json.Unmarshal(data, &m); err != nil {
            t.Fatalf("unmarshal: %v", err)
        }
        if m["grammar"] != "bnup-socratic" {
            t.Fatalf("expected grammar=bnup-socratic, got %v", m["grammar"])
        }
        if _, ok := m["grammar_backend"]; ok {
            t.Fatal("grammar_backend should be omitted when empty")
        }
    })

    t.Run("grammar backend set", func(t *testing.T) {
        req := ChatRequest{
            Model:         "test-model",
            Grammar:       "list:max(3)",
            GrammarBackend: "llguidance",
        }
        data, err := json.Marshal(req)
        if err != nil {
            t.Fatalf("marshal: %v", err)
        }
        var m map[string]interface{}
        if err := json.Unmarshal(data, &m); err != nil {
            t.Fatalf("unmarshal: %v", err)
        }
        if m["grammar"] != "list:max(3)" {
            t.Fatalf("expected grammar=list:max(3), got %v", m["grammar"])
        }
        if m["grammar_backend"] != "llguidance" {
            t.Fatalf("expected grammar_backend=llguidance, got %v", m["grammar_backend"])
        }
    })

    t.Run("grammar omitted when empty", func(t *testing.T) {
        req := ChatRequest{Model: "test-model"}
        data, err := json.Marshal(req)
        if err != nil {
            t.Fatalf("marshal: %v", err)
        }
        var m map[string]interface{}
        if err := json.Unmarshal(data, &m); err != nil {
            t.Fatalf("unmarshal: %v", err)
        }
        if _, ok := m["grammar"]; ok {
            t.Fatal("grammar should be omitted when empty")
        }
        if _, ok := m["grammar_backend"]; ok {
            t.Fatal("grammar_backend should be omitted when empty")
        }
    })
}

// #184: Grammar field is deserialized from incoming request JSON.
func TestChatRequestGrammarDeserialization(t *testing.T) {
    raw := `{"model":"bnup-math","grammar":"bnup-socratic","grammar_backend":"xgraph"}`
    var req ChatRequest
    if err := json.Unmarshal([]byte(raw), &req); err != nil {
        t.Fatalf("unmarshal: %v", err)
    }
    if req.Grammar != "bnup-socratic" {
        t.Fatalf("expected grammar=bnup-socratic, got %q", req.Grammar)
    }
    if req.GrammarBackend != "xgraph" {
        t.Fatalf("expected grammar_backend=xgraph, got %q", req.GrammarBackend)
    }
}

// #184: Request without grammar field is backward compatible.
func TestChatRequestGrammarBackwardCompat(t *testing.T) {
    raw := `{"model":"test-model","messages":[{"role":"user","content":"hi"}]}`
    var req ChatRequest
    if err := json.Unmarshal([]byte(raw), &req); err != nil {
        t.Fatalf("unmarshal: %v", err)
    }
    if req.Grammar != "" {
        t.Fatalf("expected empty grammar, got %q", req.Grammar)
    }
    if req.GrammarBackend != "" {
        t.Fatalf("expected empty grammar_backend, got %q", req.GrammarBackend)
    }
    if req.Model != "test-model" {
        t.Fatalf("expected model=test-model, got %q", req.Model)
    }
}
