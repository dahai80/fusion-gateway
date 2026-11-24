package server

import (
    "context"

    "github.com/fusion-gateway/fusion-gateway/internal/shell"
)

type shellClaimsKey struct{}

type shellTokenKey struct{}

func withShellClaims(ctx context.Context, claims shell.SessionClaims) context.Context {
    return context.WithValue(ctx, shellClaimsKey{}, claims)
}

func shellClaimsFrom(ctx context.Context) shell.SessionClaims {
    if c, ok := ctx.Value(shellClaimsKey{}).(shell.SessionClaims); ok {
        return c
    }
    return shell.SessionClaims{}
}

// withShellToken / shellTokenFrom (#715): the validated session token string
// rides the context so downstream handlers (identity messaging bridge) key
// the token vault on exactly what the middleware verified — never a raw
// client header.
func withShellToken(ctx context.Context, token string) context.Context {
    return context.WithValue(ctx, shellTokenKey{}, token)
}

func shellTokenFrom(ctx context.Context) string {
    if t, ok := ctx.Value(shellTokenKey{}).(string); ok {
        return t
    }
    return ""
}
