# Generated Go-client compatibility

This harness generates a Go consumer from the unmodified [Control OpenAPI](../../api/openapi/control-v1.yaml), compiles every generated operation and tests representative exchanges. It does not publish an SDK or replace the existing `catalogctl remote` transport or independent TypeScript UI client.

## Run

Use the repository Go toolchain (currently Go 1.27.3), Python 3.10+, Make and a C compiler for Go's race detector. Run each target separately from the repository root:

```sh
make client-go-tools-install
make client-go-check
make client-go-generate
```

Installation explicitly downloads the locked generator and consumer dependencies and builds the generator under ignored `.tools/`. Repeat it after either module's locks or the root module locks change, or after removing the Go module cache/toolchain. The generator pins oapi-codegen v2.8.1. The consumer pins runtime v1.8.0. Each module owns its dependency graph.

Keep `go-yit` compatible with `yaml-jsonpath`'s `gopkg.in/yaml.v3.Node` types. Later upstream commits use different YAML package paths and cannot replace that dependency without a compatible parent update.

Both modules use local, read-only resolution during checks; no deployment environment file is loaded, no target publishes anything, and these goals cannot be combined with other Make workflows.

The generator's transitive dependencies are explicitly patched to kin-openapi v0.152.0 and `golang.org/x/mod` v0.42.0, with the latter selecting `golang.org/x/tools` v0.52.0. These pins address the repository security gate's advisories without changing generated output or the service dependency graph. Retain the patched minimums when refreshing the generator lock; rerun `make security` as well as client compatibility checks.

Tools and tests run natively on the host: inherited `GOOS`, `GOARCH` and `CGO_ENABLED` cross-build settings are ignored. Checks also prevent `GOPRIVATE`/`GONOPROXY` from bypassing the disabled module proxy with a direct Git download. A malformed installation stamp fails with reinstall instructions, not an internal traceback.

`client-go-check` always generates fresh code twice in an invocation-owned directory under `output/clientcompat/`, compares the outputs, runs race-enabled shuffled default units followed by additive `-tags=functional` tests and functional-tagged `go vet`, tests both native launcher files from their owning directory and checks source/lock integrity. It needs no prior generation, database, Compose stack, browser or running Catalog. Dependency acquisition is disabled during checks; a missing/stale installation requires the install target.

Success removes that invocation's output; failure retains generated files and `check.log` for compiler/test diagnostics. A generation failure is reported on the terminal and retains the work directory. Existing generated files and other output directories are untouched.

`client-go-generate` writes ignored `go/generated/client.gen.go` for inspection. Do not commit it. The nested modules keep the generated code outside root unit coverage, runtime dependency selection and container build inputs.

Untagged consumer tests cover codecs and controlled transports; `*_functional_test.go` covers real HTTP/TLS exchanges and authenticated in-memory Catalog workflows; they are not database or packaged functional qualification. Run the check when changing OpenAPI, generator/runtime versions, configuration or handwritten consumer tests. It remains a separate gate from `make check` and `make verify` so ordinary Go builds do not acquire client-generation tools.

## What the tests cover

- Private ANS assessment request/result codecs, explicit version presence, base64 evidence bytes and refusal of an unsupported broad trust verdict. These codec tests do not qualify signatures or an ANS deployment. The consumer lock includes the runtime dependencies reached by the composed Catalog tests.
- Generated capabilities, empty/paged Search and Browse, exact Catalog records, and Job List/Get. Browse covers two unique governed releases, stable filtered continuation, decoded summary metadata, retained detail/digest readback and raw detail-field omission. Prepared completed and running Jobs cover created-time/ID ordering, permitted snapshot references, explicit cancellationRequested=false, absent results and private payload/credential/lease/snapshot omission.
- Foreign-tenant collection omission and exact 404, malformed/foreign/wrong-purpose cursor refusal with the same Reader for purpose controls, Browse filter binding and undeclared query-field rejection. Raw object assertions supplement typed decoding and independent OpenAPI validation. Separate recorded fault-injection checks qualified wrong IDs/order/duplicates, private-field leaks and omitted false flags; those disposable checks are not part of the ordinary target.
- Namespace create/update, version creation and retained evidence, draft `:delete`, and a prepared failed Job's `:retry` and read-back. Assertions cover 200/201/202/204, ETags, idempotent replay, stale revisions, Location, Problem codes, request IDs, roles and cross-tenant hiding. No worker or polling is needed.
- Actual generated requests and responses validated against OpenAPI independently of typed decoding. Codec fixtures use JSON Schema 2020-12 for all four resource kinds, external search alternatives and Plugin inspection fields; local results come from the in-memory workflow.
- Typed Job kind/state/retry-parent filters, literal unknown-kind omission and changed-filter cursor refusal. Succeeded indexing Jobs decode the separate semantic result variant, preserving a zero accounted-input count and omitting private retained fields. Invalid declared enum values are rejected by the checked transport; undeclared query fields reach the backend's closed-query refusal.
- Source-bound `continuation_unavailable` and `insufficient_evidence` warnings, useful bounded results and `partial: true` in both Catalog Search and administrator live-search codecs, with independent response-schema validation and undeclared-status rejection.
- Lossless raw evidence, nested extensions, arrays, null, false, empty and omitted values. Deliberately partial precision characterizations are separate from valid request and retained-evidence exchanges.
- Explicit authentication, query encoding, UUID action paths and opaque headers; bounded response reads before generated parsers, unknown Content-Length, exact/over-limit bodies, safe failures, closure, cancellation, deadlines and rejected redirects.
- Oversized response headers, malformed typed responses, preserved Problem/Retry-After/request-ID metadata without retries, and request-editor failure before sending a command.
- Private-CA acceptance, untrusted certificates and hostname mismatch rejection before authenticated HTTP transmission, and credential refresh through the request editor on each call. The body-deadline test requires receipt of response headers so connection failures cannot count as body cancellation coverage.
- Launcher installation success/failure, missing or malformed installations, subprocess output/exit/timeout handling, source/lock drift, invalid module replacements, native/offline environment settings and standalone help. Controlled mocks and short Python children keep these cases independent of Go downloads or a deployed service.

## Use the generated methods safely

Supply `WithHTTPClient` with a transport that enforces response limits before parsing, explicit deadlines, trusted TLS, no ambient proxies and no redirects. Supply authentication explicitly with `WithRequestEditorFn`; never print tokens, transport causes or private response bodies. The small test-only `boundedDoer` and `explicitHTTPClient` in [client tests](go/client_test.go) show this boundary.

Production callers should use their established credential and private-CA handling, not copy the fixture's development tokens. There is no automatic mutation retry.

The test transport disables keep-alive connection reuse: Go can transparently retry a replayable POST carrying `Idempotency-Key` after a reused connection fails. A lost-response regression verifies one HTTP attempt per caller command; the in-memory workflow separately exercises deliberate caller-directed replay. This test transport is not a production connection-pooling recommendation.

Raw methods return `*http.Response`. Always close its body, check the operation's expected status and content type, and handle Problem details separately. A successful Go return from `GetCapabilitiesWithResponse` can still represent HTTP 401; its Problem branch is not a successful capabilities result.

The generated `WithResponse` parsers use unbounded `io.ReadAll`, so checking length after parsing is too late. The harness bounds bodies to 8 MiB and headers to 32 KiB; applications should choose limits appropriate to their operations and deployment.

For an already configured bounded client `c`, a read uses the generated operation and retains the raw body:

```go
response, err := c.GetCapabilities(ctx)
if err != nil {
    return err
}
defer response.Body.Close()
// Check HTTP 200 and application/json before decoding the bounded body.
```

Ordinary control fields can use typed request bodies. For example, an authenticated Tenant Administrator creates a namespace with a caller-chosen idempotency key:

```go
response, err := c.CreateNamespace(ctx,
    &catalogclient.CreateNamespaceParams{IdempotencyKey: commandKey},
    catalogclient.CreateNamespaceJSONRequestBody{
        Slug: "examples", DisplayName: "Examples",
    })
// Handle err, close the body, require HTTP 201, and retain the returned ID/ETag.
```

Treat cursors, ETags, IDs and Location as returned values, not strings to reconstruct. An update supplies the last ETag as `If-Match`; an intentional replay sends the identical body and command headers. A stale ETag requires resolving the conflict, not blindly retrying.

### Arbitrary JSON needs the raw path

Typed `Representation.data` decoding rounds `9007199254740993` to `9007199254740992`. `UseNumber()` helps ordinary maps, but an outer decoder does not control the generated `RepresentationInput.UnmarshalJSON`; that input still rounds the number. The same caution applies to other generated `map[string]interface{}` evidence, extensions and live entries.

Typed projections are not lossless evidence, even when the response wrapper also retains raw bytes.

Use raw response methods and `json.RawMessage` for retained documents. Use a generated `WithBody` request with exact JSON bytes for submissions containing arbitrary standards data:

```go
response, err := c.CreateResourceVersionWithBody(ctx, resourceID,
    &catalogclient.CreateResourceVersionParams{IdempotencyKey: commandKey},
    "application/json", bytes.NewReader(versionJSON))
// Require HTTP 201, inspect evidence.state, and retain IDs/ETag and raw evidence.
```

`versionJSON` must be a complete valid CreateVersion body with `version` and `representations`; `label` is not a substitute. `WithBody` neither validates the schema nor checks standards compatibility. The maintained [workflow tests](go/workflow_functional_test.go) submit a valid Agent Card extension containing a large integer and verify the retained/read-back representation without number loss.

JSON object key ordering may change during Catalog canonicalization; exact numeric values and nested data must not.

## Maintain

`tools/` owns the generator lock; `go/` owns handwritten tests and the generated consumer's runtime lock. `generate.yaml` enables model/client generation and type-name collision resolution. No OpenAPI downgrade, normalization, overlay, skipped operation, generator patch or custom template is applied.

Update versions deliberately with Go tooling in the appropriate nested module, then run `go mod tidy` there. For consumer import changes, first generate the inspection copy so tidy can see runtime imports. Review both nested locks, reinstall and rerun the check twice. Also run the normal API/source gates and documentation checks; update [dependency policy](../../documentation/src/content/docs/reference/dependencies.md) when pins change. oapi-codegen and its runtime are Apache-2.0; YAML is the existing dual Apache-2.0/MIT dependency. These are test-only tools, not new service imports or shipped generated assets.
