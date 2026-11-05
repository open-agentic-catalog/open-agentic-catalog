# Test harnesses and fixtures

Most Go tests live beside their source under `cmd/`, `internal/`, `api/` and `schemas/`. This directory holds cross-process, deployment, monitoring and performance harnesses. The [testing guide](../documentation/src/content/docs/contributing/testing.md) owns prerequisites and gate selection; [functional coverage](../documentation/src/content/docs/contributing/testing.md#functional-coverage-inventory) records surface-to-evidence mappings rather than claiming every build passed.

| Directory | Boundary and entry point |
| --- | --- |
| [embeddings/](embeddings/README.md) | Service-free provider/fixture checks (`make embeddings-check`), real endpoint qualification (`make test-embeddings`) and integrated hybrid search with an owned Compose database/model, browsers and 1k-item pilot (`make test-semantic`). Explicit model acquisition uses `make embeddings-install`; multi-replica Helm qualification uses `make test-minikube-semantic`. |
| [search-relevance/](search-relevance/README.md) | Reviewed original and two-profile lexical/semantic/hybrid experiments: service-free `make search-relevance-check`, explicit installation and disposable CPU/pgvector evaluation; `make search-relevance-compare` selects a model using tuning only. |
| [clientcompat/](clientcompat/README.md) | Generated Go-client codec/injected-transport units and owned HTTP/composed functional cases: `make client-go-check`. Explicit locked-tool installation; no database or retained service. |
| [interoperability/](interoperability/README.md) | Separately selected HF, Directory, Gateway, ANS, A2A and MCP qualification against owned local clients/peers. Each guide owns installation, fixture isolation and its `test-interoperability*` target; default tests cover only harness helpers. |
| [apigenerated/](apigenerated/README.md) | Locked Schemathesis generated positive/negative Control API cases against an owned PostgreSQL/API/worker fixture: `make test-api-generated`. |
| [authorization/](authorization/README.md) | Reviewed permission/operation inventories, independent scenario oracles and PostgreSQL authorization qualification through `make test-integration`. |
| [ci/](ci/README.md) | Closed GitHub Actions selections, actionlint/ShellCheck validation and source-bound workflow helper tests: `make workflow-check`. |
| [demo/](demo/README.md) | Sample/archive helpers, owned Compose seeding and real local A2A/MCP peers; separately selected `test-demo-runtimes` and complete `test-demo-catalog`. |
| [demofixture/](demofixture) and [identityfixture/](identityfixture) | Shared synthetic sample and explicit development-identity fixture helpers; no retained service selection. |
| [deployment/](deployment/README.md) | Container/Compose/Helm source and runtime-configuration delivery contracts: `make deploy-check`; runtime qualification stays separately selected. |
| [langgraph/](../examples/langgraph/README.md) | LangGraph example tooling and test ownership: explicit `make langgraph-tools-install`, isolated `make test-langgraph-unit`, scripted SDK HTTP qualification with `make test-langgraph`, and separate `make test-langgraph-model`. |
| [mcp-scanner/](mcp-scanner/README.md) | Optional saved-capture YARA example and independent webhook receiver. Use `mcp-scanner-check` for installed YARA, `mcp-scanner-receiver-check` for SDK/TLS checks, and `test-mcp-scanner-receiver` for owned PostgreSQL/process checks. |
| [pgbouncer/](pgbouncer/README.md) | Explicit owned session-pooler qualification with distinct API/worker logins, TLS, admission and recovery: `make test-pgbouncer`. |
| [sdk/](sdk/README.md) | Four independent generated SDKs, tool installers, drift, isolated units and owned transport/package consumers: `make sdk-check`. |
| [security/](security/README.md) | Separate owned passive/active ZAP API/UI qualification and actual-tool controls: `make test-security-passive` or `make test-security-active`. |
| [sonarqube/](sonarqube/README.md) | Optional local quality dashboard and explicit selected coverage/analysis imports; no CI or runtime dependency. |
| [vscode/](vscode/README.md) | Extension helpers, VSIX packaging and separately selected Linux editor/remote/browser qualification: `make vscode-check` and `make test-vscode`. |
| [formal/](formal/README.md) | One source-bound Gobra retry-arithmetic proof and diagnostic controls: `make formal-tools-install`, then `make test-formal`. Ordinary gates run only isolated/native harness fixtures. |
| [functional/](functional/README.md) | Owned Compose stacks: `make test-functional`; focused logical backup recovery: `make test-backup-restore`; physical writer recovery: `make test-postgres-failover`. |
| [helm/](helm/README.md) | Cluster-free chart render, configuration, rejection and package contracts: `make helm-check`. |
| [hooks/](hooks/README.md) | Optional staged-secret, Go formatting and whitespace checks; disposable Git safety tests run in `make verify`. |
| [minikube/](minikube/README.md) | Service-free helper tests: `make test-minikube-contract`. Separate owned Helm runtime scenarios cover installation, topology/node loss, three-UI/API/worker integrity and short load (`make test-minikube-replicas`), optional UI, monitoring and tracing. |
| [monitoring/](monitoring/README.md) | Local-stack launcher safety and dashboard query validation; live exercise through `make test-monitoring`. |
| [packaging/](packaging/README.md) | Existing-image inspection, license extraction, SPDX SBOM generation and versioned chart packaging; service-free regressions run in `make verify`. |
| [performance/](performance/README.md) | Bounded packaged read/mixed/soak workloads, reports and resource ownership. |
| [tracing/](tracing/README.md) | Bounded OTLP decoding, focused Compose/Minikube qualification and an optional local Jaeger viewer. |
| [validation/](validation/README.md) | Read-only API lint, explicit-baseline compatibility checks and real-tool qualification: `make api-check`, `make api-validation-test`. Install locked tools explicitly first. |

Run all commands from the repository root. `make test-unit` selects isolated Go harness helpers alongside application units. `make coverage-unit` runs the same selection with application-only instrumentation; harness execution does not put harness implementation in that denominator.

`make test-tooling-unit` selects Python `test_unit_*.py` separately in each owning directory; its working-directory isolation keeps same-named helper modules independent. Native process/socket cases use `test_functional_*.py` and remain in the broad functional wrappers. API-validation and documentation Node units use their own `npm run test:unit` scripts; their existing complete gates select every `*.test.mjs`.

`make check` and `make verify` need no running service or cluster; missing pinned tools may still require explicit installation. `make python-lint`, included in `make check`, checks maintained Python with installed Ruff; see [installation and scope](../documentation/src/content/docs/contributing/testing.md#python-linting). Go tests and `make verify` do not require Ruff.

Default harness regressions use Python, Make, temporary Git repositories, fakes and test-owned loopback HTTP servers; they do not launch the stacks they test. Real Helm packaging and render tests run only through `make helm-check`. Real PostgreSQL integration lives in `internal/platform/database` and runs through `make test-integration`, not through an arbitrary database selected by a developer shell.

The local fixtures are not production topology. Runtime gates must create and clean only invocation-owned resources, retain bounded failure evidence and never reset shared or useful development data. Generated reports belong in ignored `output/` or `coverage/`, not this source tree. Do not execute child scripts independently when their supervisor supplies ownership, credentials and cleanup.
