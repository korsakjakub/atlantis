# 9. Fork: `state mv` as a first-class pipeline step

Date: 2026-10-22

## Status

Proposed

## Context

Renaming or moving a resource in Terraform state (`terraform state mv`) is today a manual shell operation outside the PR flow, or an `atlantis`-less `run:` step with no locking, no plan invalidation and no visible result (US-100, issue #20). Upstream already has the same shape for `import` and `state rm`: a step runner, a workflow stage and (for `state rm`) a comment command. This fork wants `mv` as a peer of those. It is fork policy (extra state-mutating surface), so it is not proposed upstream.

## Decision

- `runtime.StateMvStepRunner` (`server/core/runtime/state_mv_step_runner.go`), modeled on `StateRmStepRunner`. It runs `terraform state mv` only through `TerraformExec.RunCommandWithVersion`, so the interface is **not** extended and no mocks change. It is wrapped by the workspace delegate, resolves the project's Terraform distribution/version, and deletes an existing plan file after a successful mv, exactly like import/state rm. Terraform's output (including stderr) is returned unmodified and rendered in the PR comment.
- Workflow step `mv`, registered in `runSteps` next to `import`/`state_rm`:
  - `- mv: {from: ADDR, to: ADDR}` inside any stage (typically `plan`, **before** `plan`, so the plan proves the rename produced no diff). Both fields are required; an address must be non-empty, must not start with `-` (flag injection) and must not contain control characters.
  - `mv: {extra_args: [...]}` is rejected: the only config the step takes is `from`/`to`.
  - bare `- mv` takes its addresses from the comment (`atlantis state mv FROM TO`). It is meant for the `state_mv` stage. In a `plan`/`apply` stage it is **not** rejected (stages do not know which command runs them) and Terraform fails with a usage error; that is a configuration mistake, not a security boundary. Anyone who can set workflows can already use `run:`, so config validation is a usability check, not an access control.
  - `from`/`to` are handed to the runner as leading args and are never expanded (`$` is legal in an address key, unlike `extra_args` of other steps); comment args are appended only for the `state` command, so plan flags (`-- -var ...`) never reach `state mv`.
- New stage `state_mv` in `workflows:` (raw + valid), default `[init, mv]`, mirroring `state_rm`.
- Comment command `atlantis state mv FROM TO` (`-d/-w/-p` supported). `state rm` has a dedicated command, so parity requires one for `mv`: `SubCommands()` gains `mv`, arg count is exactly 2. The project runner is shared with `state rm` (`StateRm`; the steps are chosen by subcommand when the project context is built) and the `StateRmSuccess` model and templates are reused, because they only carry output and the re-plan command and already print the subcommand. The `atlantis help` text is intentionally not changed.
- The mv step takes the project lock and the working-dir lock like state rm (comment path); in a plan workflow it runs under the plan's locks.

Upstream hook-ups (minimal): `server/core/config/{raw,valid}`, one `case` in `runSteps`, one in `project_command_context_builder.go`, one in `state_command_runner.go`, `markdown_renderer.go` (`case "rm", "mv"`), `comment_parser.go` (flag gate), `server/server.go` (runner and one line setting the flag), `command/name.go`, `user_config.go`, and `runatlantis.io/docs/server-configuration.md`. The flag is registered in the `boolFlags` literal in `cmd/server.go` (no `init()`, which `gochecknoinits` forbids).

## Consequences

- `mv` in a plan stage mutates state **before** the PR is approved or applied: it runs on autoplan, regardless of `--allow-commands` (which only gates the comment command). It re-runs on every plan, and the second run fails because the source no longer exists, so the PR cannot be re-planned until the step is removed. A PR closed without merge leaves the state moved (drift against the default branch). An idempotent "skip if already moved" mode is deliberately not included. For a plain rename, prefer Terraform's native `moved {}` block (Terraform >= 1.1, OpenTofu): it is declarative, idempotent and reviewed in the plan. Use the `mv` step for cases `moved {}` cannot express, and the comment command for one-off moves.
- The comment command needs `state` in `--allow-commands` (it is not in the default). It has no approval/mergeable gate, same as upstream `state rm`. `state mv` accepts the same Terraform flags after `--` as `state rm`; the default blocked list only has `-chdir`/`-plugin-dir`, so consider adding `-state`, `-state-out`, `-backup` and `-backup-out` to `--blocked-extra-args`.
- The default `state_mv` stage (`[init, mv]`) also applies to workflows where an operator emptied `state_rm`: after enabling the flag such a workflow gets a working `state mv` even though `state rm` is disabled. Override `state_mv: {steps: []}` to turn it off per workflow.
- With the flag off the parser refuses `atlantis state mv` before pre-workflow hooks and clone run; `StateCommandRunner` checks again as defense in depth.
- Moving state between projects is not supported (US-102); `/api/state/*` and UI initiation (US-101) and bulk CSV are out of scope.
- Config validation reads a package-level switch (`valid.ForkStateMvEnabled`), because validation has no access to server flags. It is set once in `NewServer` before any config is parsed.
- Existing tests that compare whole `valid.Workflow` values needed the new default `StateMv` stage.
- Sync cost: possible conflicts in `server/core/config/raw/step.go`, `workflow.go`, `valid/global_cfg.go`, `project_command_runner.go`, `state_command_runner.go`, `server/server.go`, `server/user_config.go`.
- Not verified against a real Terraform (none in the dev environment): the "mv rename gives an empty plan" scenario is covered only through the mocked executor.

## Numbering and merge order

`main` has `0004` and two `0005` files (`0005-fork-drift-storage`, `0005-fork-oidc`). The open PRs use `0006` (#34 scheduler), `0007` (#35 RBAC), `0008` (#36 service-account tokens), and this ADR is `0009` (#37). There are no collisions: merge in the order #34 → #35 → #36 → #37 and no renumbering is needed. Only `runatlantis.io/docs/server-configuration.md` (`--fork-state-mv`) references this ADR's number.

## Feature flag

- Flag: `--fork-state-mv`
- Env var: `ATLANTIS_FORK_STATE_MV`
- Default: off. Off: a config containing an `mv` step fails validation with a message naming the flag, and `atlantis state mv` is refused. Nothing else changes.
