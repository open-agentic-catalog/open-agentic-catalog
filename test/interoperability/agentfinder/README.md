# GitHub Agent Finder Catalog Interoperability Qualification

This optional campaign qualifies one-way consumption of a pinned, unchanged publication snapshot (`ai-catalog.json`) from GitHub's Agent Finder repository (`github/agentfinder-catalog`).

## Pinned Baseline

- **Repository**: `github/agentfinder-catalog`
- **Revision**: `3286bfd843a3831b52a8a72b0ff6f3eb0f730d7f`
- **Artifact**: `ai-catalog.json`
- **Size**: `1,248,214` bytes
- **SHA-256**: `7ca6ac36768ebffb05639084681d85334b3c8b72a8637264fb01fc43710853bb`
- **Total Entries**: 1,718 unique identifier/version pairs across 5 declared media types:
  - `application/mcp-server+json`: 647 (version present)
  - `application/ai-skill`: 925 (version omitted)
  - `application/vnd.anthropic.claude-plugin+json`: 69 (version omitted)
  - `application/vnd.cursor.cursor-plugin+json`: 22 (version omitted)
  - `application/vnd.github.copilot-plugin`: 55 (version omitted)

## Objectives and Governance

1. **Preflight Confinement**: Ensure exact byte size, SHA-256 hash, entry counts, and version presence flags are validated before any source creation. No outbound public network queries are made during test execution.
2. **Native Admission & Inspection**: Exercise real Catalog Source Inspection on an owned private TLS HTTPS fixture serving unchanged publication bytes. OAC recognizes the `ai-catalog-1.0-b062278` profile envelope.
3. **Unsupported Kind Classification**: All 5 declared media types are classified as unsupported kinds under Catalog policy (`internal/federation/manager.go`).
4. **Administrative Retention vs. Search Exclusion**: Observations are retained for administrator provenance audit and SQL readback, but completely excluded from ordinary public Search results (0 searchable entries).
5. **Idempotency and Confinement**: Repeated synchronization creates no duplicate observations or false withdrawals. Simulated upstream outages preserve prior complete snapshots.

## Usage

```sh
# Acquire and verify pinned publication offline
make agentfinder-tools-install

# Run qualification suite
make test-interoperability-agentfinder
```
