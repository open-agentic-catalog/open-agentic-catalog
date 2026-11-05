# Frontend assertion strength

Install the pinned Node/npm versions and `make ui-install` first. Stryker core 10.0.0 (Apache-2.0) and fast-check 4.10.2 (MIT) are development dependencies. Neither enters the browser bundle or requires a server. The lock includes their complete dependency closure. A scoped `typed-rest-client` override uses the compatible `qs` 6.16.1 security fix.

```sh
make ui-test-property
UI_PROPERTY_RUNS=10000 UI_PROPERTY_SEED=2 make ui-test-property
UI_PROPERTY_SEED=42 UI_PROPERTY_PATH=4:5:2:4:4:4:4 make ui-test-property
make ui-test-mutation
```

Run each target alone; both bypass workflow env files. Replay paths are specific to the unchanged generator, seed and dependency versions; use the path from an actual failure. The example path illustrates the syntax, not a failing production case. Defaults are 100 property examples and seed 42. Runs are bounded to 10,000 and seeds to 0-2147483647. Ordinary UI units include the same property with bounded defaults.

The property builds a small independent JSON tree, retaining duplicate keys, large numbers, exponents, negative zero and escape spellings. Expected literal tokens and formatting come from that tree, independently of the production lexer. Reconstruction and formatting idempotence supplement those assertions. JSON parsing/stringifying is not used as the value oracle: it would collapse duplicate keys and round numbers.

Mutation runs select only `src/api/commands.ts` and `src/views/code-document.ts`. Stryker's command runner executes all 29 cases from the four owning Vitest files for every mutant. The direct Vitest plugin was rejected during qualification because it skipped selected nested tests with the pinned Vitest version. The wrapper requires complete native Vitest outcomes; missing tests, skipped assertions, tool errors, timeout and incomplete reports fail the gate. A tool exit alone is never a catch.

Inspect the printed `output/ui-mutation/run-*/report.json`, `mutation.json` and `tool.log`. Runs use two workers, a 15-minute outer bound, bounded output, fixed property inputs and owned sandbox cleanup. Source identity covers the inputs before and after execution.

`mutation-dispositions.json` binds the two source digests, mutant inventory and every reviewed survivor by exact mutation identity. Source changes or newly caught/disappearing survivors require a fresh review, not automatic baseline acceptance. No browser runs per mutant; this target is explicit rather than an aggregate prerequisite.

The sandbox and identity include `tests/required.ts`, imported by the selected command tests.

The qualified selection catches 195/211 mutants. The initial selection catches 170/200 mutants before the additional boundary, golden-format and generated assertions. Fourteen remaining mutants are equivalent or unreachable after validated tokenization. Two change the conservative internal formatting-work estimate; these are explicit residual limitations, not equivalent mutants or catches. Mutation percentage measures this selected operator/file set only, not statement coverage or exhaustive correctness.

Run `test-ui`, dependency checks and the actual mutation/property targets when changing this tooling. Native report-validation units are discovered through existing UI test scripts. The [testing guide](../../documentation/src/content/docs/contributing/testing.md) owns the broader gates.

A separate compiler/fixture requalification uses 200 operators and observes 184 catches and 16 survivors. These results use a different selection from the 211-mutant result above.
