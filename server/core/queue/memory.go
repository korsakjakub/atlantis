// SPDX-License-Identifier: Apache-2.0

package queue

import (
	"context"
	"strconv"
	"sync"
	"time"
)

// Memory is the in-process Backend: the default, with no external dependency.
type Memory struct {
	opts Options

	mu       sync.Mutex
	seq      int
	ready    []Job
	inflight map[string]*claim
	keys     map[string]struct{}
	dlq      []Job
}

type claim struct {
	job Job
	at  time.Time
}

// NewMemory returns an in-process backend.
func NewMemory(opts Options) *Memory {
	return &Memory{opts: opts.withDefaults(), inflight: map[string]*claim{}, keys: map[string]struct{}{}}
}

func (m *Memory) Enqueue(_ context.Context, job Job) (bool, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if _, dup := m.keys[job.Key()]; dup {
		return false, nil
	}
	m.keys[job.Key()] = struct{}{}
	job.Attempt = 1
	m.ready = append(m.ready, job)
	return true, nil
}

func (m *Memory) Receive(_ context.Context, consumer string) (*Job, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	now := time.Now()
	for _, c := range m.inflight {
		if now.Sub(c.at) >= m.opts.ClaimIdle {
			c.at = now
			c.job.Consumer = consumer
			j := c.job
			return &j, nil
		}
	}
	if len(m.ready) == 0 {
		return nil, nil
	}
	j := m.ready[0]
	m.ready = m.ready[1:]
	m.seq++
	j.ID = strconv.Itoa(m.seq)
	j.Consumer = consumer
	m.inflight[j.ID] = &claim{job: j, at: now}
	return &j, nil
}

func (m *Memory) Extend(_ context.Context, job Job) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	if c, ok := m.inflight[job.ID]; ok {
		c.at = time.Now()
	}
	return nil
}

func (m *Memory) Ack(_ context.Context, job Job) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	if _, ok := m.inflight[job.ID]; ok {
		delete(m.inflight, job.ID)
		delete(m.keys, job.Key())
	}
	return nil
}

func (m *Memory) Retry(_ context.Context, job Job, cause error) (bool, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if _, ok := m.inflight[job.ID]; !ok {
		return false, nil // already settled by another consumer
	}
	delete(m.inflight, job.ID)
	job.ID = ""
	if job.Attempt >= m.opts.MaxAttempts {
		if cause != nil {
			job.Err = cause.Error()
		}
		m.dlq = append(m.dlq, job)
		delete(m.keys, job.Key())
		return true, nil
	}
	job.Attempt++
	m.ready = append(m.ready, job)
	return false, nil
}

func (m *Memory) DLQ(context.Context) ([]Job, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	return append([]Job(nil), m.dlq...), nil
}
