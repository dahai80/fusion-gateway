package admin

import (
    "encoding/json"
    "testing"
)

func TestReplayNormalize_ChatForcesNonStreamAndStripsCreds(t *testing.T) {
    in := []byte(`{"model":"m","messages":[{"role":"user","content":"hi"}],"stream":true,"api_key":"sk-leak","password":"p"}`)
    out, endpoint, err := replayNormalize(in)
    if err != nil {
        t.Fatalf("normalize: %v", err)
    }
    if endpoint != "/v1/chat/completions" {
        t.Fatalf("endpoint = %s, want /v1/chat/completions", endpoint)
    }
    var m map[string]any
    if err := json.Unmarshal(out, &m); err != nil {
        t.Fatalf("normalized body not JSON: %v", err)
    }
    if v, _ := m["stream"].(bool); v {
        t.Fatalf("stream must be forced false, got %v", m["stream"])
    }
    for _, k := range []string{"api_key", "password"} {
        if _, ok := m[k]; ok {
            t.Fatalf("credential field %s must be stripped", k)
        }
    }
    if _, ok := m["messages"]; !ok {
        t.Fatal("messages must survive normalization")
    }
}

func TestReplayNormalize_EndpointByBodyShape(t *testing.T) {
    cases := []struct {
        name string
        body string
        want string
    }{
        {"completions", `{"model":"m","prompt":"hi"}`, "/v1/completions"},
        {"embeddings", `{"model":"m","input":"hi"}`, "/v1/embeddings"},
        {"chat", `{"model":"m","messages":[]}`, "/v1/chat/completions"},
    }
    for _, c := range cases {
        _, endpoint, err := replayNormalize([]byte(c.body))
        if err != nil {
            t.Fatalf("%s: %v", c.name, err)
        }
        if endpoint != c.want {
            t.Fatalf("%s: endpoint = %s, want %s", c.name, endpoint, c.want)
        }
    }
}

func TestReplayNormalize_RejectsUnknownShape(t *testing.T) {
    if _, _, err := replayNormalize([]byte(`{"model":"m"}`)); err == nil {
        t.Fatal("body with none of messages/prompt/input must be rejected")
    }
    if _, _, err := replayNormalize([]byte(`not json`)); err == nil {
        t.Fatal("non-JSON body must be rejected")
    }
}
