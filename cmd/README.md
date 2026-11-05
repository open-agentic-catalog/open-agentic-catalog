# Executable entry points

This directory contains the two backend Go commands. The optional [UI host](../catalog-ui/cmd/catalog-ui/README.md) has a separate source directory and build. Run Make targets from the repository root; `make build` writes both executables under `bin/` without installing them on `PATH`.

| Directory | Responsibility |
| --- | --- |
| [catalogd/](catalogd) | API, worker and memory-only combined process roles; startup, supervision, signals and graceful shutdown. |
| [catalogctl/](catalogctl) | Native draft preview/apply/resume, remote HTTP reads and explicit reindex requests, configuration/profile checks, diagnostics, migrations, identity provisioning, bounded repair, audit/restore verification and development smoke workflows. |

Command parsing and process lifecycle belong here. Domain behavior belongs in [internal/](../internal/README.md), and [runtime composition](../internal/platform/runtime) wires the implementations. Neither binary reads `.env`; its launcher supplies explicit environment variables.

`catalogctl remote` provides capabilities, Search, published-record and Job reads. Its explicit `reindex` command requests a tenant-authorized derived-index rebuild and returns a Job; acceptance is not completion. `remote.go` owns flags and safe JSON output; `remote_transport.go` owns the origin-bound HTTP client, token/private-trust files and response limits. It does not load server configuration, use SQL or follow result URLs. The small handwritten request slice has OpenAPI contract tests; no generated SDK is shipped.

`catalogctl import preview --file PATH` prepares one to 64 native drafts offline. Its strict YAML reader, confined asset reader and deterministic Skill packager reuse pinned native validators and the embedded Control request schemas. It makes no network calls or mutations. See the [manifest reference](../documentation/src/content/docs/guides/resource-authoring.mdx#preview-a-native-draft-locally) for input, limits and unresolved server checks. `import apply` and `import resume` use the same preparation, explicit remote client inputs and compact private receipts. They create drafts only, preserve uncertain outcomes and verify exact retained IDs without automatic mutation retries. Ordered multi-resource execution stops at the first blocked, failed or uncertain item. Whole-manifest resume preserves original keys and reports completed/current/unattempted items, without rollback or adoption.

Use the [command reference](../documentation/src/content/docs/reference/commands.md) for supported command families and the linked operator runbooks for required authority and stop conditions. Smoke, migration, provisioning and repair commands can change state; they are not interchangeable health checks.

`catalogctl seed-demo` and `make demo-seed` load selected samples on an explicitly selected owned local instance. Both default to the curated profile; Make selects `samples/catalog.yaml`. The explicit embedded `minimal` profile retains the five synthetic lifecycle examples.

`seed_demo.go` owns command selection and the bounded API workflow with its verified local receipt. It shares payload builders and request mechanics with smoke, not smoke's destructive checks. Completed reruns with unchanged verified inputs send no authoring commands. See the [demo walkthrough](../documentation/src/content/docs/guides/local-development.mdx#populated-development-catalog) for receipt retention and deliberate cleanup.

Run `go test ./cmd/catalogd ./cmd/catalogctl -shuffle=on -count=1` for isolated command tests; add `-tags=functional` for local transport, process and composed workflows. Follow [CONTRIBUTING.md](../CONTRIBUTING.md) for complete gates.
