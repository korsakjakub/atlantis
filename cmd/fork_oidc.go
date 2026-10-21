package cmd

// fork: OIDC login (US-030). The flag definitions are registered explicitly in
// the stringFlags and boolFlags literals in cmd/server.go (no init()).
const (
	ForkOIDCFlag         = "fork-oidc"
	ForkOIDCConfigFlag   = "fork-oidc-config"
	ForkOIDCAPITokenFlag = "fork-oidc-api-token" // nolint: gosec // flag name, not a credential
)
