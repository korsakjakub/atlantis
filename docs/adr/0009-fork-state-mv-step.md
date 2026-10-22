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
  - bare `- mv` takes its addresses from the comment (`atlantis state mv FROM TO`).
  - `from`/`to` are handed to the runner as leading args; comment args are appended only for the `state` command, so plan flags (`-- -var ...`) never reach `state mv`.
- New stage `state_mv` in `workflows:` (raw + valid), default `[init, mv]`, mirroring `state_rm`.
- Comment command `atlantis state mv FROM TO` (`-d/-w/-p` supported). `state rm` has a dedicated command, so parity requires one for `mv`: `SubCommands()` gains `mv`, arg count is exactly 2. The project runner is shared with `state rm` (`StateRm`; the steps are chosen by subcommand when the project context is built) and the `StateRmSuccess` model and templates are reused, because they only carry output and the re-plan command and already print the subcommand. The `atlantis help` text is intentionally not changed.
- The mv step takes the project lock and the working-dir lock like state rm (comment path); in a plan workflow it runs under the plan's locks.

Upstream hook-ups (minimal): `server/core/config/{raw,valid}`, one `case` in `runSteps`, one in `project_command_context_builder.go`, one in `state_command_runner.go`, `markdown_renderer.go` (`case "rm", "mv"`), `server/server.go` (runner and one line setting the flag), `command/name.go`, `user_config.go`, and `runatlantis.io/docs/server-configuration.md`. The flag is registered from `cmd/fork_state_mv.go`.

## Consequences

- `mv` in a plan stage mutates state **before** the PR is approved or applied, and it re-runs on every plan: the second run fails because the source no longer exists. Use the configured step for a single planned rename and remove it after apply; use the comment command otherwise. An idempotent "skip if already moved" mode is deliberately not included.
- Moving state between projects is not supported (US-102); `/api/state/*` and UI initiation (US-101) and bulk CSV are out of scope.
- Config validation reads a package-level switch (`valid.ForkStateMvEnabled`), because validation has no access to server flags. It is set once in `NewServer` before any config is parsed.
- Existing tests that compare whole `valid.Workflow` values needed the new default `StateMv` stage.
- Sync cost: possible conflicts in `server/core/config/raw/step.go`, `workflow.go`, `valid/global_cfg.go`, `project_command_runner.go`, `state_command_runner.go`, `server/server.go`, `server/user_config.go`.
- Not verified against a real Terraform (none in the dev environment): the "mv rename gives an empty plan" scenario is covered only through the mocked executor.

## Numbering and merge order

The fork already has two `0005` files on `main` (`0005-fork-drift-storage`, `0005-fork-oidc`), and the open PRs #35 and #36 collide on `0006`/`0008`. This ADR takes `0009`. No renumbering is needed if it merges last. If it merges before #35 or #36 is renumbered, only the file name and the heading number change; nothing else references the number, except one line in `runatlantis.io/docs/server-configuration.md` (`--fork-state-mv`) and `custom-workflows.md`.

## Feature flag

- Flag: `--fork-state-mv`
- Env var: `ATLANTIS_FORK_STATE_MV`
- Default: off. Off: a config containing an `mv` step fails validation with a message naming the flag, and `atlantis state mv` is refused. Nothing else changes.
