# Tracing qualification and local viewer

`otlp.go` decodes the bounded protobuf captures made by `test/functional/fixture/tracing.go`. Compose invokes `decode/`; Minikube imports the same decoder. Captures must contain only the reviewed resource fields, fixed span outcomes and private causal links, without baggage, vendor state, exception events or raw request data.

Neither package is shipped in Catalog binaries.

Before projecting causal fields, the decoder requires exactly one string `catalog.outcome` from the Catalog vocabulary, rejects instrumentation-scope attributes and checks nonzero trace/span/link identities at their exact wire widths. Roots have no parent ID; children require a valid parent ID. Isolated decoder tests retain valid roots, children, links and every reviewed outcome alongside single-fault rejection cases.

This validates the Catalog export profile, not arbitrary OTLP producers.

- `make test-tracing` runs the existing functional supervisor with a focused tracing profile: disabled startup, enabled private HTTPS with mounted authentication/CA inputs, incoming parentage, Job links, replay, observed export failure and fresh acceptance from the unchanged process, separate process restart, and disablement. The supervisor owns cleanup and records safe evidence under `output/functional/`.
- `make test-minikube-tracing` uses the existing owned-cluster harness with two APIs and two workers. It routes explicitly to each API, tests replay, reuses the database-barrier worker-loss scenario, decodes attempt links and checks process identities, Helm Secret/trust wiring and disabled transitions.
- `go test ./internal/platform/tracing -run '^$' -bench . -benchmem` measures the in-process tracing path. It is not a capacity promise or a replacement for packaged performance tests.

## Query a real Job handoff in Jaeger

Start the optional viewer with `make trace-viewer-up`. Select different loopback ports if the defaults are occupied. Then run:

```sh
make test-tracing TRACING_VIEWER=on
```

The default `TRACING_VIEWER=off` needs no Jaeger container. The selected check requires the running `jaeger` service in project `open-agentic-catalog-trace-viewer`. It verifies the configured image pin and both loopback port bindings before sending data. It does not use an arbitrary backend URL or another project's viewer.

The existing supervisor creates its own Catalog, PostgreSQL and local source. Full sampling uses a fresh incoming trace context and an installation name unique to that run. A real source-sync Job completes. Its replay uses a different incoming trace and returns the original response without another enqueue.

The existing OTLP decoder checks the exact captured batches for privacy before relay. The check sends those unchanged batches to Jaeger, without reconstructing spans or forwarding fixture credentials. This qualifies Jaeger retention of actual Catalog exports; it does not replace the private HTTPS exporter checks.

The check queries Jaeger for three exact trace IDs: the original request, the worker attempt and the replay. It requires the creating HTTP request, enqueue, separate worker-attempt root and its actual federation-sync child. The replay must retain the original cause without another enqueue. Stored span IDs, parent IDs, the cross-trace link, service names, installation and original process identities must match the decoded exports. Incomplete storage results retry within a fixed bound; wrong identities or links fail. OTLP partial rejection fails even with HTTP 200.

Search controls use Jaeger's `/api/v3/trace-summaries` endpoint and a time window derived from the stored spans. Incorrect installation and service filters must return no results. Correct filters must return the exact fresh request and attempt trace IDs. HTTP errors cannot satisfy a negative control.

The functional report includes `tracingViewer` when selected. Its `backend`, `installation`, `spans`, `apiInstance`, `workerInstance` and synthetic Job/source references provide private local evidence. The safe span projection is also retained in `output/functional/run-<id>/tracing-viewer-spans.json`. The `filterControls` and `searchWindowMicros` fields record the search checks. Browser navigation through the actual link is a separate check: Captures and export counters alone cannot satisfy this backend-retention check.

1. Open the report's `backend.queryUrl` in a browser.
2. Select `open-agentic-catalog-worker` under Service.
3. Enter `service.namespace=<installation>` under Attributes, using the report's installation value.
4. Click Find Traces.
5. Open the result that matches `spans.attempt.traceId`.
6. Click the link icon beside the `job process` row.
7. Check the new tab's trace ID against `spans.enqueue.traceId`.
8. Check the highlighted enqueue span against `spans.enqueue.spanId`.
9. Expand its parent HTTP POST span.
10. Check that span's ID against `spans.request.spanId`.

A trace can contain several requests. Its title can name another request. Use the enqueue's parent span to identify the creating request. The linked trace must differ from `spans.replay.traceId`. The worker attempt remains a separate root, not a child in the API trace.

For browser filter controls, append `-absent` to the installation attribute and require no results. Restore the correct installation and require the exact attempt. Add `service.instance.id=<workerInstance>` and select the API service to require no results. Restore the worker service to find the attempt. Keep the time range unchanged between these browser controls. Use a new run if that range excludes the recorded attempt.

Run `make trace-viewer-down` after inspection. The functional supervisor removes only its own Catalog fixtures; the explicitly started viewer remains until you stop it. Removing the viewer discards its in-memory traces. Start a new viewer and rerun the selected check when you need fresh evidence. Sampling can omit a cause. Backend eviction or retention limits can also remove it. A link does not guarantee that the linked trace remains available.

## Collector recovery evidence

The Compose profile records the original API container ID, PID, start time and restart count alongside its decoded `service.instance.id`. Collector rejection stays active until that process's failed-export counter increases over the pre-fault baseline; product reads and readiness must remain useful. After restoring the same receiver, a newly generated synthetic trace/parent must be captured, decoded and bound to the original instance before any restart.

A separate restart requires a new start identity and another fresh trace from a different service instance. Old captures, submitted-span counters alone, missing failure evidence or an implicit restart cannot pass. The supervisor requires complete bounded recovery evidence in its final report.

Existing parent/Job-link, privacy, replay, disabled-startup and four-mode diagnostic timing/allocation checks remain. The overhead profile's unavailable-collector counters belong to its separately recreated process and are not the original-outage oracle. Export counters describe submitted spans and failed batches, not exact queue loss or durable receiver retention; bounded trace loss remains allowed.

No tracing default, durable store, viewer dependency or Minikube wiring changes are needed.

## Local viewing

Use the [local trace-viewing guide](../../documentation/src/content/docs/operations/observability.mdx#view-traces-locally) for startup, Catalog configuration, viewer navigation, port overrides and cleanup. The fixture is declared in `viewer.compose.yaml` and `jaeger.yaml`; it runs under Compose project `open-agentic-catalog-trace-viewer`.

Jaeger renders a cross-trace link beside the attempt's span row. Opening it navigates to the linked trace in another tab. The linked trace can be absent due to independent sampling or retention; this does not invalidate the retained causal link.

Viewer navigation is a separate UI check, while Compose and Minikube assert the application's actual exported IDs and links.

`viewer.compose.yaml` pins Jaeger 2.22.0 by multi-platform index digest. The [selected source license](https://github.com/jaegertracing/jaeger/blob/v2.22.0/LICENSE) is Apache-2.0. This is an execution-only upstream development image, not a redistributed Catalog artifact or a claim that its entire bundled closure has one license. Review version/license information and rerun the viewer check when refreshing the pin. The minimal `jaeger.yaml` is based on the documented [Jaeger v2 configuration](https://www.jaegertracing.io/docs/2.22/getting-started/), with unnecessary receivers and extensions omitted.

The owned Compose trace journey compares exported `service.instance.id` with authenticated API service information and private worker status. Those endpoints own process identity even when export is disabled. Compact API/UI timing handlers do not probe dependencies; worker status retains its existing readiness behavior.
