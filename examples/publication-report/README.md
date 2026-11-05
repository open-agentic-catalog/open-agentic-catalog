# Go webhook publication report

Run a small HTTPS receiver that uses the Go SDK to refresh a local publication report. PostgreSQL stores accepted notifications and report results. The receiver has no Catalog database access.

Notifications trigger current-state reads. They do not establish the current publication by their arrival order, target ID, digest or generation.

## Prerequisites

- Use Go 1.27.3 or later and a source checkout.
- Provide a dedicated receiver database on PostgreSQL 18. Do not use the Catalog database or its credentials.
- Provide a Catalog API origin, tenant UUID and exact configured publication host.
- Obtain an API token with the permissions below.
- Provide a receiver TLS certificate and private key.
- Register a webhook endpoint and save its returned signing secret and version.

Enable Catalog webhooks before registering an endpoint. Start a Catalog worker for normal notification delivery. Runtime and Helm default webhooks off; local Compose enables them unless `LOCAL_WEBHOOKS=off`. Reconciliation can continue when notifications are disabled.

The example has its own `go.mod` and `go.sum`. It uses the local Go SDK and the repository's current pgx version. It copies `../webhookreceiver/verify.go` without changes. Checks compare the files byte for byte. The backend does not import this example or the SDK.

The source uses [Apache License 2.0](../../LICENSE). `SDK-NOTICE` preserves the SDK notice. Dependencies retain their licenses in their Go modules. The check validates the dependency license report before any external distribution.

## Build and test locally

Run these commands from the repository root:

```sh
make sdk-tools-install SDK_LANGUAGE=go
go -C examples/publication-report mod download
make webhook-report-build
make webhook-report-check
make test-webhook-report
```

Tool installation and module download acquire pinned inputs. The build creates `bin/examples/publication-report`. The source check also qualifies the Go SDK, isolated example units, analyzers, the copied verifier and dependency licenses.

The functional target owns a fresh PostgreSQL instance, HTTPS listeners, synthetic credentials and the actual example process. It removes its own schema, containers and volumes. It never resets retained Catalog data or changes external DNS.

The focused functional target uses a controlled API to test receiver failures, duplicate delivery and interrupted processing. It keeps those fault cases separate from the Catalog sender campaign.

Run the real Catalog delivery scenario separately:

```sh
make test-functional-webhooks
```

This target builds the example and starts it in the owned Catalog fixture. It creates a separate receiver database and an account without Catalog database access. A dedicated API identity receives only the three read permissions below.

The worker sends verified HTTPS notifications for activation and unpublish. Each report must match the exact completed receipt and current publication state, including the JSON file. Startup and periodic reconciliation cannot qualify those event checks. Both tests preserve the retained development environment.

## Prepare protected inputs

Create a private directory outside the checkout:

```sh
REPORT_INPUTS="$HOME/.local/share/catalog-publication-report"
install -d -m 700 "$REPORT_INPUTS"
```

Supply these files through your deployment or local operator setup:

| File | Contents |
| --- | --- |
| `receiver.dsn` | Complete PostgreSQL connection string for the receiver database and its own account. |
| `catalog-token` | Catalog access token, without a `Bearer` prefix. |
| `signer.json` | Exact webhook signing secret and version returned by registration or rotation. |
| `server.pem` | Receiver certificate chain for the hostname used by the Catalog. |
| `server-key.pem` | Receiver TLS private key. |
| `catalog-ca.pem` | Optional private CA for the Catalog API. Omit this input for public CA trust. |

Set credential files to mode `0600`. The example refuses symlinks, nonregular files, oversized inputs and group-readable or world-readable credentials. Keep the directory private. Do not put secret values in command arguments, source files or logs.

The signer file has this shape. Replace the placeholder with the exact returned secret and set its returned version:

```json
{"secret":"REPLACE_WITH_RETURNED_SECRET","version":1}
```

Use verified database TLS outside an owned loopback fixture. Include explicit connection settings in `receiver.dsn`. The program uses pgx's normal PostgreSQL connection parsing. Database credentials load at startup; restart the receiver after replacing them.

For a local receiver certificate, use OpenSSL in a shell with `REPORT_INPUTS` set:

```sh
umask 077
openssl req -x509 -newkey rsa:2048 -nodes -days 7 \
  -subj /CN=localhost \
  -addext 'subjectAltName=DNS:localhost,IP:127.0.0.1' \
  -keyout "$REPORT_INPUTS/server-key.pem" \
  -out "$REPORT_INPUTS/server.pem"
```

This certificate covers only local connections. For a receiver reachable from containers or another machine, use the actual receiver hostname and an appropriate certificate. Configure Catalog-side routing and trust separately.

The Catalog API origin is independent of the publication host. Use an API origin such as `https://catalog.example.test:8080`, not the UI origin. The host selects one configured publication, such as `catalog.example.test`.

## Grant API access

Grant these tenant-wide permissions to the receiver identity:

| Purpose | Catalog permissions | OAuth scopes, when enforced |
| --- | --- | --- |
| Read current snapshot summaries | `publication.read` | `catalog.read` |
| Read tenant notification loss counters | `webhook.read` and `webhook.read_evidence` | `webhooks.manage` |

A custom role can contain these three permissions. The receiver does not need Tenant Administrator or permission to change webhooks. Endpoint registration, tests and signer rotation use separate operator permissions. See [webhook access](../../documentation/src/content/docs/operations/webhooks.mdx#2-grant-only-the-required-access).

The SDK reads the token file for each request. Replace that file atomically when your issuer supplies a new token. The example does not obtain or refresh tokens.

## Initialize the receiver database

Set these nonsecret values for your environment:

```sh
CATALOG_REPORT_ORIGIN=https://catalog.example.test:8080
CATALOG_REPORT_TENANT=00000000-0000-4000-8000-000000000001
CATALOG_REPORT_HOST=catalog.example.test
```

Replace the sample tenant UUID. Create the receiver database and account before initialization. The account needs permission to create and use the example's three tables and one index.

For example, a database administrator can run these statements in `psql`. The password command prompts privately:

```sql
CREATE ROLE publication_receiver LOGIN;
\password publication_receiver
CREATE DATABASE publication_receiver OWNER publication_receiver;
```

Save a complete connection string for that account in `receiver.dsn`. A local-only example is `host=127.0.0.1 port=5432 dbname=publication_receiver user=publication_receiver password=REPLACE_ME sslmode=disable`. Replace the password placeholder. Do not use disabled database TLS outside the owned loopback setup.

Initialize only that receiver database:

```sh
bin/examples/publication-report init \
  --origin "$CATALOG_REPORT_ORIGIN" \
  --tenant "$CATALOG_REPORT_TENANT" \
  --host "$CATALOG_REPORT_HOST" \
  --database-file "$REPORT_INPUTS/receiver.dsn"
```

Initialization creates `publication_receiver_settings`, `publication_receiver_inbox` and `publication_receiver_report`. These are receiver-owned tables, outside the Catalog migration system. Initialization binds the database to the exact API origin, tenant and host. Other commands reject a different binding. Startup does not create tables automatically.

## Start the HTTPS receiver

Start one instance:

```sh
bin/examples/publication-report serve \
  --origin "$CATALOG_REPORT_ORIGIN" \
  --tenant "$CATALOG_REPORT_TENANT" \
  --host "$CATALOG_REPORT_HOST" \
  --database-file "$REPORT_INPUTS/receiver.dsn" \
  --token-file "$REPORT_INPUTS/catalog-token" \
  --signer-file "$REPORT_INPUTS/signer.json" \
  --tls-cert "$REPORT_INPUTS/server.pem" \
  --tls-key "$REPORT_INPUTS/server-key.pem" \
  --ca-file "$REPORT_INPUTS/catalog-ca.pem" \
  --listen 127.0.0.1:9443 \
  --output "$REPORT_INPUTS/publication-report.json"
```

Omit `--ca-file` when the Catalog uses public CA trust. The receiver requires TLS 1.2 or later. It does not disable certificate or hostname verification. `--allow-loopback-http` permits only an explicitly selected literal-loopback Catalog API; it does not weaken HTTPS.

The default listener accepts local connections only. Choose an interface and certificate hostname reachable by both the Catalog API and worker. Configure the Catalog's exact outbound origin policy and receiver CA trust before registering that URL. See the [webhook operations guide](../../documentation/src/content/docs/operations/webhooks.mdx).

Register the HTTPS `/webhook` URL with `publication.activated` and `publication.unpublished` subscriptions. Save the returned signer before sending an explicit endpoint test. A valid signed `webhook.test` returns `204` and completes without a snapshot lookup.

This example accepts one tenant and publication host. Other-host notifications receive `400`, which can appear as failed deliveries. Subscriptions do not provide a host filter. Do not route a multi-host notification stream here without addressing that restriction.

`GET /healthz` reports listener availability, not a successful Catalog refresh. The report stays in a private local file; the HTTP listener does not serve it.

## Read the report

The report contains `lastSuccessfulObservation`, `lastAttemptAt` and any `refreshFailure`. A successful observation includes its time, tenant, host and `active` summary. `active: null` means a complete successful read found no active publication.

The processor requests at most two active summaries for the exact host. It rejects a second active row or any continuation. It rejects duplicate keys, unknown fields and wrongly cased field names in these closed Control API responses. It validates the consumed tenant, host, state, identifiers, generation, audience, ETag and creation time. It does not download snapshot documents or follow resource URLs.

A denied, unavailable, malformed or timed-out read preserves the previous successful observation. The processor records the refresh failure separately. An old activation or withdrawal event therefore refreshes current state without reversing newer publication. Rollback can legitimately report a lower generation.

The trigger's `snapshotStateDigest`, when present, is event context. It is not an artifact digest or a hash of ARD response bytes. No report establishes trust or permission to execute a resource.

Loss counters describe the tenant notification system. They are not per-endpoint delivery cursors. `lossCountsObservedAt` records their last successful read. Before that timestamp exists, the default zero values do not establish that no notification was lost.

## Limits and processing

| Setting | Default | Accepted range or fixed limit |
| --- | --- | --- |
| Retained receipts, `--capacity` | 1,000 | 1-10,000 |
| Completed receipt retention, `--retention` | 24 hours | 1 minute-30 days |
| Work cadence, `--poll-interval` | 5 seconds | 1 second-1 minute; one pending item per iteration |
| Reconciliation, `--reconcile-interval` | 1 minute | 5 seconds-24 hours |
| HTTP work | 16 requests | Fixed nonblocking admission |
| Accepted connections | 64 | Fixed limit |
| Event body | 256 KiB | Fixed verifier limit |
| HTTP headers | 16 KiB | Fixed server limit |
| Catalog call | 5 seconds and 64 KiB | Fixed SDK deadline and response limit |
| Database pool | 4 connections | One connection holds the processor lock |

The server also bounds header reads, body reads, writes and idle connections. A request has three seconds to commit acceptance. Signature checks allow five minutes of age and 30 seconds of future skew. Keep Catalog and receiver clocks synchronized.

Acceptance verifies the exact signed bytes before event decoding. It rejects duplicate JSON keys and inconsistent envelopes. Signed `source` and body fields establish event identity; unsigned convenience headers do not.

Deduplication by `(source, id)` and queued work commit before `204`. Retained duplicates still receive `204` at capacity. New notifications receive `503` when capacity or storage prevents acceptance. Only completed receipts expire; pending work survives retention and restart.

Retention makes completed receipts eligible for pruning when a new notification is accepted. Each acceptance prunes at most 128 expired completed receipts. It checks retained duplicates first. There is no deletion timer.

The processor fetches Catalog state outside database transactions. Successful report storage and pending-work completion commit together. The report file is rendered afterward, in a separate operation. PostgreSQL and file writes do not form one transaction.

Each loop performs any due reconciliation, then processes at most one pending receipt. Reconciliation cannot replace receipt processing, even when its interval is shorter than the poll interval. Each processing and reconciliation step has a separate 12-second budget. Database reads for report rendering have a separate five-second budget. Slow work can delay the next iteration; the intervals do not promise exact execution times.

Run only one processor. A session advisory lock refuses an accidental second processor. It is not a lease or a multi-instance failover mechanism. Stop the old instance before starting a replacement, including after a database outage.

## Recover interrupted work

Restart the receiver with the same database and binding after an interrupted process. Pending work remains available. Startup and periodic reconciliation read current publication even when loss counters remain unchanged. Keep reconciliation active after missed delivery, database restore or notification disablement and reenablement.

For a one-shot reconciliation, stop `serve` and run the bounded command below. An operator schedule can repeat this command. It does not start a listener or drain pending receipts. Use `serve` for notification processing. Do not run two processors concurrently:

```sh
bin/examples/publication-report reconcile \
  --origin "$CATALOG_REPORT_ORIGIN" \
  --tenant "$CATALOG_REPORT_TENANT" \
  --host "$CATALOG_REPORT_HOST" \
  --database-file "$REPORT_INPUTS/receiver.dsn" \
  --token-file "$REPORT_INPUTS/catalog-token" \
  --ca-file "$REPORT_INPUTS/catalog-ca.pem" \
  --output "$REPORT_INPUTS/publication-report.json"
```

A failed output write does not undo the stored observation or work completion. The next receiver iteration can render it again. For manual rendering, stop `serve` before writing the same output file. Repair the output directory, then render the saved result without a Catalog call:

```sh
bin/examples/publication-report render \
  --origin "$CATALOG_REPORT_ORIGIN" \
  --tenant "$CATALOG_REPORT_TENANT" \
  --host "$CATALOG_REPORT_HOST" \
  --database-file "$REPORT_INPUTS/receiver.dsn" \
  --output "$REPORT_INPUTS/publication-report.json"
```

Replace `signer.json` atomically after rotation. Each incoming request reads its current version. During routine overlap, the copied verifier can verify the predecessor signature with the configured predecessor key. Install the new key before that overlap expires. Emergency rotation also requires immediate replacement at the receiver.

Repair failed API reads before clearing a full inbox. Do not delete pending work to recover capacity. Receipt pruning permits sufficiently delayed duplicates, so repeated processing always reads current state. Acknowledgement does not promise exactly-once processing.

Press Ctrl-C or send SIGTERM to stop. The receiver allows up to ten seconds to drain HTTP work. Successful commands and graceful shutdown exit `0`; failures exit `1` with fixed diagnostics. An interrupted output can be rendered again from the saved database result.
