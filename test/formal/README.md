# Job retry arithmetic proof

`make test-formal` verifies one production helper: `backoffCeiling` in `internal/jobs/jobs.go`. It uses Gobra's Silicon backend and Z3 to prove the stated bounds, machine-integer overflow safety and loop termination for every signed 64-bit attempt input. No attempt-limit precondition is permitted.

This supplements the ordinary retry schedule, jitter and worker regressions; it does not prove the whole worker or add to test coverage.

## Install and run

Use Linux amd64 with `gh` and Java 21 on `PATH`. No IDE is needed. Install and check separately from the repository root:

```sh
make formal-tools-install
make test-formal
```

Installation explicitly downloads the checksum-pinned GobraIDE v2.0.5 tool bundle using `gh`, extracting only the verifier JAR, Z3 executable and Z3 license into ignored `test/formal/.tools/`. The JAR retains its embedded notices. Java is an operator-installed prerequisite; the runner records its exact reported version.

Existing tools are revalidated, never silently upgraded. An invalid installation fails; remove only `test/formal/.tools/` before explicitly reinstalling. The target does not build or distribute a container, extension or release.

Execution requires exactly the three reviewed artifact paths with nonempty SHA-256 pins, checks installed hashes and performs no acquisition. A missing verifier/solver pin or an extra extraction path is rejected before any tool executes. Both targets run alone and ignore workflow `.env` files.

Ordinary `test-unit`, `check` and `verify` never invoke Java or acquire formal tools. `JAVA_TOOL_OPTIONS`, `JDK_JAVA_OPTIONS`, `_JAVA_OPTIONS`, `CLASSPATH` and externally selected solver paths are removed from child environments. The solver path, backend, overflow flag and timeouts are fixed.

The qualified host is Linux amd64; the signed 64-bit Go model matches both shipped application architectures, not a qualification of an arm64 verifier host.

## Source and contract binding

`run.py` extracts the current `backoffCeiling` function from the production file. It changes only the result declaration to name `ceiling` and inserts the four comments from `contract.txt`: two postconditions, one loop invariant and one decreasing measure. Every executable body byte is copied unchanged.

It rejects ambiguous/missing functions, changed signature/loop shape, unreviewed contract changes and proof annotations in the source. An isolated regression reverses these transformations and compares the exact function. There is no handwritten implementation copy or general Go translator.

The contract establishes:

- `5*time.Second <= ceiling <= time.Hour`.
- `0 < int64(ceiling)+1 <= int64(time.Hour)+1`, bounding the helper result before the caller applies its smaller window.
- Arithmetic safety with Gobra's explicit `--overflow` flag and default signed 64-bit `int` model.
- Loop termination through the strictly decreasing nonnegative measure `time.Hour-delay`.

`time.gobra` declares only the concrete `int64` duration type and second/hour constants; it assumes no library function behavior. `TestTimeModel` checks these declarations against the repository Go toolchain. The contract and model have reviewed checksums in `tool.json`.

The complete `backoff` caller body is also bound by checksum: changing it requires review that it still uses the helper and preserves safe conversion, positive random bound and existing fallback. That caller binding is a review gate, not a proof of `crypto/rand` or worker/store behavior. Source, model, contract and tool identities are checked again before publishing a passing report.

The reviewed caller normalizes handler minima into `0..24h` and samples `min(backoffCeiling(attempt), configuredCeiling, 24h-hint)`. Validated positive configuration keeps this window nonnegative; a maximum hint leaves zero and a random bound of one. The caller adds the hint after sampling, or to the midpoint fallback.

The current digest and drift calibration bind this reviewed composition, but the scalar proof does not establish that composition or runtime configuration delivery.

Ordinary tests own the exact exponential schedule, zero/negative policy, jitter range and lease-reclaim/exhaustion behavior. A constant five-second implementation would satisfy the bounds proof while violating ordinary schedule assertions. Do not weaken those tests or substitute formal success for them.

## Controls and evidence

Each invocation runs the real verifier eight times: the source proof, three deliberately invalid variants and four tool calibration cases. All are bounded, with a 2 GiB JVM heap, 30-second package timeout, 90-second outer deadline and bounded output. The shared subprocess supervisor kills the owned process group on deadline, overflow or interruption.

Temporary proof directories are removed on success and failure.

| Case | Required result |
| --- | --- |
| Current production helper | Complete verification, zero errors. |
| False specification requiring a zero result | Exactly one postcondition diagnostic. |
| Arithmetic variant returning twice the cap | Exactly one postcondition diagnostic. |
| Variant preventing delay growth | Exactly one decreasing-measure diagnostic. |
| Unspecified scalar function | Verifier success demonstrates that no contract is not useful behavioral assurance. The application contract gate rejects missing contracts. |
| Contradictory-precondition function | Verifier success demonstrates vacuity. The application gate forbids preconditions. |
| Generic syntax unsupported by this pin | Exactly the expected parser diagnostic; never accepted as an application proof or one of its semantic failure controls. |
| Signed integer increment at the maximum value | Exactly the expected overflow diagnostic, qualifying the mandatory machine-arithmetic flag. |

Exit codes alone are insufficient. The runner requires the pinned Gobra identity, verification-start evidence, one exact completion count and the intended diagnostic class. Wrong failures, crashes, empty/incomplete output, changed inputs, missing/corrupt tools and interruption fail the target.

Handled failures retain `status: incomplete`; a missing report after an uncatchable interruption is also incomplete evidence. Only a completely checked run publishes `status: passed` in its unique `output/formal/run-*/report.json`. Proof inputs and bounded logs are alongside it.

Reports label tool calibrations separately from application proof controls.

For helper, annotation, caller or tool changes, run `make test-formal` and review every case. Update hashes only after reviewing the changed contract/caller/model; do not refresh pins to silence a failure. Unsupported Go syntax is a tool limitation to investigate, not automatically a product bug.

Unproved obligations can indicate either an implementation defect or an insufficient contract/invariant; preserve the failing input and determine which before changing assertions.

## Harness verification and maintenance

Native helper units run through `make test-tooling-unit`. Local Make/process fixtures run with `go test -tags=functional ./test/formal` and through the normal functional/race selections. These use synthetic executables and never require Java or download tools.

They cover exact extraction/caller binding, missing/vacuous contracts, evidence classification, host/environment isolation, missing tools, deadlines, interruption and source drift. These fixture passes do not replace `make test-formal` against installed tools.

`tool.json` records the bundle/JAR/solver hashes and Gobra/Viper source identities. Review the [tool licenses and distribution boundary](licenses.md) before changing them. Preserve ordinary Go tests, existing coverage floors and the explicit acquisition boundary.

Keep one fixed proof; another target or specification language needs a separate concrete invariant and justified maintenance cost.

No PostgreSQL, application binary, server, randomness distribution, lease fencing, eventual Job completion or whole-service correctness claim follows from this proof. The verifier, Viper encoding, solver, machine model, small extraction and trusted time declarations remain assumptions. Formal qualification evidence records the reproduced application bug, its ordinary correction and measured qualification.
