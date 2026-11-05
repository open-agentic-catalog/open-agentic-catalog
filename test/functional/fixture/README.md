# Functional fixture service

This test-only Go command generates invocation-owned trust/configuration inputs and serves controlled HTTPS origins for the packaged functional tests. Files separate federation modes and peer support, credential checks, search fixtures, webhook capture and OTLP capture.

The supervisor owns the private network, selected endpoints, credentials and cleanup. Generated inputs are disposable and must not become production examples. Test controls deliberately simulate failures and are not product API routes.

Generation sets mounted directories to `0755` and input files to read-only `0444`, including when the invoking process uses `umask 077`. Non-root test containers need these explicit permissions. The supervisor's enclosing temporary directory stays private (`0700`); never generate fixture credentials under a shared parent. Isolated filesystem tests preserve exact bytes and parent permissions. A separate Unix functional test generates complete inputs in subprocesses with both `022` and `077` masks, without changing the parent test process's mask. It requires the complete generated file inventory, so permission checks cannot pass when a required input is missing.

Run `go test ./test/functional/fixture` for in-process fixture tests. Run `go test -race -tags=functional ./test/functional/fixture` for local HTTP/TLS and subprocess controls, without PostgreSQL or Compose. The [functional harness](../README.md) documents Compose profiles and exact cleanup ownership; do not launch the fixture against shared data.

The Registry List control accepts `throttled` and `unavailable` modes for synthetic HTTP 429/503 replies with `Retry-After: 2`. Both increment the existing mode counters. Normal, duplicate-page `partial`, replacement and denied behavior remain separate controls.

The `denied` List mode returns HTTP 403 with `Retry-After: 120` to verify that timing guidance cannot make a terminal failure retryable. The operator journey checks first-attempt failure and explicit child-Job recovery; recorder units verify the exact fixture response.

The webhook crash control selects exactly one configured endpoint/source/event with `POST /webhooks/barrier` (`arm`/`release`). `GET /webhooks/barrier` exposes only the bounded selection and at most eight verified attempt digests, duplicate flags and outcomes. The selected response waits for explicit release, disconnection or a 30-second expiry; expiration returns an error. Other requests retain their normal receiver behavior.

The control cannot replace an existing selection, and release is idempotent after sender loss. Once explicitly released, later redeliveries bypass the original barrier deadline while earlier disconnected/expired evidence remains unchanged. These routes are test-only, inside the invocation-owned private network and loopback listener. Recorder/synctest units cover rejection, expiry and cleanup; functional tests exercise separate real HTTP control/send connections.

The explicit `serve --publisher-dns` option adds owned UDP/TCP DNS on port 53. It never forwards queries. The HTTPS `POST /publisher-dns` control sets or removes one exact `.test` challenge value. `GET /publisher-dns` returns the verification TXT query count without exposing values.

The fixture holds at most eight names with one 255-byte value each. Unknown questions receive NXDOMAIN. The ordinary fixture mode exposes neither these controls nor DNS.

The packaged UI runner supplies this resolver only to its owned API container through Docker DNS configuration. It keeps normal service discovery and production resolver code. Successful DNS, propagation failure, replay without DNS, policy replacement and restart persistence use the actual packaged API, worker, UI and PostgreSQL. Other DNS outcomes and TCP fallback retain their focused real-DNS tests. Fixture codec/control units use recorders and byte arrays without listeners.
