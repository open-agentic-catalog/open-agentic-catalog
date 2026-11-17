# AgentField metadata consumption

This optional campaign runs unchanged AgentField against the current Catalog API and worker. Both products use separate invocation-owned PostgreSQL instances. It searches published ARD metadata, explicitly imports one result, and independently reads the saved record after a native process restart. It needs no model, browser, public platform or resource runtime.

## Run

Use a local Linux Docker engine, Compose 5.6.1 or newer, `gh` for public source acquisition, and the normal Catalog build prerequisites. Docker Desktop on macOS can provide the Linux engine; native macOS execution is not required. Native Linux/amd64 execution is qualified; ARM64 execution remains unqualified.

Run each command separately from the repository root:

```sh
make agentfield-tools-install
make test-interoperability-agentfield
make test-interoperability-agentfield-cleanup
```

The installer downloads the pinned runtime source through `gh`, its original Go modules, the pinned compiler image and the existing pinned Python runner image when missing. It builds the ordinary server without embedded browser assets. Original source, module locks, root license, linked dependency notices, build version and exact local image identity are checked separately. The runtime image is local test tooling; it is not part of Catalog delivery.

The tests do not install tools or pull images. Missing or stale prerequisites fail preflight. Prepare the normal backend/fixture build inputs and PostgreSQL image through the documented local build procedure. Application and fixture builds may use their ordinary dependency caches.

## What the campaign checks

- Authenticated native JSON APIs, a separately refused unauthenticated management request, private HTTPS trust and benign native controls before the Catalog journey. Separate synthetic controls check sparse-result defaults, absent null trust declarations and persistence of empty inline JSON data.
- Two real governed MCP releases and active anonymous/authenticated snapshots. Native Search sends the actual nested query with `federation: "none"` and no management or Catalog token.
- Exact nonempty membership and supported metadata. A real Catalog continuation is present on the wire and absent from the decoded native result.
- Healthy empty Search is distinct from source errors. A native import missing its identifier returns the intended error without changing saved metadata.
- Search does not refresh an existing import. Explicitly importing a newer native control version updates its metadata under the same identifier-derived ID, without adding a separate version record.
- Explicit import from the actual selected result. Fresh dashboard/state reads agree before and after restart and preserve exact import IDs and timestamps.
- Unallowlisted destinations, wrong private CA, unavailable discovery and required Catalog authentication retain their native refusal cause. A passing public search does not qualify private access.
- A new unpublished VersionRevision does not change the released definition. Release retirement removes the resource from new discovery; the independent native import remains stored.
- Callable bindings remain absent and external invocation remains disabled. An inert TCP trap records zero entry-URL, runtime and forbidden-destination connections. External TCP and DNS are independently denied while the owned source remains reachable.
- Assertion failures, actual SIGTERM, preserved existing reports and cleanup failure admission are covered separately. An incomplete campaign or failed cleanup cannot pass.

`inputs.json` pins AgentField commit `118b90ba42af9bc9f9366d645c9d0d38cd5f245a`, Apache-2.0 source and its original `go.mod`/`go.sum`. The native PostgreSQL store is selected so the unchanged Go server can use its normal static build; local SQLite needs CGO. The two databases never share credentials, schemas, volumes or migration authority. Telemetry is explicitly disabled. Execution is confined to an internal Docker network with no published ports.

The test-only TLS observer forwards the selected discovery routes to the real Catalog without changing their response bytes. It also provides a separately labeled synthetic native-reader control. Synthetic results do not count as Catalog publication evidence. No peer decoder, authentication behavior or production network policy is patched.

## Native limits

Search replaces each `source` with the queried registry base, drops unknown fields and response continuation/referrals, and does not enumerate the complete Catalog. A nested trust declaration retains only native `identity` and `identityType`. Imports retain identity, type, name, description, URL or inline data, representative queries and the identity trust summary where present. They drop version, tags, capabilities and other extensions. Publisher extraction recognizes legacy `urn:ai`, so Catalog `urn:air` entries have no saved publisher. Import IDs hash only the identifier; source and version do not create distinct imports.

For sparse Search results, the native decoder emits empty `displayName` and `type` strings when those fields are absent. It omits a null trust declaration, but retains present empty inline JSON data. The controls characterize those native transformations without adding fields to the original wire response.

The reviewed native reader has no response-byte ceiling in this path. The owned proxy bounds fixture bytes; that does not harden AgentField or qualify arbitrary registries and redirects. Use trusted configured sources and independent deployment egress controls.

The importer accepts caller-submitted metadata without fetching or verifying it. This harness binds its request to the recorded native result; that is not an AgentField verification guarantee. Trust summaries are unverified text. Imported state does not update or disappear automatically when the Catalog changes. No private authentication path, runtime calls, publisher qualification, reverse synchronization or full pagination is qualified.

## Results and cleanup

The runner prints its ignored result directory. The report records each phase, original wire JSON, decoded native result, independently saved state, publication/release bindings, source/image identities, traffic and cleanup. A successful result requires every phase and cleanup. Helper units run through the existing tooling selection; they cannot replace the native campaign.

The campaign removes only its own Compose containers, networks, database volumes, temporary credentials and application/fixture image tags. It retains the separately installed peer image and notices for another run. No browser or port-forward remains. If cleanup fails, use the exact invocation-specific `stopCommand` in the report. Never reset retained Catalog data or run global Docker cleanup.

For consumer-facing instructions, see [Use Catalog metadata in AgentField](../../../documentation/src/content/docs/guides/agentfield.mdx).
