# Fork conventions

This is the `korsakjakub/atlantis` fork of [runatlantis/atlantis](https://github.com/runatlantis/atlantis). Everything the fork adds follows the rules below so upstream syncs stay cheap. See [docs/upstream-sync.md](docs/upstream-sync.md) for the sync procedure.

## Feature flags: `ATLANTIS_FORK_<NAME>`

Every fork feature is **default-off** and controlled by a flag named `fork-<name>` (kebab-case), which Viper maps to the env var `ATLANTIS_FORK_<NAME>` (upper snake case). Viper is configured with the `ATLANTIS` prefix and `AutomaticEnv`, with `-` replaced by `_`, so no extra wiring is needed.

| Flag | Env var |
| --- | --- |
| `--fork-my-feature` | `ATLANTIS_FORK_MY_FEATURE` |

Registration follows the existing pattern in `cmd/server.go`:

```go
// const block
ForkMyFeatureFlag = "fork-my-feature"

// boolFlags map
ForkMyFeatureFlag: {
	description:  "Enable <what it does>. Fork-only feature.",
	defaultValue: false,
},
```

and a matching field in `server/user_config.go`:

```go
ForkMyFeature bool `mapstructure:"fork-my-feature"`
```

Rules:

- The default is always `false`; with all fork flags off the binary must behave exactly like upstream.
- Keep the flag registration and config field in one contiguous block, marked with a `// fork:` comment, so sync conflicts are easy to resolve.
- Read the flag only at the wiring point (`server/server.go`) and pass the feature in as a dependency; do not check flags deep inside upstream code.

## Layout policy

- All fork-owned code lives in **new packages** under `server/fork/<feature>/`, or behind a feature flag.
- Do **not** edit upstream files except for minimal hook-ups (flag registration, config field, one wiring call). Prefer adding a file next to the hook rather than changing existing functions.
- Tests for fork code live next to it in `server/fork/<feature>/`, in `{package}_test`.
- Fork docs live in `fork/`. Fork ADRs use [docs/adr/template-fork.md](../docs/adr/template-fork.md) and must name their feature flag.
- Ownership: `fork/` and `server/fork/` are owned by `@korsakjakub` (see `.github/CODEOWNERS`).

## CI

`.github/workflows/fork-build.yml` builds and vets on pushes to `fork/**` branches. Pushing a tag matching `v*-fork*` publishes `ghcr.io/korsakjakub/atlantis`.
