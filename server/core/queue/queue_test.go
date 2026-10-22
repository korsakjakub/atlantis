// SPDX-License-Identifier: Apache-2.0

package queue_test

import (
	"context"
	"errors"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/alicebob/miniredis/v2"
	"github.com/redis/go-redis/v9"
	"github.com/runatlantis/atlantis/server/core/queue"
	"github.com/runatlantis/atlantis/server/core/queue/queuetest"
	"github.com/runatlantis/atlantis/server/logging"
	. "github.com/runatlantis/atlantis/testing"
)

var opts = queue.Options{MaxAttempts: queuetest.MaxAttempts, ClaimIdle: queuetest.ClaimIdle}

func memoryFactory(*testing.T) queue.Backend { return queue.NewMemory(opts) }

// miniredis is a full in-process Redis implementation incl. streams; it stands in
// for a real server (see docs/adr/0010-fork-queue-backend.md for what that proves).
func redisFactory(t *testing.T) queue.Backend {
	mr := miniredis.RunT(t)
	return queue.NewRedisStreams(redis.NewClient(&redis.Options{Addr: mr.Addr()}), "t:", opts)
}

func TestMemory_Conformance(t *testing.T)       { queuetest.Run(t, memoryFactory) }
func TestRedisStreams_Conformance(t *testing.T) { queuetest.Run(t, redisFactory) }

func TestValidate(t *testing.T) {
	Ok(t, queue.Validate("", ""))
	Ok(t, queue.Validate("in-process", ""))
	Ok(t, queue.Validate("redis-streams", "redis://localhost:6379/0"))
	Assert(t, queue.Validate("redis-streams", "") != nil, "missing url must fail, not fall back")
	Assert(t, queue.Validate("redis-streams", "http://x") != nil, "bad url scheme")
	Assert(t, queue.Validate("nats", "") != nil, "unknown backend")
}

// fakeLocker models the project lock: the same owner may re-enter, others are refused.
type fakeLocker struct {
	mu      sync.Mutex
	owner   string
	refused int
}

func (l *fakeLocker) lock(owner string) bool {
	l.mu.Lock()
	defer l.mu.Unlock()
	if l.owner != "" && l.owner != owner {
		l.refused++
		return false
	}
	l.owner = owner
	return true
}

func (l *fakeLocker) unlock() {
	l.mu.Lock()
	defer l.mu.Unlock()
	l.owner = ""
}

// A consumer dies in the middle of a job; another instance takes the job over
// after ClaimIdle. The job completes exactly once, a duplicate webhook delivery
// in between is deduped, and the lock is never broken.
func TestExecutor_ConsumerRestartMidJob(t *testing.T) {
	for name, f := range map[string]queuetest.Factory{"in-process": memoryFactory, "redis-streams": redisFactory} {
		t.Run(name, func(t *testing.T) {
			b := f(t)
			ctx := context.Background()
			locker := &fakeLocker{}
			var started, completed atomic.Int32

			job := queue.Job{Repo: "org/repo", Pull: 7, Command: "apply", Step: "default:."}
			ok, err := b.Enqueue(ctx, job)
			Ok(t, err)
			Assert(t, ok, "enqueued")

			// Consumer A: takes the lock, starts the job and "crashes" (never acks).
			crashed, err := b.Receive(ctx, "A")
			Ok(t, err)
			Assert(t, crashed != nil, "A received the job")
			started.Add(1)
			Assert(t, locker.lock("org/repo#7"), "A took the lock")

			// Duplicate delivery of the same webhook while the job is in flight.
			ok, err = b.Enqueue(ctx, job)
			Ok(t, err)
			Assert(t, !ok, "duplicate deduped")

			// Another pull must not break the lock held by the dead consumer's job.
			Assert(t, !locker.lock("org/repo#8"), "foreign owner refused")

			time.Sleep(queuetest.ClaimIdle + 40*time.Millisecond)

			// Consumer B (restarted instance) reclaims and finishes the job.
			cctx, cancel := context.WithCancel(ctx)
			done := make(chan struct{})
			ex := &queue.Executor{
				Backend: b, Log: logging.NewNoopLogger(t), Name: "B", Workers: 1, Poll: 10 * time.Millisecond,
				Handler: func(context.Context, queue.Job) error {
					started.Add(1)
					if !locker.lock("org/repo#7") { // same owner re-enters
						return errors.New("lock held by someone else")
					}
					defer locker.unlock()
					completed.Add(1)
					close(done)
					return nil
				},
			}
			go ex.Run(cctx)
			select {
			case <-done:
			case <-time.After(3 * time.Second):
				t.Fatal("job was not redelivered")
			}
			time.Sleep(3 * ex.Poll) // would expose a second execution
			cancel()

			Equals(t, int32(1), completed.Load())
			Equals(t, int32(2), started.Load()) // crashed attempt + the completed one
			Equals(t, 1, locker.refused)
			dlq, _ := b.DLQ(ctx)
			Equals(t, 0, len(dlq))
		})
	}
}

func TestExecutor_FailingJobGoesToDLQ(t *testing.T) {
	b := queue.NewMemory(opts)
	var calls atomic.Int32
	ex := &queue.Executor{
		Backend: b, Log: logging.NewNoopLogger(t), Name: "w", Workers: 2, Poll: 5 * time.Millisecond,
		Handler: func(context.Context, queue.Job) error {
			if calls.Add(1) == 1 {
				panic("kaboom") // a panicking handler is just a failed attempt
			}
			return errors.New("nope")
		},
	}
	_, _ = ex.Submit(context.Background(), queue.Job{Repo: "r", Pull: 1, Command: "plan"})
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go ex.Run(ctx)
	Assert(t, eventually(func() bool { d, _ := b.DLQ(ctx); return len(d) == 1 }), "job dead-lettered")
	Equals(t, int32(queuetest.MaxAttempts), calls.Load())
}

func eventually(f func() bool) bool {
	for range 300 {
		if f() {
			return true
		}
		time.Sleep(10 * time.Millisecond)
	}
	return false
}
