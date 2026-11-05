# Local federation trust mount

This non-secret placeholder keeps the default Compose mount valid. Point `CATALOG_COMPOSE_FEDERATION_TRUST_DIRECTORY` at a reviewed directory of PEM CA bundles only when a private profile references one.

Compose mounts that host directory read-only at `/etc/open-agentic-catalog/federation/trust` in API and worker containers. For a supplied file named `corporate-ca.pem`, set the selected private profile's `trustBundleFile` to `/etc/open-agentic-catalog/federation/trust/corporate-ca.pem`, not its host-side path. The directory is not scanned automatically, and neither checked-in federation policy references a private CA.

Each referenced PEM bundle must contain at least one certificate and be no larger than 4 MiB; the combined policy limit is 16 MiB. It augments public trust only for that private federation profile and never disables certificate-chain or hostname verification. Supply CA certificates here, not mTLS client private keys; those belong in the separately scoped credential binding.

Policy and trust bytes load at startup and contribute to the policy digest. Rotate them through a coordinated API/worker rollout and retesting of affected sources, not by assuming a mounted-file update hot-reloads trust. See [outbound policy and trust](../../documentation/src/content/docs/reference/configuration.mdx#outbound-egress-policy) for the source pause, rollout, verification, and resume procedure. This mount does not alter OIDC, PostgreSQL, or webhook trust.
