# Access-token authentication

[authn.go](authn.go) implements exact development-token lookup and OIDC access-token verification. Both paths resolve current Catalog membership after identifying the principal. Issuer scopes remain claims; they do not create Catalog roles or bypass namespace and tenant authorization.

OIDC requests use the configured issuer's dedicated bounded client. The verifier rejects ambiguous JWT JSON, checks an explicit asymmetric algorithm allowlist and refreshes expired verifier state without falling back to stale keys on failure. [trust.go](trust.go) loads certificate-only private PEM roots into that client's isolated pool and retains a byte digest for diagnostics.

Run `go test ./internal/platform/authn -race -shuffle=on -count=1` for isolated development identity, signed-token validation, current-membership handoff, controlled discovery/JWKS responses, cache refresh and trust-file tests. The [OIDC units](oidc_unit_test.go) use local signing keys, the real verifier and an injected clock without a server. Add `-tags=functional` for the complete suite, including real local issuer/JWKS exchanges, rotation, outage and private TLS isolation. Trust-file units generate certificate bytes without a server. The [authentication configuration](../../../documentation/src/content/docs/reference/configuration.mdx) owns deployment inputs; [identity](../../identity) owns compact current-membership resolution and scoped permission policy. Classified policy-store outages become safe 503 at the HTTP boundary, while invalid identity retains generic 401.

The public-factory unit checks configured audience and dedicated/shared tenancy through signed tokens and independently expected membership inputs. Its serial, in-memory transport override denies unexpected requests and restores the process default during cleanup. Private trust installation on an actual HTTPS transport remains qualified by the separate functional tests.

`FuzzValidateJWTEncoding` checks compact-token framing, both encoded JSON parts and the 32 KiB token and 16 KiB decoded-part limits. Generated controls retain raw malformed Unicode, duplicate names, trailing JSON and partial base64 decoding errors. The guard does not authenticate tokens or decode signatures; the existing verifier owns algorithms, signatures and claims.

Run `go test ./internal/platform/authn -run '^$' -fuzz '^FuzzValidateJWTEncoding$' -fuzztime=10s -parallel=2` for the focused campaign. `make test-fuzz` includes this target and uses `FUZZ_TIME`, defaulting to ten seconds per target. Ordinary units execute its seed corpus without an issuer, server or database.

The early-refusal unit warms a real verifier through an injected transport and uses valid signed controls before and after malformed tokens. It checks the injected clock, issuer-request counter and membership resolver. This proves encoding refusals stop before verification, including when signing keys are cached.
