package rbac

import (
	"github.com/runatlantis/atlantis/server/events/command"
	"github.com/runatlantis/atlantis/server/events/models"
)

// CommandChecker decorates the VCS team allowlist checker. It can only
// further restrict PR commands for VCS users listed in vcs_users; the
// wrapped checker (and so the VCS identity) keeps deciding for everyone.
type CommandChecker struct {
	Inner  command.TeamAllowlistChecker // may be nil
	Engine *Engine
}

var _ command.TeamAllowlistChecker = (*CommandChecker)(nil)

func (c *CommandChecker) innerRules() bool { return c.Inner != nil && c.Inner.HasRules() }

func (c *CommandChecker) HasRules() bool { return c.innerRules() || c.Engine.HasVCSUsers() }

func (c *CommandChecker) IsCommandAllowedForTeam(ctx models.TeamAllowlistCheckerContext, team, cmd string) bool {
	return c.innerRules() && c.Inner.IsCommandAllowedForTeam(ctx, team, cmd)
}

func (c *CommandChecker) IsCommandAllowedForAnyTeam(ctx models.TeamAllowlistCheckerContext, teams []string, cmd string) bool {
	if role, ok := c.Engine.VCSRole(ctx.User.Username, ctx.BaseRepo.FullName); ok && !role.Allows(ActionForCommand(cmd)) {
		return false
	}
	return !c.innerRules() || c.Inner.IsCommandAllowedForAnyTeam(ctx, teams, cmd)
}

func (c *CommandChecker) AllTeams() []string {
	if c.Inner == nil {
		return nil
	}
	return c.Inner.AllTeams()
}

// ActionForCommand maps an Atlantis command name to the action it needs.
func ActionForCommand(cmd string) Action {
	switch cmd {
	case command.ApprovePolicies.String():
		return ActionApprove
	case command.Apply.String(), command.Import.String(), command.State.String():
		return ActionApply
	}
	return ActionPlan
}
