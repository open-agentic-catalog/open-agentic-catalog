# Scan MCP definitions after a release notification

Run this optional HTTPS receiver to collect and scan definitions from an explicitly configured local MCP server. The receiver uses the Catalog Go SDK and the MCP Go SDK. PostgreSQL stores its inbox, reports and observations in a separate database.

The receiver never invokes tools or changes Catalog records. It does not approve, revoke or block releases. It does not enable Catalog software assessments. A runtime endpoint can change after publication; these reports describe captured definitions, not immutable runtime code.

The [Compose guide](../../../documentation/src/content/docs/guides/mcp-scanner.mdx) covers the complete Catalog/worker/MCP workflow. This guide covers the receiver's configuration and commands. The locked scanner's exact LiteLLM dependency blocks native YARA and full Compose qualification. A supported scanner update must resolve its recorded advisories before installation. Focused receiver tests use an explicit scanner executable double; they do not qualify the real scanner.

## Prerequisites

- Use a non-root Linux AMD64 host and a source checkout.
- Provide Go 1.27.3 and the [installed offline scanner](../README.md#install-the-scanner).
- Provide a separate PostgreSQL 18 database and account for this receiver.
- Obtain a Catalog token with access to the configured resource namespaces.
- Configure a local HTTPS MCP endpoint with a separate bearer credential.
- Provide trusted certificates for the Catalog, MCP endpoint and receiver.

The MCP endpoint must use Streamable HTTP with protocol `2025-11-25`. Both JSON and finite SSE responses are supported. The client disables standalone SSE, sampling, elicitation, roots, automatic multi-round requests and reconnect retries.

The example pins the same MCP Go SDK and pgx versions already used by the repository. It has its own module and lock. It does not import the Catalog application module. The copied webhook verifier stays byte-identical to the [verification example](../../webhookreceiver/verify.go).

## Build and test

Install the [shared development tools](../../../documentation/src/content/docs/contributing/build-and-release.md#shared-development-tools) for the Python process fixture. Provide Docker with Compose for the owned PostgreSQL test. Receiver checks do not require an installed scanner.

Run these targets separately from the repository root:

```sh
make dev-tools-install
make sdk-tools-install SDK_LANGUAGE=go
go -C examples/mcp-scanner/receiver mod download
make mcp-scanner-receiver-build
make mcp-scanner-receiver-check
make test-mcp-scanner-receiver
```

Build writes `bin/examples/mcp-scan-receiver`. The check runs isolated units, real local MCP/TLS tests, analyzers and dependency license checks. It also qualifies the Go SDK.

The PostgreSQL target owns its database container, receiver database/account, HTTPS listeners and receiver process. It uses controlled Catalog responses and a Python executable double. It verifies the adapter without installing or executing MCP Scanner. The separate offline check qualifies actual YARA after approved installation.

## Prepare private inputs

Create a private directory outside the checkout:

```sh
MCP_SCAN_INPUTS="$HOME/.local/share/catalog-mcp-scanner"
install -d -m 700 "$MCP_SCAN_INPUTS"
```

Supply these files through your local operator setup:

| File | Contents |
| --- | --- |
| `receiver.dsn` | Complete connection string for the separate receiver database/account. |
| `catalog-token` | Catalog access token without a `Bearer` prefix. |
| `mcp-token` | Separate MCP bearer credential. |
| `mapping.json` | Exact resource, namespace and endpoint mapping. |
| `signer.json` | Webhook signing secret and version returned by endpoint registration. |
| `receiver.pem` and `receiver-key.pem` | Receiver certificate chain and private key. |
| `catalog-ca.pem` | Optional private CA for the Catalog API. |
| `mcp-ca.pem` | Optional private CA for the MCP endpoint. |

Set these files to mode `0600`. The receiver refuses symlinks, nonregular files and group-readable or world-readable inputs. It loads credentials from the selected files, not from URLs or Catalog metadata. Include explicit PostgreSQL connection and TLS settings in the DSN. Use verified database TLS outside an owned loopback fixture.

The signer file has this shape:

```json
{"secret":"REPLACE_WITH_RETURNED_SECRET","version":1}
```

Replace its secret and version with the exact registration response. During rotation, install the new signer atomically. Each request loads the current file. The verifier also supports the predecessor signature during normal overlap.

## Configure the resource mapping

Use the exact local Resource and namespace IDs from the Catalog. Configure the endpoint outside the published record:

```json
{
  "resources": [
    {
      "resourceId": "00000000-0000-4000-8000-000000000002",
      "namespaceId": "00000000-0000-4000-8000-000000000003",
      "endpoint": "https://127.0.0.1:9445/mcp",
      "tokenFile": "/absolute/private/path/mcp-token",
      "caFile": "/absolute/private/path/mcp-ca.pem"
    }
  ]
}
```

Replace the sample IDs, endpoint and paths. Omit `caFile` for public CA trust. Credential and CA paths must be absolute. The mapping accepts at most 32 Resources across eight namespaces. Endpoint URLs must fit within 1,024 UTF-8 bytes, including the path.

The released primary MCP Server Card or registry representation must declare the exact endpoint with type `streamable-http`. The receiver checks the local kind, tenant, current release, revision links and representation digests. The Resource and Listing must use the mapped namespace. It ignores newer unpublished drafts.

Every resolved endpoint address must be private or loopback. Public, unspecified, multicast and link-local addresses are refused before connection. The receiver pins a validated address for each connection and verifies TLS against the configured hostname. It follows no redirects and uses no ambient proxy. URL credentials, query strings and fragments are refused.

The MCP credential must differ from the Catalog credential. The collector checks both files for each request. It sends only the MCP credential to the runtime.

## Grant Catalog access

Give the receiver only these permissions:

| Purpose | Permission | OAuth scope, when enforced |
| --- | --- | --- |
| Read releases and browse the configured namespaces | `resource.read` in those namespaces | `catalog.read` |
| Read tenant notification-loss counters | Tenant-wide `webhook.read` and `webhook.read_evidence` | `webhooks.manage` |

Use separate operator credentials to register or rotate a webhook endpoint. The receiver needs no authoring, release-publication, webhook-management or assessment-write permission.

Register the receiver's HTTPS `/webhook` URL for `listing.release.published`. An explicit signed `webhook.test` is also accepted. Configure Catalog-side routing, the exact receiver origin and private trust through the [webhook runbook](../../../documentation/src/content/docs/operations/webhooks.mdx).

The event subject is a ListingRelease ID, not a Resource ID. The receiver reads that exact release through the SDK. A delayed event cannot substitute a newer release. Denied, missing, withdrawn or unmapped records cause no MCP contact.

## Initialize the separate database

Create the database and account before initialization. For example, a PostgreSQL administrator can use `psql`:

```sql
CREATE ROLE catalog_mcp_receiver LOGIN;
\password catalog_mcp_receiver
CREATE DATABASE catalog_mcp_receiver OWNER catalog_mcp_receiver;
```

Save the account's complete connection string in `receiver.dsn`. The account owns only the receiver database. Initialization refuses a database that already contains Catalog migrations.

Set your nonsecret Catalog inputs:

```sh
MCP_SCAN_ORIGIN=https://catalog.example.test:8080
MCP_SCAN_TENANT=00000000-0000-4000-8000-000000000001
```

Initialize the four receiver tables:

```sh
bin/examples/mcp-scan-receiver init \
  --origin "$MCP_SCAN_ORIGIN" --tenant "$MCP_SCAN_TENANT" \
  --config-file "$MCP_SCAN_INPUTS/mapping.json" \
  --database-file "$MCP_SCAN_INPUTS/receiver.dsn"
```

Initialization binds the database to the exact tenant, API origin and normalized mapping. Other commands refuse a different binding. Startup does not create tables. Credential contents can rotate at their existing paths without changing the mapping.

## Start one receiver

Run from the repository root:

```sh
bin/examples/mcp-scan-receiver serve \
  --origin "$MCP_SCAN_ORIGIN" --tenant "$MCP_SCAN_TENANT" \
  --config-file "$MCP_SCAN_INPUTS/mapping.json" \
  --database-file "$MCP_SCAN_INPUTS/receiver.dsn" \
  --token-file "$MCP_SCAN_INPUTS/catalog-token" \
  --catalog-ca-file "$MCP_SCAN_INPUTS/catalog-ca.pem" \
  --signer-file "$MCP_SCAN_INPUTS/signer.json" \
  --tls-cert "$MCP_SCAN_INPUTS/receiver.pem" \
  --tls-key "$MCP_SCAN_INPUTS/receiver-key.pem" \
  --scanner-python "$PWD/.tools/dev/bin/python" \
  --scanner-runner "$PWD/test/mcp-scanner/runner.py" \
  --reports-dir "$MCP_SCAN_INPUTS/reports" \
  --listen 127.0.0.1:9444
```

Omit `--catalog-ca-file` for public CA trust. `--allow-loopback-http` permits an explicitly selected literal-loopback Catalog API only. MCP always requires verified HTTPS.

Startup verifies the installed scanner identity before opening the listener. Missing or stale scanner tools fail startup. Setup and checks never install them implicitly. The scanner child receives no Catalog, MCP, webhook or database credentials.

The listener exposes only `GET /healthz` and `POST /webhook`. Health reports listener availability, not a successful scan. Reports remain private files and database rows.

## Processing, reports and recovery

Acceptance verifies signatures and the event schema before storing the `(source, id)` receipt. It commits receipt deduplication before returning `204`. Full or unavailable storage returns `503`. The asynchronous processor uses one pending item at a time.

The processor reads the exact eligible release before collecting definitions. It permits MCP initialization, `tools/list`, cancellation and session cleanup only. It checks Catalog access and the same release again after analysis. Changed eligibility produces a historical report, without erasing completed findings.

Each report binds the Resource, release, ListingRevision, VersionRevision, content-set digest and representation digest. Captured input, scanner image, rules and configuration have separate hashes. The event's optional digest remains transition context. It is never used as the captured-input checksum.

The report directory contains `index.json`, one metadata file per Resource and one native scan file per Resource. Metadata records the native file's SHA-256 hash. Database completion and file rendering are separate operations. Compare that hash before using a scan file, or render again after an interrupted write. The database remains the receiver's authority.

`eligible_at_check` means the release was eligible at the recorded `eligibleAt` time. It does not promise continuing permission or unchanged runtime code. Other states include pending, partial, failed, skipped, historical and not listed. An empty capture is skipped; it cannot count as completed analysis. Old observations retain their original capture context.

The processor reuses completed analysis only for the same release, capture digest and scanner identity. It still collects fresh definitions and rechecks Catalog access. Reused reports retain their original analysis timestamp. Failed or partial analysis never becomes a successful cached result.

Startup and periodic reconciliation read both loss counters and traverse the configured namespaces. Traversal continues even when counters are unchanged or decrease after a restore. A failed or partial traversal never claims that missing Resources disappeared. Reconciliation has one coalesced pending slot per configured Resource.

Restart with the same database and binding after interruption. Accepted pending work survives. A session advisory lock refuses a second processor. Loss of that lock connection stops processing; restart the single receiver after repairing the database connection.

Stop `serve` before a one-shot command. Reuse its connection, mapping, token and trust flags:

- Run `reconcile` to refresh Catalog membership and schedule scans. This command does not contact MCP.
- Run `process` to process at most one due receipt. Include the scanner flags.
- Run `render` to regenerate private files from the database. This command needs no Catalog token or scanner flags.

These commands accept the same flags as `serve`; listener and signer flags are required only for `serve`. A failed render does not undo stored results or receipt completion.

## Limits

| Work or setting | Limit or default |
| --- | --- |
| Resources and namespaces | 32 Resources, eight namespaces |
| Catalog reconciliation | Eight pages total, 32 entries per page, 30-second budget |
| MCP collection | Eight pages, 128 tools, 1 MiB, 30 seconds |
| Scan analysis | Batch 1's 30-second process budget |
| Full processing step | Three minutes, plus bounded scanner-child cleanup |
| Native report | 4 MiB |
| Retained notification receipts, `--capacity` | 1,000 by default; accepted range 1-10,000 |
| Retained scans | Same independent capacity limit |
| Reconciliation work | One slot per configured Resource, outside notification capacity |
| Retention, `--retention` | 24 hours; accepted range 1 minute-30 days |
| Poll interval, `--poll-interval` | 5 seconds; accepted range 1 second-1 minute |
| Reconciliation interval, `--reconcile-interval` | 1 minute; accepted range 5 seconds-24 hours |
| Transient retries | Three attempts with bounded exponential delay and jitter |
| HTTP admission | 16 concurrent requests, 64 connections, 256 KiB events, 16 KiB headers |
| Database pool | Four connections, including the processor lock connection |

The capacity must accommodate every configured Resource's latest scan. Full report storage records a failure; it does not evict a protected latest report. Increase capacity or let unused reports expire before scheduling another scan. Acceptance prunes at most 128 expired completed notification receipts. Report completion prunes at most 128 expired scans that no observation references. Pending receipts do not expire.

Press Ctrl-C or send SIGTERM to stop. Scanner-child cancellation allows up to one minute for its cleanup. HTTP drain allows a further ten seconds. An interrupted scan remains pending for restart. No command publishes images, uploads reports or changes retained Compose services.
