// SPDX-License-Identifier: Apache-2.0

// Package queuetest is the conformance suite every queue.Backend must pass.
package queuetest

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/runatlantis/atlantis/server/core/queue"
	. "github.com/runatlantis/atlantis/testing"
)

// ClaimIdle and MaxAttempts are what Factory must configure the backend with.
const (
	ClaimIdle   = 80 * time.Millisecond
	MaxAttempts = 3
)

// Factory returns a fresh, empty backend configured with ClaimIdle and MaxAttempts.
type Factory func(t *testing.T) queue.Backend

func job(step string) queue.Job {
	return queue.Job{Repo: "org/repo", Pull: 1, Command: "plan", Step: step}
}

func mustReceive(t *testing.T, b queue.Backend, consumer string) queue.Job {
	t.Helper()
	j, err := b.Receive(context.Background(), consumer)
	Ok(t, err)
	Assert(t, j != nil, "expected a job")
	return *j
}

// Run executes the suite.
func Run(t *testing.T, f Factory) {
	ctx := context.Background()

	t.Run("enqueue then receive then ack", func(t *testing.T) {
		b := f(t)
		ok, err := b.Enqueue(ctx, job("a"))
		Ok(t, err)
		Assert(t, ok, "first enqueue accepted")
		j := mustReceive(t, b, "c1")
		Equals(t, 1, j.Attempt)
		Equals(t, "a", j.Step)
		Ok(t, b.Ack(ctx, j))
		got, err := b.Receive(ctx, "c1")
		Ok(t, err)
		Assert(t, got == nil, "queue drained")
	})

	t.Run("dedup on repo+pull+command+step", func(t *testing.T) {
		b := f(t)
		ok, _ := b.Enqueue(ctx, job("a"))
		Assert(t, ok, "first accepted")
		dup := job("a")
		dup.Repo = "ORG/Repo" // repo is case-insensitive
		dup.Payload = []byte("other")
		ok, err := b.Enqueue(ctx, dup)
		Ok(t, err)
		Assert(t, !ok, "same key rejected")
		for name, mutate := range map[string]func(*queue.Job){
			"repo":    func(j *queue.Job) { j.Repo = "org/other" },
			"pull":    func(j *queue.Job) { j.Pull = 2 },
			"command": func(j *queue.Job) { j.Command = "apply" },
			"step":    func(j *queue.Job) { j.Step = "b" },
		} {
			j := job("a")
			mutate(&j)
			ok, err := b.Enqueue(ctx, j)
			Ok(t, err)
			Assert(t, ok, "different %s accepted", name)
		}
	})

	t.Run("key is held while in flight and released by ack", func(t *testing.T) {
		b := f(t)
		_, _ = b.Enqueue(ctx, job("a"))
		j := mustReceive(t, b, "c1")
		ok, _ := b.Enqueue(ctx, job("a"))
		Assert(t, !ok, "duplicate rejected while in flight")
		Ok(t, b.Ack(ctx, j))
		ok, _ = b.Enqueue(ctx, job("a"))
		Assert(t, ok, "accepted again after ack")
	})

	t.Run("retry until max attempts then dlq", func(t *testing.T) {
		b := f(t)
		_, _ = b.Enqueue(ctx, job("a"))
		for attempt := 1; attempt <= MaxAttempts; attempt++ {
			j := mustReceive(t, b, "c1")
			Equals(t, attempt, j.Attempt)
			dead, err := b.Retry(ctx, j, errors.New("boom"))
			Ok(t, err)
			Equals(t, attempt == MaxAttempts, dead)
		}
		got, err := b.Receive(ctx, "c1")
		Ok(t, err)
		Assert(t, got == nil, "nothing left to deliver")
		dlq, err := b.DLQ(ctx)
		Ok(t, err)
		Equals(t, 1, len(dlq))
		Equals(t, "boom", dlq[0].Err)
		Equals(t, MaxAttempts, dlq[0].Attempt)
		ok, _ := b.Enqueue(ctx, job("a"))
		Assert(t, ok, "key released after dead-lettering")
	})

	t.Run("idle job is taken over by another consumer", func(t *testing.T) {
		b := f(t)
		_, _ = b.Enqueue(ctx, job("a"))
		first := mustReceive(t, b, "dead-consumer")
		got, err := b.Receive(ctx, "c2")
		Ok(t, err)
		Assert(t, got == nil, "fresh claim is not stolen")
		time.Sleep(ClaimIdle + 40*time.Millisecond)
		second := mustReceive(t, b, "c2")
		Equals(t, first.Step, second.Step)
		Ok(t, b.Ack(ctx, second))
		got, _ = b.Receive(ctx, "c2")
		Assert(t, got == nil, "exactly one redelivery")
	})
}
