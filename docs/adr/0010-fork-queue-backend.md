# 10. Fork: queue backend

Date: 2026-10-22

## Status

Proposed

## Context

Comment commands and autoplans run inline in the webhook goroutine (`DefaultCommandRunner.RunCommentCommand` / `RunAutoplanCommand`). A restart loses everything in flight and nothing separates "accepted" from "executed". Fork issue #11 (epic B4, US-040) asks for an interface that decouples enqueue from execution, so a later worker (US-070) and scheduler (US-020) can plug in.

Note on the seam: `queueItem` in `command_runner.go` is only a BFS helper for GitHub team hierarchy, not a job queue. The real in-process execution seam is the two public entry points of `DefaultCommandRunner` (called by the VCS events controller), so that is where enqueueing is hooked. The VCS contracts (webhook → controller → `CommandRunner` interface) are unchanged.

## Decision

New package `server/core/queue`:

- `Job{Repo, Pull, Command, Step, Payload, Attempt}`; idempotency key = `lower(repo)#pull#command#step`.
- `Backend` interface: `Enqueue` (returns `false` for a duplicate key), `Receive` (takes over jobs idle longer than `ClaimIdle` first), `Ack`, `Retry` (attempt+1, or DLQ when `Attempt >= MaxAttempts`), `DLQ` (list). Delivery is **at-least-once**; the key is held from `Enqueue` until `Ack` or dead-lettering, so a duplicate webhook while a command is queued/running is dropped and the same command can be issued again afterwards.
- `Memory` backend: in-process reference implementation, used by the conformance suite.
- `RedisStreams` backend: `XADD` / `XREADGROUP` (group `atlantis`) / `XACK`+`XDEL`; `XAUTOCLAIM` for takeover of a dead consumer's job; DLQ is a second stream `<prefix>dlq`; idempotency via `SET <prefix>dedup:<key> NX EX 24h` (the TTL is only a safety net). Single-stream, single-key commands per step, with `MULTI` for settle operations.
- `Executor`: a pool of consumers (`--parallel-pool-size` workers) that calls a `Handler`; nil → ack, `ErrAbandon` → leave for redelivery, error/panic → `Retry`.
- `DefaultCommandRunner.Queue` (nil by default). When set, `RunCommentCommand`/`RunAutoplanCommand` serialize their arguments as the job payload and enqueue; `HandleQueuedJob` runs the original body (`runCommentCommand`/`runAutoplanCommand`, renamed, unchanged). Step = `workspace:dir:project` (comments) or `autoplan`.
- Wiring in `server/server.go` only when `--fork-queue-backend=redis-streams`; the executor starts in `Server.Start`.

**Default behaviour is untouched**: with `in-process` (default) `Queue` stays `nil` and commands run inline exactly as before (same goroutine, same ordering and `--parallel-pool-size` semantics inside a command). The `Memory` backend is therefore not wired into production; it exists as the reference implementation of the contract. Routing inline execution through an in-memory queue would add a hop and change ordering/back-pressure for no benefit.

Enqueue failure does not fall back to inline execution (it would bypass dedup/retry); it is logged. A missing Redis URL for `redis-streams` is a startup error.

### Handler idempotency and locks

A job may run more than once (consumer died mid-job). Safety relies on the existing project/workspace locks (`Locker`) and the plan/apply requirements, which are re-evaluated on every run; the queue does not add its own execution lock. A job whose handler finished but whose `Ack` was lost is re-run — a narrow window, accepted for at-least-once. A job that crashes its consumer on every delivery is reclaimed forever (the attempt counter only counts handler-reported failures); `ClaimIdle` should exceed the longest plan/apply.

### Conformance evidence

`server/core/queue/queuetest` is a backend-agnostic suite run against `Memory` and `RedisStreams`. Redis is exercised through `miniredis/v2` (test-only dependency, already in `go.mod`), an in-process Redis implementation with streams and `XAUTOCLAIM`; **no real Redis server and no NATS were run**. NATS is not implemented: a second real broker adds no function; the suite is the contract a NATS backend would have to pass. Restart-mid-job (claim of a stale pending entry, dedup of a duplicate delivery, lock not broken, exactly one completed execution) is covered by `TestExecutor_ConsumerRestartMidJob` for both backends.

### Out of scope

Webhook-level deduplication/worker (US-070), scheduler (US-020), UI (US-041), backoff between retries (retries are immediately visible), production Redis migration, per-project parallel jobs.

## Consequences

- Commands survive a consumer restart when `redis-streams` is on, and several Atlantis replicas can share one stream (consumer names are `host-pid-N`).
- Ordering: with the queue on, ordering between different commands is no longer webhook arrival order; same-pull commands are serialized only by the working-dir lock as before.
- With the queue on, `Drainer` accounting happens when the job runs, not when it is enqueued; on drain, jobs are abandoned (not acked) and picked up by the next instance after `ClaimIdle`.
- Redis becomes a hard dependency when enabled.
- Upstream-sync conflict surface: `cmd/server.go`, `server/user_config.go`, `server/server.go` (one block + one field + `Start`), `server/events/command_runner.go` (two renames + one field). Everything else is new files.
- **Numbering**: this ADR is `0010` assuming open PRs #34 → #35 → #36 → #37 merge first (they claim 0006–0009). Renumber (file name and title) if the merge order changes.

## Feature flag

- Flag: `--fork-queue-backend` (`in-process` | `redis-streams`; related: `--fork-queue-redis-url`, `--fork-queue-redis-key-prefix`, `--fork-queue-max-attempts`, `--fork-queue-claim-idle-seconds`)
- Env var: `ATLANTIS_FORK_QUEUE_BACKEND`
- Default: `in-process` (identical to upstream behavior)
