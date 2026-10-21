# OIDC login (fork)

Native OpenID Connect login (Authorization Code + PKCE) for the web UI and `/api/*`. Design: [ADR 0005](../../docs/adr/0005-fork-oidc.md).

## Enable

```sh
ATLANTIS_FORK_OIDC=okta                      # preset name from the config file; "off" (default) disables
ATLANTIS_FORK_OIDC_CONFIG=/etc/atlantis/oidc.yaml
ATLANTIS_FORK_OIDC_API_TOKEN=false           # true: /api/* also accepts X-Atlantis-Token without a login
ATLANTIS_API_SECRET=...                      # still required for the API to be enabled at all
```

With the flag off Atlantis behaves exactly as upstream (Basic Auth unchanged). With it on, OIDC replaces `--web-basic-auth`.

## Endpoints

| Path | Purpose |
| --- | --- |
| `/oauth/login/<preset>` | Starts the login (`?next=/path` for a same-origin return path) |
| `/oauth/callback` | Redirect URI to register at the provider |
| `/oauth/logout` | Revokes the Atlantis session |

`/events`, `/healthz`, `/readyz` and `/status` are never protected, so VCS webhooks are unaffected. Everything else, including `/api/*`, requires a session (UI requests are redirected to the login, API requests get `401`).

Register `https://<atlantis-url>/oauth/callback` as the redirect URI at the provider.

## Config file

```yaml
session:
  ttl: 8h                       # default 8h
  cookie_name: atlantis_fork_session
presets:
  <name>:
    provider: okta              # optional: okta|entra|google|gitlab|keycloak|authentik (defaults only)
    issuer: https://...         # must match the "iss" claim; discovery uses <issuer>/.well-known/openid-configuration
    client_id: ...
    client_secret_env: OIDC_CLIENT_SECRET   # preferred; or client_secret: inline
    redirect_url: https://atlantis.example.com/oauth/callback
    scopes: [openid, profile, email]        # "openid" is always added
    claim_map:                              # claim names, defaults below
      subject: sub
      email: email
      name: name
      groups: groups
    auth_params:                            # extra authorization request parameters
      prompt: select_account
```

The ID token claims are mapped to `oidc.Identity` and stored in the request context. They are not yet used for authorization (US-031).

## Provider examples

### Okta

```yaml
presets:
  okta:
    provider: okta              # adds the "groups" scope
    issuer: https://acme.okta.com          # or https://acme.okta.com/oauth2/default
    client_id: 0oa...
    client_secret_env: OIDC_CLIENT_SECRET
    redirect_url: https://atlantis.example.com/oauth/callback
```

Add a `groups` claim to the ID token in the authorization server.

### Microsoft Entra ID

```yaml
presets:
  entra:
    provider: entra             # email <- preferred_username
    issuer: https://login.microsoftonline.com/<tenant-id>/v2.0
    client_id: <application-id>
    client_secret_env: OIDC_CLIENT_SECRET
    redirect_url: https://atlantis.example.com/oauth/callback
    claim_map:
      groups: groups            # configure "groups" optional claim; or use "roles" for app roles
```

### Google

```yaml
presets:
  google:
    provider: google
    issuer: https://accounts.google.com
    client_id: <id>.apps.googleusercontent.com
    client_secret_env: OIDC_CLIENT_SECRET
    redirect_url: https://atlantis.example.com/oauth/callback
    auth_params:
      hd: example.com           # optionally restrict to a Workspace domain
```

Google ID tokens carry no groups.

### GitLab

```yaml
presets:
  gitlab:
    provider: gitlab            # groups <- groups_direct
    issuer: https://gitlab.example.com
    client_id: <application-id>
    client_secret_env: OIDC_CLIENT_SECRET
    redirect_url: https://atlantis.example.com/oauth/callback
```

### Keycloak

```yaml
presets:
  keycloak:
    provider: keycloak
    issuer: https://keycloak.example.com/realms/<realm>
    client_id: atlantis
    client_secret_env: OIDC_CLIENT_SECRET
    redirect_url: https://atlantis.example.com/oauth/callback
```

Add a "Group Membership" mapper to the client to emit `groups`.

### Authentik

```yaml
presets:
  authentik:
    provider: authentik
    issuer: https://authentik.example.com/application/o/<slug>/
    client_id: <id>
    client_secret_env: OIDC_CLIENT_SECRET
    redirect_url: https://atlantis.example.com/oauth/callback
```

## Operational notes

- Sessions are in memory; a restart (or a second replica) requires a new login.
- Cookies are `HttpOnly`, `Secure`, `SameSite=Lax`. `Secure` is dropped only if `redirect_url` is `http://` (local development).
- Discovery happens at startup; Atlantis refuses to start if the issuer cannot be reached or the preset is invalid.
- Logout revokes the Atlantis session only, not the provider session.
