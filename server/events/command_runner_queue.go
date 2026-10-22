// SPDX-License-Identifier: Apache-2.0

package events

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"

	"github.com/runatlantis/atlantis/server/core/queue"
	"github.com/runatlantis/atlantis/server/events/command"
	"github.com/runatlantis/atlantis/server/events/models"
)

// queuedCommand is the serialized form of a RunCommentCommand/RunAutoplanCommand
// call. It holds PR and user data only: CloneURL (which embeds the VCS token)
// is cleared before serialization and restored by DefaultCommandRunner.RepoAuth.
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
	// A new commit is a new event, so the head commit is part of the step.
	c.enqueue(queuedCommand{BaseRepo: baseRepo, HeadRepo: &headRepo, Pull: &pull, User: user, PullNum: pull.Num},
		command.Autoplan.String(), "autoplan@"+pull.HeadCommit)
}

// RunCommentCommand executes the command, inline or through the queue.
func (c *DefaultCommandRunner) RunCommentCommand(baseRepo models.Repo, maybeHeadRepo *models.Repo, maybePull *models.PullRequest, user models.User, pullNum int, cmd *CommentCommand) {
	if c.Queue == nil {
		c.runCommentCommand(baseRepo, maybeHeadRepo, maybePull, user, pullNum, cmd)
		return
	}
	// Flags and author are part of the step: only an identical redelivery is a duplicate.
	b, _ := json.Marshal(cmd) // CommentCommand only holds marshalable fields
	sum := sha256.Sum256(append(b, user.Username...))
	step := fmt.Sprintf("%s:%s:%s@%s", cmd.Workspace, cmd.RepoRelDir, cmd.ProjectName, hex.EncodeToString(sum[:6]))
	c.enqueue(queuedCommand{BaseRepo: baseRepo, HeadRepo: maybeHeadRepo, Pull: maybePull, User: user, PullNum: pullNum, Cmd: cmd},
		cmd.Name.String(), step)
}

// stripped returns a copy of repo without its credentials-bearing CloneURL.
func stripped(repo models.Repo) models.Repo {
	repo.CloneURL = ""
	return repo
}

func (c *DefaultCommandRunner) enqueue(qc queuedCommand, cmdName, step string) {
	qc.BaseRepo = stripped(qc.BaseRepo)
	if qc.HeadRepo != nil {
		h := stripped(*qc.HeadRepo)
		qc.HeadRepo = &h
	}
	if qc.Pull != nil {
		p := *qc.Pull
		p.BaseRepo = stripped(p.BaseRepo)
		qc.Pull = &p
	}
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

// restore re-adds the server's VCS credentials to a deserialized repo.
func (c *DefaultCommandRunner) restore(repo models.Repo) (models.Repo, error) {
	if c.RepoAuth == nil {
		return repo, nil
	}
	return c.RepoAuth(repo)
}

// HandleQueuedJob is the queue.Handler that executes a job enqueued by this runner.
func (c *DefaultCommandRunner) HandleQueuedJob(_ context.Context, job queue.Job) error {
	// Hold the drain op for the whole run: checking ShuttingDown first and letting the
	// inner StartOp race it could ack a job that only got a "shutting down" comment.
	if !c.Drainer.StartOp() {
		return queue.ErrAbandon // leave it for the next instance
	}
	defer c.Drainer.OpDone()

	var qc queuedCommand
	if err := json.Unmarshal(job.Payload, &qc); err != nil {
		return fmt.Errorf("decoding queued command: %w", err)
	}
	var err error
	if qc.BaseRepo, err = c.restore(qc.BaseRepo); err != nil {
		return fmt.Errorf("restoring base repo credentials: %w", err)
	}
	if qc.HeadRepo != nil {
		h, err := c.restore(*qc.HeadRepo)
		if err != nil {
			return fmt.Errorf("restoring head repo credentials: %w", err)
		}
		qc.HeadRepo = &h
	}
	if qc.Pull != nil {
		if qc.Pull.BaseRepo, err = c.restore(qc.Pull.BaseRepo); err != nil {
			return fmt.Errorf("restoring pull base repo credentials: %w", err)
		}
	}
	if qc.Cmd == nil {
		if qc.HeadRepo == nil || qc.Pull == nil {
			return fmt.Errorf("queued autoplan %q lacks repo or pull", job.Key())
		}
		c.doAutoplanCommand(qc.BaseRepo, *qc.HeadRepo, *qc.Pull, qc.User)
		return nil
	}
	c.doCommentCommand(qc.BaseRepo, qc.HeadRepo, qc.Pull, qc.User, qc.PullNum, qc.Cmd)
	return nil
}
