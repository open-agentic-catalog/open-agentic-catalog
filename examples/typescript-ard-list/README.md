# TypeScript published discovery client

List a configured host's active ARD publication with the TypeScript SDK. This small Node application prints bounded metadata as JSON lines.

The application calls only `GET /ard/v1/agents`. It makes no private Resource reads and does not follow entry URLs. Discovery does not grant permission to install or execute a resource.

## Prerequisites

- Use Node 22.23.4 or newer within major 22, or Node 24.20.0 or newer within major 24.
- Provide a Catalog API discovery origin with an active publication.
- Provide a token file for an authenticated discovery host.
- Configure private CA trust before starting Node, when needed.

These Node versions run the example's `.mts` file directly. No separate compiler, browser, model, database or web server is required by this application.

The [SDK guide](../../sdk/typescript/README.md) owns SDK setup and transport behavior. The source uses [Apache License 2.0](../../LICENSE). Installed SDK and dependency packages retain their separate notices and licenses.

## Install the local SDK package

No registry release is published. Build the local package from the repository root:

```sh
make sdk-tools-install SDK_LANGUAGE=typescript
make sdk-package SDK_LANGUAGE=typescript
```

Run each command separately. Tool installation downloads pinned inputs. Packaging uses installed inputs and reports the actual tarball path.

Copy this directory into your own working directory, or use it directly. Install the reported tarball there:

```sh
cd examples/typescript-ard-list
npm install /absolute/path/to/open-agentic-catalog-client-0.1.0.tgz \
  --ignore-scripts --no-audit --no-fund
```

Replace the example path with the package target's output. Normal npm installation can download runtime dependencies. The application adds no dependencies beyond the installed SDK. Git ignores local npm output and private inputs.

## Prepare the discovery host

Use the API origin, such as `https://catalog.example.test:8080`. The Catalog UI origin serves the browser client and is not the discovery endpoint.

An operator configures the exact hostname or IP in `discovery.hosts`. Each binding selects its tenant, publisher domain and `anonymous` or `authenticated` audience. See [discovery-host configuration](../../documentation/src/content/docs/reference/configuration.mdx#discovery-host-configuration).

Publish eligible releases, generate a snapshot for that host, then activate it. Follow the [publication guide](../../documentation/src/content/docs/guides/publishing.mdx). Drafts and inactive candidates do not appear in ARD List. Unknown, disabled and inactive hosts can return HTTP 404.

The origin's hostname selects the configured discovery host. Do not substitute the UI hostname or an unrelated IP. A token cannot change a host's audience. Forwarded-host headers are not a host-selection mechanism.

An anonymous host needs no token. An authenticated host requires a current same-tenant membership with tenant-wide `discovery.read`. Enforced OAuth scopes also require `catalog.read`. Private Resource-read permission is not required. The built-in Discovery reader template supplies the Catalog permission.

## Run the application

For anonymous discovery:

```sh
node index.mts --origin https://catalog.example.test:8080
```

For authenticated discovery, obtain an access token from your configured issuer. Save it in a regular private file with mode `0600`. Do not put tokens in command arguments, source files or logs.

The file contains only the token, without a `Bearer` prefix. The application rereads it for each request. It does not obtain or refresh tokens.

```sh
node index.mts --origin https://catalog.example.test:8080 \
  --token-file "$HOME/.local/share/catalog/discovery-token" \
  --page-size 20 --max-pages 10
```

The application refuses symlinks, nonregular files, group-readable or world-readable files, invalid UTF-8 and credentials larger than 8 KiB. These file permissions require a POSIX-compatible filesystem.

For a private Catalog CA, set Node's trust input before startup:

```sh
NODE_EXTRA_CA_CERTS=/absolute/path/to/catalog-ca.pem \
  node index.mts --origin https://catalog.example.test:8080 \
  --token-file "$HOME/.local/share/catalog/discovery-token"
```

`NODE_EXTRA_CA_CERTS` augments Node's trusted roots. The SDK has no `caFile` setting. Do not disable TLS verification or replace Fetch with an ambient proxy adapter.

For an explicitly selected local HTTP fixture:

```sh
node index.mts --origin http://127.0.0.1:8080 --allow-loopback-http
```

This flag permits literal loopback HTTP only. The Catalog must still configure that IP as a discovery host and activate its publication.

## Read the output

Each resource entry contains only `identifier`, `displayName` and `type`. JSON encoding escapes newlines and terminal control characters. Native documents, entry URLs, tokens and raw error bodies are omitted.

The application skips entries with type `application/ai-registry+json`, including the Registry's self-entry. The final summary reports those skipped entries separately.

```json
{"kind":"entry","identifier":"urn:air:catalog.example.test:demo:weather","displayName":"Weather agent","type":"application/a2a-agent-card+json"}
{"kind":"summary","complete":true,"reason":"exhausted","pages":1,"resourceEntries":1,"registryEntriesSkipped":1,"reportedTotal":2}
```

`reportedTotal` is ARD List's matching entry count, including Registry entries. It is not a unique Resource count or a snapshot UUID. Multiple entries can describe one Resource.

`resourceEntries` counts entries printed during this run. `complete: true` means the traversal exhausts continuation successfully. It does not describe application health, trust or resource execution.

| Exit | Meaning |
| --- | --- |
| `0` | The continuation is exhausted. An empty publication result is also successful. |
| `2` | The page budget ends with a continuation. Output is incomplete. |
| `1` | Input, access, HTTP, decoding, continuation, deadline or cancellation fails. Output is incomplete. |

## Limits and continuation

| Input or limit | Default | Allowed range or fixed bound |
| --- | --- | --- |
| `--page-size` | 20 entries | 1-100 |
| `--max-pages` | 10 pages | 1-100 |
| `--timeout-ms` | 5,000 ms per call | 1-30,000 ms |
| Response bytes | 256 KiB per page | Fixed SDK limit |
| Displayed text | Identifier: 2,048 characters; name: 512; type: 256 | Larger values fail validation |
| Continuation token | 2,048 bytes | Larger values fail validation |

The client checks runtime types, displayed field sizes, page length and nonnegative safe-integer totals. It checks consistent totals and refuses repeated continuation tokens. Unused fields are ignored. These checks are not a complete ARD schema validator.

An empty `items` array does not end traversal when `pageToken` remains. The client keeps the same origin, page size and ordering during continuation. It performs no automatic retry or fresh traversal after failure. Press Ctrl-C or send SIGTERM to cancel the current request.

The Catalog binds continuations to the host, audience, generation, page size and query criteria. Expiry or activation of a different generation can cause refusal. Do not assume every activation or caller change invalidates a continuation. After refusal, check the cause and start a new run explicitly.

## Troubleshoot discovery

| Symptom | Action |
| --- | --- |
| HTTP 401 or 403 | Check the token, tenant, `discovery.read` permission and enforced `catalog.read` scope. |
| HTTP 404 | Check the API origin, exact configured host, audience mode and active publication. |
| TLS failure | Check the certificate hostname and Node's startup trust configuration. |
| `invalid_response` | Check response types and this example's bounds. Partial output does not establish an empty Catalog. |
| HTTP 400 after an earlier page | Check continuation expiry or a changed active generation. Start a new run explicitly. |
| Exit `2` | Increase `--max-pages` within its bound, then start a new run. |
| `cancelled` or `deadline_exceeded` | Check connectivity or cancellation. Adjust `--timeout-ms` within its bound if needed. |

## Verify changes

Run the existing SDK gate from the repository root:

```sh
make sdk-check SDK_LANGUAGE=typescript
```

The gate runs the actual `.mts` entrypoint with an installed SDK tarball on all three pinned Node runtimes. Isolated units use injected Fetch and temporary credential files. Functional tests use owned HTTPS and composed Catalog publication services.

Checks copy only declared application sources. Local token files, `.env`, private keys and npm output stay outside the test staging directory.

The fixture publishes three releases and leaves a fourth Resource as a draft. It checks both audiences, multiple pages, empty results and page budgets. It also checks malformed responses, access denial, untrusted TLS, cancellation, expiry and activation of a different generation. Requests to private or entry URLs fail the fixture. These tests neither change retained Catalog data nor repeat the full OIDC or sender campaigns.
