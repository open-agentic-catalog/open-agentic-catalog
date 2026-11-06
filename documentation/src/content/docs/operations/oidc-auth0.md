---
title: Sign in with Auth0
---

Configure [Auth0](https://auth0.com/docs) with a custom API and separate browser and human CLI applications as needed. These instructions describe provider settings and Catalog requirements. Hosted Auth0 login is not part of the maintained local qualification.

Complete the [shared authentication setup](sign-in.md) first. Use one exact issuer domain throughout discovery, token issuance and Catalog configuration. Preserve its trailing slash when the issuer includes one.

## Register the API and audience

1. Create a [custom API](https://auth0.com/docs/get-started/apis) with identifier `https://catalog.example.com`. This identifier becomes the Catalog API audience.
2. Select asymmetric signed JWT access tokens, such as RS256. Do not enable encrypted access tokens for Catalog.
3. Define required API permissions if you plan to enforce Catalog's OAuth ceiling. Use the exact names from the [Catalog scope matrix](../reference/api-conventions.md).
4. Set the tenant's **Default Audience** to the API identifier before using built-in browser or human CLI login.

Catalog's login clients do not send Auth0's `audience` parameter. Auth0's [access-token guide](https://auth0.com/docs/secure/tokens/access-tokens/get-access-tokens) describes the default-audience option. Without a selected API audience, an access token can be opaque and unusable by Catalog.

:::caution[Tenant-wide setting]
The [default audience](https://support.auth0.com/center/s/article/value-of-the-audience-attribute-in-access-token-requests-and-its-significance) affects authorization requests across the Auth0 tenant. Review other applications before changing it. If this setting is unsuitable, use an external token-acquisition client; built-in login has no per-client audience override.
:::

Configure this `auth` object in your complete backend profile:

```json
{
  "mode": "oidc",
  "issuer": "https://YOUR_TENANT.auth0.com/",
  "audience": "https://catalog.example.com",
  "tenancyMode": "dedicated",
  "dedicatedTenantId": "00000000-0000-7000-8000-000000000001",
  "enforceScopes": false
}
```

Use your actual regional or custom domain instead of the placeholder. If you use a custom domain, use it consistently for the issuer and client flows. Check advertised endpoints against the [Catalog origin restrictions](sign-in.md#check-compatibility-before-deployment).

## Register the browser application

Create a [Regular Web Application](https://auth0.com/docs/get-started/applications/application-settings), not a Single Page Application. The Go UI host holds the client secret and exchanges the code.

- Enable Authorization Code and require S256 PKCE under the available client policy.
- Register the exact allowed callback `https://browse.example.com/auth/callback`. Include the configured UI base path when present.
- Use a client-secret authentication method supported by the token endpoint, such as `client_secret_post` or `client_secret_basic`.
- Set UI `issuer` to the exact selected Auth0 issuer URL, including its trailing slash.
- Set UI `clientId` to this Regular Web Application's client ID.
- Mount the client secret through UI `clientSecretFile`.
- Request only necessary scopes through UI `scopes`. The host adds `openid` and rejects `offline_access`.

Follow [browser configuration](sign-in.md#add-optional-browser-login) for cookie keys, Helm Secret keys and trust mounts. The UI signs out locally; it does not call Auth0's logout endpoint or revoke its issuer session.

## Register the human CLI application

Create a separate [Native application](https://auth0.com/docs/get-started/auth0-overview/create-applications/native-apps) for `catalogctl`. Enable Authorization Code with S256 PKCE and set the token endpoint authentication method to `none`. The CLI does not use a client secret.

Register the exact loopback callback `http://127.0.0.1:49152/oauth/callback` and select that port with `--listen`. Auth0 documents [loopback callbacks for desktop applications and CLIs](https://auth0.com/docs/secure/security-guidance/measures-against-app-impersonation). Open the authorization URL on the same machine as the CLI.

The tenant's default audience must select your Catalog API. Verify that the ID token names the CLI client, and the access token names that API. If you need renewal, enable the Refresh Token grant and the API's Allow Offline Access setting. Review [refresh token rotation and expiry](https://auth0.com/docs/secure/tokens/refresh-tokens).

```sh
unset CATALOG_REMOTE_TOKEN CATALOG_REMOTE_TOKEN_FILE
mkdir -m 700 "$HOME/.catalog-login"
catalogctl auth login \
  --issuer https://YOUR_TENANT.auth0.com/ \
  --client-id YOUR_NATIVE_CLIENT_ID \
  --base-url https://catalog.example.com \
  --listen 127.0.0.1:49152 \
  --file "$HOME/.catalog-login/auth0.json" \
  --scope offline_access

catalogctl remote capabilities \
  --base-url https://catalog.example.com \
  --login-file "$HOME/.catalog-login/auth0.json"
```

Omit `--scope offline_access` if you do not enable refresh. Add required API scopes when the Catalog enforces an OAuth ceiling. Provision the human's exact issuer and subject in Catalog before login. Follow the [CLI procedure](../reference/commands.md#human-cli-login) for private trust, credential protection, logout and failure recovery.

## Use an external client for CLI credentials

Use an external client if you cannot select the tenant-wide default audience or need provider-specific authorization parameters. Preserve the exact issuer, including its trailing slash.

Use an approved external OAuth client to obtain the API access token. Follow Auth0's [native application setup](https://auth0.com/docs/get-started/auth0-overview/create-applications/native-apps) if you manage a public native client. Use Authorization Code with S256 PKCE, the API audience and that client's exact callback. That client owns token renewal and protected file replacement.

Supply the resulting token to Catalog through a private file:

```sh
catalogctl remote capabilities \
  --base-url https://catalog.example.com \
  --token-file /run/secrets/catalog-api-token
```

Follow [remote command settings](../reference/commands.md#remote-catalog-reads) for file permissions and private API trust. This mode does not use the CLI login cache or refresh automatically. If the external client needs refresh, configure Auth0's Refresh Token grant and API Allow Offline Access setting. Its [refresh-token guide](https://auth0.com/docs/secure/tokens/refresh-tokens) describes rotation and expiry.

## Provision permissions and verify

Provision the exact access-token subject, such as the provider-assigned `auth0|...` value, through [Catalog identity setup](database-roles.mdx#initial-catalog-identity-provisioning). Do not use an email address as a substitute. Auth0 roles, groups and `permissions` arrays do not create Catalog grants.

Enable `auth.enforceScopes` only after verifying the access token's string `scope` and the requested names. Auth0 permission configuration and Catalog's retained permissions remain separate checks.

For automation, authorize a separate Machine to Machine application for the API. Follow [Auth0's client credentials flow](https://auth0.com/docs/get-started/authentication-and-authorization-flow/client-credentials-flow) with an explicit API audience. Bind its actual subject to a Catalog service membership. Do not rely on the browser tenant default for service-token acquisition.

Follow the [shared verification checklist](sign-in.md#verify-your-deployment). Check API acceptance, browser login and token-file CLI reads independently. Keep unnecessary claims out of the access token to stay within the UI cookie limit. Auth0 Organizations that require an `organization` request parameter need an external client or a separately qualified issuer policy; built-in login has no such parameter.
