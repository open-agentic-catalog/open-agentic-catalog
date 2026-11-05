import { expect, it } from "vitest";
import {
  webhookParameter,
  webhookDeliveryFilters,
  webhookDirectoryURL,
  webhookDeliveryURL,
} from "./webhook-filter-state";
it("preserves exact selections and refuses repeated or empty URL criteria", () => {
  expect(webhookParameter("", "status")).toBe("");
  expect(webhookParameter("?status=active", "status")).toBe("active");
  expect(webhookParameter("?status=", "status")).toBe("unsupported");
  expect(webhookParameter("?status=active&status=active", "status")).toBe(
    "unsupported",
  );
  expect(
    webhookDeliveryFilters("?deliveryId=one&eventId=two&state=waiting"),
  ).toEqual({ deliveryId: "one", eventId: "two", state: "waiting" });
  expect(webhookDirectoryURL("/base", "active")).toBe(
    "/base/webhooks?status=active",
  );
  expect(webhookDirectoryURL("/base", "")).toBe("/base/webhooks");
  expect(
    webhookDeliveryURL("/base", "endpoint", {
      deliveryId: "one",
      eventId: "two",
      state: "waiting",
    }),
  ).toBe("/base/webhooks/endpoint?deliveryId=one&eventId=two&state=waiting");
  expect(
    webhookDeliveryURL("/base", "endpoint", {
      deliveryId: "",
      eventId: "",
      state: "",
    }),
  ).toBe("/base/webhooks/endpoint");
});
