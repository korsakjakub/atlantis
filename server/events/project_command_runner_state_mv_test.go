// SPDX-License-Identifier: Apache-2.0

package events_test

import (
	"errors"
	"testing"

	. "github.com/petergtz/pegomock/v4"
	"github.com/runatlantis/atlantis/server/core/config/valid"
	"github.com/runatlantis/atlantis/server/events"
	"github.com/runatlantis/atlantis/server/events/command"
	"github.com/runatlantis/atlantis/server/events/mocks"
	"github.com/runatlantis/atlantis/server/events/models"
	"github.com/runatlantis/atlantis/server/logging"
	. "github.com/runatlantis/atlantis/testing"
)

// Parity suite with the Import/StateRm runner tests for the mv step (US-100).
func TestDefaultProjectCommandRunner_StateMv(t *testing.T) {
	expEnvs := map[string]string{}
	cfgSteps := []valid.Step{{StepName: "init"}, {StepName: "mv", MvFrom: "aws_s3_bucket.a", MvTo: "aws_s3_bucket.b"}}

	cases := []struct {
		description  string
		steps        []valid.Step
		lockAcquired bool
		mvErr        error
		expMvArgs    []string
		expMvCalled  bool
		expOut       string
		expFailure   string
		expErr       string
	}{
		{
			description:  "default stage takes addresses from the comment",
			steps:        valid.DefaultStateMvStage.Steps,
			lockAcquired: true,
			expMvCalled:  true,
			expOut:       "init\nmv",
		},
		{
			description:  "configured from/to are passed as args before any extra_args",
			steps:        cfgSteps,
			lockAcquired: true,
			expMvArgs:    []string{"aws_s3_bucket.a", "aws_s3_bucket.b"},
			expMvCalled:  true,
			expOut:       "init\nmv",
		},
		{
			description:  "terraform failure is returned with the step output",
			steps:        valid.DefaultStateMvStage.Steps,
			lockAcquired: true,
			mvErr:        errors.New("resource not found"),
			expMvCalled:  true,
			expErr:       "resource not found\ninit\nmv",
		},
		{
			description: "lock not acquired runs nothing",
			steps:       valid.DefaultStateMvStage.Steps,
			expFailure:  "locked by another pull",
		},
	}

	for _, c := range cases {
		t.Run(c.description, func(t *testing.T) {
			RegisterMockTestingT(t)
			mockInit := mocks.NewMockStepRunner()
			mockMv := mocks.NewMockStepRunner()
			mockWorkingDir := mocks.NewMockWorkingDir()
			mockLocker := mocks.NewMockProjectLocker()

			runner := events.DefaultProjectCommandRunner{
				Locker:            mockLocker,
				LockURLGenerator:  mockURLGenerator{},
				InitStepRunner:    mockInit,
				StateMvStepRunner: mockMv,
				WorkingDir:        mockWorkingDir,
				WorkingDirLocker:  events.NewDefaultWorkingDirLocker(),
			}
			ctx := command.ProjectContext{
				Log:         logging.NewNoopLogger(t),
				Steps:       c.steps,
				Workspace:   "default",
				RepoRelDir:  ".",
				CommandName: command.State,
				RePlanCmd:   "atlantis plan -d . -- a b",
			}
			repoDir := t.TempDir()
			When(mockWorkingDir.Clone(Any[logging.SimpleLogging](), Any[models.Repo](), Any[models.PullRequest](),
				Any[string]())).ThenReturn(repoDir, nil)
			When(mockWorkingDir.GitReadLock(Any[models.Repo](), Any[models.PullRequest](), Any[string]())).ThenReturn(func() {})
			When(mockLocker.TryLock(
				Any[logging.SimpleLogging](), Any[models.PullRequest](), Any[models.User](),
				Any[string](), Any[models.Project](), AnyBool(),
			)).ThenReturn(&events.TryLockResponse{LockAcquired: c.lockAcquired, LockFailureReason: c.expFailure, LockKey: "k"}, nil)
			When(mockInit.Run(ctx, nil, repoDir, expEnvs)).ThenReturn("init", nil)
			When(mockMv.Run(ctx, c.expMvArgs, repoDir, expEnvs)).ThenReturn("mv", c.mvErr)

			res := runner.StateRm(ctx) // shared with state rm, steps are picked by subcommand

			Equals(t, c.expFailure, res.Failure)
			if c.expErr != "" {
				Assert(t, res.Error != nil, "expected an error")
				Equals(t, c.expErr, res.Error.Error())
			} else {
				Ok(t, res.Error)
			}
			if c.expMvCalled {
				mockMv.VerifyWasCalledOnce().Run(ctx, c.expMvArgs, repoDir, expEnvs)
			} else {
				mockMv.VerifyWasCalled(Never()).Run(Any[command.ProjectContext](), Any[[]string](), Any[string](), Any[map[string]string]())
			}
			if c.expOut != "" {
				Assert(t, res.StateRmSuccess != nil, "expected success")
				Equals(t, c.expOut, res.StateRmSuccess.Output)
				Equals(t, "atlantis plan -d .", res.StateRmSuccess.RePlanCmd)
			}
		})
	}
}
