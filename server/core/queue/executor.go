// SPDX-License-Identifier: Apache-2.0

package queue

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"time"

	"github.com/runatlantis/atlantis/server/logging"
)

// Handler executes a job. A nil return acks it, ErrAbandon leaves it for
// redelivery, any other error counts as a failed attempt.
type Handler func(ctx context.Context, job Job) error

// Executor runs a pool of consumers against a Backend.
type Executor struct {
	Backend Backend
	Handler Handler
	Log     logging.SimpleLogging
	// Workers is the consumer pool size (>= 1).
	Workers int
	// Name prefixes consumer names; it must be unique per server instance.
	Name string
	// Poll is the sleep between empty receives (default 200ms).
	Poll time.Duration
	// Heartbeat is how often a running job's lease is extended so it is not
	// taken over while still alive; set it to ClaimIdle/3. Zero disables it.
	Heartbeat time.Duration
	// Paused, when set and true, stops consumers from receiving new jobs (draining).
	Paused func() bool
}

// Submit enqueues a job. It returns false for a duplicate (same idempotency key still pending).
func (e *Executor) Submit(ctx context.Context, job Job) (bool, error) {
	return e.Backend.Enqueue(ctx, job)
}

// Run blocks, consuming until ctx is cancelled.
func (e *Executor) Run(ctx context.Context) {
	var wg sync.WaitGroup
	for i := range max(e.Workers, 1) {
		wg.Go(func() { e.loop(ctx, fmt.Sprintf("%s-%d", e.Name, i)) })
	}
	wg.Wait()
}

func (e *Executor) loop(ctx context.Context, consumer string) {
	poll := e.Poll
	if poll <= 0 {
		poll = 200 * time.Millisecond
	}
	for ctx.Err() == nil {
		var job *Job
		if e.Paused == nil || !e.Paused() {
			var err error
			job, err = e.Backend.Receive(ctx, consumer)
			if err != nil && ctx.Err() == nil {
				e.Log.Err("receiving queued job: %s", err)
			}
		}
		if job == nil {
			select {
			case <-ctx.Done():
			case <-time.After(poll):
			}
			continue
		}
		e.settle(ctx, *job, e.safeHandle(ctx, *job))
	}
}

func (e *Executor) safeHandle(ctx context.Context, job Job) (err error) {
	if e.Heartbeat > 0 {
		stop := make(chan struct{})
		defer close(stop)
		go func() {
			t := time.NewTicker(e.Heartbeat)
			defer t.Stop()
			for {
				select {
				case <-stop:
					return
				case <-t.C:
					if err := e.Backend.Extend(context.WithoutCancel(ctx), job); err != nil {
						e.Log.Warn("extending lease of queued job %q: %s", job.Key(), err)
					}
				}
			}
		}()
	}
	defer func() {
		if r := recover(); r != nil {
			err = fmt.Errorf("panic: %v", r)
		}
	}()
	return e.Handler(ctx, job)
}

func (e *Executor) settle(ctx context.Context, job Job, herr error) {
	// Settle with a fresh context so a shutdown does not strand a finished job unacked.
	sctx := context.WithoutCancel(ctx)
	switch {
	case herr == nil:
		if err := e.Backend.Ack(sctx, job); err != nil {
			e.Log.Err("acking queued job %q: %s", job.Key(), err)
		}
	case errors.Is(herr, ErrAbandon):
	default:
		dead, err := e.Backend.Retry(sctx, job, herr)
		if err != nil {
			e.Log.Err("retrying queued job %q: %s", job.Key(), err)
		} else if dead {
			e.Log.Err("queued job %q moved to dlq after %d attempts: %s", job.Key(), job.Attempt, herr)
		}
	}
}
