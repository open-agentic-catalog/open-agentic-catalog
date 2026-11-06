---
title: Sign in with IBM Security Verify
description: Configure IBM Verify JWT access tokens, Catalog membership, browser sign-in and optional human CLI login.
---

Use hosted [IBM Verify](https://www.ibm.com/docs/en/security-verify?topic=applications-openid-connect), also called IBM Security Verify, as the Catalog's OIDC issuer. This guide does not configure self-managed Verify Identity Access or IBM Cloud App ID.

These instructions derive from official IBM documentation and Catalog code. Hosted IBM Verify login has not been tested by this project. The local Keycloak tests do not qualify IBM Verify. The [operator checklist below](#verify-your-deployment) identifies checks for your selected tenant and policy.

Use the [shared authentication setup](sign-in.md) for Catalog prerequisites and deployment steps. You need an existing IBM Verify tenant, application administration access and a Catalog deployment with operator-managed credentials.

## Select the application and issuer

1. Open **Applications > Applications** in the Verify administration console.
2. Add an application with the **OpenID Connect** template.
3. Complete its general settings and open **Sign-on**.
4. Read the application's discovery metadata from its documented endpoint.
5. Copy the exact `issuer` value into the Catalog API and its login clients.

The standard template uses `https://YOUR_TENANT.verify.ibm.com/oauth2/.well-known/openid-configuration`. IBM's [metadata API](https://docs.verify.ibm.com/verify/reference/get_oauth2-well-known-openid-configuration) documents this path. A corresponding issuer example is `https://YOUR_TENANT.verify.ibm.com/oauth2`. Replace the hostname with your tenant's actual domain. Preserve the advertised issuer path and any trailing slash.

Check `authorization_endpoint`, `token_endpoint` and `jwks_uri` before enabling built-in login. All three must use the configured issuer's scheme, hostname and port for the UI and CLI. Different paths on that origin are allowed. A custom domain must preserve these bindings. Do not rewrite an issuer or endpoint to bypass a mismatch.

The older **Custom Application** template uses `/oidc/endpoint/default/.well-known/openid-configuration`. IBM's [template overview](https://www.ibm.com/docs/en/security-verify?topic=applications-openid-connect) distinguishes the two providers. Keep an existing legacy application's issuer and metadata together. Do not combine its endpoints with the `/oauth2` example above.

## Configure API access tokens

Use the application's [token settings](https://www.ibm.com/docs/en/security-verify?topic=sign-configuring-single-in-openid-connect-application):

1. Select **JWT** for **Access token format**.
2. Add `open-agentic-catalog` to **Audiences**, or select another exact Catalog API audience.
3. Configure asymmetric token signing supported by Catalog, such as RS256.
4. Check the resulting access token against the advertised signing keys.

Verify access-token signing separately from the ID-token settings. Its `alg` and `kid` must select an accepted algorithm and matching provider key. Catalog rejects RSA keys larger than 4096 bits. Keep token claims small enough for the [browser session limits](../reference/ui-host-configuration.md#authentication-and-forwarding).

The API accepts signed, unencrypted JWT access tokens. It requires matching `iss` and `aud`, a nonempty `sub`, valid `iat` and `exp`, and a valid `nbf` when supplied. An opaque token, ID token or client ID does not replace an API-audience access token. See the [authentication contract](../reference/configuration.mdx#authentication-and-catalog-identity).

Put this `auth` object in your complete backend profile:

```json
{
  "mode": "oidc",
  "issuer": "https://YOUR_TENANT.verify.ibm.com/oauth2",
  "audience": "open-agentic-catalog",
  "tenancyMode": "dedicated",
  "dedicatedTenantId": "00000000-0000-7000-8000-000000000001",
  "enforceScopes": false
}
```

Replace the issuer and tenant UUID with your provisioned values. Dedicated mode maps the issuer to that Catalog tenant. An IBM tenant does not automatically create a Catalog tenant. Use the [complete configuration procedure](sign-in.md#configure-the-api-and-tenant-mapping) for database settings, identity bootstrap and deliberate shared-tenancy mappings.

### Add an OAuth scope ceiling when needed

Allow the intended Catalog scope names in the application's scope configuration. Request those names through UI `scopes` or repeated CLI `--scope` options. IBM documents allowed scopes in its [client registration settings](https://www.ibm.com/docs/en/security-verify?topic=registration-dynamic-client-new-openid-connect-provider).

Use the exact names from the [Catalog permission and scope matrix](../reference/api-conventions.md). Protected reads use `catalog.read`. Content writes can require `catalog.write`; ratings use `ratings.write`, and private library writes use `personal.write`. Other administrative operations have their own scope requirements.

Check the actual JWT access token before setting `auth.enforceScopes` to `true`. Catalog reads only a whitespace-separated string named `scope`. A token-response field, introspection result, array or similarly named claim does not establish that JWT claim.

IBM's [attribute-mapping guide](https://www.ibm.com/docs/en/security-verify?topic=cssiocoba-openid-connect-introspect-id-token-user-info-mapping) distinguishes introspection from ID-token/userinfo mappings. It reserves `scope`; do not invent a mapper that overwrites it. Qualify the selected application's emitted claim instead.

With enforcement disabled, current Catalog permissions still apply. With enforcement enabled, missing required scopes deny actions even when Catalog grants permit them. IBM API entitlements, groups and application assignments do not create Catalog grants.

## Register the browser client

Use a confidential web client for the Go UI host. React does not receive the client secret.

1. Enable **Authorization Code** with response type `code`.
2. Require PKCE with S256.
3. Leave **Public client (no client secret)** unselected.
4. Select **Client secret basic** or **Client secret POST** for client authentication.
5. Register the exact UI callback URL.
6. Assign the intended users or groups through the application's **Entitlements** tab.

IBM's [application settings](https://www.ibm.com/docs/en/security-verify?topic=sign-configuring-single-in-openid-connect-application) define these controls. An existing user must also be assigned to the application. IBM's [entitlement example](https://www.ibm.com/support/pages/ibm-verify-oidc-auth-method-hcp-boundary) illustrates that separate requirement.

The callback is `origin + basePath + /auth/callback`. For the UI fragment below, register exactly `https://browse.example.com/catalog/auth/callback`. A root UI with an empty `basePath` instead uses `https://browse.example.com/auth/callback`.

Merge these fields into the UI's complete configuration:

```json
{
  "environment": "production",
  "authMode": "oidc",
  "origin": "https://browse.example.com",
  "basePath": "/catalog",
  "upstream": "https://catalog.example.com",
  "issuer": "https://YOUR_TENANT.verify.ibm.com/oauth2",
  "clientId": "YOUR_WEB_CLIENT_ID",
  "clientSecretFile": "/run/catalog-ui/client-secret",
  "keysFile": "/run/catalog-ui/keys.json",
  "scopes": []
}
```

The ID token must name the web client; the access token must name the Catalog API. This example requests only automatic `openid`, with Catalog scope enforcement disabled. Before enabling enforcement, allow the required scopes in Verify and add them to UI `scopes`. The host rejects `offline_access` and retains no refresh token.

Use the ordinary code response in the callback query. Policies that require PAR, signed request objects or JARM do not fit the built-in clients. Neither client supports encrypted ID tokens, private-key client assertions or mTLS client authentication. Catalog does not verify DPoP or certificate-bound access-token proofs. Do not rely on those proof policies at this API.

Some policies require extra `audience`, `resource` or other provider-specific parameters. The built-in clients cannot send them. Use an approved external token-acquisition client if your policy requires them. Deliver its API token through a protected file, as described under [automation API access](sign-in.md#give-automation-api-access). That file does not establish a built-in browser session or a human CLI login file.

## Provision the matching Catalog membership

Determine the access-token `sub` through your issuer's approved administrative or local token-inspection process. Do not substitute an email, username or `uniqueSecurityName` unless it is that exact subject. Do not send tokens to online decoders or retain raw exchanges in support logs. Built-in login requires Catalog membership first; it does not provision unknown subjects.

For human login, compare the ID-token subject with the access-token subject accepted by `/api/v1/me`. They must agree exactly. IBM's [subject mapping](https://www.ibm.com/docs/en/security-verify?topic=cssiocoba-openid-connect-introspect-id-token-user-info-mapping) can affect both ID and JWT access tokens. Check its effect before provisioning; an unrelated custom claim does not replace `sub` in Catalog.

Provision the exact issuer realm, tenant and human subject through [initial identity bootstrap](database-roles.mdx#initial-catalog-identity-provisioning). Then assign the required tenant or namespace grants through [access management](../guides/access-management.mdx). Application entitlement and Catalog membership are independent prerequisites. Neither provider login nor group membership provisions Catalog access automatically.

Passwords, MFA, recovery and issuer signing keys stay with IBM Verify. Catalog membership suspension blocks Catalog access even while a signed bearer token remains valid. The UI's sign-out clears its local cookie; it does not end the provider's SSO session. Follow [expiry and session behavior](../reference/ui-host-configuration.md#authentication-and-forwarding) and [credential replacement](sign-in.md#replace-settings-and-credentials).

## Register an optional human CLI client

Create a separate OpenID Connect application with **Public client (no client secret)** selected. Enable Authorization Code, response type `code` and S256 PKCE. Do not reuse the confidential web client's secret.

Register exactly `http://127.0.0.1:49152/oauth/callback`. Use the same port in `--listen`. IBM's [implementation notes](https://www.ibm.com/docs/en/security-verify?topic=connect-openid-implementation-notes) permit literal loopback callbacks for native clients.

Select the same API audience and JWT access-token requirements for this application. Assign its users separately. Verify ID/access subject agreement for the CLI client independently; different clients can have different subject policies.

The CLI ID token must contain only its public client ID in `aud`. Set a positive access-token lifetime of at most 24 hours for CLI login.

Create a private directory before login. If it already exists, check its ownership and `0700` permissions instead of recreating it. Login requires a new credential file.

```sh
umask 077
mkdir -m 700 "$HOME/.catalog-login"
unset CATALOG_REMOTE_TOKEN CATALOG_REMOTE_TOKEN_FILE
catalogctl auth login \
  --issuer https://YOUR_TENANT.verify.ibm.com/oauth2 \
  --client-id YOUR_PUBLIC_CLIENT_ID \
  --base-url https://catalog.example.com \
  --listen 127.0.0.1:49152 \
  --file "$HOME/.catalog-login/ibm-verify.json"

catalogctl remote capabilities \
  --base-url https://catalog.example.com \
  --login-file "$HOME/.catalog-login/ibm-verify.json"
```

The CLI requests `openid` automatically. For enforced read access, allow `catalog.read` in Verify and add `--scope catalog.read` to login. Request additional scopes only for the intended tasks. Follow [human CLI login](../reference/commands.md#human-cli-login) for protected files, separate issuer/API trust, expiry and logout. API bearer files and JSON login files have different formats.

Start without depending on refresh. IBM describes **Generate refresh token** in terms of Verify APIs in its [token settings](https://www.ibm.com/docs/en/security-verify?topic=sign-configuring-single-in-openid-connect-application). Confirm that your application permits renewal for the selected Catalog audience before relying on it. Do not copy another provider's `offline_access` requirement.

If enabled, qualify the application's refresh issuance, expiry and rotation separately. The CLI preserves rotated refresh tokens and requires the same Catalog human membership after renewal. Without a refresh token, sign in again after expiry. The UI always requires a fresh sign-in after its session expires.

Automation instead uses an externally obtained API token and an explicitly provisioned service membership. A service membership cannot establish a human UI or CLI session.

## Deliver settings and secret files

Follow [configuration delivery](sign-in.md#deliver-the-configuration) for backend `auth` settings and Helm `configEnv.CATALOG_AUTH_*` inputs. Configure the UI independently through its JSON file or Helm `ui.config`. Keep required database, asset, request-limit and operational settings from your complete profiles.

Mount the UI client secret and independent cookie keys as protected files. The [UI host reference](../reference/ui-host-configuration.md#host-configuration) defines required fields. The [shared secret procedure](sign-in.md#prepare-ui-secret-files) and [chart reference](../../../../../deployments/helm/open-agentic-catalog/README.md#optional-catalog-ui) define their generation and Helm mount paths.

Publicly trusted issuer certificates need no private CA setting. Private trust remains separate: backend `auth.trustBundleFile`, UI `issuerCAFile`, and UI `apiCAFile` serve different connections. Follow [OIDC private trust](../reference/configuration.mdx#oidc-private-trust). Do not disable certificate verification to resolve a domain mismatch.

Restart or recreate affected processes after replacing mounted inputs. A Secret update alone does not activate new process settings. Follow [credential and trust rotation](maintenance.mdx#rotate-credentials-and-trust). New requested scopes require fresh sign-in; existing cookies do not acquire them.

## Verify your deployment

The checks below are operator steps, not completed IBM Verify qualification. Use owned test identities and retain safe status codes and request IDs.

1. Validate the complete backend profile with `catalogctl config validate --config /etc/catalog/config.json --role api`.
2. Obtain an API access token through the selected application's approved flow.
3. Check `/api/v1/me` with an approved client using a protected bearer file.
4. Confirm the exact subject, tenant and human or service actor type.
5. Verify an authorized Resource read with a narrowly granted test identity.
6. Verify that a Reader-only identity cannot create a Resource.
7. Sign in through the UI and inspect the subject and tenant in **My access**.
8. Test the optional CLI login independently.
9. If scope enforcement is enabled, verify denial when a token lacks a required scope despite a matching Catalog grant.
10. Check expiry and local sign-out separately from IBM SSO.
11. Suspend the test membership through Catalog administration.
12. Verify denial with its otherwise valid token or UI session.

Use the [shared verification procedure](sign-in.md#verify-your-deployment) for protected credential handling and recovery. Test reactivation deliberately if required. No hosted account, password, token or tenant was used to verify this guide.

### Troubleshoot IBM Verify sign-in

| Symptom | Check |
| --- | --- |
| Discovery fails or the issuer differs | Check the template, metadata path and exact advertised issuer. Do not mix modern and legacy endpoint families. |
| Verify denies a known user | Check application Entitlements and the provider access policy. A directory account alone does not authorize the application. |
| API returns `401` | Check JWT access-token format, audience, signature, times and the exact Catalog realm/subject membership. |
| API accepts the token but human login fails | Check ID/access subject agreement, client audience and human membership type. |
| An operation returns `403` | Check current Catalog grants and the JWT's string `scope`. An introspection or token-response scope field is insufficient. |
| UI reports `session_token_too_large` | Reduce unnecessary token claims. The encoded session cookie has a 3,600-byte ceiling. |
| Login fails with a custom domain | Check authorization, token and JWKS origins, exact issuer identity and each connection's trust settings. |
| CLI cannot renew credentials | Check whether refresh is issued for this application and audience. Check expiry and rotation before signing in again. |

Keep raw tokens, cookie values and client secrets out of diagnostics. Only the [local Keycloak qualification](oidc-keycloak.md#run-the-local-qualification) has maintained provider test evidence.
