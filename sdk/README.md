# SDK overview

Use a Catalog SDK to read discovery metadata, search resources and call authorized control operations from your application. Choose Go, Python, JavaScript/TypeScript or Rust. Packages are available from this repository; no SDK package has been published to a registry.

The SDKs derive their operations from the [Control OpenAPI contract](../api/openapi/control-v1.yaml), including host-bound ARD discovery. Rust supports typed reusable-draft export through `Client::export_version_revision_draft`; follow [Export a reusable draft](rust/README.md#export-a-reusable-draft) for permissions and response limits. SDKs do not install, invoke or deploy discovered resources. The service and CLI build independently of these packages.

## Choose a language

Each language guide covers installation, authentication, transport limits, responses, pagination and commands. The [client integration guide](../documentation/src/content/docs/guides/integrating.mdx) provides complete Search, draft creation and exact-byte examples.

| Language | Consumer package | Examples |
| --- | --- | --- |
| [Go 1.27+](go/README.md) | Source module; configure a local replacement. | [Discovery and transport](go/README.md#create-a-client-and-read-discovery), [Search](../documentation/src/content/docs/guides/integrating.mdx#search-one-page-with-go), [draft creation](../documentation/src/content/docs/guides/integrating.mdx#create-a-resource-with-go) and [exact bytes](go/README.md#preserve-native-evidence). |
| [Python 3.12-3.14](python/README.md) | Install local source or a built wheel with pip. | [Discovery](python/README.md#read-ard-discovery), [Search](../documentation/src/content/docs/guides/integrating.mdx#search-one-page-with-python), [draft creation](../documentation/src/content/docs/guides/integrating.mdx#create-a-resource-with-python) and [exact bytes](../documentation/src/content/docs/guides/integrating.mdx#read-exact-bytes-with-python). |
| [JavaScript/TypeScript](typescript/README.md) | Install the built ESM npm tarball. | [Discovery](typescript/README.md#connect-and-read-discovery), [Search](../documentation/src/content/docs/guides/integrating.mdx#search-one-page-with-javascript-or-typescript), [draft creation](../documentation/src/content/docs/guides/integrating.mdx#create-a-resource-with-javascript-or-typescript) and [exact bytes](../documentation/src/content/docs/guides/integrating.mdx#read-exact-bytes-with-javascript-or-typescript). |
| [Rust 1.88+](rust/README.md) | Local source or an extracted `.crate`; async Tokio client. | [First request](rust/README.md#make-your-first-request), [Search](rust/README.md#connect-and-search-one-page), [draft creation](rust/README.md#create-a-resource), [exact bytes](rust/README.md#read-exact-bytes) and [operation lookup](rust/README.md#find-an-operation). |

## Choose a task

Use these recipes after the language guide's client setup. Each example keeps network destinations and credentials under your application's control:

| Task | Go | Python | JavaScript/TypeScript | Rust |
| --- | --- | --- | --- | --- |
| Continue Search with a finite page budget | [Search pages](go/README.md#search-and-open-a-local-result) | [Search pages](python/README.md#search-published-resources) | [Search pages](typescript/README.md#read-a-bounded-sequence-of-pages) | [Search pages](rust/README.md#continue-search-with-a-finite-budget) |
| Read a selected local release | [Full record](go/README.md#search-and-open-a-local-result) | [Full record](python/README.md#read-a-selected-governed-record) | [Full record](typescript/README.md#read-the-selected-local-release) | [Full record](rust/README.md#open-a-local-resources-complete-record) |
| Handle errors without logging private bodies | [Error handling](go/README.md#handle-responses-and-errors) | [Error handling](python/README.md#handle-responses-and-errors) | [Error handling](typescript/README.md#responses-and-raw-evidence) | [Error handling](rust/README.md#read-results-and-handle-errors) |

A Search result ID is not a local release UUID. The local-detail recipes use `local.release.id`; indexed and live results retain their external provenance. Discovery does not authorize installation or execution.

Keep the returned cursor when a page budget stops traversal. An empty page can still have a continuation. Use the same query, filters, page size and identity when you resume. Inspect partial-result diagnostics before treating a result set as complete.

## Connect safely

Connect to the Catalog API origin, not the optional UI host. Your application supplies its access token. The SDKs do not use the UI's login cookie or acquire credentials automatically. The examples use placeholder origins, tokens and IDs; replace them with your selected Catalog inputs.

All four SDKs use the [Apache License 2.0](../LICENSE). Their package directories include license files and third-party notices. Each package guide explains its runtime dependencies and supported environments.

Each call returns one page or command response. Your application supplies cursors, command keys and revision validators. The SDKs do not acquire tokens, publish resources automatically or retry mutations. Read the selected language guide before handling private responses or arbitrary native JSON.

The [LangGraph discovery example](../examples/langgraph/README.md) shows the Python SDK in a local-model assistant. It searches and inspects governed releases through two fixed read-only tools. Its framework environment and model are optional; the SDK needs neither.

The [Rust Catalog report](../examples/rust-catalog-report/README.md) is a standalone CLI that searches authorized local releases and prints a Markdown inventory. Follow its guide for explicit token-file input, locked builds, a curated-sample query, exit statuses and troubleshooting.

## Make your first request

1. Install the selected SDK using its language guide.
2. Select the Catalog API origin and obtain an access token from your deployment's identity system.
3. Run the guide's capabilities read or authenticated Search example to check API access.
4. Use the integration examples to search, create drafts or read exact bytes.

The language guides' first-request examples read `CATALOG_SDK_ORIGIN` and `CATALOG_SDK_TOKEN` from the consumer process environment. These names belong to those examples; the SDK does not read environment settings automatically. Inject the token through your development tools or deployment. Do not put real tokens in source files or shell history. The token needs an active Catalog membership; see [authentication and membership](../documentation/src/content/docs/guides/integrating.mdx#authenticate-and-establish-membership).

For the default local Compose setup, the API origin is `http://127.0.0.1:8080`; port 4180 serves the UI. Each guide shows an explicit loopback HTTP option. Enable it only for the selected local API. Development tokens work only with configured development authentication; they are not production credentials.

ARD manifest reads also require a configured discovery host and an active publication for its audience. Unknown, disabled or inactive hosts return HTTP 404. This is a server response, not a failed SDK connection. See [discovery interfaces](../documentation/src/content/docs/guides/integrating.mdx#choose-a-discovery-interface) for the distinction between ARD discovery and authenticated Control API Search.

## Build and verify local packages

Contributors use these targets from the repository root. Consumers install the selected source package or built artifact using their language guide.

| Maintainer command | Effect |
| --- | --- |
| `make sdk-tools-install` | Install the pinned tools and test dependencies for all four SDKs. |
| `make sdk-generate` | Regenerate all checked-in clients from OpenAPI. |
| `make sdk-check` | Check drift, lint, licenses, units, local HTTP/TLS and clean installed consumers. |
| `make sdk-package` | Build local packages and record their identities; run no tests and publish nothing. |

Run each target alone. Only installation acquires dependencies. Checks do not publish packages, start Compose or contact an external Catalog. The selector accepts `SDK_LANGUAGE=go|python|typescript|rust|all`. The default is `all`; a missing prerequisite fails the command without skipping a language. Go-only selection needs no Python SDK, Node or browser tools. The Python installer requires pinned uv 0.13.6; use `UV=/path/to/uv-0.13.6` when needed. See [SDK qualification](../documentation/src/content/docs/contributing/testing.md#combined-sdk-qualification) for prerequisites and test scope.

Packages and input/tool hashes are retained under `output/artifacts/sdk/<language>/`. Python produces a wheel and source archive; TypeScript produces an npm tarball. Rust produces a `.crate`. Go remains a source module, with a source inventory instead of a binary archive. `sdk-package` with the default `SDK_LANGUAGE=all` removes any previous combined manifest and writes a replacement only after all four builds succeed. A single-language selection writes only that language's outputs. A package build does not establish test success; run `sdk-check` separately.

Root `VERSION` owns the SDK release base. Python maps `0.2.0-rc.1` to `0.2.0rc1`; npm and Cargo retain SemVer. The [version-bump workflow](../documentation/src/content/docs/contributing/build-and-release.md#application-version-and-release-checks) updates derived package metadata. Regenerate and check SDKs after a bump. Registry ownership, publication and release tags remain separate actions.
