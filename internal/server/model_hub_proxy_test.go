package server

import (
	"testing"
)

func TestInferModuleFromPath(t *testing.T) {
	tests := []struct {
		path     string
		expected string
	}{
		{"/api/v1/inference/completions", "chat"},
		{"/api/v1/chat/completions", "chat"},
		{"/api/v1/code/generate", "code"},
		{"/api/v1/rag/query", "rag"},
		{"/api/v1/agent/run", "agent"},
		{"/api/v1/design/render", "design"},
		{"/api/v1/models", ""},
		{"/api/v1/system/health", ""},
		{"/api/v1/", ""},
	}
	for _, tt := range tests {
		got := inferModuleFromPath(tt.path)
		if got != tt.expected {
			t.Errorf("inferModuleFromPath(%q) = %q, want %q", tt.path, got, tt.expected)
		}
	}
}
