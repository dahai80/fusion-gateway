package adapter

import (
    "context"
    "net/http"
    "net/http/httptest"
    "testing"
)

func TestSpaceIDFromContext(t *testing.T) {
    t.Run("present", func(t *testing.T) {
        headers := map[string]string{"X-Space-Id": "space-123"}
        ctx := context.WithValue(context.Background(), fusionHeadersKey{}, headers)
        sid := SpaceIDFromContext(ctx)
        if sid != "space-123" {
            t.Fatalf("expected space-123, got %s", sid)
        }
    })

    t.Run("absent", func(t *testing.T) {
        sid := SpaceIDFromContext(context.Background())
        if sid != "" {
            t.Fatalf("expected empty, got %s", sid)
        }
    })

    t.Run("headers_without_space_id", func(t *testing.T) {
        headers := map[string]string{"X-Fusion-Project-Id": "proj-1"}
        ctx := context.WithValue(context.Background(), fusionHeadersKey{}, headers)
        sid := SpaceIDFromContext(ctx)
        if sid != "" {
            t.Fatalf("expected empty, got %s", sid)
        }
    })
}

func TestWithFusionHeaders(t *testing.T) {
    t.Run("with_headers", func(t *testing.T) {
        req := httptest.NewRequest("POST", "http://example.com", nil)
        req.Header.Set("X-Space-Id", "space-1")
        req.Header.Set("X-Fusion-Project-Id", "proj-1")
        ctx := WithFusionHeaders(context.Background(), req)
        headers, _ := ctx.Value(fusionHeadersKey{}).(map[string]string)
        if headers["X-Space-Id"] != "space-1" {
            t.Fatalf("expected space-1, got %s", headers["X-Space-Id"])
        }
        if headers["X-Fusion-Project-Id"] != "proj-1" {
            t.Fatalf("expected proj-1, got %s", headers["X-Fusion-Project-Id"])
        }
    })

    t.Run("no_headers", func(t *testing.T) {
        req := httptest.NewRequest("POST", "http://example.com", nil)
        ctx := WithFusionHeaders(context.Background(), req)
        headers, _ := ctx.Value(fusionHeadersKey{}).(map[string]string)
        if len(headers) != 0 {
            t.Fatalf("expected no headers, got %v", headers)
        }
    })
}

func TestInjectFusionHeaders(t *testing.T) {
    t.Run("with_headers", func(t *testing.T) {
        headers := map[string]string{
            "X-Request-ID":       "req-1",
            "X-Space-Id":         "space-1",
            "X-Fusion-Project-Id": "proj-1",
        }
        ctx := context.WithValue(context.Background(), fusionHeadersKey{}, headers)
        req, _ := http.NewRequest("POST", "http://example.com", nil)
        InjectFusionHeaders(ctx, req)
        if req.Header.Get("X-Request-ID") != "req-1" {
            t.Fatalf("expected req-1, got %s", req.Header.Get("X-Request-ID"))
        }
        if req.Header.Get("X-Space-Id") != "space-1" {
            t.Fatalf("expected space-1, got %s", req.Header.Get("X-Space-Id"))
        }
    })

    t.Run("without_headers", func(t *testing.T) {
        req, _ := http.NewRequest("POST", "http://example.com", nil)
        InjectFusionHeaders(context.Background(), req)
        if req.Header.Get("X-Request-ID") != "" {
            t.Fatal("expected no X-Request-ID header")
        }
    })
}

// TestInjectFusionHeaders_TenantStamped (#150 Gap1): a ctx carrying a
// gateway-derived tenant (adapter.WithTenant) must produce an
// X-Fusion-Tenant header on the outbound request. The tenant is
// credential-derived, not client-supplied, so it is stamped here regardless of
// any inbound header.
func TestInjectFusionHeaders_TenantStamped(t *testing.T) {
    t.Run("tenant_present", func(t *testing.T) {
        ctx := WithTenant(context.Background(), "tenant-A")
        req, _ := http.NewRequest("POST", "http://upstream/v1/chat/completions", nil)
        InjectFusionHeaders(ctx, req)
        if got := req.Header.Get("X-Fusion-Tenant"); got != "tenant-A" {
            t.Fatalf("X-Fusion-Tenant: got %q, want tenant-A", got)
        }
    })

    t.Run("tenant_absent_omits_header", func(t *testing.T) {
        req, _ := http.NewRequest("POST", "http://upstream/v1/chat/completions", nil)
        InjectFusionHeaders(context.Background(), req)
        if got := req.Header.Get("X-Fusion-Tenant"); got != "" {
            t.Fatalf("X-Fusion-Tenant: got %q, want empty (no tenant)", got)
        }
    })

    t.Run("empty_tenant_noop", func(t *testing.T) {
        ctx := WithTenant(context.Background(), "")
        if TenantFromContext(ctx) != "" {
            t.Fatal("WithTenant empty must be a no-op (no ctx value)")
        }
    })
}

// #157 items 2+3: identity lease scheduling signals forwarded as outbound
// headers so fusion-mlx can apply VRAM priority + KV cache-tag isolation.
func TestInjectFusionHeaders_LeaseSignals(t *testing.T) {
    t.Run("priority_and_max_tokens_stamped", func(t *testing.T) {
        ctx := WithLeaseSignals(context.Background(), LeaseSignals{Priority: 3, MaxAllowedTokens: 2048})
        req, _ := http.NewRequest("POST", "http://upstream/v1/chat/completions", nil)
        InjectFusionHeaders(ctx, req)
        if got := req.Header.Get("X-Fusion-Priority"); got != "3" {
            t.Fatalf("X-Fusion-Priority: got %q, want 3", got)
        }
        if got := req.Header.Get("X-Fusion-Max-Tokens"); got != "2048" {
            t.Fatalf("X-Fusion-Max-Tokens: got %q, want 2048", got)
        }
    })
    t.Run("zero_signals_omitted", func(t *testing.T) {
        req, _ := http.NewRequest("POST", "http://upstream/v1/chat/completions", nil)
        InjectFusionHeaders(context.Background(), req)
        if got := req.Header.Get("X-Fusion-Priority"); got != "" {
            t.Fatalf("X-Fusion-Priority: got %q, want empty", got)
        }
        if got := req.Header.Get("X-Fusion-Max-Tokens"); got != "" {
            t.Fatalf("X-Fusion-Max-Tokens: got %q, want empty", got)
        }
    })
    t.Run("priority_only_no_max_tokens", func(t *testing.T) {
        ctx := WithLeaseSignals(context.Background(), LeaseSignals{Priority: 2})
        req, _ := http.NewRequest("POST", "http://upstream/v1/chat/completions", nil)
        InjectFusionHeaders(ctx, req)
        if got := req.Header.Get("X-Fusion-Priority"); got != "2" {
            t.Fatalf("X-Fusion-Priority: got %q, want 2", got)
        }
        if got := req.Header.Get("X-Fusion-Max-Tokens"); got != "" {
            t.Fatalf("X-Fusion-Max-Tokens should be omitted when 0, got %q", got)
        }
    })
    t.Run("zero_signals_noop", func(t *testing.T) {
        ctx := WithLeaseSignals(context.Background(), LeaseSignals{})
        if LeaseSignalsFromContext(ctx).Priority != 0 {
            t.Fatal("WithLeaseSignals zero must be a no-op")
        }
    })
}
