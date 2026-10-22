// SPDX-License-Identifier: Apache-2.0

// Package queue decouples enqueueing a command from executing it (fork US-040).
//
// Delivery is at-least-once: a job that is received but neither acked nor
// retried becomes visible again after Options.ClaimIdle, so handlers must be
// idempotent (the project/workspace lock covers the Terraform side).
package queue

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"
)

const (
	// DefaultMaxAttempts is the number of deliveries before a job goes to the DLQ.
	DefaultMaxAttempts = 3
	// DefaultClaimIdle is how long a received job may stay unacked before
	// another consumer may take it over.
	DefaultClaimIdle = 5 * time.Minute
)

// ErrAbandon may be returned by a Handler to leave the job unacked (no retry
// counted); it is redelivered after ClaimIdle. Used when the server is draining.
var ErrAbandon = errors.New("abandon job")

// Job is one unit of work. Repo+Pull+Command+Step form the idempotency key.
type Job struct {
	// ID is the backend delivery id, set by Receive.
	ID      string `json:"-"`
	Repo    string `json:"repo"`
	Pull    int    `json:"pull"`
	Command string `json:"command"`
	Step    string `json:"step"`
	Payload []byte `json:"payload,omitempty"`
	// Attempt is 1 for the first delivery.
	Attempt int `json:"attempt"`
	// Err is the last failure; only set on DLQ entries.
	Err string `json:"err,omitempty"`
}

// Key is the idempotency key. While a job with the same key is queued or in
// flight, Enqueue of another one is rejected as a duplicate.
func (j Job) Key() string {
	return fmt.Sprintf("%s#%d#%s#%s", strings.ToLower(j.Repo), j.Pull, j.Command, j.Step)
}

// Backend is the queue contract. Implementations must be safe for concurrent use.
type Backend interface {
	// Enqueue adds the job. It returns false (and no error) for a duplicate key.
	Enqueue(ctx context.Context, job Job) (bool, error)
	// Receive returns the next job for the consumer, first taking over jobs
	// whose previous consumer went idle. It returns nil when nothing is ready.
	Receive(ctx context.Context, consumer string) (*Job, error)
	// Ack marks the job done and releases its idempotency key.
	Ack(ctx context.Context, job Job) error
	// Retry requeues the job with Attempt+1, or moves it to the DLQ (reporting
	// true) when Attempt has reached MaxAttempts.
	Retry(ctx context.Context, job Job, cause error) (deadLettered bool, err error)
	// DLQ lists dead-lettered jobs.
	DLQ(ctx context.Context) ([]Job, error)
}

// Options tunes a backend. Zero values mean the defaults above.
type Options struct {
	MaxAttempts int
	ClaimIdle   time.Duration
}

func (o Options) withDefaults() Options {
	if o.MaxAttempts <= 0 {
		o.MaxAttempts = DefaultMaxAttempts
	}
	if o.ClaimIdle <= 0 {
		o.ClaimIdle = DefaultClaimIdle
	}
	return o
}
