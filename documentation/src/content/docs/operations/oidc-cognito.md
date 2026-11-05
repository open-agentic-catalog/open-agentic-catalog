---
title: Configure Amazon Cognito
---

Amazon Cognito user pools can issue signed API access tokens. Their default token claims and endpoint layout need special attention with Catalog. This guide documents those limits; hosted Cognito is not exercised by repository tests.

## Understand the current boundary

| Capability | Catalog requirement and Cognito behavior |
| --- | --- |
| API bearer validation | Requires `aud`. Cognito access tokens normally contain `client_id`; resource binding can add `aud`. |
| Built-in UI login | Requires login and token endpoints on the issuer origin. Cognito uses a separate user-pool domain. This flow is unsupported. |
| Built-in human CLI login | Has the same origin restriction and sends no Cognito `resource` parameter. This flow is unsupported. |
| External token acquisition | An external OAuth client can request a resource-bound access token and present it to the Catalog API. Operators must qualify this configuration. |

Cognito's [JWT verification guide](https://docs.aws.amazon.com/cognito/latest/developerguide/amazon-cognito-user-pools-using-tokens-verifying-a-jwt.html) distinguishes ID-token `aud` from access-token `client_id` and resource binding. Do not use an ID token as the Catalog API bearer.

## Configure API-only access

1. Create or select a Cognito user pool. Configure its HTTPS domain and an app client for the external OAuth client you control.
2. Register the external client's exact callback. Enable Authorization Code with S256 PKCE and the required scopes.
3. Select a resource URL, such as `https://catalog.example.com`, as the intended Catalog API audience.
4. Have the external client send `resource=https://catalog.example.com` in its authorization request. Follow Cognito's [resource-bound authorization procedure](https://docs.aws.amazon.com/cognito/latest/developerguide/authorization-endpoint.html).
5. Exchange the code through that client's supported flow. Confirm the access token contains the exact `aud`, issuer, subject and valid times.

The Catalog does not obtain this resource-bound token for you. Its configured audience validates the token; it does not add an OAuth request parameter. Cognito's [resource-server guide](https://docs.aws.amazon.com/cognito/latest/developerguide/cognito-user-pools-define-resource-servers.html) explains custom scopes and supported resource-binding grants.

Use this `auth` object in your complete backend profile:

```json
{
  "mode": "oidc",
  "issuer": "https://cognito-idp.REGION.amazonaws.com/USER_POOL_ID",
  "audience": "https://catalog.example.com",
  "tenancyMode": "dedicated",
  "dedicatedTenantId": "00000000-0000-7000-8000-000000000001",
  "enforceScopes": false
}
```

Replace the region and user-pool placeholders. Use the actual user-pool issuer, not the hosted login domain. Provision the exact access-token subject through [Catalog identity setup](database-roles.md#initial-catalog-identity-provisioning). Cognito usernames, groups and email addresses do not replace it.

The Catalog backend can discover signing keys from the configured issuer. The separate browser/CLI restrictions apply to their login exchanges, not to ordinary API bearer verification.

## Decide whether to enforce scopes

Cognito resource servers prefix custom scopes with their resource-server identifier, such as `catalog/catalog.read`. Catalog's scope matrix expects exact names such as `catalog.read`. Do not assume these values match.

Keep `auth.enforceScopes: false` unless your issuance policy produces a string `scope` with the exact Catalog names. Catalog permissions still apply in that mode. Review token customization separately; do not copy client-controlled values into permission claims or replace audience checks with `client_id` checks.

Client credentials tokens must also contain the required Catalog audience. Do not assume default Cognito machine-to-machine tokens meet that contract. The documented authorization-code resource-binding flow does not prove a client credentials configuration works.

## Browser login alternatives

Cognito's [endpoint reference](https://docs.aws.amazon.com/cognito/latest/developerguide/federation-endpoints.html) places authorization and token endpoints on the configured user-pool domain. The issuer stays on `cognito-idp.REGION.amazonaws.com`. A custom login domain does not make these origins identical.

Do not rewrite the Catalog issuer to the login domain or disable issuer checks. For built-in human login, select a compatible issuer. An operator-managed issuer can broker Cognito identities if you already use that architecture. Catalog then trusts the broker's tokens and provisions its subjects, not Cognito's original subjects.

The external OAuth client owns Cognito refresh, consent and logout. Cognito's [token endpoint](https://docs.aws.amazon.com/cognito/latest/developerguide/token-endpoint.html) documents its refresh behavior; do not assume the Keycloak `offline_access` instructions apply.

## Verify and troubleshoot

Follow the [API deployment checks](sign-in.md#verify-your-deployment), using a protected token file and a denied action. This is an operator procedure; repository verification does not contact AWS.

| Symptom | Check |
| --- | --- |
| API rejects a token with only `client_id` | Obtain a supported resource-bound access token with the configured `aud`. |
| API rejects issuer or subject | Use the exact user-pool issuer and provisioned access-token `sub`. |
| Scope enforcement rejects an operation | Check prefixed Cognito scope names against the Catalog scope matrix. |
| Built-in UI or CLI cannot start login | The split endpoint origins are incompatible with the current login clients. |

See [configuration delivery](sign-in.md#deliver-the-configuration) for Helm and environment inputs. The application chart does not create user pools, app clients, Lambda triggers or AWS credentials.
