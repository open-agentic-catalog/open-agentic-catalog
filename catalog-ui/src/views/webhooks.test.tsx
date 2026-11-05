import { required } from "../../tests/required";
// @vitest-environment jsdom
import { capabilities } from "../../tests/unit/responses";
import { afterEach, expect, it, vi } from "vitest";
import {
  act,
  fireEvent,
  render,
  screen,
  waitFor,
  within,
} from "@testing-library/react";
import Webhooks from "./Webhooks";
import { APIError } from "../api/client";
import { deferred } from "../../tests/unit/fixtures";
import {
  administrator,
  change,
  click,
  confirmCommand,
  etagReply,
  readReplies,
  screenLifecycle,
  screenWorkspace,
  stamp,
} from "../../tests/unit/screens";
screenLifecycle("/unit/webhooks");
afterEach(() => vi.unstubAllGlobals());
const endpoint = {
  id: "hook-1",
  name: "Compliance",
  url: "https://example.com/hooks",
  eventTypes: ["*"],
  status: "active",
  revision: 1,
  secretVersion: 1,
  createdAt: stamp,
  updatedAt: stamp,
};
const secret = {
  endpoint,
  secret: "test-only-not-a-real-signing-secret",
};
const overlapDeadline = "2026-10-19T20:15:00Z";
const delivery = {
  endpointId: endpoint.id,
  eventId: "event-1",
  updatedAt: stamp,
  id: "delivery-1",
  eventType: "example",
  state: "succeeded",
  attempt: 1,
};
function routes() {
  return {
    "/api/v1/capabilities": {
      ...capabilities,
      optionalCapabilities: { webhooks: "enabled" },
    },
    "/api/v1/webhooks": { items: [endpoint], nextCursor: "next" },
    "/api/v1/webhooks/{webhookId}": endpoint,
    "/api/v1/webhooks/{webhookId}/deliveries": {
      items: [],
      nextCursor: "next",
    },
    "/api/v1/webhooks/reconciliation": {
      pendingEvents: 2,
      pendingEventLimit: 1000,
      capacityLossCount: 3,
      deliveryLossCount: 4,
    },
  };
}

it("lists endpoints, pages them and refreshes reconciliation independently", async () => {
  const { props, get } = screenWorkspace();
  readReplies(get, routes());
  // Settle capability discovery and the resulting endpoint/counter reads.
  await act(async () => {
    render(<Webhooks {...props} access={administrator} />);
  });
  await screen.findByRole("link", { name: "Compliance" });
  expect(
    screen.getByText("Events awaiting delivery").nextElementSibling
      ?.textContent,
  ).toBe("2");
  expect(
    screen.getByText("Notifications suppressed by capacity limits")
      .nextElementSibling?.textContent,
  ).toBe("3");
  expect(
    screen.getByText("Final delivery failures").nextElementSibling?.textContent,
  ).toBe("4");
  const nextPage = deferred<unknown>();
  get.mockReturnValueOnce(nextPage.promise);
  click("Next page");
  await waitFor(() =>
    expect(get.mock.calls.at(-1)?.[1]).toMatchObject({
      params: { query: { cursor: "next" } },
    }),
  );
  // Request observation does not mean its response has rendered the pager.
  expect(screen.getByRole("button", { name: "First page" })).toHaveProperty(
    "disabled",
    true,
  );
  await act(async () => {
    nextPage.resolve(etagReply(routes()["/api/v1/webhooks"]));
  });
  fireEvent.click(await screen.findByRole("button", { name: "First page" }));
  await screen.findByRole("button", { name: "Next page" });
  click("Refresh counters");
  await waitFor(() =>
    expect(get.mock.calls.at(-1)?.[0]).toBe("/api/v1/webhooks/reconciliation"),
  );
  readReplies(get, { ...routes(), "/api/v1/webhooks": { items: [] } });
  click("Refresh");
  await screen.findByText("No webhooks yet");
});

it("rejects malformed reconciliation counts and recovers through a manual refresh", async () => {
  const { props, get, post } = screenWorkspace();
  readReplies(get, {
    ...routes(),
    "/api/v1/webhooks/reconciliation": {
      ...routes()["/api/v1/webhooks/reconciliation"],
      pendingEvents: "2",
    },
  });
  render(<Webhooks {...props} access={administrator} />);
  await screen.findByRole("alert");
  expect(screen.queryByText("Events awaiting delivery")).toBeNull();
  readReplies(get, {
    ...routes(),
    "/api/v1/webhooks/reconciliation": {
      ...routes()["/api/v1/webhooks/reconciliation"],
      pendingEvents: 1000,
      lastCapacityLossAt: stamp,
      lastDeliveryLossAt: stamp,
    },
  });
  click("Refresh counters");
  await screen.findByText("Events awaiting delivery");
  expect(screen.queryByRole("alert")).toBeNull();
  expect(
    screen.getByText("Events awaiting delivery").nextElementSibling
      ?.textContent,
  ).toBe("At least 1000");
  expect(
    screen.getByText("Maximum count shown").nextElementSibling?.textContent,
  ).toBe("1000");
  expect(
    screen.getByText("Last delivery loss").nextElementSibling?.textContent,
  ).toBe(new Date(stamp).toLocaleString());
  expect(
    screen.getByText("Last capacity loss").nextElementSibling?.textContent,
  ).toBe(new Date(stamp).toLocaleString());
  expect(post).not.toHaveBeenCalled();
});

it("does not read endpoints without administrator permission", async () => {
  const { props, get } = screenWorkspace();
  readReplies(get, routes());
  render(<Webhooks {...props} />);
  await screen.findByRole("alert");
  expect(get).toHaveBeenCalledTimes(1);
});

it.each([
  ["disabled", "Webhooks are turned off. Ask your operator to enable them."],
  [
    "unavailable",
    "Webhooks are unavailable in this deployment. Ask your operator to check the configuration.",
  ],
] as const)(
  "keeps a %s deployment free of endpoint reads, secrets and commands",
  async (state, message) => {
    const { props, get, post, remove } = screenWorkspace();
    readReplies(get, {
      "/api/v1/capabilities": {
        ...capabilities,
        optionalCapabilities: { webhooks: state },
      },
    });
    render(<Webhooks {...props} id="new" access={administrator} />);
    await screen.findByText(message);
    expect(screen.queryByLabelText("Receiver URL")).toBeNull();
    expect(screen.queryByLabelText("Signing secret")).toBeNull();
    expect(get.mock.calls.map(([path]) => path)).toEqual([
      "/api/v1/capabilities",
    ]);
    expect(post).not.toHaveBeenCalled();
    expect(remove).not.toHaveBeenCalled();
  },
);

it("validates receiver/subscriptions then creates a webhook and clears its one-time secret", async () => {
  const { props, get, post } = screenWorkspace();
  readReplies(get, routes());
  post.mockResolvedValue(etagReply(secret));
  render(<Webhooks {...props} id="new" access={administrator} />);
  await screen.findByLabelText("Webhook name");
  change("Webhook name", " Compliance ");
  change("Receiver URL", "http://example.com/hooks");
  click("Create webhook");
  await screen.findByText(
    "Use a credential-free HTTPS endpoint without a fragment.",
  );
  expect(post).not.toHaveBeenCalled();
  change("Receiver URL", endpoint.url);
  click("Create webhook");
  await screen.findByText("Choose between 1 and 64 event selectors.");
  fireEvent.click(screen.getByLabelText("All events"));
  fireEvent.click(screen.getByLabelText("All events"));
  change("Event type codes", "*\n*,");
  await confirmCommand("Create webhook");
  await screen.findByLabelText("Signing secret");
  expect(
    (screen.getByLabelText("Signing secret") as HTMLInputElement).type,
  ).toBe("password");
  click("Reveal secret");
  expect(
    (screen.getByLabelText("Signing secret") as HTMLInputElement).type,
  ).toBe("text");
  click("Hide secret");
  click("Copy secret");
  await screen.findByText(/Clipboard access failed/);
  click("Clear secret from this page");
  expect(screen.queryByLabelText("Signing secret")).toBeNull();
  expect(
    screen.getByRole("link", { name: "Open webhook" }).getAttribute("href"),
  ).toBe("/unit/webhooks/hook-1");
  expect(post.mock.calls[0]).toMatchObject([
    "/api/v1/webhooks",
    { body: { name: "Compliance", url: endpoint.url, eventTypes: ["*"] } },
  ]);
});

it("updates endpoint metadata and pages retained deliveries", async () => {
  const { props, get, patch } = screenWorkspace();
  readReplies(get, {
    ...routes(),
    "/api/v1/webhooks/{webhookId}/deliveries": {
      items: [delivery],
      nextCursor: "next",
    },
  });
  patch.mockResolvedValue(etagReply(endpoint));
  render(<Webhooks {...props} id={endpoint.id} access={administrator} />);
  await screen.findByLabelText("Webhook name");
  change("Webhook name", "Updated receiver");
  await confirmCommand("Update webhook");
  await screen.findByText(/Webhook updated\./);
  expect(patch.mock.calls[0]).toMatchObject([
    "/api/v1/webhooks/{webhookId}",
    {
      params: {
        path: { webhookId: endpoint.id },
        header: { "If-Match": '"revision-1"' },
      },
      body: { name: "Updated receiver" },
    },
  ]);
  get.mockResolvedValueOnce(etagReply({ items: [] }));
  click("Next page");
  await screen.findByText("No deliveries on this page.");
  expect(get.mock.calls.at(-1)?.[1]).toMatchObject({
    params: { query: { cursor: "next" } },
  });
  click("Refresh deliveries from first page");
  await screen.findByRole("heading", {
    name: "example: Succeeded",
  });
});

it.each(["succeeded", "dead_letter"])(
  "reports a synthetic receiver test that %s",
  async (state) => {
    const { props, get, post } = screenWorkspace();
    readReplies(get, routes());
    post.mockResolvedValue(etagReply({ ...delivery, state }));
    render(<Webhooks {...props} id={endpoint.id} access={administrator} />);
    await screen.findByLabelText("Webhook action");
    await confirmCommand("Manage webhook");
    await screen.findByText(
      state === "succeeded"
        ? /Test notification delivered\./
        : /Test notification was not delivered/,
    );
    expect(post.mock.calls[0]).toMatchObject([
      "/api/v1/webhooks/{webhookId}:test",
      { body: {} },
    ]);
  },
);

it.each(["routine", "emergency"])(
  "rotates a secret in %s mode and handles clipboard outcomes",
  async (mode) => {
    const { props, get, post } = screenWorkspace();
    readReplies(get, routes());
    post.mockResolvedValue(
      etagReply({
        ...secret,
        ...(mode === "routine" ? { previousSecretExpiresAt: stamp } : {}),
      }),
    );
    const writeText = vi
      .fn()
      .mockRejectedValueOnce(new Error("Clipboard denied"))
      .mockResolvedValueOnce(undefined);
    const original = Object.getOwnPropertyDescriptor(navigator, "clipboard");
    Object.defineProperty(navigator, "clipboard", {
      configurable: true,
      value: { writeText },
    });
    try {
      render(<Webhooks {...props} id={endpoint.id} access={administrator} />);
      await screen.findByLabelText("Webhook action");
      change("Webhook action", "rotate");
      change("Rotation mode", mode);
      const guidance =
        mode === "routine"
          ? /Catalog signs with both secrets for 15 minutes/
          : /Replace the keys accepted by your receiver immediately/;
      expect(screen.getByText(guidance)).toBeTruthy();
      click("Manage webhook");
      expect(
        within(await screen.findByRole("dialog")).getByText(guidance),
      ).toBeTruthy();
      expect(post).not.toHaveBeenCalled();
      click("Confirm");
      await screen.findByLabelText("Signing secret");
      if (mode === "routine") {
        expect(
          screen.getByText(
            /Catalog includes a signature made with the previous secret until/,
          ).textContent,
        ).toContain(new Date(stamp).toLocaleString());
        expect(
          screen.getByText(
            /Install the new secret in your receiver before then/,
          ),
        ).toBeTruthy();
      } else {
        expect(
          screen.queryByText(/signature made with the previous secret until/),
        ).toBeNull();
      }
      click("Copy secret");
      await screen.findByText(/Clipboard access failed/);
      click("Copy secret");
      await screen.findByText(/Copied\. Clear your clipboard/);
      expect(writeText).toHaveBeenLastCalledWith(secret.secret);
      writeText.mockRejectedValueOnce(new Error("Clipboard revoked"));
      click("Copy secret");
      await screen.findByText(/Clipboard access failed/);
      expect(screen.queryByText(/Copied\. Clear your clipboard/)).toBeNull();
      expect(post.mock.calls[0]).toMatchObject([
        "/api/v1/webhooks/{webhookId}:rotate-secret",
        { body: { revokePrevious: mode === "emergency" } },
      ]);
      click("Clear secret from this page");
      expect(screen.queryByLabelText("Signing secret")).toBeNull();
      const retained = screen.getByRole("region", {
        name: "Signing-key rotation guidance",
      });
      expect(retained.textContent).toContain(
        `Observed ${mode} rotation for endpoint ${endpoint.id}`,
      );
      if (mode === "routine")
        expect(retained.textContent).toContain(
          new Date(stamp).toLocaleString(),
        );
      else
        expect(retained.textContent).toContain(
          "stops adding previous-key signatures",
        );
    } finally {
      if (original) Object.defineProperty(navigator, "clipboard", original);
      else Reflect.deleteProperty(navigator, "clipboard");
    }
  },
);

it.each([
  [
    "/docs/",
    ["/docs/operations/webhooks/", "/docs/reference/receiver-example/"],
  ],
  [
    "https://manual.example.test/exact/start/#",
    ["https://manual.example.test/exact/start/#"],
  ],
  ["", []],
] as const)(
  "uses the configured manual destination %s without private configuration reads",
  async (documentationUrl, expected) => {
    const w = screenWorkspace();
    readReplies(w.get, routes());
    // Mounting a child heading does not settle its capability-triggered reads.
    await act(async () => {
      render(
        <Webhooks
          {...w.props}
          access={administrator}
          runtime={{ ...w.props.runtime, documentationUrl }}
        />,
      );
    });
    await screen.findByRole("heading", { name: "Notification counters" });
    const setup = screen
      .getByRole("heading", { name: "Set up webhook delivery" })
      .closest("section")!;
    const links = within(setup).queryAllByRole("link");
    expect(links.map((link) => link.getAttribute("href"))).toEqual(expected);
    for (const link of links) {
      expect(link).toHaveProperty("target", "_blank");
      expect(link.getAttribute("rel")).toBe("noopener noreferrer");
    }
    expect(setup.textContent).toContain(
      "Normal notification delivery needs a worker",
    );
    expect(setup.textContent).toContain("exact tenant-assigned route");
    expect(w.get.mock.calls.map((call) => call[0]).sort()).toEqual([
      "/api/v1/capabilities",
      "/api/v1/webhooks",
      "/api/v1/webhooks/reconciliation",
    ]);
  },
);

it("keeps a failed capability read distinct from disabled capability", async () => {
  const w = screenWorkspace();
  w.get.mockRejectedValue(new Error("Owned capability outage"));
  render(<Webhooks {...w.props} access={administrator} />);
  await screen.findByText("This page could not be loaded.");
  expect(screen.queryByText(/Webhooks are disabled/)).toBeNull();
  expect(w.get.mock.calls.map((call) => call[0])).toEqual([
    "/api/v1/capabilities",
  ]);
  expect(w.post).not.toHaveBeenCalled();
});

it("offers the complete public vocabulary and submits canonical aliases without private events", async () => {
  const w = screenWorkspace();
  readReplies(w.get, routes());
  w.post.mockResolvedValue(etagReply(secret));
  render(<Webhooks {...w.props} id="new" access={administrator} />);
  await screen.findByLabelText("Webhook name");
  expect(screen.getAllByRole("checkbox", { hidden: true })).toHaveLength(40);
  expect(screen.getByText("More public event types (32)")).toBeTruthy();
  change("Webhook name", "Exact public subscriptions");
  change("Receiver URL", endpoint.url);
  change(
    "Event type codes",
    "resource.created, com.open.agentic.catalog.resource.created.v1, identity.membership.suspended",
  );
  expect(screen.getByLabelText("Resource created")).toHaveProperty(
    "checked",
    true,
  );
  expect(screen.getByLabelText("Member suspended")).toHaveProperty(
    "checked",
    true,
  );
  await confirmCommand("Create webhook");
  await screen.findByLabelText("Signing secret");
  expect(w.post).toHaveBeenCalledWith(
    "/api/v1/webhooks",
    expect.objectContaining({
      body: {
        name: "Exact public subscriptions",
        url: endpoint.url,
        eventTypes: [
          "com.open.agentic.catalog.identity.membership.suspended.v1",
          "com.open.agentic.catalog.resource.created.v1",
        ],
      },
    }),
  );
});

it.each(
  ["new", endpoint.id].flatMap((id) =>
    [
      {
        kind: "wrong case",
        selectors: "RESOURCE.CREATED",
        error: "Unknown event selector.",
      },
      {
        kind: "audit-only name",
        selectors: "tenant.settings.updated",
        error: "Unknown event selector.",
      },
      {
        kind: "65 raw selectors",
        selectors: Array(65).fill("resource.created").join(","),
        error: "Choose between 1 and 64 event selectors.",
      },
    ].map((input) => ({ id, ...input })),
  ),
)(
  "refuses $kind before any $id command, then accepts the 64-selector boundary",
  async ({ id, selectors, error }) => {
    const w = screenWorkspace();
    readReplies(w.get, routes());
    const canonical = "com.open.agentic.catalog.resource.created.v1";
    const saved = { ...endpoint, eventTypes: [canonical] };
    w.post.mockResolvedValue(etagReply({ ...secret, endpoint: saved }));
    w.patch.mockResolvedValue(
      etagReply({ ...saved, revision: 2 }, '"revision-2"'),
    );
    render(<Webhooks {...w.props} id={id} access={administrator} />);
    await screen.findByLabelText("Webhook name");
    if (id === "new") {
      change("Webhook name", endpoint.name);
      change("Receiver URL", endpoint.url);
    }
    change("Event type codes", selectors);
    const command = id === "new" ? "Create webhook" : "Update webhook";
    click(command);
    await screen.findByText(error, { exact: false });
    expect(screen.queryByRole("dialog")).toBeNull();
    expect(screen.getByLabelText("Event type codes")).toHaveProperty(
      "value",
      selectors,
    );
    expect(w.post).not.toHaveBeenCalled();
    expect(w.patch).not.toHaveBeenCalled();
    expect(w.remove).not.toHaveBeenCalled();

    change("Event type codes", Array(64).fill("resource.created").join(","));
    await confirmCommand(command);
    if (id === "new") await screen.findByLabelText("Signing secret");
    else
      await screen.findByText(
        "Webhook updated. Reload endpoint before another change.",
      );
    const send = id === "new" ? w.post : w.patch;
    expect(send).toHaveBeenCalledTimes(1);
    expect(send.mock.calls[0]).toMatchObject([
      id === "new" ? "/api/v1/webhooks" : "/api/v1/webhooks/{webhookId}",
      {
        body: {
          name: endpoint.name,
          url: endpoint.url,
          eventTypes: [canonical],
        },
        ...(id === "new"
          ? {}
          : {
              params: {
                path: { webhookId: endpoint.id },
                header: { "If-Match": '"revision-1"' },
              },
            }),
      },
    ]);
    expect(id === "new" ? w.patch : w.post).not.toHaveBeenCalled();
    expect(w.remove).not.toHaveBeenCalled();
  },
);

it("treats canonical and duplicate alias subscriptions as an unchanged saved selection", async () => {
  const w = screenWorkspace();
  readReplies(w.get, {
    ...routes(),
    "/api/v1/webhooks/{webhookId}": {
      ...endpoint,
      eventTypes: ["com.open.agentic.catalog.resource.created.v1"],
    },
  });
  render(<Webhooks {...w.props} id={endpoint.id} access={administrator} />);
  await screen.findByLabelText("Event type codes");
  change(
    "Event type codes",
    "resource.created,com.open.agentic.catalog.resource.created.v1",
  );
  click("Update webhook");
  await screen.findByText(
    "Change the name, receiver URL or subscriptions before updating.",
  );
  expect(screen.queryByRole("dialog")).toBeNull();
  expect(w.patch).not.toHaveBeenCalled();
  expect(w.post).not.toHaveBeenCalled();
  expect(w.remove).not.toHaveBeenCalled();
});

it.each(["newer", "restored", "disabled"])(
  "retains non-secret overlap guidance until successful %s state proves it obsolete",
  async (state) => {
    const w = screenWorkspace();
    readReplies(w.get, routes());
    const rotated = { ...endpoint, revision: 2, secretVersion: 2 };
    w.post.mockResolvedValue(
      etagReply({
        endpoint: rotated,
        secret: secret.secret,
        previousSecretExpiresAt: overlapDeadline,
      }),
    );
    render(<Webhooks {...w.props} id={endpoint.id} access={administrator} />);
    await screen.findByLabelText("Webhook action");
    expect(
      screen.getByText(/No rotation deadline is known on this page/),
    ).toBeTruthy();
    change("Webhook action", "rotate");
    await confirmCommand("Manage webhook");
    await screen.findByLabelText("Signing secret");
    click("Clear secret from this page");
    const guidance = () =>
      screen.queryByRole("region", { name: "Signing-key rotation guidance" });
    expect(guidance()!.textContent).toContain("signing version 2");
    expect(guidance()!.textContent).toContain(
      new Date(overlapDeadline).toLocaleString(),
    );
    const pending = deferred<ReturnType<typeof etagReply>>();
    w.get.mockReturnValueOnce(pending.promise);
    click("Reload endpoint");
    expect(guidance()!.textContent).toContain(
      new Date(overlapDeadline).toLocaleString(),
    );
    await act(async () => pending.resolve(etagReply(rotated, '"revision-2"')));
    expect(guidance()!.textContent).toContain(
      new Date(overlapDeadline).toLocaleString(),
    );
    w.get.mockRejectedValueOnce(new Error("Owned read outage"));
    click("Reload endpoint");
    await screen.findByText("This page could not be loaded.");
    expect(guidance()!.textContent).toContain(
      new Date(overlapDeadline).toLocaleString(),
    );
    const observed =
      state === "newer"
        ? { ...rotated, revision: 3, secretVersion: 3 }
        : state === "restored"
          ? endpoint
          : { ...rotated, status: "disabled", revision: 3 };
    w.get.mockResolvedValueOnce(etagReply(observed));
    click("Reload endpoint");
    await waitFor(() =>
      expect(guidance()?.textContent ?? "").not.toContain(
        new Date(overlapDeadline).toLocaleString(),
      ),
    );
    w.get.mockResolvedValueOnce(etagReply(rotated, '"revision-2"'));
    click("Reload endpoint");
    await screen.findByText(/No rotation deadline is known on this page/);
    expect(screen.queryByLabelText("Signing secret")).toBeNull();
    expect(w.post).toHaveBeenCalledTimes(1);
  },
);

it("does not present a replayed older rotation receipt as current signing guidance", async () => {
  const w = screenWorkspace();
  readReplies(w.get, {
    ...routes(),
    "/api/v1/webhooks/{webhookId}": {
      ...endpoint,
      revision: 4,
      secretVersion: 3,
    },
  });
  w.post.mockResolvedValue(
    etagReply({
      endpoint: { ...endpoint, revision: 2, secretVersion: 2 },
      secret: secret.secret,
      previousSecretExpiresAt: overlapDeadline,
    }),
  );
  render(<Webhooks {...w.props} id={endpoint.id} access={administrator} />);
  await screen.findByLabelText("Webhook action");
  change("Webhook action", "rotate");
  await confirmCommand("Manage webhook");
  await screen.findByLabelText("Signing secret");
  expect(
    screen.getByRole("region", { name: "Signing-key rotation guidance" })
      .textContent,
  ).toContain("No rotation deadline is known on this page");
  expect(
    screen.queryByText(
      /Catalog includes a signature made with the previous secret until/,
    ),
  ).toBeNull();
});

it("keeps routine overlap refusal deliberate without switching to emergency or sending again", async () => {
  const w = screenWorkspace();
  readReplies(w.get, routes());
  w.post.mockRejectedValueOnce(new APIError(409, "CONFLICT", "owned-overlap"));
  render(<Webhooks {...w.props} id={endpoint.id} access={administrator} />);
  await screen.findByLabelText("Webhook action");
  change("Webhook action", "rotate");
  await confirmCommand("Manage webhook");
  await screen.findByText(/This action conflicts with the current state/);
  expect(screen.getByText(/Another routine rotation is refused/)).toBeTruthy();
  expect(screen.queryByLabelText("Signing secret")).toBeNull();
  expect(w.post).toHaveBeenCalledTimes(1);
  expect(w.post.mock.calls[0]).toMatchObject([
    "/api/v1/webhooks/{webhookId}:rotate-secret",
    { body: { revokePrevious: false } },
  ]);
  click("Back to form");
  expect(screen.getByLabelText("Rotation mode")).toHaveProperty(
    "value",
    "routine",
  );
  expect(screen.getByText(/If a known-success key was discarded/)).toBeTruthy();
  expect(w.post).toHaveBeenCalledTimes(1);
});

it("treats an emergency reply containing a predecessor deadline as uncertain", async () => {
  const w = screenWorkspace();
  readReplies(w.get, routes());
  w.post
    .mockResolvedValueOnce(
      etagReply({ ...secret, previousSecretExpiresAt: overlapDeadline }),
    )
    .mockResolvedValueOnce(etagReply(secret));
  render(<Webhooks {...w.props} id={endpoint.id} access={administrator} />);
  await screen.findByLabelText("Webhook action");
  change("Webhook action", "rotate");
  change("Rotation mode", "emergency");
  await confirmCommand("Manage webhook");
  await screen.findByText(/The outcome is unknown/);
  expect(screen.queryByLabelText("Signing secret")).toBeNull();
  const captured = structuredClone(required(w.post.mock.calls[0]));
  click("Retry same command");
  await screen.findByLabelText("Signing secret");
  expect(w.post.mock.calls[1]).toEqual(captured);
  expect(captured[1]).toMatchObject({ body: { revokePrevious: true } });
});

it("disables an active endpoint without deleting retained delivery evidence", async () => {
  const { props, get, remove } = screenWorkspace();
  readReplies(get, {
    ...routes(),
    "/api/v1/webhooks/{webhookId}/deliveries": { items: [delivery] },
  });
  remove.mockResolvedValue({ response: new Response(null, { status: 204 }) });
  render(<Webhooks {...props} id={endpoint.id} access={administrator} />);
  await screen.findByLabelText("Webhook action");
  change("Webhook action", "disable");
  await confirmCommand("Manage webhook");
  await screen.findByText(/Webhook disabled\./);
  expect(remove.mock.calls[0]).toMatchObject([
    "/api/v1/webhooks/{webhookId}",
    {
      params: {
        path: { webhookId: endpoint.id },
        header: { "If-Match": '"revision-1"' },
      },
    },
  ]);
  expect(
    screen.getByRole("heading", { name: "example: Succeeded" }),
  ).toBeTruthy();
  expect(remove).toHaveBeenCalledTimes(1);
});

it("keeps an uncertain disable retry bound to the original endpoint and command key", async () => {
  const { props, get, remove } = screenWorkspace();
  readReplies(get, routes());
  remove
    .mockResolvedValueOnce({
      error: {},
      response: new Response(null, { status: 503 }),
    })
    .mockResolvedValueOnce({ response: new Response(null, { status: 204 }) });
  render(<Webhooks {...props} id={endpoint.id} access={administrator} />);
  await screen.findByLabelText("Webhook action");
  change("Webhook action", "disable");
  await confirmCommand("Manage webhook");
  await screen.findByText(/The outcome is unknown/);
  expect(screen.queryByText(/Webhook disabled\./)).toBeNull();
  expect(screen.getByRole("button", { name: "Back to form" })).toHaveProperty(
    "disabled",
    true,
  );
  const initial = (
    required(remove.mock.calls[0])[1] as {
      params: { header: Record<string, string> };
    }
  ).params.header["Idempotency-Key"];
  expect(initial).toEqual(expect.any(String));
  expect(required(initial).length).toBeGreaterThan(0);
  click("Retry same command");
  await screen.findByText(/Webhook disabled\./);
  expect(remove).toHaveBeenCalledTimes(2);
  expect(remove.mock.calls[1]).toEqual([
    "/api/v1/webhooks/{webhookId}",
    {
      params: {
        path: { webhookId: endpoint.id },
        header: { "Idempotency-Key": initial, "If-Match": '"revision-1"' },
      },
    },
  ]);
  expect(props.onFailure).toHaveBeenCalledTimes(1);
});

it.each(["", "endpoint-1"])(
  "keeps private webhook evidence unavailable for metadata-only access (%s)",
  async (id) => {
    const { props, get, post } = screenWorkspace();
    readReplies(get, routes());
    render(
      <Webhooks
        {...props}
        id={id}
        access={{
          ...administrator,
          permissions: ["webhook.read"],
          availablePermissions: ["webhook.read"],
        }}
      />,
    );
    await screen.findByRole("link", {
      name: id ? "Back to webhooks" : endpoint.name,
    });
    expect(get.mock.calls.map(([path]) => path)).not.toContain(
      "/api/v1/webhooks/reconciliation",
    );
    expect(get.mock.calls.map(([path]) => path)).not.toContain(
      "/api/v1/webhooks/{webhookId}/deliveries",
    );
    expect(post).not.toHaveBeenCalled();
  },
);

it("keeps read-only webhook inventory usable without offering creation", async () => {
  const { props, get, post } = screenWorkspace();
  readReplies(get, routes());
  render(
    <Webhooks
      {...props}
      access={{
        ...administrator,
        permissions: ["webhook.read"],
        availablePermissions: ["webhook.read"],
        scopeEnforcement: true,
        scopes: ["webhooks.manage"],
      }}
    />,
  );
  await screen.findByRole("link", { name: endpoint.name });
  expect(screen.queryByRole("link", { name: "Create webhook" })).toBeNull();
  expect(post).not.toHaveBeenCalled();
});

it.each(["delegate", "disabled"])(
  "keeps %s read-only endpoint configuration current without a dirty draft",
  async (mode) => {
    const w = screenWorkspace();
    const status = mode === "disabled" ? "disabled" : "active";
    readReplies(w.get, {
      ...routes(),
      "/api/v1/webhooks/{webhookId}": { ...endpoint, status },
    });
    render(
      <Webhooks
        {...w.props}
        id={endpoint.id}
        access={
          mode === "disabled"
            ? administrator
            : {
                ...administrator,
                permissions: ["webhook.read"],
                availablePermissions: ["webhook.read"],
                scopeEnforcement: true,
                scopes: ["webhooks.manage"],
              }
        }
      />,
    );
    await screen.findByRole("region", { name: "Last loaded endpoint" });
    expect(screen.queryByLabelText("Webhook name")).toBeNull();
    expect(screen.queryByLabelText("Receiver URL")).toBeNull();
    expect(
      screen.getByRole("region", { name: "Last loaded endpoint" }).textContent,
    ).toContain(endpoint.url);
    await waitFor(() =>
      expect(
        screen.getByRole("button", { name: "Reload endpoint" }),
      ).toHaveProperty("disabled", false),
    );
    w.get.mockResolvedValueOnce(
      etagReply(
        {
          ...endpoint,
          status,
          revision: 2,
          name: "Latest read-only receiver",
          url: "https://example.com/current-receiver",
          eventTypes: ["com.open.agentic.catalog.source.paused.v1"],
        },
        '"revision-2"',
      ),
    );
    click("Reload endpoint");
    await screen.findByRole("heading", { name: "Latest read-only receiver" });
    const facts = screen.getByRole("region", { name: "Last loaded endpoint" });
    expect(facts.textContent).toContain("https://example.com/current-receiver");
    expect(facts.textContent).toContain("Source paused");
    expect(facts.textContent).toContain(
      "com.open.agentic.catalog.source.paused.v1",
    );
    expect(screen.queryByLabelText("Webhook name")).toBeNull();
    expect(
      window.dispatchEvent(new Event("catalog-ui:leave", { cancelable: true })),
    ).toBe(true);
    expect(w.patch).not.toHaveBeenCalled();
    expect(w.post).not.toHaveBeenCalled();
  },
);

it("keeps a direct create route read-only without inventing saved configuration", async () => {
  const w = screenWorkspace();
  readReplies(w.get, routes());
  render(
    <Webhooks
      {...w.props}
      id="new"
      access={{
        ...administrator,
        permissions: ["webhook.read"],
        availablePermissions: ["webhook.read"],
      }}
    />,
  );
  await screen.findByText(/Editing requires webhook.manage/);
  expect(screen.queryByLabelText("Webhook name")).toBeNull();
  expect(screen.queryByLabelText("Receiver URL")).toBeNull();
  expect(screen.queryByRole("button", { name: "Create webhook" })).toBeNull();
  expect(
    screen.queryByRole("region", { name: "Last loaded endpoint" }),
  ).toBeNull();
  expect(
    window.dispatchEvent(new Event("catalog-ui:leave", { cancelable: true })),
  ).toBe(true);
  expect(w.get.mock.calls.map(([path]) => path)).toEqual([
    "/api/v1/capabilities",
  ]);
  expect(w.post).not.toHaveBeenCalled();
  expect(w.patch).not.toHaveBeenCalled();
});

it.each(["copied", "failed"])(
  "masks a replacement signer, clears %s clipboard state and requires fresh acknowledgement",
  async (copyOutcome) => {
    const w = screenWorkspace();
    readReplies(w.get, routes());
    const writeText = vi.fn();
    if (copyOutcome === "copied") writeText.mockResolvedValue(undefined);
    else writeText.mockRejectedValue(new Error("Clipboard unavailable"));
    vi.stubGlobal("navigator", { clipboard: { writeText } });
    w.post.mockResolvedValueOnce(etagReply(secret)).mockResolvedValueOnce(
      etagReply({
        endpoint: { ...endpoint, revision: 3, secretVersion: 3 },
        secret: "replacement-test-only-signing-secret",
      }),
    );
    render(<Webhooks {...w.props} id={endpoint.id} access={administrator} />);
    await screen.findByLabelText("Webhook action");
    change("Webhook action", "rotate");
    await confirmCommand("Manage webhook");
    await screen.findByLabelText("Signing secret");
    click("Reveal secret");
    click("Copy secret");
    await screen.findByText(
      copyOutcome === "copied"
        ? /Copied\. Clear your clipboard/
        : /Clipboard access failed/,
    );
    w.get.mockImplementation(async (path) =>
      etagReply(
        path === "/api/v1/webhooks/{webhookId}"
          ? { ...endpoint, revision: 2, secretVersion: 2 }
          : routes()[path as keyof ReturnType<typeof routes>],
        '"revision-2"',
      ),
    );
    click("Reload endpoint");
    await waitFor(() =>
      expect(
        screen.getByRole("button", { name: "Prepare another webhook action" }),
      ).toHaveProperty("disabled", false),
    );
    click("Prepare another webhook action");
    change("Webhook action", "rotate");
    change("Rotation mode", "emergency");
    fireEvent.click(
      screen.getByLabelText(
        /I understand the signing secret still on this page will be replaced/,
      ),
    );
    await confirmCommand("Manage webhook");
    await waitFor(() =>
      expect(screen.getByLabelText("Signing secret")).toHaveProperty(
        "value",
        "replacement-test-only-signing-secret",
      ),
    );
    expect(screen.getByLabelText("Signing secret")).toHaveProperty(
      "type",
      "password",
    );
    expect(screen.queryByText(/Copied\. Clear your clipboard/)).toBeNull();
    expect(screen.queryByText(/Clipboard access failed/)).toBeNull();
    expect(
      screen.getByLabelText(
        /I understand the signing secret still on this page will be replaced/,
      ),
    ).toHaveProperty("checked", false);
    expect(writeText).toHaveBeenCalledTimes(1);
  },
);

it.each(["success", "failure"])(
  "ignores late clipboard %s from a replaced signer",
  async (outcome) => {
    const w = screenWorkspace();
    readReplies(w.get, routes());
    const pending = deferred<void>();
    const writeText = vi
      .fn()
      .mockReturnValueOnce(pending.promise)
      .mockResolvedValue(undefined);
    vi.stubGlobal("navigator", { clipboard: { writeText } });
    w.post.mockResolvedValueOnce(etagReply(secret)).mockResolvedValueOnce(
      etagReply({
        ...secret,
        secret: "new-test-only-key",
        endpoint: { ...endpoint, revision: 3, secretVersion: 3 },
      }),
    );
    render(<Webhooks {...w.props} id={endpoint.id} access={administrator} />);
    await screen.findByLabelText("Webhook action");
    change("Webhook action", "rotate");
    await confirmCommand("Manage webhook");
    click("Copy secret");
    expect(screen.getByRole("button", { name: "Copy secret" })).toHaveProperty(
      "disabled",
      true,
    );
    w.get.mockImplementation(async (path) =>
      etagReply(
        path === "/api/v1/webhooks/{webhookId}"
          ? { ...endpoint, revision: 2, secretVersion: 2 }
          : routes()[path as keyof ReturnType<typeof routes>],
        '"revision-2"',
      ),
    );
    click("Reload endpoint");
    await waitFor(() =>
      expect(
        screen.getByRole("button", { name: "Prepare another webhook action" }),
      ).toHaveProperty("disabled", false),
    );
    click("Prepare another webhook action");
    change("Webhook action", "rotate");
    change("Rotation mode", "emergency");
    // Programmatic submission also cannot bypass the replacement acknowledgement.
    fireEvent.submit(
      screen.getByRole("button", { name: "Manage webhook" }).closest("form")!,
    );
    expect(screen.queryByRole("dialog")).toBeNull();
    expect(w.post).toHaveBeenCalledTimes(1);
    fireEvent.click(
      screen.getByLabelText(
        /I understand the signing secret still on this page will be replaced/,
      ),
    );
    await confirmCommand("Manage webhook");
    await waitFor(() =>
      expect(screen.getByLabelText("Signing secret")).toHaveProperty(
        "value",
        "new-test-only-key",
      ),
    );
    click("Copy secret");
    await screen.findByText(/Copied\. Clear your clipboard/);
    await act(async () =>
      outcome === "success"
        ? pending.resolve()
        : pending.reject(new Error("Old clipboard failure")),
    );
    expect(screen.getByText(/Copied\. Clear your clipboard/)).toBeTruthy();
    expect(screen.queryByText(/Clipboard access failed/)).toBeNull();
    expect(screen.getByLabelText("Signing secret")).toHaveProperty(
      "type",
      "password",
    );
    expect(writeText).toHaveBeenNthCalledWith(1, secret.secret);
    expect(writeText).toHaveBeenNthCalledWith(2, "new-test-only-key");
    expect(writeText).toHaveBeenCalledTimes(2);
    expect(
      screen.getByText(/already-started clipboard write cannot be cancelled/),
    ).toBeTruthy();
  },
);

it.each(["endpoint", "identity", "realm", "tenant", "permission", "scope"])(
  "clears signing material and fences pending copying after %s changes",
  async (changeKind) => {
    const w = screenWorkspace();
    readReplies(w.get, routes());
    w.post.mockResolvedValue(etagReply(secret));
    const pending = deferred<void>();
    vi.stubGlobal("navigator", {
      clipboard: { writeText: vi.fn(() => pending.promise) },
    });
    const view = render(
      <Webhooks {...w.props} id={endpoint.id} access={administrator} />,
    );
    await screen.findByLabelText("Webhook action");
    change("Webhook action", "rotate");
    await confirmCommand("Manage webhook");
    expect(
      screen.getByText(/Observed routine rotation for endpoint/),
    ).toBeTruthy();
    click("Copy secret");
    readReplies(w.get, {
      ...routes(),
      "/api/v1/webhooks/{webhookId}": {
        ...endpoint,
        id: changeKind === "endpoint" ? "hook-2" : endpoint.id,
      },
    });
    const access =
      changeKind === "identity"
        ? { ...administrator, subject: "new-human" }
        : changeKind === "realm"
          ? { ...administrator, realmId: "new-realm" }
          : changeKind === "tenant"
            ? { ...administrator, tenantId: "new-tenant" }
            : changeKind === "permission"
              ? {
                  ...administrator,
                  permissions: [
                    "webhook.read",
                  ] as typeof administrator.permissions,
                }
              : changeKind === "scope"
                ? { ...administrator, scopeEnforcement: true, scopes: [] }
                : administrator;
    view.rerender(
      <Webhooks
        {...w.props}
        id={changeKind === "endpoint" ? "hook-2" : endpoint.id}
        access={access}
      />,
    );
    await waitFor(() =>
      expect(screen.queryByLabelText("Signing secret")).toBeNull(),
    );
    expect(
      screen.queryByText(/Observed routine rotation for endpoint/),
    ).toBeNull();
    await act(async () =>
      pending.reject(new Error("Obsolete clipboard result")),
    );
    expect(screen.queryByText(/Clipboard access failed/)).toBeNull();
    expect(screen.queryByText(/Copied\. Clear your clipboard/)).toBeNull();
    expect(
      window.dispatchEvent(new Event("catalog-ui:leave", { cancelable: true })),
    ).toBe(true);
    expect(w.post).toHaveBeenCalledTimes(1);
  },
);

it.each([
  ["webhook.test", "test"],
  ["webhook.rotate", "rotate"],
  ["webhook.manage", "disable"],
] as const)(
  "starts a %s delegate on its permitted action without evidence reads",
  async (permission, action) => {
    const w = screenWorkspace();
    readReplies(w.get, routes());
    const permissions = [
      "webhook.read",
      permission,
    ] as typeof administrator.permissions;
    render(
      <Webhooks
        {...w.props}
        id={endpoint.id}
        access={{
          ...administrator,
          permissions,
          availablePermissions: permissions,
          scopeEnforcement: true,
          scopes: ["webhooks.manage"],
        }}
      />,
    );
    await screen.findByLabelText("Webhook action");
    expect(screen.getByLabelText("Webhook action")).toHaveProperty(
      "value",
      action,
    );
    expect(
      screen.getByRole("button", { name: "Manage webhook" }),
    ).toHaveProperty("disabled", false);
    for (const [value, label] of [
      ["test", "Send test"],
      ["rotate", "Rotate secret"],
      ["disable", "Disable endpoint"],
    ] as const) {
      expect(screen.getByRole("option", { name: label })).toHaveProperty(
        "disabled",
        value !== action,
      );
    }
    expect(
      screen.getByLabelText("Webhook action").closest("form")!.textContent,
    ).toContain(permission);
    expect(screen.queryByLabelText("Webhook name") !== null).toBe(
      permission === "webhook.manage",
    );
    expect(w.get.mock.calls.map(([path]) => path)).not.toContain(
      "/api/v1/webhooks/{webhookId}/deliveries",
    );
    expect(w.post).not.toHaveBeenCalled();
  },
);

it("shows saved/proposed configuration and refuses a no-op before confirmation", async () => {
  const w = screenWorkspace();
  readReplies(w.get, routes());
  render(<Webhooks {...w.props} id={endpoint.id} access={administrator} />);
  await screen.findByLabelText("Webhook name");
  click("Update webhook");
  await screen.findByText(
    "Change the name, receiver URL or subscriptions before updating.",
  );
  expect(screen.queryByRole("dialog")).toBeNull();
  change("Webhook name", "New receiver name");
  change("Receiver URL", "https://example.net/new-hook");
  change("Event type codes", "com.open.agentic.catalog.resource.created.v1");
  click("Update webhook");
  const dialog = within(screen.getByRole("dialog"));
  const summary = dialog.getByLabelText("Command summary");
  for (const value of [
    "Saved name",
    endpoint.name,
    "Saved receiver",
    endpoint.url,
    "Saved events",
    "Proposed name",
    "New receiver name",
    "Proposed receiver",
    "https://example.net/new-hook",
    "Proposed events",
    "com.open.agentic.catalog.resource.created.v1",
    endpoint.id,
    '"revision-1"',
  ])
    expect(summary.textContent).toContain(value);
  expect(dialog.getByText(/Queued notifications use/).textContent).toContain(
    "in-flight request cannot be recalled",
  );
  click("Cancel");
  expect(screen.getByLabelText("Webhook name")).toHaveProperty(
    "value",
    "New receiver name",
  );
  expect(w.patch).not.toHaveBeenCalled();
});

it.each([
  {
    label: "reordered selectors",
    name: "Compliance",
    events:
      "com.open.agentic.catalog.catalog.listing.created.v1\ncom.open.agentic.catalog.resource.created.v1",
  },
  {
    label: "duplicate selectors",
    name: "Compliance",
    events:
      "com.open.agentic.catalog.resource.created.v1\ncom.open.agentic.catalog.catalog.listing.created.v1\ncom.open.agentic.catalog.resource.created.v1",
  },
  {
    label: "padded name and selectors",
    name: "  Compliance  ",
    events:
      "  com.open.agentic.catalog.resource.created.v1 ,\n com.open.agentic.catalog.catalog.listing.created.v1 \n",
  },
])(
  "refuses an unchanged configuration with $label",
  async ({ name, events }) => {
    const w = screenWorkspace();
    readReplies(w.get, {
      ...routes(),
      "/api/v1/webhooks/{webhookId}": {
        ...endpoint,
        eventTypes: [
          "com.open.agentic.catalog.resource.created.v1",
          "com.open.agentic.catalog.catalog.listing.created.v1",
        ],
      },
    });
    render(<Webhooks {...w.props} id={endpoint.id} access={administrator} />);
    await screen.findByLabelText("Webhook name");
    change("Webhook name", name);
    change("Event type codes", events);
    expect(
      screen.getByRole("button", { name: "Update webhook" }),
    ).toHaveProperty("disabled", false);
    click("Update webhook");
    await screen.findByText(
      "Change the name, receiver URL or subscriptions before updating.",
    );
    expect(screen.queryByRole("dialog")).toBeNull();
    expect(w.patch).not.toHaveBeenCalled();
    expect(w.post).not.toHaveBeenCalled();
    expect(w.remove).not.toHaveBeenCalled();
  },
);

it("creates an explicit replacement draft from disabled non-secret configuration and preserves cancelled edits", async () => {
  const w = screenWorkspace();
  const disabled = { ...endpoint, status: "disabled", revision: 2 };
  readReplies(w.get, { ...routes(), "/api/v1/webhooks/{webhookId}": disabled });
  const replacement = {
    ...secret,
    endpoint: { ...endpoint, id: "replacement-1" },
  };
  w.post.mockResolvedValue(etagReply(replacement));
  render(<Webhooks {...w.props} id={endpoint.id} access={administrator} />);
  await screen.findByRole("button", { name: "Create replacement" });
  expect(screen.getByText(/permanently disabled/)).toBeTruthy();
  await waitFor(() =>
    expect(
      screen.getByRole("button", { name: "Apply delivery filters" }),
    ).toHaveProperty("disabled", false),
  );
  const reads = w.get.mock.calls.length;
  click("Create replacement");
  expect(screen.getByLabelText("Webhook name")).toHaveProperty(
    "value",
    endpoint.name,
  );
  expect(screen.getByLabelText("Receiver URL")).toHaveProperty(
    "value",
    endpoint.url,
  );
  expect(screen.getByLabelText("All events")).toHaveProperty("checked", true);
  click("Cancel replacement");
  expect(
    screen.queryByRole("button", { name: "Create replacement webhook" }),
  ).toBeNull();
  click("Create replacement");
  change("Webhook name", "Replacement draft");
  const discard = vi.spyOn(window, "confirm").mockReturnValue(false);
  click("Cancel replacement");
  expect(screen.getByLabelText("Webhook name")).toHaveProperty(
    "value",
    "Replacement draft",
  );
  discard.mockReturnValue(true);
  click("Cancel replacement");
  click("Create replacement");
  expect(screen.getByLabelText("Webhook name")).toHaveProperty(
    "value",
    endpoint.name,
  );
  click("Create replacement webhook");
  const dialog = within(screen.getByRole("dialog"));
  expect(dialog.getByLabelText("Command summary").textContent).toContain(
    endpoint.id,
  );
  expect(
    dialog.getByText(/original endpoint and key remain unchanged/).textContent,
  ).toContain("does not backfill earlier events");
  click("Confirm");
  await screen.findByLabelText("Signing secret");
  expect(screen.getByLabelText("Signing secret")).toHaveProperty(
    "type",
    "password",
  );
  expect(
    screen.getByRole("link", { name: "Open webhook" }).getAttribute("href"),
  ).toBe("/unit/webhooks/replacement-1");
  expect(w.post.mock.calls[0]).toMatchObject([
    "/api/v1/webhooks",
    { body: { name: endpoint.name, url: endpoint.url, eventTypes: ["*"] } },
  ]);
  expect(w.get).toHaveBeenCalledTimes(reads);
  expect(w.patch).not.toHaveBeenCalled();
  expect(w.remove).not.toHaveBeenCalled();
});

it("requires fresh acknowledgement before replacement creation can replace an uncleared signer", async () => {
  const w = screenWorkspace();
  readReplies(w.get, routes());
  w.post.mockResolvedValueOnce(etagReply(secret)).mockResolvedValueOnce(
    etagReply({
      ...secret,
      endpoint: { ...endpoint, id: "replacement-2" },
      secret: "replacement-test-only-key",
    }),
  );
  render(<Webhooks {...w.props} id={endpoint.id} access={administrator} />);
  await screen.findByLabelText("Webhook action");
  change("Webhook action", "rotate");
  await confirmCommand("Manage webhook");
  await screen.findByLabelText("Signing secret");
  w.get.mockImplementation(async (path) =>
    etagReply(
      path === "/api/v1/webhooks/{webhookId}"
        ? { ...endpoint, status: "disabled", revision: 3 }
        : routes()[path as keyof ReturnType<typeof routes>],
      '"revision-3"',
    ),
  );
  click("Reload endpoint");
  await waitFor(() =>
    expect(
      screen.getByRole("region", { name: "Last loaded endpoint" }).textContent,
    ).toContain("Turned off"),
  );
  click("Create replacement");
  fireEvent.submit(
    screen
      .getByRole("button", { name: "Create replacement webhook" })
      .closest("form")!,
  );
  await screen.findByText(/Save or clear the existing signing secret/);
  expect(w.post).toHaveBeenCalledTimes(1);
  fireEvent.click(
    screen.getByLabelText(
      /I understand the signing secret still on this page will be replaced/,
    ),
  );
  click("Cancel replacement");
  click("Create replacement");
  expect(
    screen.getByLabelText(
      /I understand the signing secret still on this page will be replaced/,
    ),
  ).toHaveProperty("checked", false);
  fireEvent.click(
    screen.getByLabelText(
      /I understand the signing secret still on this page will be replaced/,
    ),
  );
  click("Create replacement webhook");
  expect(
    within(screen.getByRole("dialog")).getByLabelText("Command summary")
      .textContent,
  ).toContain("Replacement acknowledged");
  click("Confirm");
  await waitFor(() =>
    expect(screen.getByLabelText("Signing secret")).toHaveProperty(
      "value",
      "replacement-test-only-key",
    ),
  );
  expect(screen.getByLabelText("Signing secret")).toHaveProperty(
    "type",
    "password",
  );
});

it.each(["url", "event-count", "event-value"])(
  "allows a real %s-only change while retaining saved/proposed facts",
  async (field) => {
    const w = screenWorkspace();
    readReplies(w.get, routes());
    render(<Webhooks {...w.props} id={endpoint.id} access={administrator} />);
    await screen.findByLabelText("Webhook name");
    if (field === "url") change("Receiver URL", "https://example.net/events");
    else
      change(
        "Event type codes",
        field === "event-count"
          ? "*\ncom.open.agentic.catalog.resource.created.v1"
          : "com.open.agentic.catalog.resource.created.v1",
      );
    click("Update webhook");
    expect(screen.getByRole("dialog")).toBeTruthy();
    expect(
      within(screen.getByRole("dialog")).getByLabelText("Command summary")
        .textContent,
    ).toContain(endpoint.url);
    click("Cancel");
    expect(w.patch).not.toHaveBeenCalled();
  },
);

it("retains prerequisite editing and explicit reload when a valid endpoint has no ETag", async () => {
  const w = screenWorkspace();
  w.get.mockImplementation(async (path) =>
    etagReply(routes()[path as keyof ReturnType<typeof routes>], null),
  );
  render(<Webhooks {...w.props} id={endpoint.id} access={administrator} />);
  await screen.findByLabelText("Webhook name");
  expect(screen.getByLabelText("Webhook name").matches(":disabled")).toBe(
    false,
  );
  expect(
    screen.getByRole("button", { name: "Reload endpoint" }),
  ).toHaveProperty("disabled", false);
  expect(screen.getByRole("button", { name: "Update webhook" })).toHaveProperty(
    "disabled",
    true,
  );
  expect(screen.getByRole("button", { name: "Manage webhook" })).toHaveProperty(
    "disabled",
    true,
  );
  expect(w.post).not.toHaveBeenCalled();
});

it("treats a rotation reply for another endpoint as uncertain and retries only the captured command", async () => {
  const w = screenWorkspace();
  readReplies(w.get, routes());
  w.post
    .mockResolvedValueOnce(
      etagReply({ ...secret, endpoint: { ...endpoint, id: "foreign-hook" } }),
    )
    .mockResolvedValueOnce(etagReply(secret));
  render(<Webhooks {...w.props} id={endpoint.id} access={administrator} />);
  await screen.findByLabelText("Webhook action");
  change("Webhook action", "rotate");
  await confirmCommand("Manage webhook");
  await screen.findByText(/The outcome is unknown/);
  expect(screen.queryByLabelText("Signing secret")).toBeNull();
  const captured = structuredClone(required(w.post.mock.calls[0]));
  click("Retry same command");
  await screen.findByLabelText("Signing secret");
  expect(w.post.mock.calls[1]).toEqual(captured);
  expect(captured[1]).toMatchObject({
    params: {
      path: { webhookId: endpoint.id },
      header: { "If-Match": '"revision-1"' },
    },
    body: { revokePrevious: false },
  });
});

it("accepts a 160-code-point webhook name but refuses the next character before confirmation", async () => {
  const { props, get, post } = screenWorkspace();
  readReplies(get, routes());
  render(<Webhooks {...props} id="new" access={administrator} />);
  const name = await screen.findByLabelText("Webhook name");
  expect(name.getAttribute("maxlength")).toBe("320");
  change("Receiver URL", endpoint.url);
  fireEvent.click(screen.getByLabelText("All events"));
  change("Webhook name", "🙂".repeat(160) + "a");
  click("Create webhook");
  expect(screen.getByRole("alert").textContent).toContain("160 characters");
  expect(screen.queryByRole("dialog")).toBeNull();
  change("Webhook name", "🙂".repeat(160));
  click("Create webhook");
  expect(screen.getByRole("dialog")).toBeTruthy();
  expect(post).not.toHaveBeenCalled();
  click("Cancel");
});

it("fences neighboring actions after update, preserves drafts through failed reload, then uses the fresh ETag", async () => {
  const w = screenWorkspace();
  readReplies(w.get, routes());
  w.patch.mockResolvedValue(etagReply(endpoint, '"revision-2"'));
  w.post.mockResolvedValue(etagReply(delivery));
  render(<Webhooks {...w.props} id={endpoint.id} access={administrator} />);
  await screen.findByLabelText("Webhook name");
  change("Webhook name", "Updated receiver");
  click("Update webhook");
  const facts = within(screen.getByRole("dialog")).getByLabelText(
    "Command summary",
  );
  expect(facts.textContent).toContain("Updated receiver");
  expect(facts.textContent).toContain(endpoint.url);
  fireEvent.click(screen.getByRole("button", { name: "Confirm" }));
  await screen.findByText(/Webhook updated\./);
  expect(screen.getByRole("button", { name: "Manage webhook" })).toHaveProperty(
    "disabled",
    true,
  );
  expect(
    screen.getByRole("button", { name: "Edit endpoint again" }),
  ).toHaveProperty("disabled", true);
  readReplies(w.get, {
    ...routes(),
    "/api/v1/webhooks/{webhookId}": { bad: true },
  });
  click("Reload endpoint");
  await screen.findByRole("alert");
  expect(screen.getByLabelText("Webhook name")).toHaveProperty(
    "value",
    "Updated receiver",
  );
  expect(screen.getByText(/Webhook updated\./)).toBeTruthy();
  w.get.mockImplementation(async (path) =>
    etagReply(
      path === "/api/v1/webhooks/{webhookId}"
        ? { ...endpoint, name: "Updated receiver" }
        : routes()[path as keyof ReturnType<typeof routes>],
      '"revision-2"',
    ),
  );
  click("Reload endpoint");
  await waitFor(() =>
    expect(
      screen.getByRole("button", { name: "Manage webhook" }),
    ).toHaveProperty("disabled", false),
  );
  click("Edit endpoint again");
  expect(screen.getByLabelText("Webhook name")).toHaveProperty(
    "value",
    "Updated receiver",
  );
  await confirmCommand("Manage webhook");
  await screen.findByText("Test notification delivered.");
  expect(w.post.mock.calls[0]?.[1]).toMatchObject({
    params: { header: { "If-Match": '"revision-2"' } },
  });
  click("Prepare another webhook action");
  expect(screen.getByLabelText("Webhook action")).toHaveProperty(
    "value",
    "test",
  );
});

it("preserves the one-time rotated secret across endpoint readback and disable", async () => {
  const w = screenWorkspace();
  readReplies(w.get, routes());
  w.post.mockResolvedValue(etagReply(secret));
  w.remove.mockResolvedValue({ response: new Response(null, { status: 204 }) });
  render(<Webhooks {...w.props} id={endpoint.id} access={administrator} />);
  await screen.findByLabelText("Webhook action");
  change("Webhook action", "rotate");
  await confirmCommand("Manage webhook");
  await screen.findByLabelText("Signing secret");
  w.get.mockImplementation(async (path) =>
    etagReply(
      path === "/api/v1/webhooks/{webhookId}"
        ? endpoint
        : routes()[path as keyof ReturnType<typeof routes>],
      '"revision-2"',
    ),
  );
  click("Reload endpoint");
  await waitFor(() =>
    expect(
      screen.getByRole("button", { name: "Prepare another webhook action" }),
    ).toHaveProperty("disabled", false),
  );
  expect(screen.getByLabelText("Signing secret")).toHaveProperty(
    "value",
    secret.secret,
  );
  click("Prepare another webhook action");
  change("Webhook action", "disable");
  await confirmCommand("Manage webhook");
  await screen.findByText("Webhook disabled.");
  expect(screen.getByRole("button", { name: "Update webhook" })).toHaveProperty(
    "disabled",
    true,
  );
  expect(screen.getByLabelText("Signing secret")).toHaveProperty(
    "value",
    secret.secret,
  );
  w.get.mockResolvedValue(
    etagReply({ ...endpoint, status: "disabled" }, '"revision-3"'),
  );
  click("Reload endpoint");
  await waitFor(() =>
    expect(
      screen.getByRole("region", { name: "Last loaded endpoint" }).textContent,
    ).toContain("Turned off"),
  );
  expect(screen.queryByRole("button", { name: "Manage webhook" })).toBeNull();
  expect(screen.getByLabelText("Signing secret")).toHaveProperty(
    "value",
    secret.secret,
  );
  expect(w.remove).toHaveBeenCalledTimes(1);
});

it("fences a rejected stale webhook revision, preserves its draft and compares explicit readback", async () => {
  const w = screenWorkspace();
  readReplies(w.get, routes());
  w.patch.mockResolvedValue({
    error: { code: "PRECONDITION_FAILED" },
    response: new Response(null, { status: 412 }),
  });
  render(<Webhooks {...w.props} id={endpoint.id} access={administrator} />);
  await screen.findByLabelText("Webhook name");
  change("Webhook name", "Keep conflicted draft");
  await confirmCommand("Update webhook");
  await screen.findByText(/Your input is retained/);
  click("Back to form");
  expect(screen.getByLabelText("Webhook name")).toHaveProperty(
    "value",
    "Keep conflicted draft",
  );
  expect(screen.getByRole("button", { name: "Manage webhook" })).toHaveProperty(
    "disabled",
    true,
  );
  w.get.mockImplementation(async (path) =>
    etagReply(
      path === "/api/v1/webhooks/{webhookId}"
        ? { ...endpoint, name: "Other administrator update" }
        : routes()[path as keyof ReturnType<typeof routes>],
      '"revision-2"',
    ),
  );
  click("Reload endpoint");
  await screen.findByRole("heading", { name: "Other administrator update" });
  expect(screen.getByLabelText("Webhook name")).toHaveProperty(
    "value",
    "Keep conflicted draft",
  );
  expect(screen.getByRole("button", { name: "Update webhook" })).toHaveProperty(
    "disabled",
    false,
  );
  expect(w.patch).toHaveBeenCalledOnce();
});

it("shows bounded delivery outcomes and exact copyable references before complete evidence", async () => {
  const w = screenWorkspace();
  const rows = ["pending", "waiting", "succeeded", "dead_letter"].map(
    (state, index) => ({
      ...delivery,
      id: `delivery-${index}`,
      eventId: `event-${index}`,
      state,
      attempt: index,
      ...(state === "waiting"
        ? {
            responseStatus: 503,
            safeError: "<script>literal</script>" + "x".repeat(400),
            nextAttemptAt: stamp,
          }
        : {}),
    }),
  );
  readReplies(w.get, {
    ...routes(),
    "/api/v1/webhooks/{webhookId}/deliveries": { items: rows },
  });
  const writeText = vi.fn().mockResolvedValue(undefined);
  vi.stubGlobal("navigator", { clipboard: { writeText } });
  const view = render(
    <Webhooks {...w.props} access={administrator} id={endpoint.id} />,
  );
  const heading = await screen.findByRole("heading", {
    name: "example: Waiting",
  });
  const row = within(heading.closest("article")!);
  expect(row.getByText("HTTP outcome").nextElementSibling?.textContent).toBe(
    "503",
  );
  expect(
    row.getByText("Delivery error").nextElementSibling?.textContent,
  ).toContain("<script>literal</script>");
  expect(view.container.querySelector("script")).toBeNull();
  const reads = w.get.mock.calls.length;
  fireEvent.click(row.getByRole("button", { name: "Copy delivery ID" }));
  await row.findByText("Copied.");
  fireEvent.click(row.getByRole("button", { name: "Copy event ID" }));
  await waitFor(() => expect(writeText).toHaveBeenLastCalledWith("event-1"));
  expect(writeText).toHaveBeenNthCalledWith(1, "delivery-1");
  expect(w.get).toHaveBeenCalledTimes(reads);
  for (const [label, tone] of [
    ["Pending", "neutral"],
    ["Waiting", "warning"],
    ["Succeeded", "success"],
    ["Automatic retries stopped", "danger"],
  ]) {
    const heading = screen.getByRole("heading", {
      name: `example: ${label}`,
    });
    expect(
      heading.querySelector("[data-tone]")?.getAttribute("data-tone"),
    ).toBe(tone);
  }
  expect(screen.getAllByText("Not scheduled")).toHaveLength(3);
  expect(screen.getAllByText("Not reported")).toHaveLength(6);
  vi.unstubAllGlobals();
});

it("applies exact endpoint filters before paging and clears them without refreshing reconciliation", async () => {
  const { props, get, post } = screenWorkspace();
  readReplies(get, routes());
  render(<Webhooks {...props} access={administrator} />);
  await screen.findByRole("link", { name: "Compliance" });
  const counters = get.mock.calls.filter(
    ([path]) => path === "/api/v1/webhooks/reconciliation",
  ).length;
  change("Webhook name (exact match)", "Exact receiver");
  change("Status", "disabled");
  get.mockResolvedValueOnce(
    etagReply({ items: [], nextCursor: "filtered-next" }),
  );
  click("Apply endpoint filters");
  await screen.findByRole("heading", { name: "No matching webhooks" });
  expect(get.mock.calls.at(-1)?.[1]).toMatchObject({
    params: {
      query: { name: "Exact receiver", status: "disabled", limit: 25 },
    },
  });
  get.mockResolvedValueOnce(etagReply({ items: [] }));
  click("Next page");
  await waitFor(() =>
    expect(get.mock.calls.at(-1)?.[1]).toMatchObject({
      params: {
        query: {
          name: "Exact receiver",
          status: "disabled",
          cursor: "filtered-next",
        },
      },
    }),
  );
  await waitFor(() =>
    expect(
      screen.getByRole("button", { name: "Clear endpoint filters" }),
    ).toHaveProperty("disabled", false),
  );
  click("Clear endpoint filters");
  await screen.findByRole("link", { name: "Compliance" });
  expect(get.mock.calls.at(-1)?.[1]).toMatchObject({
    params: { query: { limit: 25 } },
  });
  expect(screen.getByLabelText("Webhook name (exact match)")).toHaveProperty(
    "value",
    "",
  );
  expect(screen.getByLabelText("Status")).toHaveProperty("value", "");
  expect(
    get.mock.calls.filter(
      ([path]) => path === "/api/v1/webhooks/reconciliation",
    ).length,
  ).toBe(counters);
  expect(post).not.toHaveBeenCalled();
});

it("opens a validated exact endpoint under the configured UI base", async () => {
  const { props, get } = screenWorkspace();
  const assign = vi.fn();
  vi.stubGlobal("location", { assign });
  readReplies(get, routes());
  render(
    <Webhooks
      {...props}
      runtime={{ ...props.runtime, basePath: "/catalog" }}
      access={administrator}
    />,
  );
  await screen.findByLabelText("Exact endpoint ID");
  const input = screen.getByLabelText("Exact endpoint ID");
  const form = screen.getByRole("form", { name: "Open exact endpoint" });
  fireEvent.submit(form);
  expect(assign).not.toHaveBeenCalled();
  change("Exact endpoint ID", "00000000-0000-7000-8000-000000000123");
  fireEvent.submit(form);
  expect(assign).toHaveBeenCalledWith(
    "/catalog/webhooks/00000000-0000-7000-8000-000000000123",
  );
  change("Exact endpoint ID", "AAAAAAAA-BBBB-7CCC-8DDD-EEEEEEEEEEEE");
  fireEvent.submit(form);
  expect(assign).toHaveBeenLastCalledWith(
    "/catalog/webhooks/aaaaaaaa-bbbb-7ccc-8ddd-eeeeeeeeeeee",
  );
  change("Exact endpoint ID", "not-an-endpoint-id");
  fireEvent.submit(form);
  expect(assign).toHaveBeenCalledTimes(2);
  expect(input).toHaveProperty("required", true);
});

it("intersects delivery filters, resets continuation and preserves the endpoint draft", async () => {
  const { props, get, patch } = screenWorkspace();
  readReplies(get, routes());
  const deliveryPath = "/api/v1/webhooks/{webhookId}/deliveries";
  const initialDeliveries = deferred<unknown>();
  const reply = required(get.getMockImplementation());
  get.mockImplementation((...args) =>
    args[0] === deliveryPath ? initialDeliveries.promise : reply(...args),
  );
  render(<Webhooks {...props} id={endpoint.id} access={administrator} />);
  await screen.findByLabelText("Webhook name");
  change("Webhook name", "Private unsaved receiver");
  const apply = screen.getByRole("button", { name: "Apply delivery filters" });
  expect(apply).toHaveProperty("disabled", true);
  fireEvent.click(apply);
  expect(get.mock.calls.filter(([path]) => path === deliveryPath)).toHaveLength(
    1,
  );
  // Endpoint fields can render before their independent delivery read settles.
  // Do not mistake a disabled click for an applied filter request.
  get.mockImplementation(reply);
  await act(async () => {
    initialDeliveries.resolve(etagReply(routes()[deliveryPath]));
  });
  await waitFor(() => expect(apply).toHaveProperty("disabled", false));
  const exactDelivery = "00000000-0000-7000-8000-000000000123";
  const exactEvent = "00000000-0000-7000-8000-000000000124";
  change("Exact delivery ID", exactDelivery);
  change("Exact event ID", exactEvent);
  change("Delivery state", "waiting");
  get.mockResolvedValueOnce(
    etagReply({ items: [], nextCursor: "filtered-next" }),
  );
  click("Apply delivery filters");
  await waitFor(() =>
    expect(get.mock.calls.at(-1)?.[1]).toMatchObject({
      params: {
        path: { webhookId: endpoint.id },
        query: {
          limit: 25,
          deliveryId: exactDelivery,
          eventId: exactEvent,
          state: "waiting",
        },
      },
    }),
  );
  await screen.findByRole("button", { name: "Next page" });
  get.mockResolvedValueOnce(etagReply({ error: "bad" }));
  click("Next page");
  await screen.findByRole("button", { name: "Reload" });
  get.mockResolvedValueOnce(etagReply({ items: [] }));
  click("Reload");
  await waitFor(() =>
    expect(get.mock.calls.at(-1)?.[1]).toMatchObject({
      params: {
        query: {
          deliveryId: exactDelivery,
          eventId: exactEvent,
          state: "waiting",
        },
      },
    }),
  );
  const last = required(get.mock.calls.at(-1))[1] as {
    params: { query: Record<string, unknown> };
  };
  expect(last.params.query).not.toHaveProperty("cursor");
  await waitFor(() =>
    expect(
      screen.getByRole("button", { name: "Clear delivery filters" }),
    ).toHaveProperty("disabled", false),
  );
  click("Clear delivery filters");
  await waitFor(() =>
    expect(get.mock.calls.at(-1)?.[1]).toMatchObject({
      params: { query: { limit: 25 } },
    }),
  );
  expect(screen.getByLabelText("Webhook name")).toHaveProperty(
    "value",
    "Private unsaved receiver",
  );
  expect(screen.getByLabelText("Exact event ID")).toHaveProperty("value", "");
  expect(patch).not.toHaveBeenCalled();
});

it("prepares a fresh test key after a definite no-send slot refusal", async () => {
  const { props, get, post } = screenWorkspace();
  readReplies(get, routes());
  post
    .mockResolvedValueOnce({
      error: { code: "WEBHOOK_TEST_BUSY" },
      response: new Response(null, { status: 503 }),
    })
    .mockResolvedValueOnce(
      etagReply({
        ...delivery,
        state: "dead_letter",
        responseStatus: 503,
        safeError: "Receiver returned an unsuccessful status.",
      }),
    );
  render(<Webhooks {...props} id={endpoint.id} access={administrator} />);
  await screen.findByLabelText("Webhook action");
  await confirmCommand("Manage webhook");
  const dialog = screen.getByRole("dialog");
  await within(dialog).findByText(/No request was sent/);
  expect(
    within(dialog).queryByRole("button", { name: "Retry same command" }),
  ).toBeNull();
  click("Back to form");
  await confirmCommand("Manage webhook");
  await screen.findByText(/Test notification was not delivered/);
  const receipt = screen.getByRole("region", { name: "Test delivery result" });
  expect(
    within(receipt).getByText("Receiver HTTP outcome").nextElementSibling
      ?.textContent,
  ).toBe("503");
  expect(
    within(receipt).getByText("State").nextElementSibling?.textContent,
  ).toBe("Automatic retries stopped");
  expect(screen.queryByText(/The outcome is unknown/)).toBeNull();
  const keys = post.mock.calls.map(
    (call) =>
      (required(call[1]) as { params: { header: Record<string, string> } })
        .params.header["Idempotency-Key"],
  );
  expect(keys[0]).toBeTruthy();
  expect(keys[1]).toBeTruthy();
  expect(keys[1]).not.toBe(keys[0]);
  get.mockRejectedValueOnce(new Error("readback unavailable"));
  click("Reload endpoint");
  await waitFor(() =>
    expect(
      screen.getByRole("button", { name: "Reload endpoint" }),
    ).toHaveProperty("disabled", false),
  );
  expect(
    within(receipt).getByText("Receiver HTTP outcome").nextElementSibling
      ?.textContent,
  ).toBe("503");
  expect(post).toHaveBeenCalledTimes(2);
});

it.each(["Refresh", "Reload"])(
  "recovers a refused endpoint continuation with %s and keeps exact filters",
  async (button) => {
    const { props, get } = screenWorkspace();
    readReplies(get, routes());
    render(<Webhooks {...props} access={administrator} />);
    await screen.findByRole("link", { name: "Compliance" });
    change("Webhook name (exact match)", "Compliance");
    get.mockResolvedValueOnce(
      etagReply({ items: [endpoint], nextCursor: "filtered" }),
    );
    click("Apply endpoint filters");
    await waitFor(() =>
      expect(screen.getByRole("button", { name: "Next page" })).toHaveProperty(
        "disabled",
        false,
      ),
    );
    get.mockRejectedValueOnce(new APIError(400, "INVALID_ARGUMENT"));
    click("Next page");
    await screen.findByRole("alert");
    click(button);
    await screen.findByRole("link", { name: "Compliance" });
    const last = required(get.mock.calls.at(-1))[1] as {
      params: { query: Record<string, unknown> };
    };
    expect(last.params.query).toEqual({ limit: 25, name: "Compliance" });
  },
);

it("retries failed first-page endpoint and delivery reads without inventing a cursor", async () => {
  const w = screenWorkspace();
  readReplies(w.get, routes());
  const page = render(<Webhooks {...w.props} access={administrator} />);
  await screen.findByRole("link", { name: "Compliance" });
  w.get.mockRejectedValueOnce(new APIError(403, "FORBIDDEN"));
  click("Refresh");
  await screen.findByRole("alert");
  click("Reload");
  await screen.findByRole("link", { name: "Compliance" });
  page.rerender(
    <Webhooks {...w.props} id={endpoint.id} access={administrator} />,
  );
  await waitFor(() =>
    expect(
      screen.getByRole("button", { name: "Apply delivery filters" }),
    ).toHaveProperty("disabled", false),
  );
  w.get.mockRejectedValueOnce(new APIError(403, "FORBIDDEN"));
  click("Refresh deliveries from first page");
  await screen.findByRole("alert");
  click("Reload");
  await waitFor(() =>
    expect(
      screen.getByRole("button", { name: "Apply delivery filters" }),
    ).toHaveProperty("disabled", false),
  );
  const last = required(w.get.mock.calls.at(-1))[1] as {
    params: { query: Record<string, unknown> };
  };
  expect(last.params.query).toEqual({ limit: 25 });
});

it("keeps an unknown test result on the exact-key recovery path until endpoint-bound evidence arrives", async () => {
  const { props, get, post } = screenWorkspace();
  readReplies(get, routes());
  post
    .mockResolvedValueOnce(
      etagReply({ ...delivery, endpointId: "other-endpoint" }),
    )
    .mockResolvedValueOnce(etagReply(delivery));
  render(<Webhooks {...props} id={endpoint.id} access={administrator} />);
  await screen.findByLabelText("Webhook action");
  await confirmCommand("Manage webhook");
  await screen.findByText(/The outcome is unknown/);
  expect(
    screen.queryByRole("region", { name: "Test delivery result" }),
  ).toBeNull();
  click("Retry same command");
  await screen.findByRole("region", { name: "Test delivery result" });
  expect(post.mock.calls[1]).toEqual(post.mock.calls[0]);
});

it.each([
  ["active", "info"],
  ["disabled", "neutral"],
] as const)(
  "labels the configured %s endpoint without claiming receiver health",
  async (status, tone) => {
    const { props, get, post } = screenWorkspace();
    readReplies(get, {
      ...routes(),
      "/api/v1/webhooks": { items: [{ ...endpoint, status }] },
    });
    const view = render(<Webhooks {...props} access={administrator} />);
    await screen.findByRole("link", { name: endpoint.name });
    expect(
      view.container
        .querySelector(`[data-state="${status}"]`)
        ?.getAttribute("data-tone"),
    ).toBe(tone);
    expect(post).not.toHaveBeenCalled();
  },
);

it("restores endpoint status URLs and clears page-local names on history navigation", async () => {
  history.replaceState(null, "", "/unit/webhooks?status=disabled");
  const { props, get } = screenWorkspace();
  readReplies(get, routes());
  render(<Webhooks {...props} access={administrator} />);
  await screen.findByRole("link", { name: "Compliance" });
  expect(screen.getByRole("combobox", { name: "Status" })).toHaveProperty(
    "value",
    "disabled",
  );
  expect(get).toHaveBeenCalledWith(
    "/api/v1/webhooks",
    expect.objectContaining({
      params: { query: { limit: 25, status: "disabled" } },
    }),
  );
  change("Webhook name (exact match)", "Private endpoint name");
  fireEvent.change(screen.getByRole("combobox", { name: "Status" }), {
    target: { value: "active" },
  });
  click("Apply endpoint filters");
  await waitFor(() => expect(location.search).toBe("?status=active"));
  expect(location.href).not.toContain("Private");
  await act(async () => {
    history.pushState(null, "", "/unit/webhooks?status=disabled");
    window.dispatchEvent(new PopStateEvent("popstate"));
  });
  expect(screen.getByLabelText("Webhook name (exact match)")).toHaveProperty(
    "value",
    "",
  );
  expect(screen.getByRole("combobox", { name: "Status" })).toHaveProperty(
    "value",
    "disabled",
  );
  const reads = get.mock.calls.length;
  await act(async () => {
    history.pushState(null, "", "/unit/jobs?status=active");
    window.dispatchEvent(new PopStateEvent("popstate"));
  });
  expect(get).toHaveBeenCalledTimes(reads);
  await act(async () => {
    history.pushState(null, "", "/unit/webhooks?status=active&status=disabled");
    window.dispatchEvent(new PopStateEvent("popstate"));
  });
  expect(
    screen.getByRole("option", { name: "Unsupported status" }),
  ).toBeTruthy();
  await waitFor(() =>
    expect(get).toHaveBeenLastCalledWith(
      "/api/v1/webhooks",
      expect.objectContaining({
        params: { query: { limit: 25, status: "unsupported" } },
      }),
    ),
  );
  click("Clear endpoint filters");
  await waitFor(() => expect(location.search).toBe(""));
});

it("restores exact delivery filters on arrival and history without widening invalid criteria", async () => {
  const deliveryId = "00000000-0000-7000-9000-000000000010";
  const eventId = "00000000-0000-7000-9000-000000000011";
  const query = `?deliveryId=${deliveryId}&eventId=${eventId}&state=waiting`;
  history.replaceState(null, "", "/unit/webhooks/hook-1" + query);
  const { props, get } = screenWorkspace();
  readReplies(get, routes());
  render(<Webhooks {...props} id="hook-1" access={administrator} />);
  await screen.findByRole("heading", { name: "Delivery history" });
  expect(screen.getByLabelText("Exact delivery ID")).toHaveProperty(
    "value",
    deliveryId,
  );
  expect(screen.getByLabelText("Exact event ID")).toHaveProperty(
    "value",
    eventId,
  );
  expect(
    screen.getByRole("combobox", { name: "Delivery state" }),
  ).toHaveProperty("value", "waiting");
  await waitFor(() =>
    expect(get).toHaveBeenCalledWith(
      "/api/v1/webhooks/{webhookId}/deliveries",
      expect.objectContaining({
        params: {
          path: { webhookId: "hook-1" },
          query: { limit: 25, deliveryId, eventId, state: "waiting" },
        },
      }),
    ),
  );
  fireEvent.change(screen.getByRole("combobox", { name: "Delivery state" }), {
    target: { value: "dead_letter" },
  });
  click("Apply delivery filters");
  await waitFor(() =>
    expect(new URLSearchParams(location.search).get("state")).toBe(
      "dead_letter",
    ),
  );
  click("Clear delivery filters");
  await waitFor(() => expect(location.search).toBe(""));
  await act(async () => {
    history.pushState(null, "", "/unit/webhooks/hook-1" + query);
    window.dispatchEvent(new PopStateEvent("popstate"));
  });
  expect(screen.getByLabelText("Exact delivery ID")).toHaveProperty(
    "value",
    deliveryId,
  );
  const reads = get.mock.calls.length;
  await act(async () => {
    history.pushState(null, "", "/unit/jobs?state=failed");
    window.dispatchEvent(new PopStateEvent("popstate"));
  });
  expect(get).toHaveBeenCalledTimes(reads);
  await act(async () => {
    history.pushState(
      null,
      "",
      "/unit/webhooks/hook-1?state=waiting&state=dead_letter&deliveryId=",
    );
    window.dispatchEvent(new PopStateEvent("popstate"));
  });
  expect(
    screen.getByRole("option", { name: "Unsupported state" }),
  ).toBeTruthy();
  await waitFor(() =>
    expect(get).toHaveBeenLastCalledWith(
      "/api/v1/webhooks/{webhookId}/deliveries",
      expect.objectContaining({
        params: {
          path: { webhookId: "hook-1" },
          query: { limit: 25, deliveryId: "unsupported", state: "unsupported" },
        },
      }),
    ),
  );
});
