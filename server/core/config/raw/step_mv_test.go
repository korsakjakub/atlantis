// SPDX-License-Identifier: Apache-2.0

package raw_test

import (
	"testing"

	"github.com/runatlantis/atlantis/server/core/config/raw"
	"github.com/runatlantis/atlantis/server/core/config/valid"
	. "github.com/runatlantis/atlantis/testing"
	"go.yaml.in/yaml/v4"
)

func setForkStateMv(t *testing.T, on bool) {
	old := valid.ForkStateMvEnabled
	valid.ForkStateMvEnabled = on
	t.Cleanup(func() { valid.ForkStateMvEnabled = old })
}

func TestStep_Validate_Mv(t *testing.T) {
	mv := func(args map[string]any) raw.Step {
		return raw.Step{CommandMap: map[string]map[string]any{"mv": args}}
	}
	cases := []struct {
		description string
		flag        bool
		input       raw.Step
		expErr      string
	}{
		{"from and to", true, mv(map[string]any{"from": "aws_s3_bucket.a", "to": "module.m.aws_s3_bucket.b[\"x y\"]"}), ""},
		{"bare mv key", true, raw.Step{Key: String("mv")}, ""},
		{"missing to", true, mv(map[string]any{"from": "a.b"}),
			`"mv" step must have a "to" key set to a terraform address (not empty, not starting with '-')`},
		{"missing from", true, mv(map[string]any{"to": "a.b"}),
			`"mv" step must have a "from" key set to a terraform address (not empty, not starting with '-')`},
		{"empty from", true, mv(map[string]any{"from": "", "to": "a.b"}),
			`"mv" step must have a "from" key set to a terraform address (not empty, not starting with '-')`},
		{"flag-like to", true, mv(map[string]any{"from": "a.b", "to": "-state=/tmp/x"}),
			`"mv" step must have a "to" key set to a terraform address (not empty, not starting with '-')`},
		{"newline in address", true, mv(map[string]any{"from": "a.b\nc", "to": "a.c"}),
			`"mv" step must have a "from" key set to a terraform address (not empty, not starting with '-')`},
		{"extra key", true, mv(map[string]any{"from": "a.b", "to": "a.c", "force": "yes"}),
			`"mv" steps only support keys "command", "output", "shell" and "shellArgs", found extra keys "force"`},
		{"flag off, configured step", false, mv(map[string]any{"from": "a.b", "to": "a.c"}),
			`the "mv" step requires the fork feature flag --fork-state-mv (ATLANTIS_FORK_STATE_MV=true)`},
		{"flag off, bare key", false, raw.Step{Key: String("mv")},
			`the "mv" step requires the fork feature flag --fork-state-mv (ATLANTIS_FORK_STATE_MV=true)`},
		{"flag off, other steps unaffected", false, raw.Step{Key: String("state_rm")}, ""},
	}
	for _, c := range cases {
		t.Run(c.description, func(t *testing.T) {
			setForkStateMv(t, c.flag)
			err := c.input.Validate()
			if c.expErr == "" {
				Ok(t, err)
				return
			}
			ErrEquals(t, c.expErr, err)
		})
	}
}

// mv must run before plan: the YAML order is preserved and from/to survive ToValid.
func TestWorkflow_Mv_YAMLToValid(t *testing.T) {
	setForkStateMv(t, true)
	var w raw.Workflow
	Ok(t, yaml.Unmarshal([]byte(`
plan:
  steps:
  - init
  - mv:
      from: aws_s3_bucket.a
      to: aws_s3_bucket.b
  - plan
state_mv:
  steps:
  - init
  - mv
`), &w))
	Ok(t, w.Validate())
	v := w.ToValid("custom")
	Equals(t, []valid.Step{
		{StepName: "init"},
		{StepName: "mv", MvFrom: "aws_s3_bucket.a", MvTo: "aws_s3_bucket.b"},
		{StepName: "plan"},
	}, v.Plan.Steps)
	Equals(t, []valid.Step{{StepName: "init"}, {StepName: "mv"}}, v.StateMv.Steps)

	setForkStateMv(t, false)
	ErrEquals(t, `plan: (steps: (1: the "mv" step requires the fork feature flag --fork-state-mv (ATLANTIS_FORK_STATE_MV=true).).); state_mv: (steps: (1: the "mv" step requires the fork feature flag --fork-state-mv (ATLANTIS_FORK_STATE_MV=true).).).`, w.Validate())
}
