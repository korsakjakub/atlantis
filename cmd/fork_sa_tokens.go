package cmd

// fork: scoped service-account tokens (US-032). Kept out of server.go so upstream syncs do not conflict.
const ForkSATokensFlag = "fork-sa-tokens"

func init() {
	stringFlags[ForkSATokensFlag] = stringFlag{
		description: "Path to the YAML file that defines scoped service-account tokens for /api/* (id, secret_sha256, actions, repos). Empty disables it (default). Requires --api-secret. Fork-only feature.",
	}
}
