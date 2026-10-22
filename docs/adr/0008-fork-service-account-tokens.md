# 8. Fork: scoped service-account tokens for the API

Date: 2026-10-21

## Status

Proposed

## Context

`/api/*` is protected by a single shared `X-Atlantis-Token` (`--api-secret`) that grants full access, including `/api/apply`. CI jobs that only need a plan therefore hold an admin credential (US-032, issue #8). The change is fork-specific policy, so it is not proposed upstream.

## Decision

Add `server/fork/sactoken/`, a negroni middleware placed **before** the OIDC middleware.

- Tokens are defined in a YAML file (`--fork-sa-tokens`): `id`, `secret_sha256`, `actions` (`read`, `plan`, `apply`) and `repos`.
- Actions do not imply each other: a token that may `apply` must also list `plan` to plan.
- `repos` are case-insensitive globs on the repository full name (`owner/repo`). `*` matches within one path segment (`myorg/*` does not match `myorg/a/b`), `**` spans segments (`group/**`), and a lone `*` matches everything. A wildcard in the owner segment is rejected unless the whole pattern is `*`, so `myorg/*` can never admit `myorg-evil/r`. The VCS host is not part of the match because it is not known before the controller parses the request. A request with no repository (`GET /api/locks`) needs a lone `*`.
- The repository is read from the JSON body for `POST` and from the `repository` query parameter for `GET`, exactly where the controllers read it. The same `encoding/json` decoder is used, so duplicate or differently cased keys resolve identically in the middleware and the controller.
- Endpoint to action: `GET /api/locks`, `GET /api/drift/status`, `GET /api/drift/remediate[/{id}]` need `read`; `POST /api/plan`, `/api/drift/detect` and `/api/drift/remediate` need `plan`; `POST /api/apply` and `/api/drift/remediate` with `action: "apply"` need `apply`. Every other `/api/` path (admin-only, policy approval, anything added later) is `403` for scoped tokens by default.
- Secrets are stored as hex SHA-256. Tokens are random, so a plain hash is enough and no plaintext lives in config or logs. The presented token is hashed and compared in constant time against every entry.
- A token that is not in the file (including the global `--api-secret`) passes through untouched, so existing clients behave exactly as before. A scoped token that is in scope has its header replaced with the global secret, so the controllers' own check passes and no controller changes are needed. This is why `--api-secret` is required when the flag is set.
- Denials answer `403` with `{"error":{"code":"scope_denied"|"endpoint_not_allowed", ...}}`; an unreadable body gives `400 invalid_request`.
- Audit: each scoped request logs `sa-token audit: token=<id> action=<a> repo=<r> result=allow|deny`. Secrets and payloads are never logged.
- The file is parsed strictly (unknown keys are errors) and validated at startup: hash format, known actions, unique ids, unique `secret_sha256`, no token whose hash equals the hash of `--api-secret`, non-empty scopes, valid patterns. Any error stops the server.
- Audit lines also carry `method` and `path` (no query string), so denials of unknown endpoints are attributable.

## Consequences

- With OIDC (US-030) on, a scoped token has no session, so it also needs `--fork-oidc-api-token`.
- Tokens come only from the file; there is no issuing or rotation API. Rotate by editing the file and restarting.
- The middleware reads POST bodies (up to 10 MiB) to find the repository, then restores them.
- Repository allowlist checks in the controllers still apply on top of the scope.

## Integration with RBAC (US-031)

Scoped tokens are a separate, machine-only path with no human identity. Once RBAC (US-031, PR #35) lands, the middleware chain is SA tokens, then OIDC, then RBAC. An authorized scoped request reaches OIDC and RBAC carrying the **global** secret and no identity.

Intended behavior: RBAC must not deny such a request merely for lacking an identity. The scope check has already decided it, so RBAC should treat an identity-less `/api` request that carries the global secret as the existing machine path, exactly as it does for a direct global-secret client. The scope itself must never be widened by RBAC. A scoped token that is out of scope never gets past this middleware, so RBAC cannot grant it back.

This is intent, not tested behavior: whichever of #35 and this PR merges second must add a chain test (scoped token, OIDC with `--fork-oidc-api-token`, RBAC on) covering one allowed and one denied request.

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
