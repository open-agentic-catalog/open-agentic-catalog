---
title: "ADR-0018: Optional standalone Catalog browser client"
---

- Status: Accepted

## Context

Catalog browsing, governed authoring and administration need a browser client. The service API remains the authority for identity, tenancy, permissions and domain transitions. A browser implementation must not introduce a UI dependency into the API or worker, or expose administrative bearer tokens to browser storage.

## Decision

Maintain the optional client in `catalog-ui/`: React, TypeScript, Vite, semantic CSS and native accessible controls, with OpenAPI-derived TypeScript types. Use a small Go host in the existing module for assets, runtime configuration, login, encrypted cookies and fixed API forwarding. It has no database connection, migration authority or independent permission policy. Node is build/test tooling, not a serving dependency. A separate optional image and default-off Compose/Helm wiring are implemented.

Use a confidential OIDC client with authorization code, PKCE, state, nonce and ID-token verification against the UI client ID. Forward the separate Catalog-audience access token and confirm its resolved identity through `GET /api/v1/me`. The API continues validating JWTs and resolving current Catalog grants on every request. This does not change ADR-0006 into a Catalog authorization server or move password/account management into this repository.

Use authenticated encrypted cookies through Gorilla securecookie, with shared deployment keys and no server-side session store, affinity or refresh token. Absolute lifetime is capped by access-token expiry. The encoded value is limited to 3,600 bytes. Reauthorization is explicit.

For rolling rotation, first distribute the new decoding key to every replica. Then promote it to the first position, which writes cookies. Keep old keys readable for existing sessions and login callbacks as described in [maintenance](../../operations/maintenance.mdx#rotate-ui-session-keys).

Origin, authentication mode, upstream, base path and client ID bind the cookie to its deployment. Catalog grants are never stored as session authority.

Development login is a mutually exclusive, loopback-only mode using existing synthetic identities from server-side files. It cannot start in production or act as an OIDC failure fallback. Browser requests cannot supply arbitrary identities, roles, tenant selection or forwarding destinations.

Cookie-authenticated requests use a required non-simple header, exact Origin checks for non-GET operations and Fetch Metadata validation, without a CORS grant. Callbacks use their protocol-specific state validation. Fixed API and issuer transports use separate scoped trust, cancellation, response bounds and no redirects. Private state stays out of persistent browser caches and is cleared across identity changes and sign-out.

## Alternatives and consequences

- A direct browser OAuth client would eliminate the forwarding host but expose access tokens to application JavaScript. The Go host keeps tokens outside that boundary at the cost of an additional optional process.
- A persistent session database or Redis would accommodate larger tokens and coordinated refresh. It is not selected without evidence that bounded cookie sessions and reauthorization are unsuitable.
- Server-rendered React and a microfrontend platform add serving and composition machinery not required for these authenticated application workflows. Vite produces static assets served by the Go host.
- Sharing the backend Go module avoids a second dependency and test setup. Frontend dependencies remain separate from the documentation site, and backend builds require no frontend output.

Cookie size and expiry impose real usability limits. A local test OIDC provider signs tokens with an RSA key. Host tests check authorization-code exchange with PKCE, callbacks across replicas and shared session cookies.

The packaged human-provider test checks browser login with representative claims. Operators must qualify their own issuer configuration and token size. Tokens above the cookie bound require an explicit session-design change, not automatic cookie chunks or another service.

## Implementation

The [browser guide](../../guides/catalog-ui.mdx) covers login and Catalog workflows. See [UI architecture](../catalog-ui.mdx) for session security, [Helm configuration](../../../../../../deployments/helm/open-agentic-catalog/README.md#optional-catalog-ui) for deployment, and the [testing guide](../../contributing/testing.md#optional-browser-client) for browser and provider checks.
