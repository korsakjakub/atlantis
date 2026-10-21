package oidc

import "context"

type identityKey struct{}

// WithIdentity returns a context carrying the fork identity.
func WithIdentity(ctx context.Context, id Identity) context.Context {
	return context.WithValue(ctx, identityKey{}, id)
}

// IdentityFromContext returns the OIDC identity of the request, if any.
// It is deliberately not wired into models.User or the allowlists (US-031).
func IdentityFromContext(ctx context.Context) (Identity, bool) {
	id, ok := ctx.Value(identityKey{}).(Identity)
	return id, ok
}
