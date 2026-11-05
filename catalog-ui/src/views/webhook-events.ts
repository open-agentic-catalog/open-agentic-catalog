import type { components } from "../api/schema";

type EventType = components["schemas"]["WebhookEventType"];

// Exhaustive against the generated contract; the unit parity check also reads
// the service-owned event schema, whose registry/OpenAPI parity has Go tests.
const labels: Record<EventType, string> = {
  "*": "All events",
  "com.open.agentic.catalog.resource.created.v1": "Resource created",
  "com.open.agentic.catalog.catalog.listing.created.v1": "Listing created",
  "com.open.agentic.catalog.catalog.listing.revised.v1": "Listing updated",
  "com.open.agentic.catalog.listing.release.published.v1": "Release published",
  "com.open.agentic.catalog.publication.activated.v1":
    "Discovery snapshot activated",
  "com.open.agentic.catalog.source.sync.completed.v1": "Source sync completed",
  "com.open.agentic.catalog.source.sync.failed.v1": "Source sync failed",
  "com.open.agentic.catalog.identity.membership.offboarded.v1":
    "Member offboarded",
  "com.open.agentic.catalog.identity.membership.provisioned.v1":
    "Member provisioned",
  "com.open.agentic.catalog.identity.membership.reactivated.v1":
    "Member reactivated",
  "com.open.agentic.catalog.identity.membership.suspended.v1":
    "Member suspended",
  "com.open.agentic.catalog.identity.realm.reactivated.v1":
    "Identity realm reactivated",
  "com.open.agentic.catalog.identity.realm.suspended.v1":
    "Identity realm suspended",
  "com.open.agentic.catalog.identity.role.assignment.granted.v1":
    "Role assignment granted",
  "com.open.agentic.catalog.identity.role.assignment.revoked.v1":
    "Role assignment revoked",
  "com.open.agentic.catalog.listing.release.deprecated.v1":
    "Release deprecated",
  "com.open.agentic.catalog.listing.release.retired.v1": "Release retired",
  "com.open.agentic.catalog.listing.release.revoked.v1": "Release revoked",
  "com.open.agentic.catalog.listing.release.superseded.v1":
    "Release superseded",
  "com.open.agentic.catalog.namespace.created.v1": "Namespace created",
  "com.open.agentic.catalog.namespace.updated.v1": "Namespace updated",
  "com.open.agentic.catalog.observation.adopted.v1":
    "Source observation adopted",
  "com.open.agentic.catalog.publication.unpublished.v1":
    "Discovery snapshot unpublished",
  "com.open.agentic.catalog.publisher.created.v1": "Publisher created",
  "com.open.agentic.catalog.publisher.publication.domain.approved.v1":
    "Publisher domain approved",
  "com.open.agentic.catalog.publisher.publication.domain.revoked.v1":
    "Publisher domain revoked",
  "com.open.agentic.catalog.publisher.updated.v1": "Publisher updated",
  "com.open.agentic.catalog.resource.deleted.v1": "Resource deleted",
  "com.open.agentic.catalog.resource.version.created.v1":
    "Resource version created",
  "com.open.agentic.catalog.resource.version.revised.v1":
    "Resource version revised",
  "com.open.agentic.catalog.review.decision.recorded.v1":
    "Review decision recorded",
  "com.open.agentic.catalog.review.submission.created.v1": "Review submitted",
  "com.open.agentic.catalog.source.connected.v1": "Source connected",
  "com.open.agentic.catalog.source.paused.v1": "Source paused",
  "com.open.agentic.catalog.source.removed.v1": "Source removed",
  "com.open.agentic.catalog.source.resumed.v1": "Source resumed",
  "com.open.agentic.catalog.source.revision.activated.v1":
    "Source revision activated",
  "com.open.agentic.catalog.version.revision.validated.v1":
    "Version revision validated",
  "com.open.agentic.catalog.webhook.test.v1": "Synthetic webhook test",
};

export const webhookChoices = Object.entries(labels) as [EventType, string][];
export const commonWebhookChoices = webhookChoices.slice(0, 8);
export const moreWebhookChoices = webhookChoices.slice(8);

const selectors = new Map<string, EventType>();
for (const [type] of webhookChoices) {
  selectors.set(type, type);
  if (type !== "*")
    selectors.set(
      type.slice("com.open.agentic.catalog.".length, -".v1".length),
      type,
    );
}

/** Keep the service's raw 1-64 input bound before alias normalization/deduplication. */
export function normalizeWebhookEvents(input: string): EventType[] {
  const tokens = input
    .split(/[\n,]/)
    .map((v) => v.trim())
    .filter(Boolean);
  if (!tokens.length || tokens.length > 64)
    throw new Error("Choose between 1 and 64 event selectors.");
  const selected = new Set<EventType>();
  for (const token of tokens) {
    const canonical = selectors.get(token);
    if (!canonical)
      throw new Error(
        "Unknown event selector. Use a listed public type, its exact lowercase short alias, or *.",
      );
    selected.add(canonical);
  }
  return [...selected].sort();
}

/** Invalid advanced text remains editable; only recognized exact choices are checked. */
export function webhookEventSelected(input: string, type: EventType): boolean {
  return input.split(/[\n,]/).some((v) => selectors.get(v.trim()) === type);
}

/** Toggling a known choice retains other explicit selectors, including unknown input. */
export function toggleWebhookEvent(
  input: string,
  type: EventType,
  checked: boolean,
) {
  const tokens = [
    ...new Set(
      input
        .split(/[\n,]/)
        .map((v) => v.trim())
        .filter(Boolean),
    ),
  ];
  if (checked) {
    if (!tokens.some((token) => selectors.get(token) === type))
      tokens.push(type);
    return tokens.join("\n");
  }
  return tokens.filter((token) => selectors.get(token) !== type).join("\n");
}
