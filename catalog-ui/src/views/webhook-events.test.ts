import { readFileSync } from "node:fs";
import { expect, it } from "vitest";
import {
  commonWebhookChoices,
  moreWebhookChoices,
  normalizeWebhookEvents,
  toggleWebhookEvent,
  webhookChoices,
  webhookEventSelected,
} from "./webhook-events";

const schema = JSON.parse(
  readFileSync(
    new URL(
      "../../../api/schemas/catalog-event-v1.schema.json",
      import.meta.url,
    ),
    "utf8",
  ),
) as {
  properties: {
    type: { enum: string[] };
    data: { properties: { action: { enum: string[] } } };
  };
  oneOf: {
    properties: {
      type: { const: string };
      data: { properties: { action: { const: string } } };
    };
  }[];
};

it("labels every service-owned public type exactly once, with exhaustive generated-contract types", () => {
  expect(webhookChoices.map(([type]) => type).sort()).toEqual(
    ["*", ...schema.properties.type.enum].sort(),
  );
  expect([...commonWebhookChoices, ...moreWebhookChoices]).toEqual(
    webhookChoices,
  );
  expect(new Set(webhookChoices.map(([, label]) => label)).size).toBe(
    webhookChoices.length,
  );
  for (const [type, label] of webhookChoices) {
    expect(label.trim()).not.toBe("");
    expect(normalizeWebhookEvents(type)).toEqual([type]);
  }
  for (const binding of schema.oneOf)
    expect(
      normalizeWebhookEvents(binding.properties.data.properties.action.const),
    ).toEqual([binding.properties.type.const]);
});

it("normalizes supported aliases and duplicates without narrowing an explicit wildcard", () => {
  expect(
    normalizeWebhookEvents(
      " resource.created,com.open.agentic.catalog.resource.created.v1\n*, source.paused ",
    ),
  ).toEqual([
    "*",
    "com.open.agentic.catalog.resource.created.v1",
    "com.open.agentic.catalog.source.paused.v1",
  ]);
  expect(
    normalizeWebhookEvents(Array(64).fill("resource.created").join("\n")),
  ).toEqual(["com.open.agentic.catalog.resource.created.v1"]);
});

it.each(["", " ,\n ", Array(65).fill("resource.created").join("\n")])(
  "enforces the 1-64 raw selector bound before deduplication",
  (input) => {
    expect(() => normalizeWebhookEvents(input)).toThrow(
      "Choose between 1 and 64 event selectors.",
    );
  },
);

it.each([
  "RESOURCE.CREATED",
  "tenant.settings.updated",
  "internal.indexing.requested",
  "com.open.agentic.catalog.unknown.v1",
  "<script>private()</script>",
])(
  "rejects unregistered or wrongly cased selectors without echoing %s",
  (input) => {
    expect(() => normalizeWebhookEvents(input)).toThrow(
      "Unknown event selector. Use a listed public type, its exact lowercase short alias, or *.",
    );
    expect(webhookEventSelected(input, "*")).toBe(false);
  },
);

it("synchronizes alias checkboxes while retaining unknown text for correction", () => {
  const resource = "com.open.agentic.catalog.resource.created.v1";
  expect(
    webhookEventSelected(" resource.created, source.paused", resource),
  ).toBe(true);
  expect(
    toggleWebhookEvent(
      "resource.created, unknown\nresource.created",
      resource,
      true,
    ),
  ).toBe("resource.created\nunknown");
  expect(
    toggleWebhookEvent(
      `resource.created\n${resource}\nunknown`,
      resource,
      false,
    ),
  ).toBe("unknown");
  expect(toggleWebhookEvent("source.paused\nunknown", resource, true)).toBe(
    `source.paused\nunknown\n${resource}`,
  );
  expect(toggleWebhookEvent("*,resource.created", "*", false)).toBe(
    "resource.created",
  );
});
