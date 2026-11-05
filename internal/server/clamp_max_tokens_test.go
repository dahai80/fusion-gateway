package server

import (
    "testing"

    "github.com/fusion-gateway/fusion-gateway/internal/config"
)

// #528: clampMaxTokens caps a client-supplied max_tokens at the configured
// server.max_output_tokens ceiling before the request is proxied, so provider
// behavior does not depend on the backend behind the gateway. 0 (default) or
// negative ceiling disables the clamp; nil / non-positive values pass through.
func TestClampMaxTokens(t *testing.T) {
    mk := func(ceiling int) *Server {
        return &Server{cfg: &config.ConfigSnapshot{Config: config.Config{
            Server: config.ServerConfig{MaxOutputTokens: ceiling},
        }}}
    }
    tests := []struct {
        name    string
        ceiling int
        in      *int
        want    *int
    }{
        {"nil passes", 4096, nil, nil},
        {"non-positive passes", 4096, intPtr(0), intPtr(0)},
        {"negative passes", 4096, intPtr(-5), intPtr(-5)},
        {"zero ceiling disables clamp", 0, intPtr(999999999), intPtr(999999999)},
        {"negative ceiling disables clamp", -1, intPtr(999999999), intPtr(999999999)},
        {"within ceiling unchanged", 4096, intPtr(100), intPtr(100)},
        {"at ceiling unchanged", 4096, intPtr(4096), intPtr(4096)},
        {"over ceiling clamped", 4096, intPtr(999999999), intPtr(4096)},
    }
    for _, tc := range tests {
        t.Run(tc.name, func(t *testing.T) {
            s := mk(tc.ceiling)
            s.clampMaxTokens(tc.in, "/v1/chat/completions")
            if tc.want == nil {
                if tc.in != nil {
                    t.Fatalf("want nil, got %d", *tc.in)
                }
                return
            }
            if tc.in == nil || *tc.in != *tc.want {
                t.Fatalf("clamped = %v, want %d", tc.in, *tc.want)
            }
        })
    }
}

func intPtr(v int) *int { return &v }
