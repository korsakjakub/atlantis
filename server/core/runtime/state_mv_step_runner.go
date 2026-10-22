// Copyright 2025 The Atlantis Authors
// SPDX-License-Identifier: Apache-2.0

package runtime

import (
	"os"
	"path/filepath"

	version "github.com/hashicorp/go-version"
	"github.com/runatlantis/atlantis/server/core/terraform"
	"github.com/runatlantis/atlantis/server/events/command"
)

// stateMvStepRunner runs `terraform state mv` (fork, US-100). Modeled on
// stateRmStepRunner. The addresses arrive either as extraArgs (the configured
// `mv: {from, to}` step) or as CommentArgs (`atlantis state mv FROM TO`).
type stateMvStepRunner struct {
	terraformExecutor     TerraformExec
	defaultTFDistribution terraform.Distribution
	defaultTFVersion      *version.Version
	planStore             PlanStore
}

func NewStateMvStepRunner(terraformExecutor TerraformExec, defaultTfDistribution terraform.Distribution, defaultTfVersion *version.Version, planStore PlanStore) Runner {
	runner := &stateMvStepRunner{
		terraformExecutor:     terraformExecutor,
		defaultTFDistribution: defaultTfDistribution,
		defaultTFVersion:      defaultTfVersion,
		planStore:             planStore,
	}
	return NewWorkspaceStepRunnerDelegate(terraformExecutor, defaultTfDistribution, defaultTfVersion, runner)
}

func (p *stateMvStepRunner) Run(ctx command.ProjectContext, extraArgs []string, path string, envs map[string]string) (string, error) {
	// extraArgs holds only the configured from/to addresses. Unlike other
	// steps they are never marked expandable: `$` is legal in an address key.
	tfDistribution := p.defaultTFDistribution
	tfVersion := p.defaultTFVersion
	if ctx.TerraformDistribution != nil {
		tfDistribution = terraform.NewDistribution(*ctx.TerraformDistribution)
	}
	if ctx.TerraformVersion != nil {
		tfVersion = ctx.TerraformVersion
	}

	stateMvCmd := []string{"state", "mv"}
	stateMvCmd = append(stateMvCmd, extraArgs...)
	// Only the state command carries mv addresses in the comment. In a plan
	// workflow the comment args are plan flags and must not reach state mv.
	if ctx.CommandName == command.State {
		stateMvCmd = append(stateMvCmd, ctx.CommentArgs...)
	}
	out, err := p.terraformExecutor.RunCommandWithVersion(ctx, filepath.Clean(path), stateMvCmd, envs, tfDistribution, tfVersion, ctx.Workspace)

	// If the state mv was successful and a plan file exists, delete the plan.
	planPath := GetPlanFilePath(ctx, path)
	if err == nil {
		if _, planPathErr := os.Stat(planPath); !os.IsNotExist(planPathErr) {
			ctx.Log.Info("state mv successful, deleting planfile")
			if removeErr := p.planStore.Remove(ctx, planPath); removeErr != nil {
				ctx.Log.Warn("failed to delete planfile after successful state mv: %s", removeErr)
			}
		}
	}
	return out, err
}
