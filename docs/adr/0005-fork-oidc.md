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
- API controllers still check `X-Atlantis-Token`. For session-authenticated `/api/*` requests the middleware replaces the header with the configured `--api-secret` (a client supplied value is discarded), so controllers are unchanged. Machine clients may keep using the token only when `--fork-oidc-api-token` is set (default off).
- When OIDC is on it supersedes `--web-basic-auth`.

Upstream hook-ups (minimal): flag registration in `cmd/server.go`, three fields in `server/user_config.go`, a `ForkOIDC` field plus three lines in `server/server.go`, and `server/fork_oidc.go`.

## Consequences

- Real per-user identity for the UI and API without an external proxy.
- Sessions live in memory: a restart or a multi-replica deployment (without sticky routing) forces a new login. Acceptable for Atlantis's single-instance model; a shared store can follow later.
- Logout revokes only the Atlantis session, it does not end the IdP session (no RP-initiated logout yet).
- Cookies are `Secure` unless `redirect_url` is plain `http` (local development only).
- State-changing UI requests rely on `SameSite=Lax` for CSRF protection.
- Discovery runs at startup; the server fails to start if the issuer is unreachable.
- Sync cost: possible conflicts in `cmd/server.go`, `server/user_config.go`, `server/server.go`, `go.mod`/`go.sum`.

## Feature flag

- Flag: `--fork-oidc` (`off` or a preset name), with `--fork-oidc-config` and `--fork-oidc-api-token`
- Env var: `ATLANTIS_FORK_OIDC` (also `ATLANTIS_FORK_OIDC_CONFIG`, `ATLANTIS_FORK_OIDC_API_TOKEN`)
- Default: `off`
