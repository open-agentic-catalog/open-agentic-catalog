# Python SDK

`open-agentic-catalog` provides the `catalog_sdk` import for Python 3.12-3.14. It includes every operation in the [Control OpenAPI](../../api/openapi/control-v1.yaml), including ARD discovery. The SDK uses the [Apache License 2.0](../../LICENSE).

## Install the SDK

Maintainer-published GitHub Releases provide SDK downloads. Follow the [download and checksum instructions](../../documentation/src/content/docs/contributing/build-and-release.md#download-release-packages) before installation. Packages are not published to ecosystem registries. Source installation remains available below.

No registry release is available yet. Install the local source package in your Python environment:

```sh
python -m venv .venv
. .venv/bin/activate
python -m pip install ./sdk/python
```

Use Python 3.12-3.14 for the virtual environment. On Windows, activate it with `.venv\Scripts\Activate.ps1` in PowerShell. An existing application environment can use the same install command.

Alternatively, install a wheel built by a maintainer with `python -m pip install /absolute/path/to/open_agentic_catalog-0.1.0-py3-none-any.whl`. Replace the path and version with the actual artifact. Maintainers use `make sdk-tools-install SDK_LANGUAGE=python` before `make sdk-package SDK_LANGUAGE=python`; see [tool prerequisites](../../documentation/src/content/docs/contributing/testing.md#python-sdk-verification). Consumer installation does not run the generator or require Go, Java, a database or the UI.

Run the source-install command from the repository root, or pass an absolute source path from your own Python environment. Normal pip installation can download build and runtime dependencies. Use the Catalog API origin; the optional UI host and its login cookie are not SDK credentials.

## Use the client

Save this example as `catalog_example.py`. Supply `CATALOG_SDK_ORIGIN` and `CATALOG_SDK_TOKEN` through the process environment. The token needs an active Catalog membership.

```python
import os

from catalog_sdk import Client, Config
from catalog_sdk.generated.api.service_api import ServiceApi

client = Client(Config(
    origin=os.environ["CATALOG_SDK_ORIGIN"],
    token=os.environ["CATALOG_SDK_TOKEN"],
    # allow_loopback_http=True,  # Only for an explicitly selected local API.
))
result = ServiceApi(client).get_capabilities_sync_with_http_info()
if result.status_code != 200:
    raise RuntimeError(f"Capabilities returned HTTP {result.status_code}")
print("Catalog API connection succeeded")
```

Run the file in the environment where you installed the SDK:

```sh
python catalog_example.py
```

This request reads capability metadata without changing Catalog data.

Use a generated API class with the maintained `Client`. The generated unmanaged default constructors refuse requests. Each operation has async methods and synchronous `_sync` methods. Use `await api.get_capabilities_with_http_info()` in async code. Synchronous methods use `asyncio.run`; they refuse use inside an active event loop. They start no background thread. Each request owns and closes its HTTP client and response stream.

Generated `_without_preload_content` methods also return a fully buffered, bounded `Response`. They do not expose an open network stream.

### Use an async application

Save this complete example as `catalog_async.py`. It uses the same environment inputs as the synchronous example:

```python
import asyncio
import os

from catalog_sdk import Client, Config
from catalog_sdk.generated.api.service_api import ServiceApi

async def main():
    client = Client(Config(
        origin=os.environ["CATALOG_SDK_ORIGIN"],
        token=os.environ["CATALOG_SDK_TOKEN"],
        # allow_loopback_http=True,  # Only for an explicitly selected local API.
    ))
    response = await ServiceApi(client).get_capabilities_with_http_info()
    if response.status_code != 200:
        raise RuntimeError(f"Capabilities returned HTTP {response.status_code}")
    print("Catalog API connection succeeded")

if __name__ == "__main__":
    asyncio.run(main())
```

Run `python catalog_async.py`. In a framework or notebook with an active event loop, call `await main()` instead. Do not call `asyncio.run` or a `_sync` method there.

## Handle responses and errors

Prefer `_with_http_info` or `_sync_with_http_info`. Results preserve status, case-insensitive headers, typed data and bounded `raw_data`. Control error data uses `Problem`; ARD error data uses `ARDError`. A 4xx or 5xx response is returned without an automatic retry. Transport, parsing and local configuration failures raise `SDKError` with a fixed code. Its optional status and request ID support explicit correlation. Default exception text contains no private body or dependency error.

Required typed bodies cannot be empty or JSON `null`. Documented 204/304 responses remain bodyless. The raw methods retain exact response bytes for caller inspection.

Do not log raw results, model values, tokens or caller arguments. Generated models are data containers, not safe log projections. Pydantic call errors hide input values. Model constructors used directly by your application retain normal Pydantic validation behavior.

For example, classify a transport or decoding failure with `SDKError`. Your application decides whether to stop or reconcile an uncertain command:

```python
import sys

from catalog_sdk import SDKError

try:
    result = ServiceApi(client).get_capabilities_sync_with_http_info()
except SDKError as failure:
    print(f"SDK failure: {failure.code}; HTTP status: {failure.status}", file=sys.stderr)
else:
    if result.status_code != 200:
        raise RuntimeError(f"Capabilities returned HTTP {result.status_code}")
```

`failure.status` and `failure.request_id` are `None` when unavailable. A valid HTTP 401 or 403 uses the returned result, not this exception path. Do not catch cancellation as an ordinary retryable failure.

The examples below use this helper to check HTTP results. It includes the fixed Control error code without printing the private response body:

```python
from catalog_sdk.generated.models.problem import Problem

def require_status(response, expected):
    if response.status_code != expected:
        code = response.data.code.value if isinstance(response.data, Problem) else "HTTP_ERROR"
        raise RuntimeError(f"Catalog returned HTTP {response.status_code}; code={code}")
    return response
```

Use the returned `X-Request-ID` header for support correlation when needed. A transport failure after a command can leave its outcome unknown. Preserve the command body, key and preconditions before any reconciliation.

## Read ARD discovery

Omit credentials only when the selected host permits anonymous discovery:

```python
from catalog_sdk import Client, Config
from catalog_sdk.generated.api.discovery_api import DiscoveryApi

client = Client(Config(origin="https://catalog.example.com"))
response = DiscoveryApi(client).get_ard_manifest_sync_with_http_info()
if response.status_code != 200:
    raise RuntimeError(f"Discovery returned HTTP {response.status_code}")
manifest = response.data
etag = response.headers.get("ETag")
```

Use `await DiscoveryApi(client).get_ard_manifest_with_http_info()` in async code. API operation IDs become snake_case methods in their generated API group. For example, `searchCatalog` uses `CatalogDiscoveryApi.search_catalog_with_http_info`; model attributes use `next_cursor` for the JSON field `nextCursor`.

## Search published resources

Use authenticated Catalog Search for governed releases. The following async generator reads at most five pages by default. Copy `require_status` from the error example into the same module:

```python
from catalog_sdk.generated.api.catalog_discovery_api import CatalogDiscoveryApi
from catalog_sdk.generated.models.catalog_search import CatalogSearch

async def search_local_pages(client, text, *, namespace_id=None, cursor=None, max_pages=5):
    if max_pages < 1:
        raise ValueError("max_pages must be positive")
    api = CatalogDiscoveryApi(client)
    for _ in range(max_pages):
        response = await api.search_catalog_with_http_info(
            catalog_search=CatalogSearch(
                text=text, origins=["local"], namespaceId=namespace_id,
                limit=20, cursor=cursor,
            ),
        )
        page = require_status(response, 200).data
        yield page
        cursor = page.next_cursor
        if not cursor:
            return
```

Consume each page in your async application:

```python
async def show_local_results(client):
    next_cursor = None
    async for page in search_local_pages(client, "weather"):
        if page.partial:
            print("Search results are incomplete")
        for model in page.items:
            item = model.to_dict()
            print(item["kind"], item["displayName"])
        next_cursor = page.next_cursor
    return next_cursor
```

The cursor, not the item count, determines whether another page exists. An empty page can still have a continuation. Keep filters, page size and identity unchanged. If `show_local_results` returns a cursor, more pages remain beyond its budget. Pass that cursor to `search_local_pages` for an explicit continuation. Treat names and other resource fields as private data when you display them.

`origins=["local"]` excludes indexed and live federation results. A namespace filter also selects local Listing membership; it is not an AIR or JSON-LD namespace. Inspect `page.diagnostics` when a response is partial. See [Search filters and continuation](../../documentation/src/content/docs/guides/integrating.mdx#search-one-page-with-python) for the shared contract.

For an executable assistant that uses these reads, see [the LangGraph discovery example](../../examples/langgraph/README.md). It uses a local tool-capable model, fixed read-only SDK tools and a separate optional Python environment. It never installs or invokes discovered resources.

### Read a selected governed record

Only a local Search item contains a governed release in this Catalog. Supply the item dictionary returned by `model.to_dict()`:

```python
from uuid import UUID

from catalog_sdk.generated.api.catalog_discovery_api import CatalogDiscoveryApi

async def read_local_record(client, item):
    if item["type"] != "local":
        raise ValueError("Select a local governed result")
    release_id = UUID(item["local"]["release"]["id"])
    response = await CatalogDiscoveryApi(client).get_catalog_record_with_http_info(
        catalog_record_id=release_id,
    )
    return require_status(response, 200).data
```

For example, call `record = await read_local_record(client, item)` after the user selects a local result. Inspect `record.version.version`, `record.listing_revision` and the retained evidence. Do not pass the Search item's prefixed `id` or canonical AIR identifier as a record UUID. Indexed and live results retain external provenance; they are not local releases or automatic installation instructions. See [discovery interfaces](../../documentation/src/content/docs/guides/integrating.mdx#choose-a-discovery-interface).

## Find an operation

The HTTP reference groups operations by their OpenAPI tags. Import each class from `catalog_sdk.generated.api.<module>`:

| Task | Class | Module |
| --- | --- | --- |
| Capability state | `ServiceApi` | `service_api` |
| ARD manifest or Search | `DiscoveryApi` | `discovery_api` |
| Authenticated Catalog Search | `CatalogDiscoveryApi` | `catalog_discovery_api` |
| Draft authoring | `CatalogLifecycleApi` | `catalog_lifecycle_api` |
| Namespaces and access | `IdentityApi` | `identity_api` |
| Background Jobs | `JobsApi` | `jobs_api` |

For example, use `from catalog_sdk.generated.api.jobs_api import JobsApi`. The `getJob` operation becomes `get_job_with_http_info`; its synchronous form is `get_job_sync_with_http_info`. Model classes live in `catalog_sdk.generated.models`; the integration examples show their required imports and arguments.

## Credentials, TLS and limits

Supply either `token` or a synchronous `token_provider`, which is called once per request. Omit both for anonymous discovery. The provider must return a current token or `None`; it must not block. The SDK does not log in or refresh credentials. Caller headers and generated authentication overrides cannot replace the configured credentials, Host or encoding policy.

The origin must be HTTPS without a path, credentials, query or fragment. Empty `?` and `#` suffixes are also refused. TLS verification is always enabled. `ca_file` selects an explicit CA bundle; otherwise the Python system trust set applies. Local tests can opt into literal loopback HTTP with `allow_loopback_http=True`. Only the Boolean value `True` permits HTTP; strings and numbers do not. The SDK ignores ambient proxy and credential configuration, follows no redirects and accepts only identity-encoded responses. It uses HTTP/1.1 with transport retries disabled.

| Config field | Default | Meaning |
| --- | --- | --- |
| `timeout` | 30 seconds | Whole network-call deadline, including the response body. |
| `phase_timeout` | 10 seconds | Connect, pool, write and read inactivity budgets. A slow but active stream still has the whole-call deadline. |
| `request_bytes` | 16 MiB | Maximum serialized request body. |
| `response_bytes` | 8 MiB | Maximum response body before parsing. |
| `header_bytes` | 32 KiB | Maximum parsed response header bytes before body consumption. |

All budgets must be positive and finite. Byte budgets have a 1 GiB maximum. The pinned HTTP/1.1 parser also has a fixed 100 KiB incomplete-header guard; a larger configured limit does not remove it. Header accounting excludes the status line. `_request_timeout` can shorten the whole-call deadline with one positive scalar; tuple phase overrides are unsupported. Configure phases through `Config`.

Async cancellation propagates as `asyncio.CancelledError` and closes the stream. Python cannot interrupt a blocking synchronous token provider or arbitrary serializer; these must remain bounded. Network deadlines do not promise to interrupt CPU-bound serialization or typed parsing.

Use a larger explicit budget for a valid download:

```python
from catalog_sdk import response_limit

with response_limit(32 * 1024 * 1024):
    response = client.raw_sync("GET", f"/api/v1/artifacts/{artifact_id}/content")
if response.status != 200:
    raise RuntimeError(f"Artifact read returned HTTP {response.status}")
exact_bytes = response.data
```

Supply the UUID of a readable retained artifact as `artifact_id`. The override is local to the current context, including concurrent async tasks. Exceeding a bound refuses the result; the SDK never returns a truncated body. Binary/file operations return bytes, not a temporary filename.

## Commands and pages

Pass the original `idempotency_key` and current `if_match` explicitly where the operation requires them. Use keyword arguments for generated methods. The SDK never generates keys, retries commands, rebases conflicts, polls Jobs or follows Location. A lost mutation response leaves an uncertain outcome for the caller to resolve.

The completed-command replay window is 24 hours from reservation. Do not reuse a retained key indefinitely. Pending reservations have separate recovery rules. See [API request rules](../../documentation/src/content/docs/reference/api-conventions.md#request-rules).

Each collection or Search method returns one page. Submit its opaque continuation with the same filters and page size. The SDK does not enumerate pages or restart an expired cursor. Successful partial results and their headers remain available.

For a namespace update, supply an existing UUID, its current ETag and a retained command key. Choose a different display name; an unchanged patch returns HTTP 400:

```python
from catalog_sdk.generated.api.identity_api import IdentityApi
from catalog_sdk.generated.models.patch_namespace import PatchNamespace

response = IdentityApi(client).update_namespace_sync_with_http_info(
    namespace_id=namespace_id,
    idempotency_key=command_key,
    if_match=current_etag,
    patch_namespace=PatchNamespace(displayName="Examples", reason="Update namespace name"),
)
if response.status_code != 200:
    raise RuntimeError(f"Namespace update returned HTTP {response.status_code}")
updated_etag = response.headers["ETag"]
```

A 412 response requires a new read and an explicit decision. Do not replace its validator and retry automatically.

See the integration guide for [one-page Search](../../documentation/src/content/docs/guides/integrating.mdx#search-one-page-with-python), [draft resource creation](../../documentation/src/content/docs/guides/integrating.mdx#create-a-resource-with-python) and [exact-byte reads](../../documentation/src/content/docs/guides/integrating.mdx#read-exact-bytes-with-python). The examples keep credentials, namespace IDs and command keys explicit.

The maintained client prefers `application/json` when the operation advertises it. Do not force an alternative media type without checking service support.

### Create a draft Resource

Use a token with `resource.create` and the required namespace read access. Enforced OAuth scopes also require `catalog.write`. Supply an existing namespace UUID, a valid AIR identifier and a key retained for this exact command:

```python
from catalog_sdk.generated.api.catalog_lifecycle_api import CatalogLifecycleApi
from catalog_sdk.generated.models.create_resource import CreateResource
from catalog_sdk.generated.models.resource_kind import ResourceKind

async def create_draft_resource(client, namespace_id, identifier, kind, command_key):
    response = await CatalogLifecycleApi(client).create_resource_with_http_info(
        idempotency_key=command_key,
        create_resource=CreateResource(
            namespaceId=namespace_id,
            canonicalIdentifier=identifier,
            kind=ResourceKind(kind),
        ),
    )
    return require_status(response, 201)
```

Pass `agent`, `mcp_server`, `skill` or `plugin` as `kind`. Retain the returned `response.data.id` and `response.headers["ETag"]`. This creates a Resource without a version, listing or published release. Follow [native authoring](../../documentation/src/content/docs/guides/resource-authoring.mdx) and [publication](../../documentation/src/content/docs/guides/publishing.mdx) to complete that workflow. Do not reuse the example identifier or key for unrelated drafts.

### Inspect an asynchronous command

An HTTP 202 response means that work was accepted, not completed. Retain its Job UUID. Read that Job once with a currently authorized token:

```python
from catalog_sdk.generated.api.jobs_api import JobsApi

async def read_job_state(client, job_id):
    response = await JobsApi(client).get_job_with_http_info(job_id=job_id)
    return require_status(response, 200).data.state
```

Pass a `uuid.UUID` as `job_id`. `queued`, `running` and `waiting` are nonterminal states. `succeeded`, `failed`, `cancelled` and `dead_letter` are terminal. The SDK does not poll or retry the command. Job access is permission-controlled, and retained terminal Jobs can expire. See [asynchronous operation status](../../documentation/src/content/docs/guides/publishing.mdx#when-another-operation-returns-a-job) before implementing bounded polling or recovery.

## Troubleshoot a request

| Result | Check or next action |
| --- | --- |
| HTTP 401 | Supply a current API access token with the configured issuer and audience. A UI cookie is not an API credential. |
| HTTP 403 | Check current Catalog permissions, namespace scope and any enforced OAuth scope ceiling. |
| HTTP 404 | Check the selected identity and object ID. ARD reads also need an active publication for the configured host. |
| HTTP 409 | Inspect the Control error code. Do not reuse a command key with a different request. |
| HTTP 412 | Read the current revision and decide whether to make a new command. Do not automatically replace `If-Match`. |
| Invalid or expired Search cursor | Make an explicit restart decision. Keep the original query for a valid continuation. |
| `invalid_tls_configuration` | Check the explicit CA file. Keep certificate and hostname verification enabled. |
| `use_async_method` | Use the async operation inside an active event loop. |
| `response_too_large` | Increase the explicit response budget only for a valid bounded read. |
| `SDKError` after a mutation | Reconcile the uncertain outcome with the original command. Do not blindly repeat it with a new key. |

## Exact evidence and type limits

Use `await client.raw(method, path, body=exact_bytes, headers=...)` or `client.raw_sync` for exact bytes. The path stays on the selected origin. Raw data preserves large integers, decimal text, unknown extensions and absent/null/false/empty distinctions. Inline JSON is not necessarily an external artifact's original byte stream; retain the server's digest scope.

Typed models are not JSON Schema validators or lossless evidence. They can normalize dates, omit optional values and discard unknown closed-object fields.

Open metadata objects preserve additional keys during construction and decoding, including null values. Names such as `additional_properties` remain literal JSON keys. Use an extension model's `to_dict()` method to read its values as a dictionary. `ARDEntry` also preserves unknown keys and distinguishes an absent `@context` from an explicit null.

Four conditional schemas use explicit JSON-object containers because the generator cannot represent their rules correctly:

| Model | Access |
| --- | --- |
| `RepresentationInput` | Construct native input with `Model.model_validate(dictionary)`. |
| `TrustAssessmentRequest` | Construct assessment input with `Model.model_validate(dictionary)`. |
| `CatalogSearchResult` | Read each Search item with `item.to_dict()`. Its `type` identifies `local`, `indexed` or `live` evidence. |
| `AuditDetailsV1` | Decode a details dictionary with `AuditDetailsV1.from_dict(record.details).to_dict()`. Its `principalType` identifies the attribution fields. |

These containers preserve every supplied key, including common fields and nested evidence. They do not validate the complete JSON Schema. The API validates input alternatives and supplies the response contract. No second schema is maintained.

Root `VERSION` supplies the package version; release candidates use PEP 440 (`0.2.0-rc.1` becomes `0.2.0rc1`). Version bumps update package metadata and the maintainer lock. Regenerate and check before distribution. Generated calls send this package version in the default User-Agent. Local package naming does not establish PyPI ownership.

See [Python SDK verification](../../documentation/src/content/docs/contributing/testing.md#python-sdk-verification) for generation, locks, test selections, package builds and generator limitations. The SDK depends on [HTTPX2](https://github.com/pydantic/httpx2), Pydantic, python-dateutil and typing-extensions; its reviewed runtime closure has no certifi or MPL dependency. Generator and test tools stay outside consumer requirements. Return to the [SDK overview](../README.md) to compare languages.
