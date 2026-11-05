# Scan saved MCP tool definitions

Run Cisco MCP Scanner's YARA analyzer against a saved `tools/list` result. The scanner checks tool descriptions and serialized parameter metadata.

This optional example runs outside the Catalog. The saved-capture command does not contact an MCP server. It does not call tools or change Catalog records. It does not scan source code, container vulnerabilities or runtime behavior. A scan does not establish trust or approve a release.

The optional [signed-webhook receiver](receiver/README.md) collects definitions from explicitly mapped local HTTPS endpoints. Its focused SDK, TLS, PostgreSQL and process checks use a scanner executable double. The [Compose guide](../../documentation/src/content/docs/guides/mcp-scanner.mdx) describes the separate native YARA campaign. Native qualification still requires the installed scanner.

## Prerequisites

- Use a non-root Linux AMD64 account with access to the local Docker socket.
- Install Python 3.11 or later and Make on the host.
- Allow registry downloads during explicit setup.
- Keep enough disk space for the independent scanner image and its dependencies.

The image uses Python 3.13.17, MCP Scanner 4.8.7 and yara-python 4.5.4. Its [hash lock](../../test/mcp-scanner/requirements.txt) fixes the complete Python dependency set. Its [pins](../../test/mcp-scanner/pins.json) identify the base image, source revision and bundled rules.

MCP Scanner has no YARA-only installation extra. Its installed dependencies include LLM libraries. This example selects only YARA and supplies no provider credentials. It disables `.env` loading and container networking.

The repository's dependency policy applies to setup. Version 4.8.7 needs a recorded age exception before November 3, 2026, at 17:30:42 UTC. Setup refuses an unapproved early installation.

## Install the scanner

Run the explicit setup target from the repository root:

```sh
make mcp-scanner-tools-install
```

Setup downloads the pinned base and hashed wheels. It builds one local image and verifies package versions and the rules digest. It saves the installation receipt under ignored `test/mcp-scanner/.tools/`.

Scan and check commands never install missing tools. After an implementation, lock or pin change, run setup again. A stale receipt fails the command.

See the [tooling guide](../../test/mcp-scanner/README.md) for lock maintenance and dependency limitations. Preserve the [third-party notices](NOTICE.md) when you copy this example.

## Prepare a capture

Save the result object from an authorized MCP `tools/list` response. Do not save the surrounding JSON-RPC envelope. The included [tools.json](tools.json) is an inert example:

```json
{
  "tools": [
    {
      "name": "add_integers",
      "description": "Add two integers.",
      "inputSchema": {
        "type": "object",
        "properties": {
          "left": { "type": "integer" },
          "right": { "type": "integer" }
        },
        "required": ["left", "right"],
        "additionalProperties": false
      }
    }
  ]
}
```

The example accepts the recommended MCP tool-name characters: ASCII letters, digits, periods, underscores and hyphens. Names must contain 1 to 128 characters and be unique. Input schemas must describe objects. Output schemas can describe other JSON types.

Schemas use JSON Schema draft 2020-12. The example rejects other declared dialects without downloading them. It also rejects duplicate JSON keys, malformed UTF-8 and invalid item shapes.

The fixed limits are:

| Input or operation | Limit |
| --- | --- |
| Captured JSON | 1 MiB |
| Tools in one capture | 128 |
| Caller context | 16 KiB |
| Scan process | 30 seconds |
| Report and each captured output stream | 4 MiB |

The 30-second limit applies to scanner startup and analysis. Allow additional time for image checks, container creation and cleanup. If the scanner times out before returning results, the report cannot recover its unfinished results.

## Scan a saved capture

Set the source label and time to describe your capture. Use a new output path for each scan:

```sh
make mcp-scanner-scan \
  MCP_SCANNER_INPUT=examples/mcp-scanner/tools.json \
  MCP_SCANNER_OUTPUT=output/review/mcp-scanner/sample-report.json \
  MCP_SCANNER_SOURCE='included offline sample' \
  MCP_SCANNER_CAPTURED_AT=2026-10-28T00:00:00Z
```

The time must include its timezone. Source and time are caller-supplied facts; the scanner does not verify them.

To attach private context, add `MCP_SCANNER_CONTEXT=/path/to/context.json`. The file must contain a JSON object. Do not include credentials or secrets. The report retains this object under `callerContext`, separately from capture and scanner identities. Caller context is not verified Catalog evidence.

The runner copies only the capture and context into a private temporary directory. It mounts that directory read-only. It does not mount your home, repository or `.env` files. Each scan uses a non-root container with no network access and removes its owned container afterward.

Reports use private file permissions. Existing output files are never overwritten. The runner suppresses raw scanner diagnostics because they can contain captured text. Treat captures and reports as private data.

## Read the report

The report records exact input and context hashes. It also records scanner/image identity, rules/configuration hashes, tool counts, analyzer errors and attributed findings.

| Status | Meaning |
| --- | --- |
| `completed` | Every supplied tool reports successful YARA analysis with no analyzer errors. |
| `partial` | Some analysis completes but other analysis fails, or the capture contains `nextCursor`. Existing findings remain available. |
| `failed` | Analysis or report validation failed, or container cleanup could not be confirmed. |
| `skipped` | The capture contains no tools, or the scanner explicitly skipped all supplied tools. |

The scope is always the supplied page. Without independent collection evidence, a missing `nextCursor` does not prove a complete server inventory. Tool-level completion does not prove checks beyond those reported by the selected analyzer.

The `cleanup` field reports cleanup separately from any scan error. A cleanup failure does not hide the original scan error. If final metadata exceeds the report limit, the runner saves a failed summary with `reportError: scanner_report_limit`. That summary keeps tool counts but omits findings. It does not claim a successful scan.

Exit status `0` means completed analysis without findings. Status `1` means completed analysis with findings. Status `2` means partial, failed, skipped or invalid work. Do not interpret a missing finding as proof that a tool is safe.

## Test the example

Install the [shared development tools](../../documentation/src/content/docs/contributing/build-and-release.md#shared-development-tools) for the host-side tests. After scanner installation, run:

```sh
make mcp-scanner-check
```

This command runs isolated validation tests, owned subprocess tests and the real installed YARA controls. The controls cover benign input, description and parameter findings, analyzer failures, invalid rules, pagination, empty work and timeout cleanup.

Use `make test-tooling-unit PYTHON_UNIT_DIRS=mcp-scanner` for isolated tests only. These tests do not start Docker or import the independent scanner. The [tooling guide](../../test/mcp-scanner/README.md) explains functional selection and local cleanup.
