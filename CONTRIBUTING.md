# Contributing

GitHub Actions use the same Make targets as local development. Contribution checks run on pull requests and pushes to `main`. Pages prepares the manual on `main`; deployment requires maintainer activation. Other suites use reusable workflows or manual dispatch. See [workflow selection, prerequisites and reports](documentation/src/content/docs/contributing/testing.md#github-actions) and [release-candidate checks](documentation/src/content/docs/contributing/build-and-release.md#run-checks-for-a-release-candidate).

Start with the [development walkthrough](documentation/src/content/docs/guides/local-development.mdx) for running the service. Use the [testing guide](documentation/src/content/docs/contributing/testing.md) for test selection and tool requirements, and [build and release](documentation/src/content/docs/contributing/build-and-release.md) for local artifacts.

## Keep changes focused

Describe the user or operator outcome. State the compatibility impact, completed verification and remaining limitations.

Keep domain behavior in its owning package and infrastructure details at the edge. Preserve tenant isolation, immutable evidence, current authorization on replay and bounded work. Discovery must never imply installation, execution or trust. See [design principles](documentation/src/content/docs/architecture/design.mdx) and, for coding agents, [AGENTS.md](AGENTS.md).

Do not edit an applied numbered migration or silently replace a pinned standards profile. API changes must align routes, OpenAPI, authorization, examples and positive/negative contract tests. Configuration changes must align parsing, supported overrides, sample profiles and deployment wiring.

## Code and documentation conventions

Use `gofmt`, idiomatic Go and focused tests of observable behavior. Keep package tests beside their source; separate database and deployed-system gates from service-free tests. Follow [test placement and selection](documentation/src/content/docs/contributing/testing.md#test-placement-and-selection) rather than creating mirrored unit and functional source trees.

Follow [Go doc conventions](https://go.dev/doc/comment). Introduce each package. Begin declaration comments with the symbol name. Explain behavior, preconditions, errors and ownership; do not paraphrase the signature.

Give maintained source files a short purpose comment. Explain each named function's useful contract, including cancellation, concurrency, authorization and cleanup assumptions where relevant. Use the same concise approach for frontend comments and Python docstrings.

Keep relevant directories navigable with a short README describing responsibilities and verification. Link to the owning guide instead of copying it. Generated code, vendored inputs and immutable migrations retain their original documentation; fixture trees can use a parent guide. Manual sections use landing pages, not additional README pages.

Keep reader procedures in [documentation/](documentation/README.md). Follow the [writing and authoring guide](documentation/src/content/docs/contributing/documentation.mdx) for source placement and style; do not copy work-session narratives into project prose or hand-edit generated pages.

## Verify the changed boundary

Install each gate's required tools explicitly. Run `make validation-tools-install` before the first API lint run and `make workflow-tools-install` before workflow validation, which joins `make check`. Install the pinned [Ruff tool](documentation/src/content/docs/contributing/testing.md#python-linting) for Python checks.

`verify` and packaged functional workflows also require the pinned [ARD conformance Python prerequisite](documentation/src/content/docs/contributing/testing.md#schema-validation-and-ard-conformance). For Go changes, format the changed files and run focused package tests. Then run:

```sh
make test-unit
make check verify
```

Run `test-unit` separately. Substitute `coverage-unit` when measuring the same isolated selection. `check verify` shares source-lint prerequisites and runs the broad local Go gate. It does not run a real database, browser, deployed Catalog or built documentation site.

For an explicit unit-only task, run isolated selections and requested linters. Report broader functional gates as unrun. For documentation-only changes, run `make check`, which includes `docs-check`. Also run `make docs-site-check` for reader-site content or tooling. Use `docs-check` alone during prose/link iteration.

Select additional checks from the [test-layer matrix](documentation/src/content/docs/contributing/testing.md#test-boundaries):

| Boundary | Additional checks |
| --- | --- |
| PostgreSQL repositories, migrations, roles or transactions | `make test-integration` with exact-role, RLS, lifecycle and failure assertions. |
| HTTP workflows and process behavior | Relevant contract tests and `make test-functional`. |
| API schemas and generated consumers | `make api-contract-check`, `make client-go-check`, `make ui-client-check`; `make api-validation-test` for validator changes. |
| SDK source, bindings or examples | `make sdk-check SDK_LANGUAGE=<language>` for each affected language: `go`, `python`, `typescript` or `rust`. Install its tools explicitly. Follow [SDK qualification](documentation/src/content/docs/contributing/testing.md#combined-sdk-qualification). |
| UI behavior | `make test-ui`; appearance changes also use `make ui-visual` with reviewed baselines. Packaging/login changes also use `make test-ui-compose` and `make test-ui-oidc`. |
| UI dependencies | Local `make ui-deps-check`, plus the functional, visual and affected packaging gates in [UI dependency maintenance](documentation/src/content/docs/reference/dependencies.md#ui-dependency-maintenance). `ui-deps-audit` submits dependency metadata to its registry; external registry audits are maintainer-only. Leave them unrun and report that limit. |
| Images, Compose and Helm | `make container-verify`, `make compose-check`, `make helm-check`, `make deploy-check`, and the affected owned deployment scenarios. |
| Security, parsers or dependencies | Adversarial tests, applicable fuzz targets and `make security`. |
| Markdown or manual content | `make docs-check`; also `make docs-site-check` after `make docs-install` for reader-site changes. |

Use the manual's focused recovery, federation, replica and performance gates when those behaviors change. Do not run a long scale trial or deploy a cluster solely for a prose or dashboard edit. Update the functional inventory when adding or changing an HTTP, worker or CLI surface; inventory accounting is not proof that every packaged journey ran.

`make test` delegates to `test-functional-local`, which adds functional-tagged tests. Broad race, coverage and focused contract gates also retain those tests. Keep isolated tests untagged. Use `//go:build functional` for local server/process or composed application cases.

`test-unit` selects isolated Go tests, including Minikube helpers through their existing tag. It enables no functional, database or Helm tests. Use `test-tooling-unit` for Python helpers. This target bypasses workflow env files and must run alone.

Documentation and API-tooling Node units have separate selections: `npm --prefix documentation run test:unit` and `npm --prefix test/validation run test:unit`. Install each project's locked dependencies first. Follow the testing guide for database/deployment tags and fixture ownership.

Tests must own their mutable fixtures. `make smoke` creates records and activates a snapshot; use it only against an intentionally selected instance. Never reset retained development data or use a shared/production database to make tests pass. Preserve running developer sessions, keep source unchanged during artifact-integrity trials, and record cleanup failures honestly.

## Dependencies and artifacts

Add a dependency only for a demonstrated need. Update the [dependency inventory](documentation/src/content/docs/reference/dependencies.md). Review maintenance, licenses, resource bounds and security. Run the applicable toolchain's dependency, test and security gates. The Go license allowlist is Apache-2.0, MIT, BSD-2-Clause and BSD-3-Clause; do not widen it merely to pass a check or apply it to npm tooling. UI, documentation and API toolchains, external test images and optional monitoring tools follow their separate policies in that inventory.

Preserve third-party notices and attribution. Project source uses [Apache License 2.0](LICENSE). Local Make targets build, test and package. External publication and uploads are maintainer-only. CI/CD configuration changes require separate task scope.

Use signed conventional commits. Keep credentials, local configuration, build output and raw test reports out of commits. Maintained generated bindings, fixtures and deliberately reviewed [visual baselines](catalog-ui/tests/browser/snapshots/README.md) remain source inputs. Optional [pre-commit checks](documentation/src/content/docs/contributing/testing.md#optional-pre-commit-checks) help catch simple mistakes but do not replace verification.
