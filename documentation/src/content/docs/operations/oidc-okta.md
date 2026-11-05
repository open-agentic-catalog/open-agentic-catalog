---
title: Sign in with Okta
---

Use an [Okta custom authorization server](https://developer.okta.com/docs/concepts/auth-servers/) for Catalog access. The org authorization server issues access tokens for Okta itself, not your Catalog API. Custom authorization servers require Okta API Access Management in production.

This guide describes configuration from provider documentation and Catalog code. Hosted Okta login is not qualified by the repository's local tests. Follow the [shared setup](sign-in.md) for membership, deployment and secret delivery.

## Configure the authorization server

1. Create or select a custom authorization server under **Security > API**. Add an access policy and rule for the intended clients and grants.
2. Set its audience to `open-agentic-catalog` or another exact API identifier you control.
3. Use its exact issuer, for example `https://YOUR_ORG.okta.com/oauth2/default`, in backend and client configuration.
4. Define the Catalog scope names required by your clients. Select them from the [permission and scope matrix](../reference/api-conventions.md).

Do not use `https://YOUR_ORG.okta.com` as an interchangeable issuer. That selects the org authorization server. A `default` custom server still needs an applicable access policy; its name alone does not authorize clients.

Use this `auth` object in your complete backend profile:

```json
{
  "mode": "oidc",
  "issuer": "https://YOUR_ORG.okta.com/oauth2/default",
  "audience": "open-agentic-catalog",
  "tenancyMode": "dedicated",
  "dedicatedTenantId": "00000000-0000-7000-8000-000000000001",
  "enforceScopes": false
}
```

Replace the organization and authorization-server placeholders with your selected values. See [Helm and environment delivery](sign-in.md#deliver-the-configuration) for equivalent settings.

## Align human subjects

Okta's ID token normally identifies the user by the internal user ID. Its access-token subject can use a username instead. The Catalog UI and human CLI require exact subject agreement. See Okta's [subject-claim limits](https://support.okta.com/help/s/article/sub-claim-limitation?language=en_US).

For a human-only custom server policy, configure the existing access-token `sub` claim to use `user.getInternalProperty("id")`. Okta's [expression reference](https://developer.okta.com/docs/reference/okta-expression-language/) defines this internal ID expression. Verify the resulting ID/access subjects before provisioning Catalog membership or enabling login. A separate custom identity claim does not replace `sub` in Catalog.

Do not apply a user-only expression blindly to client credentials grants. Service clients have no human user context. Review their subject policy separately and provision their exact token subject as a Catalog service membership. Do not switch an established subject policy without a Catalog membership transition.

## Supply the optional scope ceiling

Okta normally emits `scp` as an array. Catalog requires a whitespace-separated string named `scope` for `auth.enforceScopes`.

Add an access-token custom claim named `scope`. Include it for the required requests and use this documented [Okta scope expression](https://developer.okta.com/docs/reference/okta-expression-language/#access-scope-samples):

```text
String.replace(Arrays.toCsvString(access.scope),","," ")
```

This expression copies granted scopes; it does not invent permissions. Follow Okta's [custom-claim procedure](https://developer.okta.com/docs/guides/customize-tokens-returned-from-okta/main/). Verify that `scope` is a string with the intended granted names before enabling Catalog scope enforcement.

If you keep `enforceScopes: false`, current Catalog grants still control every protected action. Okta groups and API permissions do not create those grants.

## Register browser and CLI clients

Register a confidential **Web Application** for the Go UI host. Enable Authorization Code with S256 PKCE. Register `https://browse.example.com/auth/callback`, including the UI base path when configured. Mount its secret in UI `clientSecretFile`; do not expose it to React.

Register a separate public **Native Application** for the CLI. Enable Authorization Code with S256 PKCE and no client secret. Register `http://127.0.0.1:49152/oauth/callback`. Enable Refresh Token only if CLI renewal is needed. Add both clients to the custom server's access policy and assign the intended users.

Use the custom server issuer for UI `issuer` and CLI `--issuer`. Request Catalog scopes through UI `scopes` or repeated CLI `--scope` options. The clients add `openid` themselves. Request `offline_access` only from the CLI when refresh is permitted; the UI rejects it.

For example, after creating a private credential directory and provisioning the exact human subject:

```sh
catalogctl auth login \
  --issuer https://YOUR_ORG.okta.com/oauth2/default \
  --client-id YOUR_NATIVE_CLIENT_ID \
  --base-url https://catalog.example.com \
  --listen 127.0.0.1:49152 \
  --file "$HOME/.catalog-login/okta.json"
```

Follow [CLI login](../reference/commands.md#human-cli-login) for private file permissions and refresh. Follow [browser setup](sign-in.md#add-optional-browser-login) for UI keys, exact callbacks and Helm inputs. These commands are operator instructions, not evidence of a hosted Okta test.

## Verify and troubleshoot

Complete the [shared deployment checklist](sign-in.md#verify-your-deployment). Check these provider-specific conditions before debugging Catalog permissions:

| Symptom | Check |
| --- | --- |
| Login policy denies the client | Check the custom server's access policy, grant rule and user assignment. |
| API returns `401` | Check the custom server issuer, audience and provisioned access-token subject. |
| API accepts the bearer but UI/CLI refuses login | Check ID/access `sub` equality and the human membership type. |
| Scope-limited operations return `403` | Check the string `scope` mapper. An `scp` array alone supplies no Catalog ceiling permissions. |

Use a separate approved client credentials flow for automation. Service accounts cannot establish a human UI or CLI session.
