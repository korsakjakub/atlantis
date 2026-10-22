// SPDX-License-Identifier: Apache-2.0

package runtime

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"testing"

	"github.com/hashicorp/go-version"
	. "github.com/petergtz/pegomock/v4"
	tf "github.com/runatlantis/atlantis/server/core/terraform"
	"github.com/runatlantis/atlantis/server/core/terraform/mocks"
	tfclientmocks "github.com/runatlantis/atlantis/server/core/terraform/tfclient/mocks"
	"github.com/runatlantis/atlantis/server/events/command"
	"github.com/runatlantis/atlantis/server/logging"
	. "github.com/runatlantis/atlantis/testing"
)

// Parity with the StateRmStepRunner tests (comment args, workspace, plan
// deletion, distribution) plus the mv specifics: configured addresses, plan
// workflow ignoring comment args, and Terraform failure.
func TestStateMvStepRunner_Run(t *testing.T) {
	cases := []struct {
		description string
		cmdName     command.Name
		extraArgs   []string
		commentArgs []string
		tfErr       error
		expCmd      []string
		expPlanGone bool
	}{
		{
			description: "state command uses comment args",
			cmdName:     command.State,
			commentArgs: []string{"-lock=false", "a.b", "a.c"},
			expCmd:      []string{"state", "mv", "-lock=false", "a.b", "a.c"},
			expPlanGone: true,
		},
		{
			description: "configured step passes from/to and ignores plan comment args",
			cmdName:     command.Plan,
			extraArgs:   []string{"a.b", "a.c"},
			commentArgs: []string{"-var", "x=y"},
			expCmd:      []string{"state", "mv", "a.b", "a.c"},
			expPlanGone: true,
		},
		{
			description: "terraform failure is returned and the plan is kept",
			cmdName:     command.State,
			commentArgs: []string{"a.b", "a.c"},
			tfErr:       errors.New("no such resource"),
			expCmd:      []string{"state", "mv", "a.b", "a.c"},
		},
	}

	for _, c := range cases {
		t.Run(c.description, func(t *testing.T) {
			RegisterMockTestingT(t)
			workspace := "something"
			tmpDir := t.TempDir()
			planPath := filepath.Join(tmpDir, fmt.Sprintf("%s.tfplan", workspace))
			Ok(t, os.WriteFile(planPath, nil, 0600))

			ctx := command.ProjectContext{
				Log:         logging.NewNoopLogger(t),
				CommandName: c.cmdName,
				CommentArgs: c.commentArgs,
				Workspace:   workspace,
			}
			terraform := tfclientmocks.NewMockClient()
			tfVersion, _ := version.NewVersion("0.15.0")
			tfDistribution := tf.NewDistributionTerraformWithDownloader(mocks.NewMockDownloader())
			s := NewStateMvStepRunner(terraform, tfDistribution, tfVersion, &LocalPlanStore{})

			When(terraform.RunCommandWithVersion(Any[command.ProjectContext](), Any[string](), Any[[]string](), Any[map[string]string](), Any[tf.Distribution](), Any[*version.Version](), Any[string]())).
				ThenReturn("output", nil)
			// Fail only the mv command, not the workspace commands run before it.
			When(terraform.RunCommandWithVersion(Any[command.ProjectContext](), Any[string](), Eq(c.expCmd), Any[map[string]string](), Any[tf.Distribution](), Any[*version.Version](), Any[string]())).
				ThenReturn("output", c.tfErr)
			output, err := s.Run(ctx, c.extraArgs, tmpDir, map[string]string(nil))

			Equals(t, c.tfErr, err)
			Equals(t, "output", output) // stderr/stdout is never hidden
			terraform.VerifyWasCalledOnce().RunCommandWithVersion(Any[command.ProjectContext](), Eq(tmpDir), Eq(c.expCmd), Eq(map[string]string(nil)), Any[tf.Distribution](), Eq(tfVersion), Eq(workspace))
			_, statErr := os.Stat(planPath)
			Equals(t, c.expPlanGone, os.IsNotExist(statErr))
		})
	}
}

func TestStateMvStepRunner_Run_UsesConfiguredDistribution(t *testing.T) {
	RegisterMockTestingT(t)
	workspace := "default"
	tmpDir := t.TempDir()
	projTFDistribution := "opentofu"
	ctx := command.ProjectContext{
		Log:                   logging.NewNoopLogger(t),
		CommandName:           command.State,
		CommentArgs:           []string{"a.b", "a.c"},
		Workspace:             workspace,
		TerraformDistribution: &projTFDistribution,
	}
	terraform := tfclientmocks.NewMockClient()
	tfVersion, _ := version.NewVersion("0.15.0")
	tfDistribution := tf.NewDistributionTerraformWithDownloader(mocks.NewMockDownloader())
	s := NewStateMvStepRunner(terraform, tfDistribution, tfVersion, &LocalPlanStore{})

	When(terraform.RunCommandWithVersion(Any[command.ProjectContext](), Any[string](), Any[[]string](), Any[map[string]string](), Any[tf.Distribution](), Any[*version.Version](), Any[string]())).
		ThenReturn("output", nil)
	_, err := s.Run(ctx, nil, tmpDir, map[string]string(nil))
	Ok(t, err)
	terraform.VerifyWasCalledOnce().RunCommandWithVersion(Any[command.ProjectContext](), Eq(tmpDir), Eq([]string{"state", "mv", "a.b", "a.c"}), Eq(map[string]string(nil)), NotEq(tfDistribution), Eq(tfVersion), Eq(workspace))
}
