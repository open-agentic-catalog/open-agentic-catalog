# Rust SDK

The Rust SDK provides typed asynchronous operations for the [Control API](../../api/openapi/control-v1.yaml). It connects to one configured Catalog API. It does not log in, execute resources, follow referrals or install packages.

Use Rust 1.88 or newer and a Tokio runtime with time and I/O enabled. Source builds also need a native C toolchain for TLS dependencies. The maintained qualification runs on Linux AMD64 with Rust 1.88.0 and 1.99.0. Other native platforms are not qualified. Blocking, WASM and FFI interfaces are not provided.

## Install from local source

Maintainer-published GitHub Releases provide SDK downloads. Follow the [download and checksum instructions](../../documentation/src/content/docs/contributing/build-and-release.md#download-release-packages) before installation. Packages are not published to ecosystem registries. Source installation remains available below.

The package is named `open-agentic-catalog`; Rust imports use `open_agentic_catalog`. Registry publication is disabled. Use an existing Cargo binary project, or create one:

```sh
cargo new --bin catalog-consumer
cd catalog-consumer
```

Add these entries to the application's `[dependencies]` table. Replace the source path with your Catalog checkout:

```toml
[dependencies]
open-agentic-catalog = { path = "/path/to/open-agentic-catalog/sdk/rust" }
tokio = { version = "1.49", features = ["macros", "rt-multi-thread"] }
serde_json = "1"
```

Consumers compile the checked-in Rust source. They do not need the generator, Java, Go, another Catalog SDK or a running Catalog during compilation. Cargo can download dependencies during the initial installation. Your application owns its dependency lock. The library's `Cargo.lock` records repository qualification; it does not fix downstream dependency resolution.

To use a local archive, run `make sdk-package SDK_LANGUAGE=rust` after tool installation. Extract `output/artifacts/sdk/rust/open-agentic-catalog-<version>.crate` into a local directory. Point the dependency's `path` at the extracted crate. Require a successful package command and matching artifact manifest before reuse. A package build does not establish test success.

## Make your first request

Save this program as `src/main.rs` in the consumer project. Supply `CATALOG_SDK_ORIGIN` and `CATALOG_SDK_TOKEN` through its process environment. The token needs an active [Catalog membership](../../documentation/src/content/docs/guides/integrating.mdx#authenticate-and-establish-membership).

```rust no_run
use open_agentic_catalog::{Client, Config};

#[tokio::main]
async fn main() {
    if check_connection().await.is_err() {
        eprintln!("Catalog capabilities request failed");
        std::process::exit(1);
    }
}

async fn check_connection() -> Result<(), Box<dyn std::error::Error>> {
    let mut config = Config::new(std::env::var("CATALOG_SDK_ORIGIN")?);
    config.token = Some(std::env::var("CATALOG_SDK_TOKEN")?);
    config.allow_loopback_http =
        std::env::var("CATALOG_SDK_ALLOW_LOOPBACK_HTTP").as_deref() == Ok("on");
    if let Ok(path) = std::env::var("CATALOG_SDK_CA_FILE") {
        config.root_certificates.push(std::fs::read(path)?);
    }
    let client = Client::new(config)?;
    let response = client.get_capabilities().await?;
    response.data.ok_or("Expected capability metadata")?;
    println!("Catalog API connection succeeded");
    Ok(())
}
```

Run the program from the consumer project:

```sh
cargo run
```

The program reads capability metadata without changing Catalog data. Its diagnostics omit private error details. Use the [error-handling guidance](#read-results-and-handle-errors) when your application needs a safe failure classification.

| Example input | Value |
| --- | --- |
| `CATALOG_SDK_ORIGIN` | Required API origin, such as `https://catalog.example.com`. The default local Compose API uses `http://127.0.0.1:8080`. |
| `CATALOG_SDK_TOKEN` | Required access token supplied by your deployment. Do not use the UI session cookie. |
| `CATALOG_SDK_ALLOW_LOOPBACK_HTTP` | Set to `on` only for an explicitly selected local HTTP API. Other values leave HTTP disabled. |
| `CATALOG_SDK_CA_FILE` | Optional path to a PEM file with your private CA certificates. Certificate and hostname checks remain enabled. |

These environment names belong to the program; the SDK itself does not read them. Supply credentials through your development or deployment tools. Do not put tokens in source files or shell history. The remaining examples are functions for an application with a configured client and active Tokio runtime.

## Connect and search one page

Connect to the API origin, not the optional UI host. Supply your deployment's access token explicitly. The API enforces current membership, tenant, namespace and OAuth permissions.

```rust no_run
use open_agentic_catalog::{CatalogSearch, Client, Config};
use serde_json::json;

async fn search_one_page(client: &Client, cursor: Option<String>)
    -> Result<open_agentic_catalog::CatalogSearchResponse, Box<dyn std::error::Error>>
{
    let mut request = json!({
        "text": "documentation helper", "limit": 10, "origins": ["local"]
    });
    if let Some(cursor) = cursor {
        request["cursor"] = serde_json::Value::String(cursor);
    }
    let response = client.search_catalog(serde_json::from_value::<CatalogSearch>(request)?).await?;
    Ok(response.data.ok_or("Expected a Search page")?)
}

async fn connect_and_search(origin: &str, access_token: &str)
    -> Result<(), Box<dyn std::error::Error>>
{
    let mut config = Config::new(origin);
    config.token = Some(access_token.to_owned());
    let client = Client::new(config)?;
    let page = search_one_page(&client, None).await?;
    println!("Items on this page: {}", page.items.len());
    // Pass page.next_cursor to search_one_page with the same query and filters.
    Ok(())
}
```

The [complete discovery example](examples/discover.rs) reads explicit example inputs from `CATALOG_SDK_ORIGIN` and `CATALOG_SDK_TOKEN`. The SDK does not read environment credentials itself. The example accepts optional `CATALOG_SDK_CA_FILE` and `CATALOG_SDK_ALLOW_LOOPBACK_HTTP=on` inputs. Run it with `cargo run --manifest-path sdk/rust/Cargo.toml --example discover`. Supply credentials through your development or deployment tools; do not put tokens in source files or shell history.

Each call returns one page. Pass `next_cursor` unchanged in a subsequent request with the same query and filters. Do not decode the cursor or collect an unbounded number of pages. Indexed and live result types preserve their separate provenance. The SDK does not contact the URLs in those results.

The example explicitly selects local published releases. Indexed or live discovery needs `federation.discover` and deployment-approved sources. See the [Search filter contracts](../../documentation/src/content/docs/guides/integrating.mdx#example-filter-and-continue-control-api-search) before changing origins or adding filters.

For a standalone application, follow the [Rust Catalog report](../../examples/rust-catalog-report/README.md). It reads an explicit token file, follows bounded local Search pages, verifies exact release records and emits escaped Markdown with complete/partial exit status. Its guide includes a worked curated-sample query, private CA setup and troubleshooting.

## Continue Search with a finite budget

This function reads at most three pages and ten results per page. Pass `None` for the first cursor:

```rust no_run
use open_agentic_catalog::{CatalogSearch, CatalogSearchResponse, Client};
use serde_json::json;

async fn search_local_pages(client: &Client, cursor: Option<String>)
    -> Result<(Vec<CatalogSearchResponse>, Option<String>), Box<dyn std::error::Error>>
{
    let mut pages = Vec::with_capacity(3);
    let mut next = cursor;
    for _ in 0..3 {
        let mut request = json!({
            "text": "documentation helper", "limit": 10, "origins": ["local"]
        });
        if let Some(cursor) = &next {
            request["cursor"] = json!(cursor);
        }
        let response = client.search_catalog(
            serde_json::from_value::<CatalogSearch>(request)?
        ).await?;
        let page = response.data.ok_or("Expected a Search page")?;
        next = page.next_cursor.clone().filter(|value| !value.is_empty());
        pages.push(page);
        if next.is_none() {
            break;
        }
    }
    Ok((pages, next))
}
```

Inspect `partial` and `diagnostics` on every page. A returned cursor means that the three-page budget ended before traversal completed. Save it for another explicit call with the same query, filters, page size and identity. A short or empty page can still have a cursor. Do not use item count as the stop condition.

An error returns no completed traversal. Do not silently restart an expired cursor or discard a partial-result warning. An application that displays each page as it arrives should keep its existing pages and cursor explicitly.

## Open a local resource's complete record

A local Search item's release UUID identifies its complete Catalog record. The Search result ID and resource UUID are different identifiers:

```rust no_run
use open_agentic_catalog::{CatalogRecord, CatalogSearchResult, CatalogSearchResultType, Client};

async fn read_local_result(client: &Client, item: &CatalogSearchResult)
    -> Result<CatalogRecord, Box<dyn std::error::Error>>
{
    if item.r#type != CatalogSearchResultType::Local {
        return Err("Select a local published result".into());
    }
    let local = item.local.as_ref().ok_or("Missing local release metadata")?;
    let response = client.get_catalog_record(local.release.id.to_string()).await?;
    Ok(response.data.ok_or("Expected a Catalog record")?)
}
```

Indexed and live results describe external evidence. The function rejects them rather than treating an external identifier as a local release UUID. Inspect their provenance and declared representation before an explicit [client handoff](../../documentation/src/content/docs/guides/integrating.mdx#interpret-results-before-handing-them-off). The SDK does not invoke their URLs or forward your Catalog credentials to them.

## Export a reusable draft

Use an exact VersionRevision UUID. Your identity needs current resource metadata and content read permissions in its Namespace.

```rust no_run
use open_agentic_catalog::{CatalogDraftFile, Client};

async fn export_draft(client: &Client, revision_id: &str)
    -> Result<CatalogDraftFile, Box<dyn std::error::Error>>
{
    let response = client.export_version_revision_draft(
        revision_id, None::<&str>
    ).await?;
    Ok(response.data.ok_or("Expected a reusable draft file")?)
}
```

Pass a matching ListingRevision UUID as the second argument to include Listing metadata. The file contains authoring input, without credentials or governance approvals. Import it through the [draft transfer workflow](../../documentation/src/content/docs/guides/draft-transfer.mdx).

The client's response limit still applies. Export can require a larger explicit limit, as shown under [Read exact bytes](#read-exact-bytes).

## Private trust and local HTTP

HTTPS verifies certificates and hostnames. Add private CA certificates as PEM bytes in `config.root_certificates`; these supplement platform trust. There is no option to disable certificate verification. Each supplied PEM bundle is limited to 1 MiB, with at most 32 bundles.

```rust
use open_agentic_catalog::{Client, Config};

fn private_client(origin: &str, ca_pem: Vec<u8>)
    -> Result<Client, open_agentic_catalog::Error>
{
    let mut config = Config::new(origin);
    config.root_certificates.push(ca_pem);
    Client::new(config)
}
```

For an owned local API, set `config.allow_loopback_http = true` and use an explicit literal address such as `http://127.0.0.1:8080`. This option does not permit HTTP to `localhost`, arbitrary hostnames or non-loopback addresses. Origins cannot contain credentials, paths, queries or fragments.

## Supply credentials per call

Use either `config.token` or `config.token_provider`. A provider returns a future for one token; its output passes the same validation as a static token. The SDK evaluates it once after request validation. The call deadline includes credential acquisition. A provider must cooperate with asynchronous cancellation and must not block the runtime.

```rust
use open_agentic_catalog::{Config, Error};
use std::sync::Arc;

fn provider_config(origin: &str) -> Config {
    let mut config = Config::new(origin);
    config.token_provider = Some(Arc::new(|| Box::pin(async {
        // Replace this example with your application's credential provider.
        Err(Error::CredentialFailed)
    })));
    config
}
```

For anonymous ARD reads, leave both credential fields unset. Use `get_ard_manifest` or the other generated ARD operations. A configured discovery host and an active publication are still required. HTTP 404 can mean that the selected host has no active publication. A declared HTTP 304 returns `data: None`, with its status, headers and raw bytes preserved.

## Read results and handle errors

Successful typed calls return `Response<T>` with `status`, `headers`, `raw_body` and optional `data`. Headers preserve ETag, request ID, Retry-After and command-replay metadata. Read raw bytes when exact evidence matters. Typed serialization can change JSON formatting, date spelling or URL normalization.

Generated methods return `ApiOpError::Api` for HTTP refusals and successful responses that cannot be decoded. JSON decoding rejects invalid UTF-8 instead of replacing bytes. Its explicit fields retain bounded raw evidence and a typed error when available. Control problem details and ARD errors use their own generated types. `ApiOpError::Transport` reports a safe `Error` code when no complete bounded result is available.

`Client::raw` returns every HTTP status without classifying it as an error. Inspect `status` yourself. Its `json` helper decodes the retained body without replacing it. Configuration, client, response and error formatting omit credentials, headers, private bodies and dependency causes. Do not log those explicit fields indiscriminately.

The SDK never retries, follows redirects, forwards referrers, uses ambient proxies/cookies or decompresses responses automatically. This remains true when another dependency enables reqwest compression features. A transport failure after a command does not prove that the command failed to commit. Read back its state or deliberately repeat the exact command with its original key and validator.

For example, classify Search failures without printing private response bodies:

```rust
use open_agentic_catalog::{ApiOpError, SearchCatalogApiError};

fn search_failure_message(failure: &ApiOpError<SearchCatalogApiError>) -> String {
    match failure {
        ApiOpError::Transport(error) => format!("{error}"),
        ApiOpError::Api(response) if response.status == 401 =>
            "Catalog access token is absent, expired or refused".to_owned(),
        ApiOpError::Api(response) if response.status == 403 =>
            "Current Catalog permissions do not allow this search".to_owned(),
        ApiOpError::Api(response) if (200..300).contains(&response.status) =>
            "Catalog returned an unexpected or malformed success response".to_owned(),
        ApiOpError::Api(response) => format!("Search returned HTTP {}", response.status),
    }
}
```

Use `response.headers.get("x-request-id")` for operator correlation when available. Inspect `response.typed` deliberately when your application needs the Control problem code. Do not print `body`, `raw_body` or `parse_error` as routine diagnostics. An HTTP 429 or 503 remains a refusal; the SDK neither sleeps nor retries it.

## Create a resource

Supply the UUID of a namespace where your identity can create resources. This example creates an Agent draft. It does not upload a card, approve a version or publish a release.

```rust no_run
use open_agentic_catalog::{Client, CreateResource, Resource};
use serde_json::json;

async fn create_agent(client: &Client, namespace_id: &str,
    identifier: &str, command_key: &str)
    -> Result<Resource, Box<dyn std::error::Error>>
{
    let request: CreateResource = serde_json::from_value(json!({
        "namespaceId": namespace_id,
        "kind": "agent",
        "canonicalIdentifier": identifier
    }))?;
    let created = client.create_resource(command_key, request).await?;
    Ok(created.data.ok_or("Expected a created resource")?)
}
```

Use `mcp_server`, `skill` or `plugin` for another supported kind. Follow [resource authoring](../../documentation/src/content/docs/guides/resource-authoring.mdx) for complete version evidence and [publication](../../documentation/src/content/docs/guides/publishing.mdx) for review and release. Retain the returned identity and command key before another step.

## Send an authorized command

The caller supplies the idempotency key and ETag. Keep the request unchanged for exact-key replay. A different request needs a different key.

```rust no_run
use open_agentic_catalog::{Client, CreateNamespace, PatchNamespace};

async fn create_and_update(client: &Client, create_key: &str, update_key: &str)
    -> Result<(), Box<dyn std::error::Error>>
{
    let created = client.create_namespace(
        create_key,
        CreateNamespace::new("Example".to_owned(), "example".to_owned()),
    ).await?;
    let etag = created.headers.get("etag")
        .ok_or("Created namespace response has no ETag; read back its state")?
        .to_str()?.to_owned();
    let namespace = created.data.ok_or("Expected a namespace")?;
    let mut patch = PatchNamespace::new("Update the display name".to_owned());
    patch.display_name = Some("Updated example".to_owned());
    let updated = client.update_namespace(
        namespace.id.to_string(), update_key, etag, patch,
    ).await?;
    if updated.status != 200 {
        return Err(format!("Namespace update returned HTTP {}", updated.status).into());
    }
    Ok(())
}
```

Generated models expose ordinary fields and schema alternatives. Some conditional schemas retain JSON values rather than validate every branch locally. The API remains the authority for domain rules. Native and extension JSON supports large numbers without floating-point conversion. Use raw request bytes if the original JSON text itself must remain unchanged.

The application must retain a different key for each intended command. The example creates and updates a namespace; it does not make either command safe to repeat with a fresh key. A stale ETag requires a new read and an explicit decision. A lost response requires readback or exact-key reconciliation within the [command replay window](../../documentation/src/content/docs/reference/api-conventions.md#request-rules).

A successful write without its expected ETag or body also needs readback before the next command. Do not repeat that write with a fresh key.

## Inspect an asynchronous command

An HTTP 202 command response means that the Catalog accepted work. It does not prove that a source synchronization or publication succeeded. Retain the returned Job UUID and read its current state:

```rust no_run
use open_agentic_catalog::{Client, Job, UUID};

async fn read_job(client: &Client, job_id: UUID)
    -> Result<Job, Box<dyn std::error::Error>>
{
    let response = client.get_job(job_id.to_string()).await?;
    Ok(response.data.ok_or("Expected a Job")?)
}
```

This function makes one read; it does not poll or retry. `Queued`, `Running` and `Waiting` are nonterminal states. `Succeeded`, `Failed`, `Cancelled` and `DeadLetter` are terminal states. Only `Succeeded` reports successful completion. Inspect the returned state and permitted result fields before the next workflow step.

Job reads require current `job.read` permission plus the relevant family and target permissions. A Job UUID grants no access by itself. Private trust evidence uses a separate authorized result read. See [Job access and result projections](../../documentation/src/content/docs/reference/api-conventions.md#practical-administration) for the shared rules.

If your application polls, use an explicit deadline and finite request budget. Preserve cancellation and stop on terminal states. The SDK adds no background task, token refresh or automatic polling.

## Set finite call limits

| Setting (`Limits` field) | Default | Accepted override |
| --- | --- | --- |
| Whole call timeout (`timeout`) | 30 seconds | Greater than zero, at most 24 hours |
| Request body (`request_bytes`) | 16 MiB | 1 byte to 1 GiB |
| Response body (`response_bytes`) | 8 MiB | 1 byte to 1 GiB |
| Returned headers (`header_bytes`) | 32 KiB | 1 byte to 1 GiB |

Set `config.limits` at construction. Use `client.with_limits(limits)?` for an independent client clone with different budgets. It does not change the original client. The deadline covers credentials, transport and body consumption. Drop or abort the future to cancel the operation. The returned-header budget does not describe every allocation inside the HTTP parser.

```rust
use open_agentic_catalog::{Client, Limits};
use std::time::Duration;

fn larger_evidence_read(client: &Client) -> Result<Client, open_agentic_catalog::Error> {
    client.with_limits(Limits {
        timeout: Duration::from_secs(60),
        response_bytes: 32 << 20,
        ..Limits::default()
    })
}
```

An optional `Transport` implementation receives validated fixed-origin requests. The standard implementation handles bounded real HTTP/TLS. A custom implementation must bound its own reads and cooperate with cancellation; the client also checks its returned status, headers and body.

## Read exact bytes

Use the UUID of a readable VersionRevision. The client below permits a larger finite response for that read. It leaves the original client's limits unchanged.

```rust no_run
use open_agentic_catalog::{Client, Limits, UUID};

async fn read_revision(client: &Client, revision_id: UUID)
    -> Result<Vec<u8>, Box<dyn std::error::Error>>
{
    let reader = client.with_limits(Limits {
        response_bytes: 32 << 20,
        ..Limits::default()
    })?;
    let response = reader.raw("GET",
        &format!("/api/v1/version-revisions/{revision_id}"),
        Default::default(), Vec::new()).await?;
    if response.status != 200 {
        return Err(format!("Revision read returned HTTP {}", response.status).into());
    }
    Ok(response.raw_body)
}
```

These bytes preserve the API response. They are not automatically the original external artifact. An exceeded budget refuses the response; it never returns truncated evidence.

## Find an operation

Rust methods use snake_case names for OpenAPI operation IDs. The [OpenAPI contract](../../api/openapi/control-v1.yaml) owns request fields, permissions, status codes and command requirements.

| Task | Rust method |
| --- | --- |
| Check API access and capabilities | `get_capabilities` |
| Read current access | `get_current_access` |
| Search one page | `search_catalog` |
| Read a released Catalog record | `get_catalog_record` |
| Create a resource draft | `create_resource` |
| Read a VersionRevision | `get_version_revision` |
| Inspect a Job's current outcome | `get_job` |
| Read the ARD manifest | `get_ard_manifest` |
| Preserve an exact HTTP response | `raw` |

Generate browsable Rust API documentation from a local checkout:

```sh
cargo doc --manifest-path sdk/rust/Cargo.toml --no-deps
```

Cargo prints the output location. Open `open_agentic_catalog/index.html` there, then select `Client` or a generated model. This consumer command can acquire dependencies. Maintainer checks use the installed, frozen workflow below.

## Troubleshoot a call

| Result | Check or next action |
| --- | --- |
| `InvalidConfig` | Check the origin, finite limits, mutually exclusive credentials and PEM certificate inputs. Origins cannot contain a path, query or fragment. |
| `InvalidRequest` | Check the method, path, headers and input serialization. Invalid requests are refused before credential acquisition or transport. |
| `InvalidCredential` | Supply a valid bounded bearer value. Do not include whitespace, control characters or non-ASCII bytes. |
| `CredentialFailed` | Check your credential provider. The SDK does not acquire or refresh tokens automatically. |
| `RequestFailed` | Check API reachability, certificate trust and hostname. A failed write can have committed; preserve its command bindings. |
| `Timeout` | Check the provider and API budgets. Do not assume a timed-out write rolled back. |
| `RequestTooLarge` | Reduce the request or select a valid finite request budget. The SDK refuses the body before transmission; server limits still apply. |
| `ResponseTooLarge` or `HeadersTooLarge` | Review the expected response. Increase a finite budget only when the response is valid and needs it. |
| `UnsupportedEncoding` | The server returned a compressed response. The SDK requires identity encoding and does not decompress it. |
| `DecodeFailed` | An explicit `Response::json` read failed. The retained raw bytes remain available; generated typed-call decoding failures use `ApiOpError::Api`. |
| HTTP 401 | Supply a current token accepted by the API's configured issuer and audience. The SDK does not refresh it. |
| HTTP 403 | Check current Catalog permissions, namespace scope and OAuth scopes. A different UI role label does not grant API access. |
| HTTP 404 | Check the selected identity and object. ARD manifests also need a configured host with an active publication. |
| HTTP 429 or 503 | Inspect Retry-After. The application chooses whether and when a safe operation can be repeated. |
| A typed response cannot decode | Inspect its status and bounded raw evidence deliberately. Treat a malformed write response as an uncertain outcome. |

Use the returned `X-Request-ID` to correlate an API refusal with operator diagnostics. Do not log tokens, headers or private response bodies. See the [integration guide](../../documentation/src/content/docs/guides/integrating.mdx#use-the-rust-client) for command recovery and discovery boundaries.

## Develop and verify

Use the existing targets, one at a time:

```sh
make sdk-tools-install SDK_LANGUAGE=rust
make sdk-generate SDK_LANGUAGE=rust
make sdk-check SDK_LANGUAGE=rust
make sdk-package SDK_LANGUAGE=rust
```

Maintainers need Linux AMD64, Python 3.12+, rustup, Git, the GitHub CLI, a native C toolchain and the repository Go toolchain.

Run `gh auth status` to check the GitHub CLI account. Run `gh auth login` if the CLI has no authenticated account. The installer uses `gh api` to download public source archives. That command requires authentication even for public repositories. Do not put a token in a command or repository file.

Installation acquires pinned compilers, components, generator sources and dependencies. It changes no personal Rust default. Ordinary generation, checks and packaging use the installed tools and frozen locks without downloads.

`sdk-check` combines isolated Rust units, compiler/example checks, local HTTP/TLS controls, actual in-memory Catalog workflows and consumers outside the checkout. It is not a unit-only target. It reports separate handwritten unit and combined local functional coverage; generated bindings and test drivers are excluded. See [SDK verification](../../test/sdk/README.md) for native unit commands, locks, notices, coverage, source identities and cleanup.

Tools and compiler caches live under `test/sdk/.tools/rust/`. Successful staging is removed. Failed staging remains under `output/sdk/` for investigation. `make clean` removes owned tools, default artifacts and Rust `target/` directories. It preserves the public advisory cache under `output/sdk-rust-advisories/` and retained reports.

Cargo-deny and RustSec data belong to optional audit tools, separate from generation, builds and tests. Install them with `make sdk-rust-audit-tools-install`. Run `make sdk-rust-audit` for offline license, source and advisory checks. The check refuses missing or changed audit inputs and advisory data older than seven days.

An audit result applies only to its selected inputs and advisory snapshot. Normal SDK manifests retain `dependencyAudit: not_run`. Follow [Rust verification](../../documentation/src/content/docs/contributing/testing.md#rust-sdk-verification) for the separate audit procedure. The [dependency policy](../../documentation/src/content/docs/reference/dependencies.md#rust-sdk-dependencies) defines its scope and the generator-only informational exception.

The crate uses Apache-2.0. Generated material retains the MIT generator notice. Normal installation records dependency inventories, source checksums, release ages and license metadata. The separate audit enforces license and advisory policy for runtime, build, test and tool dependencies. Downstream distributors must retain notices for their own complete dependency distribution. See the [dependency policy](../../documentation/src/content/docs/reference/dependencies.md#rust-sdk-dependencies).

See the [SDK overview](../README.md#choose-a-language) to compare languages and the [integration guide](../../documentation/src/content/docs/guides/integrating.mdx#use-the-rust-client) for shared workflows.
