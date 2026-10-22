// SPDX-License-Identifier: Apache-2.0

package events

import (
	"context"
	"encoding/json"
	"testing"

	"github.com/runatlantis/atlantis/server/core/queue"
	"github.com/runatlantis/atlantis/server/events/command"
	"github.com/runatlantis/atlantis/server/events/models"
	"github.com/runatlantis/atlantis/server/logging"
	. "github.com/runatlantis/atlantis/testing"
)

func TestRunCommentCommand_Queue(t *testing.T) {
	backend := queue.NewMemory(queue.Options{})
	c := &DefaultCommandRunner{
		Logger:  logging.NewNoopLogger(t),
		Drainer: &Drainer{},
		Queue:   &queue.Executor{Backend: backend},
	}
	repo := models.Repo{FullName: "org/repo"}
	cmd := &CommentCommand{Name: command.Plan, Workspace: "default", RepoRelDir: ".", Flags: []string{"-target=x"}}
	user := models.User{Username: "u", Teams: []string{"t"}}

	c.RunCommentCommand(repo, nil, nil, user, 5, cmd)
	c.RunCommentCommand(repo, nil, nil, user, 5, cmd) // duplicate webhook delivery
	cmd2 := *cmd
	cmd2.Workspace = "prod"
	c.RunCommentCommand(repo, nil, nil, user, 5, &cmd2) // different step

	var jobs []queue.Job
	for {
		j, err := backend.Receive(context.Background(), "t")
		Ok(t, err)
		if j == nil {
			break
		}
		jobs = append(jobs, *j)
	}
	Equals(t, 2, len(jobs))
	Equals(t, "org/repo#5#plan#default:.:", jobs[0].Key())

	var qc queuedCommand
	Ok(t, json.Unmarshal(jobs[0].Payload, &qc))
	Equals(t, *cmd, *qc.Cmd)
	Equals(t, user, qc.User)

	// A draining server leaves the job for the next instance.
	c.Drainer.ShutdownBlocking()
	Equals(t, queue.ErrAbandon, c.HandleQueuedJob(context.Background(), jobs[0]))
}

func TestHandleQueuedJob_BadPayload(t *testing.T) {
	c := &DefaultCommandRunner{Logger: logging.NewNoopLogger(t), Drainer: &Drainer{}}
	err := c.HandleQueuedJob(context.Background(), queue.Job{Payload: []byte("{")})
	Assert(t, err != nil, "undecodable payload must fail (and so end in the DLQ)")
}
