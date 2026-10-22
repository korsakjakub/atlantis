// SPDX-License-Identifier: Apache-2.0

package queue

import (
	"fmt"

	"github.com/redis/go-redis/v9"
)

const (
	// BackendInProcess is the default backend name.
	BackendInProcess = "in-process"
	// BackendRedisStreams is the Redis Streams backend name.
	BackendRedisStreams = "redis-streams"
)

// Validate checks the backend name and its required settings. A missing Redis
// URL is an error, never a silent fallback to in-process.
func Validate(backend, redisURL string) error {
	switch backend {
	case "", BackendInProcess:
		return nil
	case BackendRedisStreams:
		if redisURL == "" {
			return fmt.Errorf("queue backend %q requires a redis url", backend)
		}
		if _, err := redis.ParseURL(redisURL); err != nil {
			return fmt.Errorf("parsing queue redis url: %w", err)
		}
		return nil
	}
	return fmt.Errorf("queue backend must be one of %s or %s, got %q", BackendInProcess, BackendRedisStreams, backend)
}

// Build returns the configured backend.
func Build(backend, redisURL, keyPrefix string, opts Options) (Backend, error) {
	if err := Validate(backend, redisURL); err != nil {
		return nil, err
	}
	if backend == BackendRedisStreams {
		ro, _ := redis.ParseURL(redisURL) // validated above
		return NewRedisStreams(redis.NewClient(ro), keyPrefix, opts), nil
	}
	return NewMemory(opts), nil
}
