package identity

// #630: identity#72 enforces a service token on every gRPC RPC
// (ServiceTokenInterceptor, x-fusion-service-token metadata, constant-time
// compare server-side). The gateway client previously dialed with no
// credential at all, so AuthorizeAndAcquire could never succeed once
// enforcement went default-on — every inference call 503'd in strict mode.

import (
    "context"
    "google.golang.org/grpc"
    "google.golang.org/grpc/metadata"
    "os"
    "path/filepath"
    "testing"
)

func Test630_ResolveServiceToken(t *testing.T) {
    if got := ResolveServiceToken(""); got != "" {
        t.Fatalf("empty path = %q, want \"\"", got)
    }
    if got := ResolveServiceToken(filepath.Join(t.TempDir(), "missing")); got != "" {
        t.Fatalf("missing file = %q, want \"\"", got)
    }
    f := filepath.Join(t.TempDir(), "tok")
    if err := os.WriteFile(f, []byte("  sec-token-630 \n"), 0600); err != nil {
        t.Fatal(err)
    }
    if got := ResolveServiceToken(f); got != "sec-token-630" {
        t.Fatalf("resolved = %q, want trimmed \"sec-token-630\"", got)
    }
}

func Test630_InterceptorAttachesServiceToken(t *testing.T) {
    var gotMD metadata.MD
    invoker := func(ctx context.Context, method string, req, reply any, cc *grpc.ClientConn, opts ...grpc.CallOption) error {
        gotMD, _ = metadata.FromOutgoingContext(ctx)
        return nil
    }
    err := serviceTokenUnaryInterceptor("tok-630")(
        context.Background(), "/pb.IdentityService/AuthorizeAndAcquire",
        nil, nil, nil, invoker)
    if err != nil {
        t.Fatalf("invoker err: %v", err)
    }
    vals := gotMD.Get(ServiceTokenMetadataKey)
    if len(vals) != 1 || vals[0] != "tok-630" {
        t.Fatalf("outgoing metadata %v, want [tok-630] under %s", vals, ServiceTokenMetadataKey)
    }
}
