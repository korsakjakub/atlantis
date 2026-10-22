// SPDX-License-Identifier: Apache-2.0

package events

import (
	"context"
	"encoding/json"
	"fmt"

	"github.com/runatlantis/atlantis/server/core/queue"
	"github.com/runatlantis/atlantis/server/events/command"
	"github.com/runatlantis/atlantis/server/events/models"
)

// queuedCommand is the serialized form of a RunCommentCommand/RunAutoplanCommand call.
type queuedCommand struct {
	BaseRepo models.Repo
	HeadRepo *models.Repo
	Pull     *models.PullRequest
	User     models.User
	PullNum  int
	Cmd      *CommentCommand // nil for autoplan
}

// RunAutoplanCommand runs plan and policy_checks when a pull request is opened or updated.
func (c *DefaultCommandRunner) RunAutoplanCommand(baseRepo models.Repo, headRepo models.Repo, pull models.PullRequest, user models.User) {
	if c.Queue == nil {
		c.runAutoplanCommand(baseRepo, headRepo, pull, user)
		return
	}
	c.enqueue(queuedCommand{BaseRepo: baseRepo, HeadRepo: &headRepo, Pull: &pull, User: user, PullNum: pull.Num},
		command.Autoplan.String(), "autoplan")
}

// RunCommentCommand executes the command, inline or through the queue.
func (c *DefaultCommandRunner) RunCommentCommand(baseRepo models.Repo, maybeHeadRepo *models.Repo, maybePull *models.PullRequest, user models.User, pullNum int, cmd *CommentCommand) {
	if c.Queue == nil {
		c.runCommentCommand(baseRepo, maybeHeadRepo, maybePull, user, pullNum, cmd)
		return
	}
	step := fmt.Sprintf("%s:%s:%s", cmd.Workspace, cmd.RepoRelDir, cmd.ProjectName)
	c.enqueue(queuedCommand{BaseRepo: baseRepo, HeadRepo: maybeHeadRepo, Pull: maybePull, User: user, PullNum: pullNum, Cmd: cmd},
		cmd.Name.String(), step)
}

func (c *DefaultCommandRunner) enqueue(qc queuedCommand, cmdName, step string) {
	payload, err := json.Marshal(qc)
	if err != nil {
		c.Logger.Err("encoding queued command: %s", err)
		return
	}
	job := queue.Job{Repo: qc.BaseRepo.FullName, Pull: qc.PullNum, Command: cmdName, Step: step, Payload: payload}
	ok, err := c.Queue.Submit(context.Background(), job)
	switch {
	case err != nil:
		// No inline fallback: that would silently bypass the dedup/retry contract.
		c.Logger.Err("enqueueing %q: %s", job.Key(), err)
	case !ok:
		c.Logger.Info("dropping duplicate queued command %q", job.Key())
	}
}

// HandleQueuedJob is the queue.Handler that executes a job enqueued by this runner.
func (c *DefaultCommandRunner) HandleQueuedJob(_ context.Context, job queue.Job) error {
	if c.Drainer.GetStatus().ShuttingDown {
		return queue.ErrAbandon // leave it for the next instance instead of acking a "shutting down" no-op
	}
	var qc queuedCommand
	if err := json.Unmarshal(job.Payload, &qc); err != nil {
		return fmt.Errorf("decoding queued command: %w", err)
	}
	if qc.Cmd == nil {
		if qc.HeadRepo == nil || qc.Pull == nil {
			return fmt.Errorf("queued autoplan %q lacks repo or pull", job.Key())
		}
		c.runAutoplanCommand(qc.BaseRepo, *qc.HeadRepo, *qc.Pull, qc.User)
		return nil
	}
	c.runCommentCommand(qc.BaseRepo, qc.HeadRepo, qc.Pull, qc.User, qc.PullNum, qc.Cmd)
	return nil
}
