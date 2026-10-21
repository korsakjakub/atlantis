# 6. Fork: in-process drift scheduler

Date: 2026-10-21

## Status

Proposed

## Context

Upstream drift detection is only reachable through `POST /api/drift/detect` and `/api/drift/remediate`, so scheduling needs an external cron calling Atlantis over HTTP. With drift status now persistent ([ADR 5](0005-fork-drift-storage.md)) the fork can schedule inside the server (fork issue #4, epic B2). Fork-only; not proposed upstream.

## Decision

New package `server/fork/driftsched/` built on `github.com/robfig/cron/v3`, started from `Server.Start()` next to `ScheduledExecutorService` (a separate cron, not `scheduled.ExecutorService`: that service is a fixed-interval ticker for upstream metrics and has no cron expressions or timezones, so extending it would touch more upstream code).

- **Config**: `--fork-drift-scheduler=off|on` (default `off`) plus `--fork-drift-schedule-file` pointing at a YAML file (`schedules:` list; unknown keys are rejected). Entry fields: `id`, `repos`, `projects`, `cron`, `tz` (default UTC), `action` (`detect` | `plan` | `apply`), `max_per_run`, and, beyond the original brief because the API requires them, `ref` (default `main`), `base_branch`, `type` (default `Github`).
- **Fail fast**: the file is loaded and validated in `NewServer`; a bad cron, timezone, action, duplicate id or empty repos aborts startup with `drift schedule <index> (id "<id>"): ...`. `on` requires `--enable-drift-detection`, `--api-secret` and the file; `action: apply` requires `--enable-drift-remediation`.
- **Repos** are concrete `owner/repo` names. Allowlist-style wildcards cannot be enumerated without a VCS listing API, so they are rejected at startup. The handlers still enforce `--repo-allowlist` on every call. `max_per_run` caps repos per tick and rotates through the list between ticks.
- **Execution**: one `cron.Cron` per timezone (`cron.WithLocation`), jobs wrapped in `cron.SkipIfStillRunning`, so a run that outlives its next tick is skipped, never doubled (logged). `detect` calls detect; `plan`/`apply` call remediate with `drift_only: true`, so they act on drift already stored by an earlier detect.
- **Same logic, no HTTP**: the handlers hold the whole detect/remediate flow and are not split into a service layer. The minimal refactor is one new file, `server/controllers/api_inprocess.go`, whose `APIController.CallDrift(path, body)` invokes the existing handler with an `httptest.ResponseRecorder` and the configured API secret. No existing handler or `/api/drift` contract changes, and there is no localhost round trip. The scheduler depends on a one-method `Service` interface, so tests use a fake. Extracting a true service layer would be cleaner but touches ~300 lines of upstream-owned handler code (merge conflicts); revisit if upstream splits it.
- **Out of scope**: concurrency limits (US-021), UI (B8), HA.

## Consequences

- **Single replica only.** Each replica runs its own scheduler, so N replicas fire every tick N times. Leader election / distributed locking is US-070.
- Runs missed while the server is down are not replayed.
- Per-run results are only in logs and the drift storage; no run history.
- Upstream-sync surface: `cmd/server.go` (flags, validation), `server/user_config.go`, `server/server.go` (field, one block, start/stop), `go.mod`/`go.sum`. Everything else is new files.

## Feature flag

- Flag: `--fork-drift-scheduler` (`off` | `on`; related: `--fork-drift-schedule-file`)
- Env var: `ATLANTIS_FORK_DRIFT_SCHEDULER` (`ATLANTIS_FORK_DRIFT_SCHEDULE_FILE`)
- Default: `off`

Example schedule file:

```yaml
schedules:
  - id: nightly-detect
    repos: [myorg/infra, myorg/network]
    cron: "0 2 * * *"
    tz: Europe/Warsaw
    action: detect
    max_per_run: 5
```
