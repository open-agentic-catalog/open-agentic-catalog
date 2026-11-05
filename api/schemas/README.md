# Webhook event schema

[catalog-event-v1.schema.json](catalog-event-v1.schema.json) is the JSON Schema 2020-12 contract for the closed CloudEvents envelope delivered by optional webhooks. It defines public event types, their action/target combinations, and the host required on publication notifications. Payloads identify changed objects; they do not contain full resources or audit records.

Control request and response schemas live in [OpenAPI](../openapi/control-v1.yaml). Native resource validation schemas and pinned upstream specifications live in the repository's [standards bundle](../../schemas/README.md), not here.

The event `dataschema` is the stable URN in this schema's `$id`, not a downloadable HTTP endpoint. Consumers can use this checked-in schema, with format assertions enabled, without remote schema retrieval. Cross-field equality such as `subject == data.targetId` is additionally enforced by [the event validator](../../internal/events/events.go); schema validation alone does not verify a webhook signature, delivery freshness or authorization to read its target.

Keep changes aligned with the Go event registry, OpenAPI webhook selectors and [delivery examples](../README.md#reviewed-examples). Run `make api-check` and `make api-contract-check` for registry parity, all registered public-event projections, example validation and invalid-envelope cases. See [webhook operations](../../documentation/src/content/docs/operations/webhooks.mdx) for receiver verification and key management.
