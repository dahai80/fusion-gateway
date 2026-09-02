package identity

import (
    "time"

    "google.golang.org/grpc"
    pb "github.com/fusion-gateway/fusion-gateway/internal/identity/pb"
)

// NewClientForTesting constructs a Client over a pre-built gRPC connection
// (e.g. a bufconn listener in tests). Exported so cross-package tests
// (middleware) can exercise the real Client + breaker without depending on
// grpc.NewClient's network dial. Not used by production code.
func NewClientForTesting(conn *grpc.ClientConn, deadlineMS, breakerThreshold, breakerOpenSec int, fallback bool) *Client {
    return &Client{
        conn:     conn,
        stub:     pb.NewIdentityServiceClient(conn),
        breaker:  newBreaker(breakerThreshold, time.Duration(breakerOpenSec)*time.Second),
        deadline: time.Duration(deadlineMS) * time.Millisecond,
        fallback: fallback,
        endpoint: "test",
    }
}
