# Outbound HTTP boundary

Use this package for resource-declared destinations and Catalog-controlled egress. Remote bytes, validators and URLs remain untrusted after retrieval; callers validate their content and construct approved public or audit projections.

| Files | Responsibility |
| --- | --- |
| [fetcher.go](fetcher.go) | URL/origin normalization, DNS and address admission at dial time, redirect checks, header limits, deadlines and bounded response reads. |
| [router.go](router.go) | Operator-approved tenant/origin routes, private trust, policy and credential revisions, and deployment-file credential resolution. |
| [telemetry.go](telemetry.go) | Exporter-compatible client bound to one collector URL, with isolated collector header and address permissions. |

Routing fields identify existing authority; clients cannot expand a route by supplying a profile name or credential binding. A permitted hostname does not authorize every resolved address. Redirects are rechecked and may be prohibited by the calling workflow. Archive callers request identity encoding to retain exact digest-bound bytes.

Run `go test ./internal/platform/fetcher -shuffle=on -count=1` for isolated URL, injected DNS/transport, redirect-policy, archive-byte, router credential admission, credential-file, scoped trust-pool and telemetry-policy tests. [Router preflight units](router_preflight_test.go) stop before DNS or transmission; local certificate checks do not perform TLS handshakes. Add `-tags=functional` to include local HTTP/TLS exchanges for credential rotation, private trust, mTLS, redirect rejection and telemetry requests. [Runtime composition](../runtime/README.md) wires egress inputs, and the [configuration reference](../../../documentation/src/content/docs/reference/configuration.mdx) owns their deployment contract.

[Metadata expiry units](router_mtls_test.go) use generated certificates and temporary files without a listener or network request. They separate key-pair parsing from certificate validity and reject expired binding metadata without usable credentials. The [source TLS workflow](router_mtls_functional_test.go) uses a verifying peer and counts accepted connections. It checks the exact expired client leaf, typed X.509 refusal, zero HTTP disclosure, metadata refusal before contact and atomic successor publication through the original router/resolver. Tenant, origin, profile and stale-revision refusals remain enforced. The shared certificate fixture stays inert; only the functional file opens listeners.
