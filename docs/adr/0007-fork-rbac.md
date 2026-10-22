# 7. Fork: RBAC from OIDC claims, per repo

Date: 2026-10-21

## Status

Proposed

## Context

US-030 (ADR 0005) gives the web UI and `/api/*` a real identity, but "logged in" still means "any account at the IdP". Atlantis itself only knows VCS identities (`models.User{Username, Teams}`) checked by the team allowlist (`GH_TEAM_ALLOWLIST`, `GITLAB_GROUP_ALLOWLIST`, `external_team_allowlist` / `TeamAuthz`). An admin needs one place to say which people may read, plan, apply or approve on which repos, for commands, API and UI alike (US-031, issue #7). Fork-specific policy, so not proposed upstream.

## Decision

New package `server/fork/rbac/`.

**Roles** are ordered and cumulative: `viewer < plan < apply < approver < admin`. `approver` includes `apply` and is reserved for per-resource approvals (US-090); only `Engine.Authorize(…, ActionApprove, repo)` and the `approve_policies` command use it today.

**Config** (`--fork-rbac-config`, YAML), validated at startup (any error aborts the server). Parsing is strict: an unknown key (e.g. `repo:` for `repos:`) is an error, so a typo can never widen a rule. `default_role` is mandatory. Duplicate `vcs_users` (case-insensitive) or `local_users` names and non-bcrypt hashes are rejected:

```yaml
default_role: viewer          # REQUIRED: role when no rule matches; "none" = no access
rules:                        # roles of all matching rules combine, highest wins
  - {claim: groups, value: platform, role: admin}
  - {claim: groups, value: "dev-*",  role: plan,  repos: ["org/*", "!org/prod-*"]}
  - {claim: email,  value: "*@corp.example", role: viewer}
api_token: {role: apply, repos: ["org/ci-*"]}   # X-Atlantis-Token holders (machine clients)
vcs_users:                    # optional extra restriction of PR commands
  - {username: alice, role: plan, repos: ["org/*"]}
local_users:                  # break-glass
  - {name: root, password_bcrypt: "$2a$…", role: admin, break_glass: true, totp_seed_env: ATLANTIS_RBAC_TOTP_ROOT}
```

UI note: `GET /` and other pages are repo-less requests, so a user whose roles are all repo-scoped can only load them if `default_role` is at least `viewer` (`/static/` is open to any authenticated identity).

Claims are `groups`, `email`, `name`, `sub`, `issuer` of the OIDC `Identity`; values are `path.Match` globs. Repo scopes reuse `events.RepoAllowlistChecker` (same glob and `!` exclusion syntax as `--repo-allowlist`) on `owner/repo` (any number of segments); the VCS hostname is not part of API requests, so it is not matched, and patterns are anchored at the start (`team-a/*` does not match `prod/team-a/x`). A request with no repo (e.g. `GET /api/locks`) is only granted by rules without `repos`.

**Enforcement points**, all behind `--fork-rbac` (default off, requires `--fork-oidc`):

1. *UI and API*: a negroni middleware after the OIDC middleware (`Engine.ServeHTTP`). Endpoint → minimum role: reads → viewer; `POST /api/plan`, `/api/drift/detect` → plan; `POST /api/apply`, `/api/drift/remediate` → apply; everything else (lock deletion, global apply lock, `/debug/`, `/github-app/`, unknown `/api/*`) → admin, so new endpoints are closed by default. The repo comes from where the controller reads it: the JSON body for `POST /api/*` (peeked and restored), the `repository` query parameter for `GET`. The other source is ignored, so a query parameter cannot smuggle a different repo past the check. A session identity uses its claim-derived role; an `/api/*` request authenticated only by `X-Atlantis-Token` gets the `api_token` role. `/events`, `/healthz`, `/readyz`, `/status` stay open. Denied requests get `403`.
2. *PR commands*: `CommandChecker` decorates the existing `command.TeamAllowlistChecker`. It can only **narrow**: a VCS user listed in `vcs_users` additionally needs the role for the command on that repo (`plan`/`unlock` → plan; `apply`/`import`/`state` → apply; `approve_policies` → approver). The wrapped checker (GitHub teams, GitLab groups, external checker) still decides for everyone, and users without a `vcs_users` entry are decided by it alone.
3. *Identity bridge*: OIDC identity (claims/groups) cannot be matched to a PR comment author without trusting email/username equality, so `vcs_users` maps VCS usernames explicitly. Both layers resolve to the same role vocabulary and the same per-repo scopes.

**Break-glass**: `local_users` authenticate by HTTP Basic (bcrypt) through `oidc.Options.Fallback`, so they work when the IdP is down. A user marked `break_glass` must also send a TOTP code in `X-Atlantis-TOTP` (RFC 6238: SHA-1, 30 s, 6 digits, ±1 step); each step is accepted once and 5 failures lock the user for a minute. The base32 seed (≥ 80 bits) comes from the environment variable named by `totp_seed_env`; there is no enrollment UI. TOTP is only active when `--fork-rbac` is on, since local users are only loaded then. An `/api/*` request by a local user still needs the API secret, as for any session.

Decisions and trade-offs:
- Local users without `break_glass` are password-only accounts that would bypass the IdP and MFA (in effect service accounts, US-032), so they are limited to `viewer` at startup.
- No session is created: every request needs its own Basic credentials and a fresh TOTP code (a code is single use). Break-glass is therefore curl/API-oriented; browsers cannot send `X-Atlantis-TOTP`.
- Lockout is per username, so anyone who can reach the server can lock `root` for a minute with 5 bad attempts while the IdP is down. Chosen over per-IP keying, which a distributed guess would bypass; revisit if abused.
- Unknown usernames are compared against a dummy bcrypt hash to equalise timing.
- Successes and failures are logged at warn with username and remote address (never the password, code or seed); RBAC denials are logged at debug without claims.

## Why the VCS allowlists are not overridden

They are the source of truth for who may comment-command on a repository and are what upstream users and the external checker already rely on. Replacing them would make this fork's behaviour diverge for every PR, break `external_team_allowlist` hierarchy expansion, and let a mistake in IdP claims grant PR-command rights. The OIDC role is a second layer: it can only remove access on commands, and it is the only authority on UI and API, where no VCS identity exists.

## Consequences

- Flag off: no new code path runs (no middleware, no checker wrapper, no fallback); behaviour is identical to before.
- With `vcs_users` configured and no VCS allowlist, the command checker reports rules, so every command and autoplan resolves the user's VCS teams (`GetTeamNamesForUser`) and fails closed on VCS API errors, even for users not listed in `vcs_users`.
- Two places describe access for PR commands (VCS allowlist, `vcs_users`) when both are used; the stricter wins.
- Role changes need a config reload (restart); claims are read at login, so a group change applies at the next login (session TTL ≤ 8h by default).
- Break-glass has no persistent lockout across restarts and no password rotation tooling.
- `approver` is defined but not yet enforced on any endpoint (US-090). Scoped per-service-account keys are US-032; here `api_token` is one shared role.
- `golang.org/x/crypto` (bcrypt) becomes a direct dependency (already in the module graph).
- Sync cost: small edits in `server/server.go`, `server/user_config.go`, `server/fork_oidc.go`, `go.mod`, and the docs flag list.

## Feature flag

- Flag: `--fork-rbac` with `--fork-rbac-config`
- Env var: `ATLANTIS_FORK_RBAC` (also `ATLANTIS_FORK_RBAC_CONFIG`)
- Default: `off`
