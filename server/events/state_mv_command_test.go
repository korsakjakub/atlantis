// SPDX-License-Identifier: Apache-2.0

package events_test

import (
	"strings"
	"testing"

	. "github.com/petergtz/pegomock/v4"
	"github.com/runatlantis/atlantis/server/core/config/valid"
	"github.com/runatlantis/atlantis/server/events"
	"github.com/runatlantis/atlantis/server/events/command"
	"github.com/runatlantis/atlantis/server/events/models"
	"github.com/runatlantis/atlantis/server/events/models/testdata"
	"github.com/runatlantis/atlantis/server/logging"
	"github.com/runatlantis/atlantis/server/metrics/metricstest"
	. "github.com/runatlantis/atlantis/testing"
)

func setForkStateMv(t *testing.T, on bool) {
	old := valid.ForkStateMvEnabled
	valid.ForkStateMvEnabled = on
	t.Cleanup(func() { valid.ForkStateMvEnabled = old })
}

func TestCommentParser_StateMv(t *testing.T) {
	setForkStateMv(t, true)
	r := commentParser.Parse(`atlantis state mv -d dir 'a["x"]' b -- -lock=false`, models.Github)
	Equals(t, "", r.CommentResponse)
	Equals(t, command.State, r.Command.Name)
	Equals(t, "mv", r.Command.SubName)
	Equals(t, "dir", r.Command.RepoRelDir)
	Equals(t, `-lock=false a["x"] b`, strings.Join(r.Command.Flags, " "))
}

// Negative cases at the comment boundary. Each must be rejected before any
// project command is built, so nothing reaches terraform.
func TestCommentParser_StateMv_Rejected(t *testing.T) {
	setForkStateMv(t, true)
	cases := []struct{ comment, expErr string }{
		{"atlantis state mv a", "unknown argument(s)"},
		{"atlantis state mv a b c", "unknown argument(s)"},
		{"atlantis state mv", "unknown argument(s)"},
		{"atlantis state mv -x a b", "unknown shorthand flag"},                  // address starting with '-' is parsed as a flag
		{"atlantis state mv a -state-out=/x", "unknown shorthand flag"},         // terraform flag before the dash separator
		{"atlantis state mv a b -- -chdir=x", `flag "-chdir=x" is not allowed`}, // blocked extra arg after --
		{"atlantis state mv a b -- --plugin-dir=/x", `is not allowed in extra args`},
	}
	for _, c := range cases {
		t.Run(c.comment, func(t *testing.T) {
			r := commentParser.Parse(c.comment, models.Github)
			Assert(t, r.Command == nil, "expected no command for %q", c.comment)
			Assert(t, strings.Contains(r.CommentResponse, c.expErr), "expected %q in response for %q, got %q", c.expErr, c.comment, r.CommentResponse)
		})
	}
}

// Control characters in an address are passed to terraform as one argv entry
// (no shell), so the boundary is only that they do not split the command.
func TestCommentParser_StateMv_ControlCharsStayInOneArg(t *testing.T) {
	setForkStateMv(t, true)
	r := commentParser.Parse("atlantis state mv 'a\tb' c", models.Github)
	Equals(t, "", r.CommentResponse)
	Equals(t, []string{"a\tb", "c"}, r.Command.Flags)
}

func TestCommentParser_StateMv_DisabledBeforeHooks(t *testing.T) {
	setForkStateMv(t, false)
	r := commentParser.Parse("atlantis state mv a b", models.Github)
	Assert(t, r.Command == nil, "expected no command")
	Assert(t, strings.Contains(r.CommentResponse, "--fork-state-mv"), "got %q", r.CommentResponse)
	// state rm is unaffected by the flag.
	r = commentParser.Parse("atlantis state rm a", models.Github)
	Equals(t, "", r.CommentResponse)
}

func TestStateCommandRunner_StateMvFlagGate(t *testing.T) {
	for _, on := range []bool{false, true} {
		name := "flag off"
		if on {
			name = "flag on"
		}
		t.Run(name, func(t *testing.T) {
			setForkStateMv(t, on)
			vcsClient := setup(t)
			logger := logging.NewNoopLogger(t)
			modelPull := models.PullRequest{BaseRepo: testdata.GithubRepo, State: models.OpenPullState, Num: testdata.Pull.Num, HeadCommit: "abc123"}
			projectCmd := command.ProjectContext{
				CommandName: command.State,
				SubCommand:  "mv",
				RepoRelDir:  "dir1",
				Workspace:   events.DefaultWorkspace,
				ProjectName: "projA",
				BaseRepo:    testdata.GithubRepo,
				Pull:        modelPull,
			}
			cmd := events.CommentCommand{Name: command.State, SubName: "mv", ProjectName: "projA"}
			ctx := &command.Context{
				User:     testdata.User,
				Log:      logger,
				Scope:    metricstest.NewLoggingScope(t, logger, "atlantis"),
				Pull:     modelPull,
				HeadRepo: testdata.GithubRepo,
				Trigger:  command.CommentTrigger,
			}
			When(projectCommandBuilder.BuildStateRmCommands(ctx, &cmd)).ThenReturn([]command.ProjectContext{projectCmd}, nil)
			When(projectCommandRunner.StateRm(projectCmd)).ThenReturn(command.ProjectCommandOutput{StateRmSuccess: &models.StateRmSuccess{Output: "moved"}})

			stateCommandRunner.Run(ctx, &cmd)

			_, _, _, comment, _ := vcsClient.VerifyWasCalledOnce().CreateComment(
				Any[logging.SimpleLogging](), Eq(testdata.GithubRepo), Eq(modelPull.Num), Any[string](), Eq("state")).GetCapturedArguments()
			if on {
				projectCommandBuilder.VerifyWasCalledOnce().BuildStateRmCommands(ctx, &cmd)
				projectCommandRunner.VerifyWasCalledOnce().StateRm(projectCmd)
				Assert(t, strings.Contains(comment, "moved"), "got: %s", comment)
				return
			}
			projectCommandBuilder.VerifyWasCalled(Never()).BuildStateRmCommands(Any[*command.Context](), Any[*events.CommentCommand]())
			projectCommandRunner.VerifyWasCalled(Never()).StateRm(Any[command.ProjectContext]())
			Assert(t, strings.Contains(comment, "--fork-state-mv"), "got: %s", comment)
		})
	}
}
