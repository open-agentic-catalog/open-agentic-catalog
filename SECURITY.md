# Security

## Report a vulnerability in the Catalog

Report suspected vulnerabilities in the Catalog code through [GitHub private vulnerability reporting](https://github.com/open-agentic-catalog/open-agentic-catalog/security/advisories). Select "Report a vulnerability" on the repository's Security advisories page. This option requires the maintainer to enable private vulnerability reporting for the repository. The project maintainer reviews and handles these reports.

Include the following information:

- The affected Catalog version or commit.
- Steps to reproduce the issue with synthetic data.
- The expected behavior, observed behavior and security impact.
- Relevant configuration details with credentials and private data removed.

Do not open a public issue containing exploit details, credentials, private Catalog records, source URLs, or tenant data. Do not include real credentials or private Catalog data in a private report.

## Report an incident in a deployment

Report deployment incidents privately to the accountable security contact for the affected deployment.

Follow [security operations](documentation/src/content/docs/operations/security.md) for deployment controls and build checks, and [incident response](documentation/src/content/docs/operations/incident-response.md) for containment and recovery. The [security architecture](documentation/src/content/docs/architecture/security.mdx) describes identity, tenant isolation, outbound validation and threat controls.

Use production identity configuration. Protect network-exposed traffic with Transport Layer Security (TLS). An operator-managed terminator may protect the application listener. Never enable development identities in production. Inject secrets through the documented deployment inputs, not source, configuration examples or issue reports.

Structural validation and local review are not malware scanning or cryptographic trust verification. Unavailable assessment providers cannot satisfy a scanning or trust policy. Enabled but unimplemented capabilities fail startup. Consult the [supported configuration](documentation/src/content/docs/reference/configuration.mdx) before relying on an optional feature.
