package admin

import "context"

type adminContextKey string

const claimsKey adminContextKey = "admin_claims"

func WithAdminContext(ctx context.Context, claims *AdminClaims) context.Context {
    return context.WithValue(ctx, claimsKey, claims)
}

func GetAdminClaims(ctx context.Context) *AdminClaims {
    c, _ := ctx.Value(claimsKey).(*AdminClaims)
    return c
}
