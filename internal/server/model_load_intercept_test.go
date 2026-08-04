package server

import (
    "testing"
)

func TestParseModelLoadPath(t *testing.T) {
    tests := []struct {
        path       string
        wantModel  string
        wantAction string
    }{
        {"/v1/models/qwen3/load", "qwen3", "load"},
        {"/v1/models/qwen3/unload", "qwen3", "unload"},
        {"/v1/models/llama-3.2/load", "llama-3.2", "load"},
        {"/v1/models/llama-3.2", "llama-3.2", ""},
        {"/v1/models/", "", ""},
        {"/v1/chat/completions", "", ""},
        {"/v1/models/qwen3/other", "qwen3", "other"},
    }
    for _, tt := range tests {
        modelID, action := parseModelLoadPath(tt.path)
        if modelID != tt.wantModel || action != tt.wantAction {
            t.Errorf("parseModelLoadPath(%q) = (%q, %q), want (%q, %q)",
                tt.path, modelID, action, tt.wantModel, tt.wantAction)
        }
    }
}
