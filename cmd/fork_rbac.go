package cmd

// fork: RBAC (US-031). Kept out of server.go so upstream syncs do not conflict.
const (
	ForkRBACFlag       = "fork-rbac"
	ForkRBACConfigFlag = "fork-rbac-config"
)

func init() {
	boolFlags[ForkRBACFlag] = boolFlag{
		description:  "Enforce roles (viewer|plan|apply|approver|admin) per repo on the web UI, /api/* and PR commands. Requires --fork-oidc and --fork-rbac-config. Fork-only feature.",
		defaultValue: false,
	}
	stringFlags[ForkRBACConfigFlag] = stringFlag{
		description: "Path to the YAML file that maps claims, VCS users and the API token to roles. Fork-only feature.",
	}
}
