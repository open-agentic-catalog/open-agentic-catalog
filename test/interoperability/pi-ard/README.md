# Pi ARD interoperability

These optional checks run real Pi sessions with the unchanged pi-ard extension. The preparation target checks startup, offline settings, active tools, budgets and cleanup. The full campaign adds real Catalog HTTP, continuation, refusals and declined installation.

## Inputs and setup

[inputs.json](inputs.json) pins unchanged pi-ard 0.2.1 at `8a9db2d345b9978d60e182a652d72cc09ed94d83`. The upstream development lock remains unchanged. The separate first-party [runtime lock](runtime/package-lock.json) selects Pi 1.5.0 through the extension's supported peer dependencies. It contains 124 package paths and 123 distinct package/version identities. No npm override or upstream patch is used.

The maintainer approves ten exact age exceptions: the eight Pi packages at 1.5.0, minimatch 11.0.3 and @isaacs/nonrecursive-glob-match 1.0.3. Other versions meet the 14-day rule. The pins retain each release date and each exception's integrity hash. Source, manifest and lock digests reject changed inputs.

Use non-root Linux AMD64 with Python, GitHub CLI, bubblewrap and systemd. Use the repository's Node 22.23.4/npm 12.2.2 and Trivy pins from the [verification tool setup](../../ci/README.md). Reuse already installed matching tools. For a fresh Linux AMD64/Python 3.12 workflow environment, the `security` profile supplies both Node/npm and Trivy; do not run multiple profiles into an existing tool destination. Execution needs noninteractive permission for `sudo systemd-run` and `sudo systemctl` on the invocation-owned transient services. The launcher does not change sudo policy, enable user services or change host settings.

Run each target alone from the repository root:

```sh
make pi-ard-tools-install
make test-interoperability-pi-ard-prepare
make test-interoperability-pi-ard
```

The installer downloads exact GitHub source and locked npm packages. It disables lifecycle scripts, npm audits and funding requests. Package acquisition verifies the lock's SHA-512 integrity values. It retains package/license inventories, available notices and the Pi repository's MIT license under ignored `output/interoperability/pi-ard/tools/`. Pi packages without standalone license files retain that source-license reference. These are external test tools, not Catalog runtime dependencies or shipped notices.

The installer checks the lock with a private, fresh Trivy database without submitting project metadata. It retains all findings. The Pi 1.5.0 runtime closure has no reported findings in the recorded scan. Any reported vulnerability stops installation. The unchanged source, installed files, notices and review evidence receive digests. Execution rejects missing or changed tools and never installs them automatically.

If explicit installation fails, inspect its log and remove only `output/interoperability/pi-ard/tools/` before retrying. Do not remove retained Catalog data or unrelated caches. Review a changed source, lock or dependency closure before acquisition.

## What preparation proves

The launcher creates one transient systemd service per case, running as the invoking user. Bubblewrap creates separate user, PID, IPC, UTS and network namespaces. Its root and tool mounts are read-only. The only writable artifact paths are an owned disk directory and its `/tmp` bind. The sandbox receives no developer home, Docker socket, proxy variables or provider credentials.

The namespace has no external route. A reserved-address connection must fail with `ENETUNREACH`. A tiny owned loopback HTTP listener provides successful native-fetch controls. No existing Catalog, public Registry or model service is contacted. Published Catalog records and a disposable Catalog deployment are not needed for this startup check.

The first-party session harness uses Pi's public extension loader to create a real `AgentSession` with the unchanged extension. This loader supplies Pi's supported compatibility view for older extension imports. It uses the native faux provider, in-memory sessions/settings and empty owned agent directory. Creation uses `allowModelNetwork=false` and `refreshOnCreate=false`. `PI_OFFLINE=1` also disables later model networking. Ambient extensions and Skills remain disabled. The launcher precreates the exact local configuration before importing Pi, then activates only `ard_search`, `ard_catalog` and `ard_install`.

A native `/ard` command must read the local configuration. One scripted text turn must complete without tool calls or confirmation. It creates no installed Skill or installation lock. The global fetch function must remain unchanged.

Importing Pi initializes Undici 8.12.3's global dispatcher through its native module imports. The check records and verifies that actual dispatcher, then requires it to remain stable. It does not replace fetch, inject another dispatcher, patch Pi or override dependencies. The selected transport controls use fixed plain HTTP responses. They do not use TLS, proxy/cache interceptors, content encodings, WebSockets or EventSource. Glob patterns and package paths stay fixed and invocation-owned.

| Limit | Enforced value |
| --- | --- |
| Execution / stop allowance | 180 seconds overall, also enforced per service; five seconds to stop a service |
| CPU / memory / swap | Two cores / 2 GiB / zero swap |
| Tasks / open files | 256 / 256 |
| Individual file / captured output | 8 MiB / 1 MiB |
| Scripted tool calls | At most 20, only the three ARD tools |
| Search continuation walk | At most three pages |
| Native HTTP defaults | 15 seconds and 1,000,000 response bytes |

Native controls verify the tool and page budgets before excess calls reach tool dispatch. They use an unconfigured registry and cannot contact a Catalog. Additional controls cover missing configuration, native failure, timeout, excess output and interruption. Cleanup stops only each generated `oac-pi-<id>.service`, checks its state/cgroup, and removes its private directory. Setup failures before process launch also run cleanup. Cleanup failures fail the result and retain the owned service/path for inspection.

## Reports and helper checks

Each invocation writes `output/interoperability/pi-ard/run-<id>/report.json`. A successful preparation requires every native control and cleanup to pass. Reports bind the source inputs, installed closure, scanner/database identity and observed runtime controls. Earlier failed runs remain separate. SIGKILL or host failure can prevent cleanup; inspect only the exact service/path recorded by that run.

Run isolated Python decisions and local process/Make fixtures separately:

```sh
make test-tooling-unit
go test -tags=functional ./test/interoperability -shuffle=on -count=1
.tools/ci/node-v22.23.4-linux-x64/bin/node --test test/interoperability/pi-ard/script.unit.test.mjs
```

These helper suites do not import Pi or acquire packages. Only the explicit preparation and campaign targets run the independent host.

## Qualify native discovery and declined installation

Run `make test-interoperability-pi-ard` after explicit tool installation. The campaign needs the cached repository Go toolchain and modules. It builds current static Catalog commands offline. Missing dependencies fail instead of triggering downloads.

The campaign starts one disposable in-memory Catalog inside the same private network namespace. Two CLI smoke workflows create six governed Resources: two Agents, two MCP Servers and two Skills. Each workflow validates, independently reviews, publishes and activates its records. The resulting manifest has seven entries, including the Registry. This tests client interoperability, not PostgreSQL persistence or recovery.

An owned recording frontend forwards the native client's HTTP bodies and response bytes unchanged. Its only real upstream is the owned Catalog. It records the native requests without replacing fetch or translating formats. A separate listener supplies labeled synthetic controls. The frontend and fixture bind only inside the private namespace.

| Native cases | Required evidence |
| --- | --- |
| Real manifest, two MCP result pages, changed query and private read | Exact published identities; unchanged returned token and query; HTTP 400 after the query changes; actual private HTTP 401 |
| Unconfigured Registry, unsafe URL and untrusted catalog | Native refusal before any destination receives a request |
| Synthetic Search in two sessions, HTTP 403, invalid JSON, missing results and timeout | Sparse/unfamiliar entries remain data; one invalid entry is dropped; referrals and attestations receive no contact; the actual timeout occurs after 15 seconds |
| Declined install, unknown identifier, canonical Skill and no-UI install | Exactly one native confirmation returns decline; no artifact fetch, installed Skill, MCP tool or lock entry appears |

The scripted provider dispatches 18 tool calls across two native sessions. Fixed provider budgets permit at most 20 calls overall. The continuation walk permits three Search attempts, including the changed-query refusal. The native HTTP timeout and response limit remain unchanged. The three-minute execution deadline includes building and running this campaign; tool acquisition remains separate.

Independent Catalog counters must match the five forwarded HTTP requests. The six synthetic requests must match their exact paths. The campaign rechecks manifest bytes, ETag and all six retained records after native dispatch. The original backend configuration is removed before Pi starts. Pi receives no authentication input or ambient credentials.

The second session has no interactive UI. It first sees the synthetic Skill through native Search, then refuses installation without a confirmation. Both sessions keep only the three ARD tools active. The launcher checks their installation paths independently. It also checks the real host's MCP registry before dispatch and after every tool result. All processes, listeners and private directories belong to the invocation and use the existing cleanup controls.

### Client limits

Pi 1.5.0 and pi-ard 0.2.1 qualify only the selected scripted exchange. They do not qualify a real model's choices or a human terminal interface.

- The extension has no authentication input. Actual private Catalog reads fail with HTTP 401.
- The tools expose text and type filters, not `queryContext`, general ARD filters, Explore or List.
- The MCP type filter adds `application/mcp-server+json`. Actual Catalog results retain `application/mcp-server-card+json`.
- The client does not recognize `text/markdown; profile="urn:air:agent-skills"`. The canonical Skill remains unchanged and installation returns `refused`.
- The decline control uses a separate synthetic `application/agent-skills+md` entry. It does not relabel a Catalog Skill.
- Native formatted Search text omits each result's `source` field. The report preserves and checks the actual HTTP provenance separately.
- Descriptions, configured-domain labels and declared signatures remain data. Referrals, artifacts and attestation URLs receive no requests.

Reports retain native outcomes, raw owned HTTP, source/build identities, bounds, publication membership and cleanup. Failed runs remain separate. A failed fixture check is not a peer incompatibility. Positive installation and discovered-resource execution remain outside this campaign. A clean dependency scan does not extend the tested behavior.
