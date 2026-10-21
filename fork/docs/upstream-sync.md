# Upstream sync

## Cadence

Sync once per upstream release (`runatlantis/atlantis` tags `vX.Y.Z`), within a week of the release. Security releases: sync immediately.

## Procedure

```sh
git remote add upstream https://github.com/runatlantis/atlantis.git   # once
git fetch upstream --tags
git checkout -b fork/sync-vX.Y.Z main
git rebase vX.Y.Z          # resolve conflicts using the checklist below
make go-generate           # if interfaces changed
make build-service
make check-fmt
make test-all              # ignore TestNewServer_GitHubUser (pre-existing failure)
```

`make test-all` includes integration tests and needs Terraform 1.11.1+ and about 5 minutes. With all `ATLANTIS_FORK_*` flags off, results must match upstream. Run the fork packages with the flags on too: `go test ./server/fork/...`.

After a green run, fast-forward `main`, then tag a release as `vX.Y.Z-forkN`.

## Conflict hotspots checklist

Check each file after the rebase; fork changes there are small hook-ups and tend to conflict:

- [ ] `server/server.go`: fork wiring calls (constructors, dependency injection).
- [ ] `server/user_config.go`: `ForkXxx` config fields.
- [ ] `cmd/server.go`: `Fork*Flag` constants and flag map entries, plus validation.
- [ ] `server/core/config/raw/` and `server/core/config/valid/`: fork-added repo config keys.
- [ ] Mocks: rerun `make go-generate` if any interface changed upstream.
- [ ] `go.mod` / `go.sum`: take upstream, then re-add fork-only dependencies and run `go mod tidy`.
- [ ] `AGENTS.md` and `.github/workflows/`: take upstream; keep `fork-build.yml` and the fork `CODEOWNERS` entries.
