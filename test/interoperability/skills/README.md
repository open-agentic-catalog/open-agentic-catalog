# Independent Skill consumption

This explicit functional test qualifies unchanged `skills-ref` 0.1.0 at Agent Skills commit `69ef37e9424c0a7ea9dd2293b559e43ec8176379`. It validates a directory, reads its metadata and generates an available-Skills prompt listing. The test driver, not skills-ref, discovers Catalog records and materializes immutable Git files.

Neither component executes Skill instructions/scripts or installs into a developer home.

```sh
make skill-tools-install
make test-interoperability-skills
```

Run each target alone from the repository root. Installation requires Python 3.10+, GitHub CLI (`gh`), a local Linux Docker engine, Go and network access for pinned toolchain/modules, source and hash-locked wheels. Python 3.13 runs inside the digest-pinned image.

Execution requires a non-root developer, local Git and the installed image/build cache, with no downloads, public endpoints, exposed ports, PostgreSQL, Compose, Kubernetes or precompiled Catalog binary. Targets ignore deployment `.env` selection. They never stop shared containers or change retained data.

## Scope and assertions

- Ten direct controls calibrate valid input, missing document, malformed frontmatter, missing field, parent mismatch, lowercase filename, trimmed/NFKC names, empty compatibility and a quoted description containing `---`. Rejections must report the intended validation diagnostic for the addressed directory; unrelated failures cannot count as passing negatives. Properties/prompt commands can accept input rejected by full validation; accepted governed consumption must validate first.
- A disposable memory Catalog receives one ZIP built from a real local Git commit, through normal contributor/reviewer/publisher APIs. Real CLI Search and exact read bind every published identity, profile, archive digest and handoff. The HTTPS Git locator is an exact fixture mapping to an owned repository; no remote fetch or claim of remote existence is made.
- The consumer reads only regular blobs at that exact commit/subtree, under existing package ceilings, with inherited Git configuration disabled. It compares the complete retained paths, bytes, SHA-256, modes, root, count and expanded size. Original ZIP identity is checked separately from Git IDs and file hashes. A branch advance and unpublished draft must preserve published revisions/evidence while allowing legitimate mutable candidate pointers to advance.
- Six Git/inventory refusals and missing/invalid/wrong-tenant reads must precede native consumption. Failure classification rejects unrelated Git/tool failures as false passes. Later readback preserves the published evidence. The independent reader has only a read-only package mount and no Catalog credentials; an executable-marked sentinel stays unexecuted.
- Each native command has a 15-second/64-KiB output bound; Git has a 15-second bound and package-specific output ceilings. The scenario has a 600-second bound plus bounded independent cleanup. Exact consumer commands, exits and outputs are recorded. Complete phases and source/tool identity are required for success; missing output, unexpected outcomes, cancellation or cleanup failure cannot pass.

Package-only Skills are authenticated metadata/handoff records, excluded from public ARD/AI Catalog delivery. This test adds no archive server, public download API, OCI transport, runtime or prompt-safety certification. Existing archive/fuzz suites own the exhaustive admission attack matrix; `make client-skill-check` separately checks the Catalog discovery instruction package.

## Tool provenance and limitations

`provenance.json` binds the complete package source and root Apache-2.0 license. `requirements.txt` hashes the resolved build/runtime dependencies; regenerate deliberately with the repository-pinned uv using `uv pip compile test/interoperability/skills/requirements.in --python-version 3.13 --generate-hashes --output-file test/interoperability/skills/requirements.txt`, then review the closure and rerun installation/native controls. The installer compares every installed skills-ref Python source file and version with the pin and records installed package/license inventory.

Build-only packaging tools and pip are removed after installation. The retained build closure includes MPL-2.0 pathspec; it is an unmodified execution-only build dependency, not imported into or distributed with Catalog. Runtime dependencies are Click (BSD-3-Clause), StrictYAML and six (MIT), and python-dateutil (dual Apache-2.0/BSD); upstream license files remain with installed distributions.

The test image is not a shipped application image.

Native validation is narrower and sometimes more permissive than Catalog's pinned contract. Lowercase `skill.md`, stripped/NFKC names and empty compatibility are admitted by this reader. Its substring-based frontmatter split rejects a valid quoted description containing `---`.

Properties/projection omit full validation; prompt locations are raw paths, so this fixture uses safe owned paths. These are recorded tool limitations, not reasons to modify selected bytes, relax Catalog validation or patch upstream. Prompt generation does not establish model instruction-following or general script safety.

## Reports and ordinary checks

Installation evidence is under ignored `output/interoperability/skills/`; each run creates `<run-id>/report.json` with the phase, exact native outputs, selected identities, source/tool/binary fingerprints and cleanup result. Reports contain synthetic fixture content, not Catalog tokens. Failed runs preserve fixed error types/source frames; cancellation retains a failure report and cleans only named containers/private files.

The installed image remains reusable.

`test_unit_skills.py` checks pure inventory, selection, native-output, report completeness and failure classification without subprocesses. `test_functional_skills.py` uses actual temporary Git/Make and child-process interruption controls, including interruption during the first report write, without Docker or acquiring/executing skills-ref. Both use ordinary native discovery through existing unit/local-functional Make gates. `container-check` includes the test image's Containerfile. The explicit interoperability target alone runs the installed native reader and disposable Catalog. This is functional scenario evidence, not a new statement-coverage denominator.
