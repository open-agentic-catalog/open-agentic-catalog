# ARD Explorer qualification

This optional check runs unchanged Explorer against one owned in-memory Catalog. It tests native Search, filters, paging, Explore and List. Separate synthetic HTTP controls characterize client limits and error handling. These results are not full ARD certification.

The source pin is `ba00c28ec268af379a5b7d06dd2a5f5c1d2fd821` from `asklar/ard-explorer`. It declares version 0.1.1 and includes its MIT license. The earlier `v0.1.1` tag is not the selected artifact. The harness verifies the source tree, upstream lock and license hashes in [inputs.json](inputs.json). It does not install the published npm package or patch upstream code.

The qualification requires an existing systemd user manager. The preflight checks it before staging private runtime files. A missing manager refuses the run. The harness does not start a manager or enable lingering. Cleanup fails if manager queries cannot confirm that the owned unit stopped.

## Install tools explicitly

Use a non-root Linux AMD64 host with Bubblewrap, a running systemd user manager and cgroup v2. The user manager must enforce CPU, memory, swap and process limits. Install the repository Go, Node, npm, Chromium and Playwright tools first. The exact tool versions are in `inputs.json`. Installation also requires `gh`, the pinned repository Trivy and access to public source, npm packages and advisory data.

Run these targets separately from the repository root:

```sh
make ard-explorer-tools-install
make test-interoperability-ard-explorer
```

Installation downloads the exact GitHub source through `gh`. It runs locked `npm ci` with lifecycle scripts and npm audit disabled. It then runs the reviewed upstream build scripts. The platform-specific esbuild binary arrives through a locked optional package; its postinstall script is unnecessary. The build never starts the esbuild development server.

Installation records package manifests, license declarations, notice-file hashes, browser files, Node identity and built-tool hashes. The local Trivy scan includes development packages and requires every locked package. Advisory data downloads do not submit source files or dependency metadata. An unreviewed advisory fails selection. The current esbuild advisory affects its unused development server; the exact finding remains recorded rather than suppressed globally.

Tools and evidence stay under ignored `output/interoperability/ard-explorer/`. The installation directory retains upstream notices. No Explorer files enter Catalog images, source bundles or shipped notices. To refresh a pin, review the closure and remove only this harness's `tools/` directory before explicit installation.

## Check startup and confinement

Execution builds current Catalog commands from cached Go inputs. It starts one in-memory Catalog, one native Explorer process and one disposable Chromium context. Explorer uses native loopback binding, an ephemeral port and `--no-open`. The UI receives only the owned manifest URL. The optional `make test-interoperability-ard-explorer-startup` target checks this boundary without populating the Catalog or running browsing scenarios.

All three processes share a new network namespace with loopback and no external route. The filesystem exposes selected read-only tools and one private, disk-backed state directory. It exposes no developer home, credentials, proxy configuration, endpoint history or Docker socket. Browser routing also refuses external requests. The native proxy remains unchanged and independently fails an external-address control because no route exists.

Each service has a five-minute deadline, two CPU cores, 2 GiB memory, no swap and 256 tasks. The browser allows at most 50 proxy operations and 1 MiB request bodies. The Search walk stops after three pages. These bounds cover small owned responses; the proxy buffers responses before truncation. This is not a shared-proxy or large-body test.

The test checks the rendered UI, manifest prefill, empty browser history and the actual Catalog version through the proxy. It checks limits inside the namespace. Separate native controls exercise failed exit, supervisor timeout and interruption. Cleanup stops the whole owned service group and removes temporary binaries, configuration and browser state. The overall result fails if process cleanup or temporary-state cleanup fails. Private logs, screenshots and JSON evidence remain.

Execution never downloads missing tools. Changed source, package files, Node or browser files fail the installation check. The result binds source, binary and browser identities to the executed bounds. A startup pass is not an ARD compatibility verdict.

## Qualify native browsing

The full target first runs `catalogctl smoke` inside its disposable namespace. This creates an Agent, an MCP Server and a Skill through ordinary authoring, validation, independent review and publication. It activates an immutable snapshot. The test binds expected identifiers to retained release records and that snapshot before opening the client. No retained Catalog or shared sample changes.

The browser connects using the explicit current manifest and the Registry base URL. It follows three distinct Search pages to termination, checks matching and contradictory type filters, and changes a filter with a retained token. That changed request must show HTTP 400. Cards must render the expected identifier, title, type, source and score. Explore must show three type buckets with one result each. The native Agents tab lists one manifest entry; it can include Registry entries and other resource kinds.

Separate loopback HTTP fixtures return sparse results, an unfamiliar media type and a valid neighbor. Other controls return a malformed envelope or HTTP 400, 401 and 503. A reserved loopback port tests an unavailable target. Assertions distinguish the proxy's HTTP 200 from the target's status or transport failure. Fixtures do not translate actual Catalog responses.

The full result contains 18 case outcomes and separate capability dispositions. Four assertions verify client limitations:

| Client limit | Observed behavior |
| --- | --- |
| Authentication | The same owned Catalog requires authentication on its second host. All three native probes receive HTTP 401. Explorer sends no token and offers no authentication field. |
| `queryContext` | Search has no context input. A synthetic context-required endpoint receives no `queryContext` and returns HTTP 400. Use expanded filter names where applicable. |
| List continuation | The response contains a continuation token, but the Agents tab has no next-page control. Only one List page is qualified. |
| Full Search referral URL | A synthetic referral ends in `/search`. The Connect action appends another `/search`; its probes receive HTTP 404. Ordinary direct Registry-base connection remains supported. |

The browser never selects public presets, follows artifact links or invokes resources. Independent Catalog HTTP counters must exactly match the captured native exchanges. Synthetic-server observations must also match. Extra artifact, trust or control-API requests fail qualification. Public snapshot bytes and ETag remain unchanged. This case covers three governed resource kinds, not Plugin consumption or authenticated Explorer browsing.

Reports use private `output/interoperability/ard-explorer/browse-*/result.json` files. They retain source/tool identities, expected resource references, exact native exchanges, server observations and cleanup results. Screenshots show the native Search and List views. A failed browser run retains separate diagnostic exchanges and a screenshot; it cannot produce a passing full result. The full target includes the startup checks and cleanup controls, so it does not require a preceding startup run.

Run helper checks separately:

```sh
make test-tooling-unit
go test -tags=functional ./test/interoperability -shuffle=on -count=1
```

These selections do not acquire or execute Explorer. The explicit native targets own browser and confinement qualification. Read each invocation's report for actual results and limits.
