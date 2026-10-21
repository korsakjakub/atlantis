# 6. Fork: scoped service-account tokens for the API

Date: 2026-10-21

## Status

Proposed

## Context

`/api/*` is protected by a single shared `X-Atlantis-Token` (`--api-secret`) that grants full access, including `/api/apply`. CI jobs that only need a plan therefore hold an admin credential (US-032, issue #8). The change is fork-specific policy, so it is not proposed upstream.

## Decision

Add `server/fork/sactoken/`, a negroni middleware placed **before** the OIDC middleware.

- Tokens are defined in a YAML file (`--fork-sa-tokens`): `id`, `secret_sha256`, `actions` (`read`, `plan`, `apply`) and `repos`.
- Actions do not imply each other: a token that may `apply` must also list `plan` to plan.
- `repos` are case-insensitive globs (`*` wildcard) on the repository full name (`owner/repo`). The VCS host is not part of the match because it is not known before the controller parses the request. A request with no repository (`GET /api/locks`) needs a `*` pattern.
- Endpoint to action: `GET /api/locks`, `GET /api/drift/status`, `GET /api/drift/remediate[/{id}]` need `read`; `POST /api/plan`, `/api/drift/detect` and `/api/drift/remediate` need `plan`; `POST /api/apply` and `/api/drift/remediate` with `action: "apply"` need `apply`. Every other `/api/` path (admin-only, policy approval, anything added later) is `403` for scoped tokens by default.
- Secrets are stored as hex SHA-256. Tokens are random, so a plain hash is enough and no plaintext lives in config or logs. The presented token is hashed and compared in constant time against every entry.
- A token that is not in the file (including the global `--api-secret`) passes through untouched, so existing clients behave exactly as before. A scoped token that is in scope has its header replaced with the global secret, so the controllers' own check passes and no controller changes are needed. This is why `--api-secret` is required when the flag is set.
- Denials answer `403` with `{"error":{"code":"scope_denied"|"endpoint_not_allowed", ...}}`; an unreadable body gives `400 invalid_request`.
- Audit: each scoped request logs `sa-token audit: token=<id> action=<a> repo=<r> result=allow|deny`. Secrets and payloads are never logged.
- The file is validated at startup (hash format, known actions, unique ids, non-empty scopes and patterns). Any error stops the server.

## Consequences

- With OIDC (US-030) on, a scoped token has no session, so it also needs `--fork-oidc-api-token`.
- Tokens come only from the file; there is no issuing or rotation API. Rotate by editing the file and restarting.
- The middleware reads POST bodies (up to 10 MiB) to find the repository, then restores them.
- Repository allowlist checks in the controllers still apply on top of the scope.

## Integration with RBAC (US-031)

Scoped tokens are a separate, machine-only path. When RBAC lands, a token's `actions` can become a fixed role (read/plan/apply map to roles) and the audit line can carry the resulting identity. Until then they do not interact.

## Feature flag

- Flag: `--fork-sa-tokens` (path to the YAML file)
- Env var: `ATLANTIS_FORK_SA_TOKENS`
- Default: empty (off)

Example file:

```yaml
tokens:
  - id: ci:plan-only
    secret_sha256: <sha256 of the token, e.g. printf %s "$TOKEN" | sha256sum>
    actions: [plan, read]
    repos: ["myorg/*"]
```
