package cmd

// fork: state mv as a pipeline step (US-100). Kept out of server.go so upstream syncs do not conflict.
const ForkStateMvFlag = "fork-state-mv"

func init() {
	boolFlags[ForkStateMvFlag] = boolFlag{
		description:  "Enable the 'mv' workflow step and the 'atlantis state mv' comment command. Fork-only feature.",
		defaultValue: false,
	}
}
