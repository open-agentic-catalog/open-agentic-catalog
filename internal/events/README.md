# Public notification contract

[events.go](events.go) defines the public event vocabulary, bounded notification references, CloudEvents envelopes and schema validation against the embedded [event schema](../../api/schemas/). Domain transitions commit their outbox records atomically; webhook delivery projects those records into this contract.

Event payloads carry references and reviewed facts, not private documents or credentials. A public event classification is separate from internal outbox work. Keep the vocabulary, embedded schema and positive/negative tests aligned when changing a notification.

Run `go test ./internal/events -shuffle=on -count=1`. Delivery and receiver behavior are documented under [webhook](../webhook/README.md).
