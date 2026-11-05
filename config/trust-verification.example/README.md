# Public ANS verification keys

This directory is the default, empty Compose mount for optional ANS public keys. Verification remains disabled. No private keys or TLS certificates belong here.

Select your own directory with `CATALOG_COMPOSE_TRUST_VERIFICATION_DIRECTORY`. Mount public SPKI PEM files and set their container paths in `CATALOG_TRUST_VERIFICATION`. API and worker read the same settings. The migration owner and UI do not receive this mount.

See [ANS verification configuration](../../documentation/src/content/docs/reference/configuration.mdx#ans-registration-verification) for policy examples, limits and rollout requirements. Administrators can request exact source-entry assessments through the [ANS assessment procedure](../../documentation/src/content/docs/operations/federation.mdx#request-an-ans-registration-assessment). Results are private and do not establish software safety or rights.
