package server

import (
    "context"

    "github.com/fusion-gateway/fusion-gateway/internal/shell"
)

type shellClaimsKey struct{}

func withShellClaims(ctx context.Context, claims shell.SessionClaims) context.Context {
    return context.WithValue(ctx, shellClaimsKey{}, claims)
}

func shellClaimsFrom(ctx context.Context) shell.SessionClaims {
    if c, ok := ctx.Value(shellClaimsKey{}).(shell.SessionClaims); ok {
        return c
    }
    return shell.SessionClaims{}
}
