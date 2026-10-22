// SPDX-License-Identifier: Apache-2.0

package events

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"testing"

	"github.com/runatlantis/atlantis/server/core/queue"
	"github.com/runatlantis/atlantis/server/events/command"
	"github.com/runatlantis/atlantis/server/events/models"
	"github.com/runatlantis/atlantis/server/logging"
	. "github.com/runatlantis/atlantis/testing"
)

func queueRunner(t *testing.T) (*DefaultCommandRunner, *queue.Memory) {
	backend := queue.NewMemory(queue.Options{})
	return &DefaultCommandRunner{
		Logger:  logging.NewNoopLogger(t),
		Drainer: &Drainer{},
		Queue:   &queue.Executor{Backend: backend},
	}, backend
}

func drain(t *testing.T, b queue.Backend) []queue.Job {
	var jobs []queue.Job
	for {
		j, err := b.Receive(context.Background(), "t")
		Ok(t, err)
		if j == nil {
			return jobs
		}
		jobs = append(jobs, *j)
	}
}

func TestRunCommentCommand_Queue(t *testing.T) {
	c, backend := queueRunner(t)
	repo := models.Repo{FullName: "org/repo"}
	cmd := &CommentCommand{Name: command.Plan, Workspace: "default", RepoRelDir: ".", Flags: []string{"-target=a"}}
	user := models.User{Username: "u", Teams: []string{"t"}}

	c.RunCommentCommand(repo, nil, nil, user, 5, cmd)
	c.RunCommentCommand(repo, nil, nil, user, 5, cmd) // identical redelivery
	cmd2 := *cmd
	cmd2.Workspace = "prod"
	c.RunCommentCommand(repo, nil, nil, user, 5, &cmd2) // different workspace

	jobs := drain(t, backend)
	Equals(t, 2, len(jobs))

	var qc queuedCommand
	Ok(t, json.Unmarshal(jobs[0].Payload, &qc))
	Equals(t, *cmd, *qc.Cmd)
	Equals(t, user, qc.User)

	// A draining server leaves the job for the next instance.
	c.Drainer.ShutdownBlocking()
	Equals(t, queue.ErrAbandon, c.HandleQueuedJob(context.Background(), jobs[0]))
}

func TestRunCommentCommand_Queue_FlagsAndAuthorAreDistinct(t *testing.T) {
	c, backend := queueRunner(t)
	repo := models.Repo{FullName: "org/repo"}
	user := models.User{Username: "u"}
	plan := func(u models.User, flags ...string) {
		c.RunCommentCommand(repo, nil, nil, u, 5, &CommentCommand{Name: command.Plan, Workspace: "default", RepoRelDir: ".", Flags: flags})
	}
	plan(user, "-target=a")
	// other flags, other author, then an identical redelivery of the first
	plan(user, "-target=b")
	plan(models.User{Username: "other"}, "-target=a")
	plan(user, "-target=a")
	Equals(t, 3, len(drain(t, backend)))
}

func TestRunAutoplanCommand_Queue(t *testing.T) {
	c, backend := queueRunner(t)
	repo := models.Repo{FullName: "org/repo"}
	pull := func(sha string) models.PullRequest {
		return models.PullRequest{Num: 5, HeadCommit: sha, BaseRepo: repo}
	}

	c.RunAutoplanCommand(repo, repo, pull("aaa"), models.User{})
	c.RunAutoplanCommand(repo, repo, pull("aaa"), models.User{}) // same commit: duplicate
	c.RunAutoplanCommand(repo, repo, pull("bbb"), models.User{}) // new push: must not be dropped
	jobs := drain(t, backend)
	Equals(t, 2, len(jobs))
	Equals(t, "autoplan@aaa", jobs[0].Step)
	Equals(t, "autoplan@bbb", jobs[1].Step)
}

func TestRunCommentCommand_Queue_PayloadHasNoSecrets(t *testing.T) {
	const token = "s3cr3t-token"
	parser := &EventParser{GithubUser: "bot", GithubToken: token}
	repo, err := models.NewRepo(models.Github, "org/repo", "https://github.com/org/repo.git", "bot", token, "")
	Ok(t, err)
	Assert(t, bytes.Contains([]byte(repo.CloneURL), []byte(token)), "fixture repo carries the token")
	pull := models.PullRequest{Num: 5, BaseRepo: repo, HeadCommit: "aaa"}

	c, backend := queueRunner(t)
	c.RunCommentCommand(repo, &repo, &pull, models.User{Username: "u"}, 5, &CommentCommand{Name: command.Plan})
	jobs := drain(t, backend)
	Equals(t, 1, len(jobs))
	Assert(t, !bytes.Contains(jobs[0].Payload, []byte(token)), "payload must not contain the VCS token")

	// The handler restores the credentials from the server's config before running.
	// RepoAuth fails on the third repo so the (unmocked) command body is never reached.
	var restored []models.Repo
	stop := errors.New("stop")
	c.RepoAuth = func(r models.Repo) (models.Repo, error) {
		out, err := parser.RestoreCloneURL(r)
		restored = append(restored, out)
		if len(restored) == 3 {
			return out, stop
		}
		return out, err
	}
	err = c.HandleQueuedJob(context.Background(), jobs[0])
	Assert(t, errors.Is(err, stop), "got %v", err)
	Equals(t, 3, len(restored))
	for _, r := range restored {
		Equals(t, repo.CloneURL, r.CloneURL)
	}
}

func TestRestoreCloneURL(t *testing.T) {
	p := &EventParser{
		GithubUser: "gh", GithubToken: "t1", GitlabUser: "gl", GitlabToken: "t 2", GiteaUser: "gt", GiteaToken: "t3",
		BitbucketUser: "bb", BitbucketToken: "t4", AzureDevopsUser: "ad", AzureDevopsToken: "t5",
	}
	for name, tc := range map[string]struct {
		typ   models.VCSHostType
		name  string
		url   string
		user  string
		token string
	}{
		"github": {typ: models.Github, name: "o/r", url: "https://github.com/o/r.git", user: "gh", token: "t1"},
		"gitlab": {typ: models.Gitlab, name: "o/r", url: "https://gitlab.com/o/r.git", user: "gl", token: "t 2"},
		"gitea":  {typ: models.Gitea, name: "o/r", url: "https://gitea.io/o/r.git", user: "gt", token: "t3"},
		"azure":  {typ: models.AzureDevops, name: "o/p/r", url: "https://dev.azure.com/o/p/_git/r", user: "ad", token: "t5"},
	} {
		t.Run(name, func(t *testing.T) {
			want, err := models.NewRepo(tc.typ, tc.name, tc.url, tc.user, tc.token, "")
			Ok(t, err)
			in := want
			in.CloneURL = ""
			got, err := p.RestoreCloneURL(in)
			Ok(t, err)
			Equals(t, want.CloneURL, got.CloneURL)
		})
	}
}

func TestHandleQueuedJob_BadPayload(t *testing.T) {
	c := &DefaultCommandRunner{Logger: logging.NewNoopLogger(t), Drainer: &Drainer{}}
	err := c.HandleQueuedJob(context.Background(), queue.Job{Payload: []byte("{")})
	Assert(t, err != nil, "undecodable payload must fail (and so end in the DLQ)")
}
