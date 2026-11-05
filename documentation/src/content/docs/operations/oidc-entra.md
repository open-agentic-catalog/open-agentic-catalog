---
title: Configure Microsoft Entra ID
---

Use [Microsoft Entra ID](https://learn.microsoft.com/en-us/entra/identity-platform/) to issue access tokens for the Catalog API. This guide describes configuration and current client limits. It is not a live Entra qualification. Catalog's maintained provider tests use local Keycloak.

## Register the API

1. Register a single-tenant API application in your Entra directory. Follow Microsoft's [API registration guide](https://learn.microsoft.com/en-us/entra/identity-platform/quickstart-configure-app-expose-web-apis).
2. Configure the API registration to request v2 access tokens with `api.requestedAccessTokenVersion: 2`. Calling a v2 endpoint alone does not select the access-token version.
3. Expose the delegated permissions your external clients need. Configure application permissions separately for service clients, if required. Grant consent through your organization's normal controls.
4. Select the tenant-specific issuer `https://login.microsoftonline.com/DIRECTORY_TENANT_ID/v2.0`. Replace the placeholder with your directory UUID. Do not use `/common` or `/organizations` as the Catalog issuer.
5. Set Catalog's audience to the API application's client-ID UUID. For v2 access tokens, this is distinct from the `api://...` URI used in requested scope names.

Microsoft's [access-token reference](https://learn.microsoft.com/en-us/entra/identity-platform/access-token-claims-reference) defines `iss`, `aud`, `sub`, `scp` and `roles`. Its [token-version guide](https://learn.microsoft.com/en-us/entra/identity-platform/access-tokens) explains version selection. Do not use a Microsoft Graph token or an ID token as the Catalog bearer.

This fragment replaces the `auth` object in your complete backend profile:

```json
{
  "mode": "oidc",
  "issuer": "https://login.microsoftonline.com/DIRECTORY_TENANT_ID/v2.0",
  "audience": "API_APPLICATION_CLIENT_ID",
  "tenancyMode": "dedicated",
  "dedicatedTenantId": "00000000-0000-7000-8000-000000000001",
  "enforceScopes": false
}
```

Replace both uppercase placeholders before deployment. The directory UUID selects an issuer; the Catalog tenant UUID selects local data. They are separate identities. See [configuration delivery](sign-in.md#deliver-the-configuration) for environment and Helm settings.

## Provision the access-token identity

Use the exact access-token `sub` when you [provision Catalog membership](database-roles.md#initial-catalog-identity-provisioning). Catalog does not substitute Entra's `oid`, email, display name or application ID. Keep the issuer and subject together when identifying an account.

For automation, use your existing Entra credential mechanism. Microsoft's [client credentials guide](https://learn.microsoft.com/en-us/entra/identity-platform/v2-oauth2-client-creds-grant-flow) describes application consent and `api://API_APPLICATION_CLIENT_ID/.default` requests. The resulting token must still meet every Catalog claim requirement. Provision its actual subject as a service membership and assign the required Catalog permissions separately.

## Understand scope and human login limits

Entra's delegated tokens normally use a string `scp` claim. Application tokens use `roles` for application permissions. Catalog reads neither claim as its OAuth ceiling. With `enforceScopes: false`, current Catalog permissions still apply. Do not enable scope enforcement expecting Entra's `scp` or app roles to satisfy it.

The built-in UI and human CLI require identical ID-token and access-token subjects. Entra uses application-specific subjects, so separate API and login registrations can produce different values. Catalog has no `oid` mapping setting. See Microsoft's [ID-token claim reference](https://learn.microsoft.com/en-us/entra/identity-platform/id-token-claims-reference) for that subject behavior.

Do not treat this API setup as a working built-in browser or CLI login recipe. A mismatch fails login even if the API accepts the bearer. Use an external Entra-capable client to acquire an API token, or an operator-managed compatible issuer that brokers Entra login. Brokered login uses the broker's issuer and subject in Catalog membership.

The built-in clients also have no Entra-specific claims-challenge handler. Conditional Access policies can require an external client with that capability. Do not weaken tenant, audience or subject validation to bypass a policy or identity mismatch.

## Verify and troubleshoot

Run the [shared deployment checks](sign-in.md#verify-your-deployment) in your own environment. For API-only use, supply the approved token through `--token-file`. Do not assume that `catalogctl auth login` is compatible.

| Symptom | Check |
| --- | --- |
| API returns `401` | Verify the tenant-specific issuer, v2 token version, API UUID audience and exact provisioned subject. |
| API returns `403` with scope enforcement | Check the `scp` limitation and current Catalog grants. Disabling the ceiling is a policy choice, not an issuer permission grant. |
| Built-in login reports identity refusal | Compare ID/access subjects through approved local inspection. Do not replace the access token with the ID token. |

No Entra tenant, hosted login or Microsoft API is contacted by this guide's repository verification.
