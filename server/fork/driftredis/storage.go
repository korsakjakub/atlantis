// SPDX-License-Identifier: Apache-2.0

// Package driftredis is a Redis-backed implementation of drift.Storage so
// drift results survive server restarts. Fork-only; enabled with
// ATLANTIS_FORK_DRIFT_STORAGE=redis.
package driftredis

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"github.com/redis/go-redis/v9"
	"github.com/runatlantis/atlantis/server/core/drift"
	"github.com/runatlantis/atlantis/server/events/models"
)

// DefaultKeyPrefix is used when Options.KeyPrefix is empty.
const DefaultKeyPrefix = "atlantis:fork:drift:"

const opTimeout = 5 * time.Second

// Options configures RedisStorage.
type Options struct {
	// KeyPrefix is prepended to every key. Defaults to DefaultKeyPrefix.
	KeyPrefix string
	// Retention is the TTL applied to each record. Zero disables expiry.
	Retention time.Duration
}

// RedisStorage stores drift records in Redis.
//
// Layout (P = key prefix):
//
//	P+"rec:"+sha256(repo, identity) -> JSON envelope, TTL = retention
//	P+"idx:"+repo                   -> SET of record keys for the repo
//	P+"repos"                       -> SET of repositories with an index
//
// Only single-key commands are issued per key so the layout works on Redis
// Cluster. Index members whose record has expired are pruned lazily on read.
type RedisStorage struct {
	client    redis.Cmdable
	prefix    string
	retention time.Duration
}

var (
	_ drift.Storage       = (*RedisStorage)(nil)
	_ drift.HealthChecker = (*RedisStorage)(nil)
)

type envelope struct {
	Repository string              `json:"repository"`
	Drift      models.ProjectDrift `json:"drift"`
}

type identity struct {
	ProjectName string
	Path        string
	Workspace   string
	Ref         string
	BaseBranch  string
}

// NewStorage creates a RedisStorage on top of an existing client.
func NewStorage(client redis.Cmdable, opts Options) *RedisStorage {
	if opts.KeyPrefix == "" {
		opts.KeyPrefix = DefaultKeyPrefix
	}
	return &RedisStorage{client: client, prefix: opts.KeyPrefix, retention: opts.Retention}
}

func (s *RedisStorage) recKey(repository string, d models.ProjectDrift) (string, error) {
	id, err := json.Marshal(identity{d.ProjectName, d.Path, d.Workspace, d.Ref, d.BaseBranch})
	if err != nil {
		return "", fmt.Errorf("encoding drift identity: %w", err)
	}
	sum := sha256.Sum256(append([]byte(repository+"\x00"), id...))
	return s.prefix + "rec:" + hex.EncodeToString(sum[:]), nil
}

func (s *RedisStorage) idxKey(repository string) string { return s.prefix + "idx:" + repository }
func (s *RedisStorage) reposKey() string                { return s.prefix + "repos" }

func unavailable(op string, err error) error {
	return fmt.Errorf("%s: %w: %w", op, drift.ErrStorageUnavailable, err)
}

func opCtx() (context.Context, context.CancelFunc) {
	return context.WithTimeout(context.Background(), opTimeout)
}

// Healthy pings the backend.
func (s *RedisStorage) Healthy() error {
	ctx, cancel := opCtx()
	defer cancel()
	if err := s.client.Ping(ctx).Err(); err != nil {
		return unavailable("pinging redis", err)
	}
	return nil
}

// Store saves a drift result, stripping PlanOutput.
func (s *RedisStorage) Store(repository string, d models.ProjectDrift) error {
	d.PlanOutput = ""
	key, err := s.recKey(repository, d)
	if err != nil {
		return err
	}
	data, err := json.Marshal(envelope{Repository: repository, Drift: d})
	if err != nil {
		return fmt.Errorf("encoding drift record: %w", err)
	}

	ctx, cancel := opCtx()
	defer cancel()
	_, err = s.client.Pipelined(ctx, func(p redis.Pipeliner) error {
		p.Set(ctx, key, data, s.retention)
		p.SAdd(ctx, s.idxKey(repository), key)
		if s.retention > 0 {
			p.Expire(ctx, s.idxKey(repository), s.retention)
		}
		p.SAdd(ctx, s.reposKey(), repository)
		return nil
	})
	if err != nil {
		return unavailable("storing drift record", err)
	}
	return nil
}

type loaded struct {
	key string
	env envelope
}

// load returns all live records of a repository, pruning expired index members.
func (s *RedisStorage) load(repository string) ([]loaded, error) {
	ctx, cancel := opCtx()
	defer cancel()

	keys, err := s.client.SMembers(ctx, s.idxKey(repository)).Result()
	if err != nil {
		return nil, unavailable("listing drift index", err)
	}
	if len(keys) == 0 {
		return nil, nil
	}
	cmds := make([]*redis.StringCmd, len(keys))
	_, err = s.client.Pipelined(ctx, func(p redis.Pipeliner) error {
		for i, k := range keys {
			cmds[i] = p.Get(ctx, k)
		}
		return nil
	})
	if err != nil && !errors.Is(err, redis.Nil) {
		return nil, unavailable("reading drift records", err)
	}

	var out []loaded
	var stale []any
	for i, cmd := range cmds {
		raw, err := cmd.Bytes()
		if errors.Is(err, redis.Nil) {
			stale = append(stale, keys[i])
			continue
		}
		if err != nil {
			return nil, unavailable("reading drift record", err)
		}
		var env envelope
		if err := json.Unmarshal(raw, &env); err != nil {
			return nil, fmt.Errorf("decoding drift record %q: %w", keys[i], err)
		}
		out = append(out, loaded{key: keys[i], env: env})
	}
	if len(stale) > 0 {
		// Best effort; a failure only delays pruning.
		s.client.SRem(ctx, s.idxKey(repository), stale...)
	}
	return out, nil
}

// Get retrieves drift results for a repository with optional filtering.
func (s *RedisStorage) Get(repository string, opts drift.GetOptions) ([]models.ProjectDrift, error) {
	recs, err := s.load(repository)
	if err != nil {
		return nil, err
	}
	now := time.Now()
	result := make([]models.ProjectDrift, 0, len(recs))
	for _, r := range recs {
		if drift.MatchesGet(r.env.Drift, opts, now) {
			result = append(result, r.env.Drift)
		}
	}
	return result, nil
}

// Delete removes drift results for a repository, or for one project when
// projectName is set.
func (s *RedisStorage) Delete(repository string, projectName string) error {
	return s.deleteWhere(repository, func(d models.ProjectDrift) bool {
		return projectName == "" || d.ProjectName == projectName
	})
}

// DeleteMatching removes drift results that match the filters.
func (s *RedisStorage) DeleteMatching(repository string, opts drift.GetOptions) error {
	if opts == (drift.GetOptions{}) {
		return fmt.Errorf("at least one drift delete filter is required")
	}
	now := time.Now()
	return s.deleteWhere(repository, func(d models.ProjectDrift) bool {
		return drift.MatchesDelete(d, opts, now)
	})
}

func (s *RedisStorage) deleteWhere(repository string, match func(models.ProjectDrift) bool) error {
	recs, err := s.load(repository)
	if err != nil {
		return err
	}
	var keys []string
	for _, r := range recs {
		if match(r.env.Drift) {
			keys = append(keys, r.key)
		}
	}
	if len(keys) == 0 {
		return nil
	}

	ctx, cancel := opCtx()
	defer cancel()
	members := make([]any, len(keys))
	for i, k := range keys {
		members[i] = k
	}
	_, err = s.client.Pipelined(ctx, func(p redis.Pipeliner) error {
		for _, k := range keys {
			p.Del(ctx, k)
		}
		p.SRem(ctx, s.idxKey(repository), members...)
		return nil
	})
	if err != nil {
		return unavailable("deleting drift records", err)
	}
	if len(keys) == len(recs) {
		// Repository is now empty; drop it from the repos set. A concurrent
		// Store re-adds it, and GetAll tolerates empty repos either way.
		if n, err := s.client.SCard(ctx, s.idxKey(repository)).Result(); err == nil && n == 0 {
			s.client.SRem(ctx, s.reposKey(), repository)
		}
	}
	return nil
}

// GetAll retrieves all stored drift results across repositories.
func (s *RedisStorage) GetAll() (map[string][]models.ProjectDrift, error) {
	ctx, cancel := opCtx()
	repos, err := s.client.SMembers(ctx, s.reposKey()).Result()
	cancel()
	if err != nil {
		return nil, unavailable("listing drift repositories", err)
	}
	result := make(map[string][]models.ProjectDrift)
	for _, repo := range repos {
		recs, err := s.load(repo)
		if err != nil {
			return nil, err
		}
		if len(recs) == 0 {
			continue
		}
		drifts := make([]models.ProjectDrift, 0, len(recs))
		for _, r := range recs {
			drifts = append(drifts, r.env.Drift)
		}
		result[repo] = drifts
	}
	return result, nil
}
