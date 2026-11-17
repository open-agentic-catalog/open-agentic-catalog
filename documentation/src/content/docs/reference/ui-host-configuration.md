---
title: UI host configuration
---

The optional `catalog-ui` host serves the browser client and forwards authorized requests to one Catalog API. It has no database connection. Browser, API and identity-provider origins can differ.

## Host configuration

Start the host with its required JSON file:

```sh
./bin/catalog-ui --config /path/to/ui.json
```

The binary reads no `.env`, backend configuration or launcher environment overrides. Logging defaults and the disabled public-discovery flag are built in. Local launchers and Helm generate this independent file from their documented inputs. See [configuration delivery](configuration.mdx#configuration-delivery-and-precedence) and [CLI invocation](commands.md#optional-ui-host).

Configuration is a regular UTF-8 JSON file of at most 64 KiB. Field names are case-sensitive. Only the documented fields are accepted. Duplicate or unknown fields, invalid Unicode, explicit nulls and trailing values fail validation. Relative file and asset paths resolve from the process's working directory, not beside the JSON file. Use absolute mounted paths in containers.

Supply the required fields explicitly. This production example selects separately operated HTTPS services and mounted credentials; replace their addresses, client registration and file paths:

```json
{
  "logLevel": "info",
  "logFormat": "json",
  "listen": "0.0.0.0:8080",
  "environment": "production",
  "origin": "https://browse.example.com",
  "basePath": "",
  "assets": "/app/assets",
  "upstream": "https://catalog.example.com",
  "cookieName": "catalog-session",
  "keysFile": "/run/secrets/ui-keys.json",
  "sessionSeconds": 3600,
  "requestBytes": 2097152,
  "responseBytes": 16777216,
  "timeoutSeconds": 60,
  "authMode": "oidc",
  "issuer": "https://identity.example.com/realms/catalog",
  "clientId": "catalog-ui",
  "clientSecretFile": "/run/secrets/ui-client-secret",
  "scopes": ["catalog.read"]
}
```

This example grants no Catalog permissions. Add the OAuth scopes required by the intended actions when the API enforces them. For example, human rating writes require `ratings.write` as well as current Catalog authority. Follow [sign-in setup](../operations/sign-in.md) for client registration, provider users and Catalog memberships.

## Configuration fields

Required values have no binary defaults, except for `logLevel` and `logFormat`. The optional `publicDiscoveryEnabled` boolean defaults to false. Omitted optional strings are empty; omitted `scopes` is empty. Launcher and chart defaults do not change direct-binary validation.

| Field | Requirement, default and bound |
| --- | --- |
| `logLevel` | Default `info`; accepts `debug`, `info`, `warn`, `error`. |
| `logFormat` | Default `text`; accepts `text`, `json`. |
| `listen` | Required host and port, such as `127.0.0.1:4180` locally or `0.0.0.0:8080` in a container. |
| `environment` | Required `development` or `production`. |
| `origin` | Required exact public HTTP(S) origin, without a path or trailing slash. No credentials, query or fragment. HTTP requires development and `localhost` or a loopback IP. |
| `basePath` | Optional; empty or omitted for root, or canonical slash-separated letters, digits, underscores and hyphens. No trailing slash. `/docs` and its descendants are reserved. |
| `assets` | Required built-asset directory. It contains `index.html` and its compiled `assets/` files. Packaged images also include the validated `docs/` bundle. |
| `upstream` | Required fixed Catalog HTTP(S) origin, without a path or trailing slash. No credentials, query or fragment. Private HTTP is permitted; deployment networking must protect it. |
| `apiCAFile` | Optional API PEM roots. These replace public roots for this transport; they do not affect issuer trust. |
| `cookieName` | Required 1-48-character name matching `[a-z][a-z0-9-]{0,47}`. Use distinct names for deployments on the same hostname, including different ports. |
| `keysFile` | Required session-key JSON file. See [file inputs](#file-inputs). |
| `sessionSeconds` | Required integer 60-28,800. Absolute session lifetime, capped further by access-token expiry. |
| `requestBytes` | Required integer 1-67,108,864. Independent API request limit. Helm and local launchers supply 2,097,152 bytes by default. |
| `responseBytes` | Required integer 1-67,108,864. Independent API response limit. Helm and local launchers supply 16,777,216 bytes by default. |
| `timeoutSeconds` | Required integer 1-300. API request timeout; the local launcher supplies 60. |
| `authMode` | Required `development` or `oidc`; their inputs are mutually exclusive. |
| `developmentUsersFile` | Required only in development authentication; forbidden in OIDC mode. Contains 1-32 fixed users. |
| `issuer` | Required only in OIDC mode. Exact credential-free issuer URL; HTTPS in production. Its realm path and trailing slash, if any, are part of the issuer identity. |
| `issuerCAFile` | Optional OIDC PEM roots; independent of API trust. Forbidden in development authentication. |
| `clientId` | Required nonempty confidential web-client ID in OIDC mode. Forbidden in development authentication. |
| `clientSecretFile` | Required nonempty secret-file path in OIDC mode. Forbidden in development authentication. |
| `scopes` | Optional additional OIDC scopes, at most 16 distinct values. Each is 1-128 bytes with no spaces, tabs or newlines. `openid` is automatic and must not be repeated. `offline_access` is rejected. Forbidden in development authentication. |
| `publicDiscoveryEnabled` | Optional boolean, default `false`. Enables `/discover` and three credential-free public ARD forwarding operations on the fixed upstream. Does not enable a backend publication. |
| `documentationUrl` | Optional credential-free HTTPS URL, without query or fragment. Empty selects `/docs/` when the bundled manual is present. Without a bundle or override, the UI omits the link. This is navigation, not a proxy target. |

The request and response limits are separate from backend admission and client retrieval limits. See [payload and client limits](configuration.mdx#payload-and-client-limits). Logging follows the [UI host event policy](../operations/observability.mdx#ui-host-events).

## Optional public discovery

Set `"publicDiscoveryEnabled": true` in the host JSON to enable `/discover` beneath `basePath`. Restart the host after changes. With the setting omitted or false, both the page and its `/_discovery/` forwarding routes return 404. Disabling the UI reader does not withdraw an active backend publication. Use **Unpublish host** to withdraw discovery for that host.

The API must explicitly admit an anonymous discovery binding for the hostname in `upstream`. A publisher must build and activate a snapshot for that exact binding. The browser-facing UI hostname does not select the publication. For example, `"upstream": "http://api:8080"` selects the backend binding `api`, not `127.0.0.1` or the public UI domain. Do not use forwarded headers or Host substitution to select another feed. Follow [discovery host configuration](configuration.mdx#discovery-host-configuration) and [publication](../guides/publishing.mdx#manage-discovery-snapshots-in-the-ui).

The host admits only these operations beneath `basePath`:

| UI host route | Fixed API operation | Inputs |
| --- | --- | --- |
| `GET /_discovery/manifest` | `GET /.well-known/ard.json` | None. |
| `GET /_discovery/agents` | `GET /ard/v1/agents` | Optional `pageSize` (1-100) and opaque `pageToken` (at most 4,096 bytes). |
| `POST /_discovery/search` | `POST /ard/v1/search` | Closed JSON: nonblank `text` (at most 500 Unicode code points), required `pageSize` (1-100), optional `pageToken` (at most 4,096 bytes). |

These are browser-host routes, not additions to the Control API. Every request requires `X-Catalog-Request: 1`. Search also requires the exact browser Origin. Foreign Origin and Fetch Metadata, forwarding-authority headers, unknown inputs and redirects are refused. Configured admission, byte limits, API trust and timeout apply. The browser additionally limits responses to 16 MiB and reads to 30 seconds.

Public bootstrap and reads omit browser credentials. The host never reads a session or forwards cookies, bearer tokens or caller-selected authority on these paths. Search constructs `federation: "none"` itself and accepts no remote contexts or peer selection. The optional configured embedding provider can still receive text; the visitor must acknowledge this before searching. Runtime and public metadata responses use `no-store`; upstream reads request revalidation. Refresh is explicit after an invalid continuation or unavailable publication.

For local Compose, set `CATALOG_UI_PUBLIC_DISCOVERY=on` with `LOCAL_UI=on`. Supply the backend's complete intended binding array through `CATALOG_DISCOVERY_HOSTS`, including an anonymous `api` binding for the selected tenant. Preserve any other required bindings. Activate that host through **Discovery publication** in the private workspace. For a fresh development tenant, the array can contain:

```json
[{"host":"api","tenantId":"00000000-0000-7000-8000-000000000001","mode":"anonymous","publisherDomain":"catalog.example.com"}]
```

This is an explicit publication choice. Do not replace retained deployment bindings or publish private data merely to enable the UI. `make ui-dev CATALOG_UI_PUBLIC_DISCOVERY=on` instead uses its literal-loopback API origin; that API needs the corresponding anonymous binding and active snapshot. Helm uses `ui.config.publicDiscoveryEnabled: true` and the same hostname rule for its API Service. The flag changes neither backend authentication nor readiness.

## File inputs

Protect credentials and keys through deployment permissions and scoped mounts. The host accepts a symlink that resolves to a bounded regular file, including projected Secrets. It rejects directories, devices and FIFOs. It reads these inputs at startup; replacing the file alone does not reload a running host.

| Input | Format and maximum size |
| --- | --- |
| Configuration | Strict JSON object, 64 KiB. |
| `keysFile` | Strict JSON array, 64 KiB, with 1-3 objects containing only `hash` and `block`. Each `hash` encodes 64 random bytes as hex; each `block` encodes 32 random bytes as hex. The first pair writes cookies; all pairs can read them. |
| `developmentUsersFile` | Strict JSON array, 64 KiB, with 1-32 objects. Each has `alias`, `label`, `token` and optional `description`, `profileGroup`, `elevated`, `separateTenant`. `profileGroup` is `bundle` or `template`; omit it for Other configured identities. The two flags are optional booleans, false when omitted. Aliases are unique and use the cookie-name pattern. Labels must be nonblank and at most 100 bytes; descriptions are at most 300 bytes. Display text rejects control characters. Tokens are 1-16,384 printable ASCII bytes without whitespace. |
| `clientSecretFile` | Regular text file, 16 KiB. Surrounding whitespace is trimmed; the result must be nonempty. |
| `apiCAFile`, `issuerCAFile` | Independent regular PEM files, each 1 MiB. Each must contain at least one valid certificate. Supplied roots replace that client's public pool. Hostname verification and TLS 1.2 or later remain required. |
| `assets/index.html` | Readable regular file, at most 1 MiB, with at least one compiled asset reference. Referenced entry assets must exist as nonempty regular files. |

Generate independent random cookie keys. Do not reuse a JWT signing key, API token or client secret as a cookie key. Keep development-user tokens on the host; browser login options expose only their aliases and display text.

The three presentation fields only group and annotate development choices. `elevated` marks configured administrative intent; `separateTenant` marks the other tenant. They never change the selected principal, actor type, tenant mapping or grants. Host `/auth/options` returns these public hints without tokens. The browser validates their types and finite groups. Check My access for current authority. Regenerate mounted user inputs and restart the host after editing them; OIDC mode has no development roster.

## Authentication and forwarding

Development authentication requires a loopback development UI origin. It cannot start in production or with OIDC settings. OIDC requires the issuer, client ID and secret file, with no development-user file. The public runtime response contains only the base path, login mode and documentation link.

OIDC uses Authorization Code with PKCE, state and nonce checks. Register the exact callback `ORIGIN/BASE_PATH/auth/callback` for a confidential client. The ID token targets that UI client; the access token targets the Catalog API. Login also requires the API to accept the token for the same active human subject. Provider users, passwords, MFA and provider logout stay with the identity provider.

Provider discovery and login are explicit network operations. Authorization, token and key endpoints remain on the configured issuer origin. Issuer requests have a 15-second timeout and 1 MiB response ceiling. API and issuer clients have separate connections and trust. They do not follow redirects or use proxy environment variables.

Session cookies are authenticated and encrypted, HttpOnly, SameSite=Lax, and Secure for HTTPS. All replicas need the same deployment configuration and key ring. No session database, Redis or affinity is required. Encoded cookie values have a 3,600-byte ceiling; oversized tokens fail login. Pending login state expires after five minutes, with at most four pending login cookies.

The host retains no refresh token. Activity does not extend the absolute expiry. Sign in again after expiration. Local sign-out clears this UI's cookie; it does not revoke a bearer token or end the provider session. Cross-tab notifications clear private browser state. Only appearance preferences use local storage.

Every cookie-authenticated session/API request requires `X-Catalog-Request: 1`. Non-GET requests also require the exact Origin. The host rejects foreign Origin and Fetch Metadata and grants no CORS preflight. OIDC callbacks validate their temporary state cookie separately.

The forwarding allowlist derives from the embedded OpenAPI's product routes, plus version and contract downloads. It excludes worker operations, metrics, arbitrary destinations and upstream redirects. The host forwards the session's bearer token and required API headers. Browser cookies, Authorization overrides, tenant selectors and forwarding headers are excluded. Private responses use `no-store`; hashed static assets are cached. At most 32 session/API requests run concurrently per host.

ETags, digests, request IDs, `Idempotency-Replayed` and applicable `Retry-After` values remain available to the browser. The backend still enforces authorization, idempotency and concurrency. UI permissions never replace those checks.

## Restart and rotation

Restart the host after changing configuration, session keys, development users, client secrets, trust or assets. Coordinate replicas so they share the intended inputs. Changing mode, origin, upstream, base path, cookie name, issuer or client ID invalidates existing deployment-bound cookies. Requested-scope changes apply to new sign-ins. Sign out and sign in again to obtain them; existing cookies and JWT scopes remain unchanged.

Add the new key after the current writing key in the key ring for routine rotation. Deploy that configuration. Move the new key to the first position in a second rollout. Retain old keys until sessions and five-minute callbacks from the last old-key writer expire. Follow [UI session-key rotation](../operations/maintenance.mdx#rotate-ui-session-keys) for ordering and emergency removal.

The root `/livez` and `/readyz` endpoints report local process state. They do not test the API or issuer. During shutdown, the host reports not ready and stops accepting new authenticated and public discovery requests. It allows up to 15 seconds for in-flight requests. The fixed `catalog-ui --probe` checks only loopback port 8080; use [operator probes](commands.md#configuration-diagnostics-and-probes) for another listener port.

The listener uses a 10-second header timeout, `timeoutSeconds` for reads, `timeoutSeconds + 5` for writes, a 60-second idle timeout and a 32 KiB header ceiling. Login or API failures return bounded errors without making static pages unavailable.
