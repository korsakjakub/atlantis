// Package rbac maps OIDC claims, VCS usernames and the API token to roles
// per repository and enforces them on the web UI, /api/* and PR commands.
// Fork-only feature, see docs/adr/0006-fork-rbac.md.
package rbac

import "fmt"

// Role is ordered: every role includes the permissions of the lower ones.
type Role int

const (
	None   Role = iota // no access; also the zero value
	Viewer             // read-only: UI pages, list endpoints
	Plan               // may plan and detect drift
	Apply              // may apply and remediate
	// Approver is reserved for approvals (US-090); it includes Apply.
	Approver
	Admin // locks, global apply lock, unknown endpoints
)

var roleNames = [...]string{"none", "viewer", "plan", "apply", "approver", "admin"}

func (r Role) String() string { return roleNames[r] }

// ParseRole parses a configured role name. "none" is not configurable.
func ParseRole(s string) (Role, error) {
	for i := Viewer; i <= Admin; i++ {
		if roleNames[i] == s {
			return i, nil
		}
	}
	return None, fmt.Errorf("unknown role %q (want viewer|plan|apply|approver|admin)", s)
}

// Action is something a principal wants to do; its value is the minimum role.
type Action Role

const (
	ActionRead    = Action(Viewer)
	ActionPlan    = Action(Plan)
	ActionApply   = Action(Apply)
	ActionApprove = Action(Approver)
	ActionAdmin   = Action(Admin)
)

// Allows reports whether the role may perform the action.
func (r Role) Allows(a Action) bool { return r >= Role(a) }
