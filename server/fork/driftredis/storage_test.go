// SPDX-License-Identifier: Apache-2.0

package driftredis_test

import (
	"errors"
	"testing"
	"time"

	"github.com/alicebob/miniredis/v2"
	"github.com/redis/go-redis/v9"
	"github.com/runatlantis/atlantis/server/core/drift"
	"github.com/runatlantis/atlantis/server/core/drift/storagetest"
	"github.com/runatlantis/atlantis/server/events/models"
	"github.com/runatlantis/atlantis/server/fork/driftredis"
)

func newRedis(t *testing.T, opts driftredis.Options) (*driftredis.RedisStorage, *miniredis.Miniredis) {
	t.Helper()
	mr := miniredis.RunT(t)
	client := redis.NewClient(&redis.Options{Addr: mr.Addr()})
	t.Cleanup(func() { _ = client.Close() })
	return driftredis.NewStorage(client, opts), mr
}

func TestRedisStorage_Conformance(t *testing.T) {
	storagetest.Run(t, func(t *testing.T) drift.Storage {
		s, _ := newRedis(t, driftredis.Options{})
		return s
	})
}

func TestRedisStorage_SurvivesRestart(t *testing.T) {
	mr := miniredis.RunT(t)
	first := redis.NewClient(&redis.Options{Addr: mr.Addr()})
	s1 := driftredis.NewStorage(first, driftredis.Options{KeyPrefix: "t:"})
	if err := s1.Store("o/r", models.ProjectDrift{ProjectName: "a", Ref: "main", LastChecked: time.Now()}); err != nil {
		t.Fatal(err)
	}
	_ = first.Close()

	second := redis.NewClient(&redis.Options{Addr: mr.Addr()})
	defer second.Close()
	s2 := driftredis.NewStorage(second, driftredis.Options{KeyPrefix: "t:"})
	got, err := s2.Get("o/r", drift.GetOptions{})
	if err != nil || len(got) != 1 || got[0].ProjectName != "a" {
		t.Fatalf("record did not survive restart: %v %v", got, err)
	}
}

func TestRedisStorage_PrefixIsolation(t *testing.T) {
	mr := miniredis.RunT(t)
	client := redis.NewClient(&redis.Options{Addr: mr.Addr()})
	defer client.Close()
	a := driftredis.NewStorage(client, driftredis.Options{KeyPrefix: "a:"})
	b := driftredis.NewStorage(client, driftredis.Options{KeyPrefix: "b:"})
	if err := a.Store("o/r", models.ProjectDrift{ProjectName: "x", Ref: "main"}); err != nil {
		t.Fatal(err)
	}
	got, err := b.Get("o/r", drift.GetOptions{})
	if err != nil || len(got) != 0 {
		t.Fatalf("prefix leaked: %v %v", got, err)
	}
}

func TestRedisStorage_RetentionExpiresRecords(t *testing.T) {
	s, mr := newRedis(t, driftredis.Options{Retention: time.Hour})
	if err := s.Store("o/r", models.ProjectDrift{ProjectName: "a", Ref: "main"}); err != nil {
		t.Fatal(err)
	}
	mr.FastForward(2 * time.Hour)
	got, err := s.Get("o/r", drift.GetOptions{})
	if err != nil || len(got) != 0 {
		t.Fatalf("expired record returned: %v %v", got, err)
	}
	all, err := s.GetAll()
	if err != nil || len(all) != 0 {
		t.Fatalf("expired repo listed: %v %v", all, err)
	}
}

func TestRedisStorage_FailsClosedWhenBackendDown(t *testing.T) {
	s, mr := newRedis(t, driftredis.Options{})
	if err := s.Healthy(); err != nil {
		t.Fatalf("healthy backend reported error: %v", err)
	}
	mr.Close()

	if err := s.Healthy(); !errors.Is(err, drift.ErrStorageUnavailable) {
		t.Fatalf("Healthy: want ErrStorageUnavailable, got %v", err)
	}
	if _, err := s.Get("o/r", drift.GetOptions{}); !errors.Is(err, drift.ErrStorageUnavailable) {
		t.Fatalf("Get: want ErrStorageUnavailable, got %v", err)
	}
	if err := s.Store("o/r", models.ProjectDrift{ProjectName: "a"}); !errors.Is(err, drift.ErrStorageUnavailable) {
		t.Fatalf("Store: want ErrStorageUnavailable, got %v", err)
	}
	if err := s.Delete("o/r", ""); !errors.Is(err, drift.ErrStorageUnavailable) {
		t.Fatalf("Delete: want ErrStorageUnavailable, got %v", err)
	}
	if _, err := s.GetAll(); !errors.Is(err, drift.ErrStorageUnavailable) {
		t.Fatalf("GetAll: want ErrStorageUnavailable, got %v", err)
	}
}

func TestBuild(t *testing.T) {
	mem, err := driftredis.Build("", driftredis.Config{}, driftredis.Options{})
	if err != nil {
		t.Fatal(err)
	}
	if _, ok := mem.(*drift.InMemoryStorage); !ok {
		t.Fatalf("default backend should be in-memory, got %T", mem)
	}
	if _, err := driftredis.Build("bogus", driftredis.Config{}, driftredis.Options{}); err == nil {
		t.Fatal("expected error for unknown backend")
	}
	mr := miniredis.RunT(t)
	r, err := driftredis.Build("redis", driftredis.Config{Hostname: mr.Host(), Port: mr.Server().Addr().Port}, driftredis.Options{})
	if err != nil {
		t.Fatal(err)
	}
	if _, ok := r.(*driftredis.RedisStorage); !ok {
		t.Fatalf("want RedisStorage, got %T", r)
	}
}
