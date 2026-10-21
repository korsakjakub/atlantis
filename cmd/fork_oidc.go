package cmd

// fork: OIDC login (US-030). Kept out of server.go so upstream syncs do not conflict.
const (
	ForkOIDCFlag         = "fork-oidc"
	ForkOIDCConfigFlag   = "fork-oidc-config"
	ForkOIDCAPITokenFlag = "fork-oidc-api-token" // nolint: gosec // flag name, not a credential
)

func init() {
	stringFlags[ForkOIDCFlag] = stringFlag{
		description: "Enable OIDC login for the web UI and API using the named preset from --fork-oidc-config. Empty or 'off' disables it (default). Fork-only feature.",
	}
	stringFlags[ForkOIDCConfigFlag] = stringFlag{
		description: "Path to the YAML file that defines the OIDC presets. Fork-only feature.",
	}
	boolFlags[ForkOIDCAPITokenFlag] = boolFlag{
		description:  "With --fork-oidc enabled, also accept the X-Atlantis-Token API secret on /api/* without a login session (machine clients). Fork-only feature.",
		defaultValue: false,
	}
}
