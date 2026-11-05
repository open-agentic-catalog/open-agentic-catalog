# Private item feedback

This package owns human submissions, private submitter/admin reads, the current administrator response and revision-fenced lifecycle changes. Feedback never changes published evidence, discovery results or resource revisions.

Mutations use the retained update timestamp as a lower bound through `catalog.TransitionTime`, so replica clock skew cannot move state or its audit evidence backward. Revision checks remain the concurrency boundary; neither clocks nor previously stored timestamps are changed.

| File | Responsibility |
| --- | --- |
| `types.go` | Records, closed inputs and persistence/context ports. |
| `service.go` | Membership ownership, exact local context, transitions, text bounds and process-local admission. |
| `store.go` | In-memory persistence and shared state/audit validation. |
| `overview.go` | Bounded open-category counts and the oldest original submission time, excluding deleted Resources. |
| `service_test.go` | Service-free privacy, lifecycle, Unicode, failure and concurrency tests. |
| `service_failures_test.go` | Required dependencies, context/ID failures, admission release and rejected-command atomicity. |
| `store_test.go` | Creation/transition guards, terminal erasure, audit binding, cancellation and caller-bound pagination. |

PostgreSQL implements the same store in `internal/platform/database/postgres_feedback.go`; its integration-tagged tests use disposable PostgreSQL. HTTP contracts live in `internal/platform/httpserver/feedback_functional_test.go`. Run `go test -race ./internal/feedback` for this package and `make test-integration` for the actual database boundary. The [feedback guide](../../documentation/src/content/docs/guides/feedback.mdx) describes user/admin behavior and limits.
