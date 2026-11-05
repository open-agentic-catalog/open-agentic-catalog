---
title: Configure authentication and sign-in
---

Configure the Catalog API to accept signed [JWT](../reference/glossary.md#jwt) [access tokens](../reference/glossary.md#access-token-and-id-token) from your selected [OpenID Connect (OIDC)](https://openid.net/specs/openid-connect-core-1_0.html) [issuer](../reference/glossary.md#issuer). Register separate clients for optional browser and human CLI login. The issuer manages accounts, credentials and token issuance. Catalog manages its own tenant mappings, memberships and permissions.

This procedure connects the existing configuration and identity controls. The [authentication configuration](../reference/configuration.mdx#authentication-and-catalog-identity) owns accepted claims and exact backend settings. The [UI chart reference](../../../../../deployments/helm/open-agentic-catalog/README.md#optional-catalog-ui) owns chart settings and mounted browser inputs. Operators must qualify their selected issuer and client configuration.

:::note[Login does not grant access]
An issuer login confirms identity. Catalog membership and current permissions determine access to the Catalog.
:::

Development mode instead offers explicit local choices grouped as workflow bundles or exact templates, with counts, search and elevated/separate-tenant captions. These are shortcuts, not production accounts or passwords. Their descriptions summarize the configured development grants. Retained grants can differ.

Check My access for the current permission and OAuth view. Reader includes eligible personal ratings and private local reads; exact Discovery reader has no local Resource reads or operational Job authority by itself. See the [development identity matrix](../guides/access-management.mdx#development-identities-and-role-handoffs).

## Choose development or regular sign-in

| Setup | Start or configure | Sign-in screen |
| --- | --- | --- |
| Passwordless development | Use the existing `make compose-up LOCAL_UI=on` workflow. Keep API `auth.mode: development` and UI `authMode: development`. | Choose a development identity. These shortcuts have no passwords. |
| Local passwords | Use [one local Catalog with Keycloak](oidc-keycloak.md#start-one-local-catalog-with-passwords), through `make local-auth-up`. | Select **Sign in**, then enter a Keycloak username and password. |
| Production | Configure the API and UI for OIDC with your production issuer. Follow the settings below. | Select **Sign in**, then authenticate with your provider. |

Changing `environment` does not select an authentication mode. Both the API and UI must use the intended mode. The UI hides development choices in OIDC mode; the API still enforces authentication and permissions.

### Replace development settings for production

Start from `config/production.example.json`. Keep its other required database, worker, HTTP and operational settings. Set these fields for your deployment:

```json
{
  "environment": "production",
  "profile": "production",
  "auth": {
    "mode": "oidc",
    "issuer": "https://identity.example.com/realms/catalog",
    "audience": "open-agentic-catalog",
    "tenancyMode": "dedicated",
    "dedicatedTenantId": "00000000-0000-7000-8000-000000000001",
    "trustBundleFile": "/run/catalog-trust/issuer-ca.pem",
    "enforceScopes": false
  }
}
```

Replace the tenant ID and issuer with your provisioned values. Omit `trustBundleFile` when the issuer uses publicly trusted certificates. Configure OAuth scopes separately if you enable `enforceScopes`; Catalog grants remain necessary in either case.

Set the UI fields below in its existing configuration. Keep its required listen, asset and request-limit settings:

```json
{
  "environment": "production",
  "authMode": "oidc",
  "origin": "https://browse.example.com",
  "upstream": "https://catalog.example.com",
  "issuer": "https://identity.example.com/realms/catalog",
  "clientId": "catalog-ui",
  "clientSecretFile": "/run/catalog-ui/client-secret",
  "keysFile": "/run/catalog-ui/keys.json",
  "issuerCAFile": "/run/catalog-trust/issuer-ca.pem",
  "apiCAFile": "/run/catalog-trust/api-ca.pem"
}
```

Register the exact UI callback, `https://browse.example.com/auth/callback`, for an empty `basePath`. Omit each UI CA field when its destination uses public trust. Supply private trust independently for the issuer and API. Mount the client secret and cookie keys as protected files.

Remove backend `auth.developmentTokens` and UI `developmentUsersFile`. Remove their development mounts and conflicting configuration overrides. Restart or recreate the affected processes with the replacement inputs. A cookie from development mode cannot authenticate an OIDC host, even when both hosts share cookie keys. Sign in again through the provider.

Production rejects development authentication. OIDC rejects mixed development inputs. Keep deliberately public documentation, health and discovery routes governed by their existing settings.

Use the [API authentication reference](../reference/configuration.mdx#authentication-and-catalog-identity), [browser setup](#add-optional-browser-login), and [Helm UI settings](../../../../../deployments/helm/open-agentic-catalog/README.md#optional-catalog-ui) for complete delivery details. The local Keycloak `start-dev` deployment remains a test environment. Manage the production provider and its accounts separately.

## Choose an identity provider

Start with this page for the shared setup. Then follow the guide for your provider.

| Provider | Guide and current boundary |
| --- | --- |
| [Keycloak](oidc-keycloak.md) | Complete API, browser and human CLI setup. Maintained tests use an owned local Keycloak deployment. |
| [Microsoft Entra ID](oidc-entra.md) | API token setup, tenant-specific issuers and application registration. Built-in human login has subject and scope limitations. |
| [Auth0](oidc-auth0.md) | Custom API, browser and human CLI setup with a default audience. Hosted flows require operator qualification. |
| [Okta](oidc-okta.md) | Custom authorization server, matching subjects and a string `scope` claim. The org authorization server is unsuitable. |
| [IBM Security Verify](oidc-ibm-security-verify.md) | JWT access tokens, application entitlements, Catalog membership and separate browser/CLI clients. Hosted login remains unqualified. |
| [Amazon Cognito](oidc-cognito.md) | Resource-bound API access tokens. Its split endpoint origins prevent built-in browser and CLI login. |

The hosted-provider guides describe configuration and limits from provider documentation and Catalog code. They are not live-provider qualification results. Catalog tests do not contact those hosted providers.

For local passwords, start with [one Catalog and two prepared accounts](oidc-keycloak.md#start-one-local-catalog-with-passwords). Use the [interactive Keycloak tutorial](oidc-keycloak.md#run-the-interactive-local-tutorial) for the separate A/B federation setup. It starts an isolated issuer, PostgreSQL, Catalog API, worker and UI. The procedure includes initial password changes, administrator resets, ordinary Reader grants and membership suspension. The automated OIDC test targets remove their environments; they are not interactive startup commands.

### Decide where to manage an account

| Change | Owner and effect |
| --- | --- |
| Create a person, change/reset a password, require MFA or disable issuer login | The identity provider. Existing bearer tokens can remain valid until expiry. |
| Add or revoke Catalog permissions | Catalog role assignments in the exact tenant or namespace. Issuer groups and roles do not create grants. |
| Suspend/reactivate Catalog access | Catalog membership administration. A suspended membership cannot use an otherwise valid JWT. |
| Create database logins or rotate database passwords | The database operator, separately for schema owner, API and worker. These are not human Catalog accounts. |

[OAuth2 Proxy](https://oauth2-proxy.github.io/oauth2-proxy/) delegates login to an identity provider; it does not store users' passwords. The built-in UI already supports direct OIDC login. A proxy can form part of an operator's ingress, but identity headers do not replace the Catalog API's signed-token validation and current membership checks.

### Check compatibility before deployment

Catalog is an OAuth resource server with selected OIDC clients. An OIDC provider can support the protocol while differing from these client requirements:

- The API requires a signed JWT access token with its configured `aud`. A `client_id` claim does not replace `aud`.
- Only a whitespace-separated string `scope` supplies the optional OAuth ceiling. Catalog does not read `scp`, scope arrays or provider `permissions` claims.
- Browser and human CLI login require the ID token's `sub` to equal the access-token subject returned by `/api/v1/me`. There is no configurable `oid`, email or username substitution.
- The UI and CLI confine issuer HTTP requests to the configured origin, including discovery, token and signing-key requests. Authorization endpoints must also use that origin.
- Built-in login sends standard code/PKCE parameters and configured scopes. It has no extra `audience`, `resource`, `organization` or provider-specific parameter setting.
- The configured issuer must match discovery metadata and signed tokens exactly, including any trailing slash. API, browser and human CLI inputs preserve it.

An origin consists of scheme, host and port. Different paths can share an origin. Do not append query parameters to the configured issuer or replace it with a login endpoint.

Use dedicated tenancy unless you need an explicit organization mapping within one issuer. A provider tenant, directory or user pool is not automatically a Catalog tenant. Shared mode requires an exact string claim and a provisioned organization mapping. Built-in login has no organization chooser.

## Configure the API and tenant mapping

1. Select the exact issuer URL and a Catalog API audience. Use HTTPS in production. Configure the issuer to issue compact, asymmetrically signed JWT access tokens with that audience. Opaque access tokens do not meet the API contract.
2. Set `auth.mode` to `oidc`, `auth.issuer` to the exact issuer, and `auth.audience` to the API audience. Tokens need matching `iss` and `aud`, a non-empty `sub`, and valid `iat` and `exp` claims. Optional `nbf` must also be valid.
3. Select one tenant mapping. Dedicated mode uses `auth.tenancyMode: dedicated` and `auth.dedicatedTenantId`; omit `organizationClaim`. Shared mode uses `auth.tenancyMode: shared` and `auth.organizationClaim`; omit `dedicatedTenantId`. Shared tokens must contain the configured claim as one bounded string.
4. Decide whether to enable `auth.enforceScopes`. When enabled, the token's OAuth scopes limit permitted actions. Scopes never grant Catalog permissions. Arrange the required scopes with the issuer and the client that obtains the token.
5. Configure issuer trust before starting the API. Use [OIDC private trust](../reference/configuration.mdx#oidc-private-trust) for private certificate authorities. This API trust input does not configure the UI, federation or webhooks.
6. Apply migrations and provision the initial Catalog identity through the [schema-owner bootstrap procedure](database-roles.md#initial-catalog-identity-provisioning). Provision the exact issuer realm, tenant or external organization mapping, subject membership and initial administrator authority.
7. Start the API with its own least-privilege database identity. Obtain a token through the issuer's supported client procedure. Verify an authenticated `GET /api/v1/me` and an intended authorized read.

Unknown realms, organization mappings and subjects fail authentication. Suspended memberships and ambiguous selectors also fail closed. Signing in at the issuer does not create a Catalog membership. Token-carried tenant UUIDs, roles, groups and namespace grants do not grant Catalog authority.

After bootstrap, use [access management](../guides/access-management.mdx) to bind existing issuer identities and assign current Catalog permissions. Use the exact token subject, not an email address or display name unless it is the actual `sub`. Retain tenant and namespace boundaries when granting access.

## Add optional browser login

Complete the API setup first. Browser login needs a provisioned human membership and an API-accepted access token. Service memberships cannot establish an OIDC browser session.

1. Select the UI's exact public `origin` and optional `basePath`. Keep the origin free of paths and trailing slashes. Use an empty base path for root, or a canonical path such as `/catalog` without a trailing slash.
2. Register a confidential web client with the issuer. Enable Authorization Code with [S256 Proof Key for Code Exchange (PKCE)](https://www.rfc-editor.org/rfc/rfc7636.html). Register the callback as the exact concatenation `origin + basePath + /auth/callback`.
3. Configure the client to receive an ID token for its UI client ID and a JWT access token for the Catalog API audience. These audiences have separate purposes. The ID token identifies the browser login; the access token authorizes API requests after Catalog membership and permission checks.
4. Set the UI's `authMode: oidc`, `issuer`, `clientId` and `clientSecretFile`. Deliver the client secret through a protected mounted file. Do not put the secret in JSON configuration, Helm values or logs.
5. Set additional UI `scopes` only when needed. The host adds `openid` automatically; do not include it explicitly. `offline_access` is rejected. When API scope enforcement is enabled, request the scopes required by the intended Catalog actions. After changing requested UI scopes, sign out and sign in again. Existing JWTs retain their original scope claim. Private saved-item reads need `catalog.read`; mutations need `personal.write`. Enable `personal.write` in the issuer and request it through UI `scopes` for human library users. The scope does not grant `library.own` or any resource-edit permission.
6. Set `upstream` to the fixed API origin. Configure `apiCAFile` and `issuerCAFile` independently when private trust is needed. Each UI trust file replaces that transport's public trust pool. Include all roots required by its destination.
7. Deliver a protected `keysFile` with the cookie signing and encryption key ring. Use one to three pairs with independent random 64-byte `hash` and 32-byte `block` values, encoded as hex. Use the same deployment configuration, cookie name and key ring on every UI replica. Follow the [UI chart settings](../../../../../deployments/helm/open-agentic-catalog/README.md#optional-catalog-ui) for mounted inputs and [payload configuration](../reference/configuration.mdx#payload-and-client-limits) for forwarding limits.
8. Preserve Host, Origin and the configured base path through the reverse proxy. Do not strip the base path. Start the UI, sign in and verify the subject and tenant in **My access**. Confirm an intended read and an action outside that identity's authority.

For example, `origin: https://browse.example.com` with `basePath: /catalog` requires exactly `https://browse.example.com/catalog/auth/callback`. An empty base path requires `https://browse.example.com/auth/callback`. A different port, hostname, scheme or path changes the registered callback.

The UI requires authorization and token endpoints on the configured issuer origin. It verifies state, nonce and the ID token before asking the API to accept the access token. The API must return the same subject with `actorType: human` before the host issues a session cookie. Provider-specific account setup, passwords, multi-factor authentication and provider logout remain issuer responsibilities.

For Helm, use the [optional Catalog UI settings](../../../../../deployments/helm/open-agentic-catalog/README.md#optional-catalog-ui). The existing UI Secret supplies `keys.json` and `client-secret`; the chart pins their mount paths. API issuer trust, UI issuer trust and UI API trust use separate ConfigMap references. Enabling the UI does not create issuer users or Catalog memberships.

## Register a human CLI client

Register a separate public native client for `catalogctl auth login`. Do not reuse the UI client or its confidential secret. Enable Authorization Code with S256 PKCE. Disable password and implicit flows. Register the `/oauth/callback` URL with a literal loopback IP address. Allow a variable port when supported; otherwise select the registered port with `--listen`.

Issue ID tokens for the CLI client ID and access tokens for the configured Catalog API audience. Configure that audience in the issuer. `openid` is required; optional `offline_access` requests offline refresh. A provider can issue regular refresh tokens without that scope.

Additional scopes only constrain access when Catalog enforces them. Provision the human's issuer and subject in Catalog before login. Issuer groups do not create Catalog permissions.

The CLI validates the ID token, then checks API acceptance and subject agreement through `/api/v1/me`. Saved credentials stay in a caller-selected private file. They have no effect on UI sessions. Follow the [CLI login procedure](../reference/commands.md#human-cli-login) for trust files, refresh, logout and limits.

## Give automation API access

Obtain an API-audience access token through the issuer's supported service-account flow. Bind its exact subject to a Catalog service membership and grant only required permissions. In shared mode, arrange the exact organization claim too. A service identity does not need the browser client, ID token or UI cookie keys.

For automation, deliver an externally obtained token to a protected file and use an explicit API origin:

```sh
catalogctl remote capabilities --base-url https://catalog.example.com --token-file /run/secrets/catalog-api-token
```

The [remote command reference](../reference/commands.md#remote-catalog-reads) owns private API trust and other client options. Token acquisition, renewal and replacement belong to your credential mechanism. Human users can instead use [explicit CLI login](#register-a-human-cli-client). Keep Catalog credentials separate from credentials for discovered resource endpoints.

Follow the [worked Keycloak service-account procedure](oidc-keycloak.md#give-a-service-account-access) for protected `client_credentials` acquisition, the actual token subject, explicit `service_client` plus Reader grants, CLI/SDK reads, denial and token replacement. `service_client` supplies no product permissions. A broad grant does not permit a service identity to submit human-only feedback or ratings, or establish a human browser session.

A human CLI credential file is JSON and belongs to `--login-file`. Remote commands, online import and export accept this file directly. A manual bearer file contains only an access token and belongs to `--token-file`. A UI session cookie is neither format. Use the [private credential conversion](oidc-keycloak.md#prepare-private-http-credentials) when a tutorial needs curl headers or another client's bearer file.

## Deliver the configuration

Update the selected backend profile's `auth` object or its supported `CATALOG_AUTH_*` environment variables. The [configuration reference](../reference/configuration.mdx#authentication-and-catalog-identity) defines precedence and validation. Binaries do not read `.env` automatically.

For Helm, set backend authentication through `configEnv.CATALOG_AUTH_*`. Set browser authentication independently through `ui.config`. For example, these values select a dedicated Keycloak realm:

```yaml
configEnv:
  CATALOG_AUTH_MODE: oidc
  CATALOG_AUTH_ISSUER: https://identity.example.com/realms/catalog
  CATALOG_AUTH_AUDIENCE: open-agentic-catalog
  CATALOG_AUTH_TENANCY_MODE: dedicated
  CATALOG_AUTH_DEDICATED_TENANT_ID: 00000000-0000-7000-8000-000000000001
  CATALOG_AUTH_ENFORCE_SCOPES: "false"
ui:
  enabled: true
  existingSecret: catalog-ui-auth
  config:
    origin: https://browse.example.com
    authMode: oidc
    issuer: https://identity.example.com/realms/catalog
    clientId: catalog-ui
    scopes: []
```

Merge these values into your complete deployment values. They do not supply database credentials, ingress, network policy or initial Catalog membership. Create the named UI Secret with `keys.json` and `client-secret` before deployment. Use the [chart's secret and trust settings](../../../../../deployments/helm/open-agentic-catalog/README.md#optional-catalog-ui); do not place secret values in this YAML.

### Prepare UI secret files

Use your secret-management process to supply the provider's client secret as a private file. Generate independent random cookie keys. For example, run this once in a private operator account:

```sh
umask 077
mkdir -m 700 "$HOME/.catalog-ui-inputs"
python3 - <<'PY'
import json
from pathlib import Path
import secrets

target = Path.home() / ".catalog-ui-inputs" / "keys.json"
with target.open("x", encoding="utf-8") as output:
    json.dump([{"hash": secrets.token_hex(64), "block": secrets.token_hex(32)}], output)
    output.write("\n")
PY
```

The exclusive file creation prevents replacement of an existing key ring. Keep this directory outside the repository. Do not generate different keys for each UI replica. Follow the [rotation procedure](maintenance.mdx#rotate-ui-session-keys) for an existing deployment.

Create the Secret in the existing release namespace. This example assumes the client secret is already in `/protected/provider-client-secret`:

```sh
kubectl -n catalog create secret generic catalog-ui-auth \
  --from-file=keys.json="$HOME/.catalog-ui-inputs/keys.json" \
  --from-file=client-secret=/protected/provider-client-secret
```

This command creates a Kubernetes Secret; it does not register an issuer client or provision Catalog membership. For a standalone UI host, mount the same files and select them with `keysFile` and `clientSecretFile`. See the [UI host configuration](../reference/ui-host-configuration.md#host-configuration).

Permit API discovery/key traffic and UI issuer traffic in your network policy. Browser users must also reach the issuer and the UI callback. Provider guides use public example hostnames; they are not endpoints supplied by this project.

The retained developer Compose stack uses local development identities. `LOCAL_UI=on` does not enable OIDC. Use the disposable [local Keycloak qualification](oidc-keycloak.md#run-the-local-qualification) for repeatable login tests. Use your own complete profiles and secret mounts for an OIDC deployment.

## Verify your deployment

Use RSA signing keys of 4096 bits or fewer when your issuer publishes RSA JWKs. The API and UI reject larger RSA JWKs. This limit bounds key parsing; it does not change the supported signature algorithms.

1. Run offline configuration validation with the selected profile: `catalogctl config validate --config /etc/catalog/config.json --role api`. This checks settings, not issuer reachability.
2. Provision the initial administrator with the exact access-token issuer and subject through the [schema-owner procedure](database-roles.md#initial-catalog-identity-provisioning).
3. Use a protected API access-token file for `catalogctl remote capabilities --base-url https://catalog.example.com --token-file /run/secrets/catalog-api-token`.
4. Read `GET /api/v1/me` through your approved HTTP client or the UI's **My access** page. Confirm the subject, tenant and actor type. Check one permitted read and one action outside that membership's authority.
5. If enabled, sign in through the UI and test the [human CLI client](../reference/commands.md#human-cli-login) separately. Test expiry, sign-out and current membership suspension.
6. If scope enforcement is enabled, confirm the actual `scope` claim and a denied operation outside its ceiling.

Do not paste tokens into public JWT viewers, tickets, shell arguments or logs. Inspect claims only through an approved local process. Decoding a token without signature verification does not prove that it is valid. Keep provider credentials separate from the Catalog schema-owner identity.

## Replace settings and credentials

Issuer, audience, tenant mapping and UI configuration are startup inputs. Restart affected processes after replacement. Mounted client-secret, trust and cookie-key changes do not reload running consumers. Updating an existing external Secret or ConfigMap alone does not establish a rollout.

Use the [per-consumer rotation procedure](maintenance.mdx#rotate-credentials-and-trust) for client-secret and trust replacement. Use [UI session-key rotation](maintenance.mdx#rotate-ui-session-keys) for staged reader/writer overlap across replicas. Changing UI login mode, origin, upstream, base path or client ID invalidates its deployment binding and requires fresh sign-in.

Normal issuer signing-key refresh is separate from startup trust replacement. A known API signing key can bridge an issuer outage within the fixed 15-minute verifier cache. After that bound, a failed refresh rejects the token. Do not assume every request contacts the issuer or immediately observes provider-side token revocation.

The UI retains no refresh token. Session activity does not extend expiry; sign in again when the session expires. Local sign-out clears the UI cookie but does not revoke the bearer or end the issuer session. Catalog suspension, offboarding and grant revocation affect current Catalog access independently of issuer account removal. See [credential and authorization limits](security.md#credential-and-authorization-limits).

### Change an established issuer or subject

Signing-key or client-secret rotation does not change Catalog membership when the exact issuer and subject remain unchanged. Use the rotation procedures above for that case.

A different issuer requires a matching Catalog identity realm. A different `sub` within an existing realm requires a matching membership. Matching emails or display names do not transfer grants, group membership or private saved items. The API accepts one configured issuer; these steps do not provide simultaneous login through two issuers.

Retain the previous issuer setup and subject-mapping policy until replacement access and a recovery path are verified.

For a subject-only change, keep the realm. An administrator in that realm can provision the new subject through ordinary membership administration. Use the owner bootstrap if no usable administrator remains. The bootstrap requires an initial administrator grant; it is not a bulk-user migration command.

1. Inventory the current human and service subjects, realm mappings, direct grants, group paths and namespace scopes.
2. Record the existing tenant UUID and display name, runtime profiles, client settings and protected credential locations.
3. Confirm the new issuer metadata, access-token subjects, API audience and any shared-tenancy organization mapping.
4. Preserve the existing tenant UUID and exact display name in the provisioning inputs.
5. Provision a new realm's initial administrator through [schema-owner identity bootstrap](database-roles.md#initial-catalog-identity-provisioning) when the issuer changes.
6. Validate the replacement API and worker profiles before deployment.
7. Replace the runtime inputs through the normal [deployment procedure](maintenance.mdx#coordinated-release-deployment).
8. Update the UI issuer, client settings and private trust through [configuration delivery](#deliver-the-configuration).
9. Sign in afresh and verify the administrator's exact tenant, realm, subject and current access through `/api/v1/me`.
10. Assign the intended access through [access management](../guides/access-management.mdx), including explicit group membership and namespace scopes.
11. Verify an allowed read and an out-of-scope refusal using each representative new identity.
12. Retire the old identity path only after the new administrator and required service clients work.

Membership histories and private ownership remain bound to their retained IDs. The new identity cannot inherit old bookmarks or saved searches. No identity merge or automatic transfer is available. Assignment and group-member writes require the administrator's authenticated realm. A new-realm administrator cannot revoke old-realm assignments; use the documented [membership lifecycle and realm controls](database-roles.md#tenant-membership-administration).

Use a separate [CLI login file](../reference/commands.md#human-cli-login) for the new issuer and account. Keep the old file separate from new credentials. Fresh UI sign-in is required after an issuer or client-ID change; replacing files alone does not update running clients.

If verification fails, stop the transition. Keep old memberships and the old realm available until recovery is verified. For a subject-policy change, restore the prior provider mapping before using the old membership. Restore the previous complete API/UI settings and trust through the deployment procedure. Then sign in again. Reactivate a suspended realm through the schema-owner [realm procedure](database-roles.md#identity-realm-administration) when necessary.

Do not offboard an old membership as a reversible test; offboarding is terminal. Respect the final-active-realm safeguards before suspension. A configuration rollback does not recreate revoked grants or offboarded memberships.

## Check representative failures

| Symptom | Check and response |
| --- | --- |
| API returns `401` / `UNAUTHENTICATED` | Check token issuer, API audience, signature, times, subject and current Catalog membership. In shared mode, check the exact organization claim and realm mapping. Do not use the UI ID token as the API bearer. |
| API returns `403` / `FORBIDDEN` | Check current permissions for the target tenant or namespace and the optional OAuth ceiling. Browser navigation hints and issuer role/group claims do not grant authority. |
| UI reports `invalid_login_state` | Restart login after stale or missing callback state. Verify the exact origin, base path, callback and shared key ring. Pending login state expires after five minutes. |
| UI reports `invalid_login_identity` or `catalog_login_denied` | Check UI client audience, subject agreement, human membership and API acceptance. Issuer login success alone does not establish Catalog access. |
| UI reports `identity_provider_unavailable` or `catalog_unavailable` | Check the named dependency's reachability, trust and fixed configuration. UI readiness checks local process state, not issuer or API connectivity. |
| UI reports `session_token_too_large` | Reduce unnecessary issuer token claims and retry login. Encoded cookie values are bounded to 3,600 bytes. |

Record the safe error code and request ID when investigating. Follow [UI host events](observability.mdx#ui-host-events) and [troubleshooting](troubleshooting.md). Do not collect bearer tokens, client secrets, cookies or raw login exchanges in support output.

The maintained OIDC fixtures qualify an exact pinned local Keycloak configuration. They cover signed API tokens, dedicated/shared mappings, confidential browser code/PKCE and public CLI login with refresh. They do not establish compatibility with every provider feature or deployment. See [local provider qualification](oidc-keycloak.md#run-the-local-qualification) and the [UI workflow matrix](../../../../../catalog-ui/tests/README.md).
