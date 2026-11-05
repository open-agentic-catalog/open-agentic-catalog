# Receiving Catalog webhooks

For a runnable consumer, use the [Go publication report](../publication-report/README.md). It combines an unchanged copy of this verifier with PostgreSQL acceptance and Go SDK reads. This directory remains the standard-library-only helper and handler outline.

Your application supplies an HTTPS URL, and a tenant administrator registers it with the Catalog. HTTPS protects transport. The shared-secret signature authenticates the exact notification bytes. [CloudEvents](https://cloudevents.io/) defines the JSON envelope.

Registration selects 1-64 public event selectors. Exact lowercase short aliases such as `resource.created` normalize to versioned types such as `com.open.agentic.catalog.resource.created.v1`; duplicates are removed after the input bound is checked. `*` includes all supported public types, including future public types. Combining it with specific selectors does not narrow coverage. Private audit, internal events and tenant settings changes are not public notifications. Endpoint registration and signing rotation do not contact the receiver; an explicit test makes a real API-side send, and normal notifications need a running worker.

Obtain a source checkout of this repository and copy `examples/webhookreceiver/verify.go` into your receiver package, or use it as a reference. Preserve the [Apache License 2.0](../../LICENSE) and any applicable notices with redistributed source. The manual does not distribute the Go source as a separate download. It uses only the Go standard library. Configure the exact one-time `whsec_` secret and `secretVersion` returned by registration. Keep the secret out of source code, logs and query strings. Terminate HTTPS at your application or trusted ingress and set bounded server timeouts, header limits, concurrency, and request size. Verification accepts at most five minutes of age and 30 seconds of future clock skew; keep receiver and Catalog clocks synchronized.

The essential HTTP handler flow is:

```go
func receive(w http.ResponseWriter, r *http.Request) {
    if r.Method != http.MethodPost {
        w.Header().Set("Allow", http.MethodPost)
        http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
        return
    }
    r.Body = http.MaxBytesReader(w, r.Body, webhookreceiver.MaxBodyBytes)
    body, err := io.ReadAll(r.Body)
    if err != nil {
        var tooLarge *http.MaxBytesError
        if errors.As(err, &tooLarge) {
            http.Error(w, "body too large", http.StatusRequestEntityTooLarge)
        } else {
            http.Error(w, "invalid body", http.StatusBadRequest)
        }
        return
    }
    if err := webhookreceiver.Verify(secret, secretVersion, r.Header, body, time.Now()); err != nil {
        http.Error(w, "invalid signature", http.StatusUnauthorized)
        return
    }
    if err := validateEvent(body); err != nil {
        http.Error(w, "invalid event", http.StatusBadRequest)
        return
    }
    if err := acceptEvent(r.Context(), body); err != nil {
        http.Error(w, "temporarily unavailable", http.StatusServiceUnavailable)
        return
    }
    w.WriteHeader(http.StatusNoContent)
}
```

The outline needs the standard-library `errors`, `io`, `net/http` and `time` imports, the copied verifier package, and your configured `secret` and `secretVersion`. Two application functions are deliberately left for your integration:

- `validateEvent(body []byte) error` parses duplicate-key-safe JSON and checks the [event schema](../../api/schemas/catalog-event-v1.schema.json), expected tenant-bound `source`, allowed event types, event ID and `subject == data.targetId`. The signature verifier does not parse JSON. Use the signed envelope as event authority. The convenience `Ce-*` and `X-Catalog-*` identity headers are outside the hash-based message authentication code (HMAC).
- `acceptEvent(ctx context.Context, body []byte) error` atomically deduplicates by `(source, id)` and durably accepts the work, returning nil for an already accepted duplicate. It must return an error when acceptance cannot commit; only success receives a 2xx acknowledgement.

Do not replace durable acceptance with an in-memory deduplication map or a fire-and-forget goroutine. Fetch current authorized Catalog state when processing depends on the latest revision. A deletion or withdrawal may make the previous resource unavailable. Process duplicate, delayed, and out-of-order notifications safely, and reconcile after missed or exhausted delivery rather than reconstructing authority from arrival order. Receiving a notification does not grant Catalog API access or permission to execute a resource.

Verify the exact delivered bytes before decoding or reformatting them. `X-Catalog-Timestamp` describes the signed send attempt; the CloudEvent `time` describes the original event and may be much older during retries. Apply the verifier's freshness window to the signature timestamp, not the event time. Expected payload media type is `application/cloudevents+json`.

Routine secret rotation supplies the new secret once, keeps the predecessor signature in `X-Catalog-Previous-Signature` for 15 minutes, and returns `previousSecretExpiresAt`. The main `X-Catalog-Signature` header always uses the current version. This verifier works with either receiver-configured version during overlap. Update the receiver before expiry; another routine rotation during overlap is rejected. Emergency `revokePrevious: true` rotation omits overlap; immediately replace the receiver's accepted key as well. Already in-flight requests cannot be recalled, and continuing to accept a compromised secret at the receiver defeats revocation.

The UI can retain the returned signing version and known overlap deadline after you clear plaintext, but only within the same page and access context. Endpoint reads cannot recover the key or deadline. If a command result is uncertain, retain its original key/input for exact-command recovery. If a known-success key was discarded and recovery is unavailable, inspect current endpoint state before deliberately rotating again with a fresh ETag. Wait for an active routine overlap to expire, or explicitly choose emergency rotation; neither the UI nor the Catalog installs that key in your receiver. Deployment encryption-key replacement remains separate operator maintenance.

See the [operations runbook](../../documentation/src/content/docs/operations/webhooks.mdx) for enablement, retries, reconciliation, and the separate deployment encryption-key boundary. Runtime and Helm default webhooks off; the local `make compose-up` launcher enables them unless `LOCAL_WEBHOOKS=off` or an explicit runtime override selects otherwise.

The Catalog preserves valid writes at notification saturation and records missed notifications. `GET /api/v1/webhooks/reconciliation` requires current tenant-wide `webhook.read_evidence` and `webhooks.manage` when OAuth scopes are enforced. A permitted custom role can delegate this read without Tenant Administrator authority. Persist `capacityLossCount` and `deliveryLossCount` checkpoints. Refresh the receiver's current authorized Catalog view when either counter increases or moves backward after a restore. Also refresh after a disable/re-enable interval.

Periodic reconciliation covers gaps that counters cannot identify. Reading loss counters does not grant permission to read the resources that your integration needs to refresh.

Counts do not reset after reading or delivery cleanup, and there is no missed-event replay API. Keep this separate from event deduplication: deduplication prevents processing the same accepted event twice, while reconciliation repairs missed or exhausted delivery. See the runbook for checkpoint and tenant-isolation details.

## Connect and operate a receiver

Follow the [setup and recovery procedure](../../documentation/src/content/docs/operations/webhooks.mdx#setup-and-recovery) for one complete tenant workflow. It covers exact API/worker configuration, private-origin policy and CA paths, minimal grants and OAuth ceilings, registration, protected signer installation, a synchronous test, normal worker delivery, rotation, permanent disable/replacement and loss/restore reconciliation. Resource reads need separate authorization; receiving an event supplies none.

Implement `validateEvent` and `acceptEvent` before exposing the receiver. Verify the signed bytes first, validate the expected tenant/event shape, then commit deduplication and queued work atomically. A duplicate already durably accepted should return 2xx without scheduling the work again. A failed acceptance transaction should return a non-2xx response. Process accepted work independently, with retry/reconciliation appropriate to your application. Do not claim exactly-once business execution from a successful Catalog send.

For a local executable exercise, run the existing packaged HTTPS fixture:

```sh
make test-functional-webhooks CATALOG_ENV_FILE=/dev/null
```

Install its [documented prerequisites](../../test/functional/README.md) first. The invocation owns its PostgreSQL, HTTPS receiver, private trust, signer inputs, network and cleanup. The journey registers an endpoint, installs its key through a private fixture control, verifies normal signed delivery, tests rotations and admission, and exercises retry, loss, reconciliation and restart. The control routes are test tooling, not Catalog API or production receiver endpoints. They must stay in the owned fixture.

The fixture's in-memory ledger is intentionally bounded and volatile. Passing it qualifies the sender/verifier boundary and the tested recovery cases; it does not implement your receiver's durable acceptance store. No hosted account or external deployment is required. Do not launch the fixture against retained data.

## Verify changes

Run `go test ./examples/webhookreceiver ./internal/webhook -shuffle=on -count=1`. Example tests cover malformed input, exact bytes and freshness bounds; the sender's rotation test also verifies actual generated deliveries with this helper, including overlap expiry and emergency rotation. Neither test starts an external receiver or database.
