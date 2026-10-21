# 5. Fork: persistent drift storage

Date: 2026-10-21

## Status

Proposed

## Context

Drift detection results are held by `drift.InMemoryStorage` and are lost on every server restart, so `/api/drift/status` is empty after a deploy and remediation apply loses the cached drift it requires. The `drift.Storage` interface already allows other backends, but `server/server.go` hard-wires the in-memory one. This is a fork-specific operational need (fork issue #3, epic B1), not proposed upstream.

## Decision

Add `server/fork/driftredis/` with a `RedisStorage` implementing `drift.Storage`, selected by a flag at the single wiring point in `server/server.go` (drift detection block).

- Layout: one JSON record per drift identity (`<prefix>rec:<sha256(repo, project, path, workspace, ref, base_branch)>`), a per-repository index set (`<prefix>idx:<repo>`) and a set of repositories (`<prefix>repos`). Only single-key commands are used so it works on Redis Cluster. Expired records are pruned from the index lazily on read.
- Retention: each record carries a TTL (`--fork-drift-retention-hours`, default 720, `0` = no expiry). Key prefix is configurable (`--fork-drift-redis-key-prefix`).
- Semantics match `InMemoryStorage` exactly: filter logic is shared through exported `drift.MatchesGet`/`drift.MatchesDelete`, and `PlanOutput` is never persisted. A shared table-driven suite (`server/core/drift/storagetest`) runs against both backends.
- Connection reuses the existing `--redis-*` flags.
- Fail closed: backend errors wrap `drift.ErrStorageUnavailable`. `/api/drift/status` answers `503 service unavailable` on it, and `/api/drift/remediate` with `action: apply` is rejected with `503` when the storage's optional `drift.HealthChecker` reports unhealthy. Apply never falls back to memory.
- Out of scope: persistence of remediation results (still in memory), scheduler, planfile persistence, API contract and JSON field changes.

## Consequences

- Drift status survives restarts and is shared between replicas pointing at the same Redis.
- Redis becomes a hard dependency for drift endpoints when enabled; an outage makes them return 503 rather than stale or empty data.
- Upstream-sync conflict surface: small edits in `cmd/server.go`, `server/user_config.go`, `server/server.go` (one block), `server/controllers/api_controller.go` (two error branches), plus new `server/core/drift/storage_shared.go`. Everything else is new files.

## Feature flag

- Flag: `--fork-drift-storage` (`memory` | `redis`; related: `--fork-drift-redis-key-prefix`, `--fork-drift-retention-hours`)
- Env var: `ATLANTIS_FORK_DRIFT_STORAGE`
- Default: `memory` (identical to upstream behavior)
