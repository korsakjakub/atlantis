// SPDX-License-Identifier: Apache-2.0

package queue

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/redis/go-redis/v9"
)

const (
	redisGroup = "atlantis"
	// dedupTTL bounds how long a key can stay reserved if a job is lost
	// outside the queue's control (e.g. stream deleted by an operator).
	dedupTTL = 24 * time.Hour
)

// RedisStreams is a Backend on Redis Streams: XADD/XREADGROUP/XACK, XAUTOCLAIM
// for takeover of jobs from a dead consumer, a separate stream as DLQ and a
// SET NX key for idempotency.
type RedisStreams struct {
	client redis.Cmdable
	opts   Options
	prefix string
}

// NewRedisStreams builds the backend on an existing client. prefix namespaces all keys.
func NewRedisStreams(client redis.Cmdable, prefix string, opts Options) *RedisStreams {
	return &RedisStreams{client: client, opts: opts.withDefaults(), prefix: prefix}
}

func (r *RedisStreams) stream() string { return r.prefix + "stream" }
func (r *RedisStreams) dlq() string    { return r.prefix + "dlq" }
func (r *RedisStreams) dedup(j Job) string {
	return r.prefix + "dedup:" + j.Key()
}

func (r *RedisStreams) ensureGroup(ctx context.Context) error {
	err := r.client.XGroupCreateMkStream(ctx, r.stream(), redisGroup, "0").Err()
	if err != nil && !strings.HasPrefix(err.Error(), "BUSYGROUP") {
		return fmt.Errorf("creating consumer group: %w", err)
	}
	return nil
}

func encode(j Job) (map[string]any, error) {
	b, err := json.Marshal(j)
	if err != nil {
		return nil, fmt.Errorf("encoding job: %w", err)
	}
	return map[string]any{"job": string(b)}, nil
}

func decode(m redis.XMessage) (*Job, error) {
	s, _ := m.Values["job"].(string)
	var j Job
	if err := json.Unmarshal([]byte(s), &j); err != nil {
		return nil, fmt.Errorf("decoding job %s: %w", m.ID, err)
	}
	j.ID = m.ID
	return &j, nil
}

func (r *RedisStreams) Enqueue(ctx context.Context, job Job) (bool, error) {
	job.Attempt = 1
	values, err := encode(job)
	if err != nil {
		return false, err
	}
	ok, err := r.client.SetNX(ctx, r.dedup(job), 1, dedupTTL).Result()
	if err != nil {
		return false, fmt.Errorf("reserving idempotency key: %w", err)
	}
	if !ok {
		return false, nil
	}
	if err := r.client.XAdd(ctx, &redis.XAddArgs{Stream: r.stream(), Values: values}).Err(); err != nil {
		r.client.Del(ctx, r.dedup(job))
		return false, fmt.Errorf("adding job to stream: %w", err)
	}
	return true, nil
}

func (r *RedisStreams) Receive(ctx context.Context, consumer string) (*Job, error) {
	if err := r.ensureGroup(ctx); err != nil {
		return nil, err
	}
	// Take over a job abandoned by a dead consumer first.
	msgs, _, err := r.client.XAutoClaim(ctx, &redis.XAutoClaimArgs{
		Stream: r.stream(), Group: redisGroup, Consumer: consumer,
		MinIdle: r.opts.ClaimIdle, Start: "0-0", Count: 1,
	}).Result()
	if err != nil {
		return nil, fmt.Errorf("claiming idle jobs: %w", err)
	}
	if len(msgs) == 0 {
		var res []redis.XStream
		res, err = r.client.XReadGroup(ctx, &redis.XReadGroupArgs{
			Group: redisGroup, Consumer: consumer, Streams: []string{r.stream(), ">"}, Count: 1, Block: -1,
		}).Result()
		if errors.Is(err, redis.Nil) {
			return nil, nil
		}
		if err != nil {
			return nil, fmt.Errorf("reading stream: %w", err)
		}
		if len(res) == 0 || len(res[0].Messages) == 0 {
			return nil, nil
		}
		msgs = res[0].Messages
	}
	return decode(msgs[0])
}

func (r *RedisStreams) Ack(ctx context.Context, job Job) error {
	pipe := r.client.TxPipeline()
	pipe.XAck(ctx, r.stream(), redisGroup, job.ID)
	pipe.XDel(ctx, r.stream(), job.ID)
	pipe.Del(ctx, r.dedup(job))
	if _, err := pipe.Exec(ctx); err != nil {
		return fmt.Errorf("acking job: %w", err)
	}
	return nil
}

func (r *RedisStreams) Retry(ctx context.Context, job Job, cause error) (bool, error) {
	dead := job.Attempt >= r.opts.MaxAttempts
	next := job
	if dead {
		if cause != nil {
			next.Err = cause.Error()
		}
	} else {
		next.Attempt++
	}
	values, err := encode(next)
	if err != nil {
		return false, err
	}
	target := r.stream()
	if dead {
		target = r.dlq()
	}
	pipe := r.client.TxPipeline()
	pipe.XAdd(ctx, &redis.XAddArgs{Stream: target, Values: values})
	pipe.XAck(ctx, r.stream(), redisGroup, job.ID)
	pipe.XDel(ctx, r.stream(), job.ID)
	if dead {
		pipe.Del(ctx, r.dedup(job))
	}
	if _, err := pipe.Exec(ctx); err != nil {
		return false, fmt.Errorf("retrying job: %w", err)
	}
	return dead, nil
}

func (r *RedisStreams) DLQ(ctx context.Context) ([]Job, error) {
	msgs, err := r.client.XRange(ctx, r.dlq(), "-", "+").Result()
	if err != nil {
		return nil, fmt.Errorf("reading dlq: %w", err)
	}
	var out []Job
	for _, m := range msgs {
		j, err := decode(m)
		if err != nil {
			return nil, err
		}
		out = append(out, *j)
	}
	return out, nil
}
