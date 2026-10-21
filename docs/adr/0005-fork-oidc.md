# 5. Fork: native OIDC login for the web UI and API

Date: 2026-10-21

## Status

Proposed

## Context

Atlantis only offers a shared Basic Auth credential for the web UI (`--web-basic-auth`) and a shared `X-Atlantis-Token` secret for `/api/*`. Neither identifies a person, and neither works with a corporate identity provider. This fork needs real SSO logins (US-030, issue #6) as the first step towards per-user authorization (US-031). It is fork-specific policy and depends on a new library, so it is not proposed upstream.

## Decision

Add `server/fork/oidc/`, a self-contained package implementing OpenID Connect Authorization Code flow with PKCE (S256) on top of `github.com/coreos/go-oidc/v3` and `golang.org/x/oauth2`:

- Discovery through `.well-known/openid-configuration` at startup; ID token verification (signature, `iss`, `aud`, `exp`) by go-oidc; mandatory `state` (single use, also bound to the browser by a short-lived cookie) and `nonce`.
- Presets are defined in a YAML file (`--fork-oidc-config`): issuer, client ID/secret, scopes and a claim map. A `provider` key (`okta`, `entra`, `google`, `gitlab`, `keycloak`, `authentik`) supplies defaults. No code is needed per provider. See `fork/docs/oidc.md`.
- Endpoints: `/oauth/login/<preset>`, `/oauth/callback`, `/oauth/logout`. They are served by the middleware itself, so no routes are added to the upstream router.
- Sessions are server side, in memory, identified by a random cookie (`HttpOnly`, `Secure`, `SameSite=Lax`) with a configurable timeout (default 8h). Logout deletes the session, which revokes it immediately.
- A negroni middleware protects the web UI and `/api/*`. `/events`, `/healthz`, `/readyz` and `/status` are never behind the login, so VCS webhooks keep working. Unauthenticated UI `GET`s are redirected to the login; `/api/*` gets `401`.
- The verified identity (`Identity`: subject, email, name, groups, issuer, preset) is stored in the request context (`oidc.IdentityFromContext`). It is **not** connected to `models.User` or the allowlists; that is US-031.
- `/api/*` requires a session **and** the `X-Atlantis-Token` API secret (checked by the middleware, then again by the controllers). A login alone never grants API access, because authorization is US-031 and "logged in" can mean any account at the IdP. Machine clients may use the token without a session only when `--fork-oidc-api-token` is set (default off).
- A preset may set `required_claims` (exact string match, e.g. Google `hd`); a mismatch rejects the login. Pending logins are capped (10000, then `503`) and garbage collected at most once a minute. `/oauth/logout` is POST only.
- When OIDC is on it supersedes `--web-basic-auth`.

Upstream hook-ups (minimal): three fields at the end of `UserConfig` in `server/user_config.go`, a `ForkOIDC` field plus a few lines in `server/server.go`, and `server/fork_oidc.go`. Flags are registered from `cmd/fork_oidc.go` (an `init()` adding to the flag maps), so `cmd/server.go` is untouched. `runatlantis.io/docs/server-configuration.md` also gets three flag entries, because `TestAllFlagsDocumented` requires every flag to be documented there.

## Consequences

- Real per-user identity for the UI and API without an external proxy.
- Sessions live in memory: a restart or a multi-replica deployment (without sticky routing) forces a new login. Acceptable for Atlantis's single-instance model; a shared store can follow later.
- Logout revokes only the Atlantis session, it does not end the IdP session (no RP-initiated logout yet).
- Cookies are always `Secure`, unless the config file sets `session.insecure_cookie: true` (local development only).
- Known limitation: the upstream `RequestLogger` logs request URIs at debug level, so the one-time `code` and `state` of `/oauth/callback` can appear in debug logs. The code is single use and PKCE protected; fixing it would require changing `server/middleware.go`.
- State-changing UI requests rely on `SameSite=Lax` for CSRF protection.
- Discovery runs at startup; the server fails to start if the issuer is unreachable.
- Sync cost: possible conflicts in `cmd/server.go`, `server/user_config.go`, `server/server.go`, `go.mod`/`go.sum`.

## Feature flag

- Flag: `--fork-oidc` (`off` or a preset name), with `--fork-oidc-config` and `--fork-oidc-api-token`
- Env var: `ATLANTIS_FORK_OIDC` (also `ATLANTIS_FORK_OIDC_CONFIG`, `ATLANTIS_FORK_OIDC_API_TOKEN`)
- Default: `off`
