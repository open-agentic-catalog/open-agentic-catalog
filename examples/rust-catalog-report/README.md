# Rust Catalog report

Build a small read-only CLI with the [Rust SDK](../../sdk/rust/README.md). Search authorized local releases, inspect each exact record and print a Markdown inventory. The report includes name, kind, version, release UUID, canonical identifier and declared license.

The program calls only Catalog Search and exact local record reads. It does not follow metadata URLs, install packages or invoke resources.

## Prerequisites

Use a source checkout of this repository, Rust 1.88 or newer, Cargo and a native C toolchain for the SDK's TLS dependencies. The [Rust SDK guide](../../sdk/rust/README.md#install-from-local-source) explains source installation. Qualification uses the maintained compiler and Rust 1.88 on Linux AMD64.

Obtain the API origin and a bare access-token file from your operator. Use [Connect as a reader](../../documentation/src/content/docs/guides/reader-quickstart.mdx) to establish access. A tenant Reader sees its authorized local releases; a namespace Reader sees releases in granted namespaces. Enforced OAuth scopes also require `catalog.read`.

The token file contains the access token alone, with an optional final LF or CRLF. Do not supply a `Bearer` prefix and space or the JSON credential cache from `catalogctl auth login`. Protect the file with mode 0600 or equivalent access controls. The program does not log in, refresh credentials or read environment files.

Use the API origin, such as `https://catalog.example.com`, rather than the optional UI origin. Private certificate authorities require `--ca-file PATH`. Supply a PEM certificate bundle of at most 1 MiB. HTTPS certificate and hostname verification remain enabled.

## Build and connect

Run these commands from the repository root. Acquire the locked dependencies explicitly, then build without further acquisition:

```sh
cargo fetch --locked --manifest-path examples/rust-catalog-report/Cargo.toml
cargo build --frozen --manifest-path examples/rust-catalog-report/Cargo.toml
```

Supply your API origin and private token-file path:

```sh
cargo run --frozen --manifest-path examples/rust-catalog-report/Cargo.toml -- \
  --origin https://catalog.example.com \
  --token-file /secure/catalog-reader.token
```

Add `--ca-file /secure/catalog-ca.pem` for a private issuer of the API's TLS certificate. This is independent of the OAuth issuer's signing keys. The SDK uses a fixed API origin, omits ambient proxies and refuses redirects.

Keep the committed example `Cargo.lock`. The SDK's library lock does not constrain this application's dependency resolution. This crate uses a local path to `sdk/rust/`; preserve the repository layout if you relocate the checkout.

## Try the populated sample Catalog

Prepare an owned development Catalog with the [curated sample procedure](../../documentation/src/content/docs/guides/sample-catalog.mdx#start-the-catalog-with-compose-and-load-sample-data). Loading samples is a separate authoring operation. Running the report does not load or modify data.

Keep the API port selected in the sample guide. Its default is 18086; the UI uses a separate port. Set the API origin and create a private file containing the synthetic Reader credential:

```sh
export CATALOG_REPORT_ORIGIN="http://127.0.0.1:${CATALOG_COMPOSE_HTTP_PORT:-18086}"
mkdir -p output/rust-report
(umask 077; printf '%s\n' 'local-reader' > output/rust-report/reader.token)
cargo run --frozen --manifest-path examples/rust-catalog-report/Cargo.toml -- \
  --origin "$CATALOG_REPORT_ORIGIN" \
  --token-file output/rust-report/reader.token \
  --allow-loopback-http > output/rust-report/catalog-report.md
```

If you selected another API port, retain it in `CATALOG_COMPOSE_HTTP_PORT` before setting this origin. `local-reader` applies only to the configured development identity; it is not a production credential. HTTP requires the explicit opt-in and a literal loopback IP, such as `127.0.0.1` or `[::1]`. A hostname such as `localhost` does not qualify.

A fresh default curated Catalog contains twelve published releases, three per kind, plus two resources outside discovery. With tenant Reader access, the report completes in two pages. Existing Catalogs can contain more records; a namespace grant can expose fewer. Sample loading and [receipt extension](../../documentation/src/content/docs/guides/sample-catalog.mdx#receipts-and-reruns) have their own counts and permissions.

The report starts with the selected query, kind, scope and status. Each subsequent row comes from an exact record read. For example, the `Web Application Testing` row identifies a Skill, its actual version, its assigned release UUID, `urn:air:demo.catalog.example.com:demo:webapp-testing` and its `Apache-2.0` declaration. UUIDs come from that instance; they are not sample constants.

This excerpt shows one of the twelve rows. `<assigned UUID>` stands for the instance's actual release ID:

```markdown
# Catalog report

Scope: caller-authorized local releases. Paging is not a point-in-time snapshot.

Query: all

Kind: all

Status: Complete (2 pages, 12 releases).

| Name | Kind | Version | Release UUID | Canonical identifier | Declared license |
| --- | --- | --- | --- | --- | --- |
| Web Application Testing | skill | source-8a1541c4 | <assigned UUID> | urn:air:demo.catalog.example.com:demo:webapp-testing | Apache-2.0 |
```

Search the same Catalog for testing resources:

```sh
cargo run --frozen --manifest-path examples/rust-catalog-report/Cargo.toml -- \
  --origin "$CATALOG_REPORT_ORIGIN" \
  --token-file output/rust-report/reader.token \
  --allow-loopback-http --query testing --kind skill
```

The curated query includes `Web Application Testing`. Omit both filters to search all four kinds. `--kind` accepts one of `agent`, `mcp_server`, `skill` or `plugin`; `--query` accepts at most 500 UTF-8 bytes.

## Read the result and exit status

| Exit | Meaning | Output |
| --- | --- | --- |
| 0 | Search continuation is exhausted, with no partial page. | Complete Markdown report, or `No authorized matches.` |
| 2 | The page budget leaves continuation, or a page reports partial results. | Markdown report visibly labeled `Incomplete`, including safe diagnostic status codes. |
| 1 | Invalid inputs, denied/unavailable reads, malformed records, expired continuation or a deadline. | Safe stderr diagnostic; discovery failures emit no report. |

All selected reads finish before report output begins. Do not interpret an empty output file from a failed command as an empty Catalog. Check the process status when using redirection or automation. A write failure also returns exit 1.

The client requests `origins: ["local"]` on every page. It follows unchanged cursors with identical filters, including after short or empty pages. It reads at most three pages of ten items and thirty exact records, sequentially, within a sixty-second run deadline. Each SDK call retains its thirty-second deadline, 8 MiB response limit and other transport bounds.

An incomplete report with zero rows says `No rows were obtained within the budget.` It cannot establish that no matches exist. The report does not expose continuation tokens or free-form server diagnostics. Rerun deliberately after an expired cursor; the client never restarts it automatically.

Completion describes paging for this query and caller. Sequential reads do not form a point-in-time snapshot. A release can become unavailable or permission can change during the run; either makes the command fail safely.

Missing license text appears as `not declared`. Values above 512 Unicode characters appear as `omitted: exceeds 512 characters`, without a truncated declaration. Markdown, HTML, control characters and directional overrides are escaped. A declared license is metadata, not legal clearance, trust or execution approval.

## Troubleshoot

| Symptom | Action |
| --- | --- |
| Token-file error before requests | Check the selected regular file, permissions, size and bare-token format. Tokens are limited to 32 KiB, plus one optional final newline. |
| `InvalidConfig` | Check the API origin. Use HTTPS, or explicitly enable HTTP at a literal loopback IP. Remove paths, credentials, query strings and fragments. Check private CA bytes. |
| HTTP 401 | Obtain a valid access token for this Catalog. Check issuer, audience and expiry with your operator. |
| HTTP 403 | Check current Reader membership, namespace grants and the enforced `catalog.read` scope. |
| HTTP 404 during record reads | The release may no longer be discoverable or authorized. Rerun Search deliberately. |
| HTTP 400 `INVALID_ARGUMENT` during continuation | The cursor is invalid, expired or stale. Start a new report with the same intended query and current access. |
| HTTP 409 | Read the returned conflict code. A conflict is distinct from an invalid Search cursor; do not treat it as a continuation response. |
| HTTP 503 | A required dependency is unavailable. Check readiness and retry the complete read-only report deliberately. |
| Unexpected or malformed success response | Check that the API and SDK use compatible contracts. The command failed to decode the response; HTTP 200 alone does not establish a successful read. |
| Timeout or request failure | Check API readiness, private trust and network access. Reduce the query's matches if sequential reads exceed the run budget. |
| Exit 2 | Treat the output as incomplete. Narrow `--query` or `--kind`, or use the SDK in an application with another explicit finite budget. |
| No authorized matches | Check the selected Catalog, query and grants. Confirm that resources have governed releases; drafts and pending reviews stay outside discovery. |
| Frozen Cargo reports unavailable dependencies | Run the explicit locked fetch step. Maintainers use the SDK tool installer below; checks never install tools. |

## Verify and clean up

Run only native isolated example units with an installed compiler and cached dependencies:

```sh
cargo test --frozen --manifest-path examples/rust-catalog-report/Cargo.toml
```

The maintained [Rust qualification](../../test/sdk/README.md#rust-sdk) additionally builds and tests both compilers, runs formatting and Clippy, and executes owned HTTP/TLS and composed Catalog fixtures:

```sh
make sdk-tools-install SDK_LANGUAGE=rust
make sdk-check SDK_LANGUAGE=rust
```

The executable tests verify real paging, exact release IDs, namespace denials, partial/empty pages, safe failures, cancellation and the twelve-release curated report. Curated preparation uses owned in-memory API/worker and TLS archive fixtures. No external Catalog, database deployment, model or resource execution is required. Unit and functional checks remain separate.

Ordinary Cargo output uses `examples/rust-catalog-report/target/`; qualification uses the existing ignored SDK tool cache. `make clean-dry-run` includes both, and `make clean` removes them through the explicit cleanup inventory. Reports and private inputs under `output/rust-report/` remain; remove only your own files when finished. Cleanup does not stop or clear a Catalog.

Project source uses [Apache License 2.0](../../LICENSE). Preserve the SDK and dependency notices when distributing a consumer; this example's package publication is disabled.

Dependency audits are separate from builds and tests. Install their tools with `make sdk-rust-audit-tools-install`. Run `make sdk-rust-audit` to check the report's complete dependency graph.

Results depend on the selected installed inputs and advisory data no older than seven days. Missing or changed audit inputs cause refusal. Normal builds and tests do not establish audit success. Follow [Rust verification](../../documentation/src/content/docs/contributing/testing.md#rust-sdk-verification) for the separate audit procedure and [Rust dependency policy](../../documentation/src/content/docs/reference/dependencies.md#rust-sdk-dependencies) for its scope.
