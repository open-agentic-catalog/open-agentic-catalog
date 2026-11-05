import { testAccess } from "./access-fixtures";
// Screen tests use exact route replies and fail teardown when a request was not configured.
import { afterEach, beforeEach, expect, vi } from "vitest";
import {
  act,
  cleanup,
  fireEvent,
  screen,
  within,
} from "@testing-library/react";
import { installDialog, reader, workspace } from "./fixtures";

export const administrator = {
  ...reader,
  ...testAccess(["tenant_admin"]),
};
export const stamp = "2026-09-01T12:00:00Z";
export const namespace = {
  id: "ns-1",
  tenantId: "tenant-a",
  slug: "tools",
  displayName: "Tools",
  revision: 1,
  createdAt: stamp,
  updatedAt: stamp,
};
export const resource = {
  id: "resource-1",
  tenantId: "tenant-a",
  namespaceId: namespace.id,
  canonicalIdentifier: "urn:air:example.com:agents:helper",
  kind: "agent" as const,
  revision: 1,
  createdBy: reader.subject,
  createdAt: stamp,
  updatedAt: stamp,
};
export const version = {
  id: "version-1",
  tenantId: "tenant-a",
  resourceId: resource.id,
  version: "1.0.0",
  revision: 1,
  createdAt: stamp,
  updatedAt: stamp,
};
export const listing = {
  tenantId: "tenant-a",
  id: "listing-1",
  resourceId: resource.id,
  namespaceId: namespace.id,
  revision: 1,
  createdAt: stamp,
  updatedAt: stamp,
};
export const publisher = {
  id: "publisher-1",
  tenantId: "tenant-a",
  displayName: "Example publisher",
  domains: ["example.com"],
  approvedPublicationDomains: [],
  revision: 1,
  createdAt: stamp,
  updatedAt: stamp,
};

/** Build a typed transport reply with an optional concurrency token for screen tests. */
export function etagReply<T>(data: T, etag: string | null = '"revision-1"') {
  return {
    data,
    response: new Response(null, { headers: etag ? { ETag: etag } : {} }),
  };
}

// A caught transport error must not accidentally satisfy an expected error UI.
// Each screenLifecycle resets and checks this list, including after unmount.
let unexpectedRequests: string[] = [];
function unexpectedRequest(message: string): never {
  unexpectedRequests.push(message);
  throw new Error(message);
}

/** Only transport is substituted; unconfigured calls also fail test teardown. */
export function screenWorkspace() {
  const base = workspace();
  base.get.mockImplementation(async () =>
    unexpectedRequest("Unconfigured screen GET"),
  );
  const unexpected = async () => {
    return unexpectedRequest("Unconfigured screen mutation");
  };
  const post = vi.fn<(...args: unknown[]) => Promise<unknown>>(unexpected);
  const patch = vi.fn<(...args: unknown[]) => Promise<unknown>>(unexpected);
  const remove = vi.fn<(...args: unknown[]) => Promise<unknown>>(unexpected);
  Object.assign(base.props.client.api, {
    POST: post,
    PATCH: patch,
    DELETE: remove,
  });
  return { ...base, post, patch, remove };
}

/** jsdom substitutes only missing modal/scroll behavior; Playwright owns layout/focus trapping. */
export function screenLifecycle(path: string) {
  let restore: () => void;
  let scroll: PropertyDescriptor | undefined;
  let close: PropertyDescriptor | undefined;
  let media: PropertyDescriptor | undefined;
  beforeEach(() => {
    media = Object.getOwnPropertyDescriptor(window, "matchMedia");
    Object.defineProperty(window, "matchMedia", {
      configurable: true,
      writable: true,
      value: vi.fn(() => ({ matches: false })),
    });
    unexpectedRequests = [];
    history.replaceState(null, "", path);
    restore = installDialog();
    close = Object.getOwnPropertyDescriptor(
      HTMLDialogElement.prototype,
      "close",
    );
    Object.defineProperty(HTMLDialogElement.prototype, "close", {
      configurable: true,
      value(this: HTMLDialogElement) {
        this.removeAttribute("open");
      },
    });
    scroll = Object.getOwnPropertyDescriptor(
      HTMLElement.prototype,
      "scrollIntoView",
    );
    Object.defineProperty(HTMLElement.prototype, "scrollIntoView", {
      configurable: true,
      value: vi.fn(),
    });
  });
  afterEach(() => {
    cleanup();
    if (media) Object.defineProperty(window, "matchMedia", media);
    else Reflect.deleteProperty(window, "matchMedia");
    restore();
    vi.restoreAllMocks();
    if (close)
      Object.defineProperty(HTMLDialogElement.prototype, "close", close);
    else Reflect.deleteProperty(HTMLDialogElement.prototype, "close");
    if (scroll)
      Object.defineProperty(HTMLElement.prototype, "scrollIntoView", scroll);
    else Reflect.deleteProperty(HTMLElement.prototype, "scrollIntoView");
    history.replaceState(null, "", "/");
    expect(unexpectedRequests, "Unexpected requests caught by the UI").toEqual(
      [],
    );
  });
}

export const change = (name: string, value: string) =>
  fireEvent.change(screen.getByLabelText(name), { target: { value } });
export const click = (name: string | RegExp) =>
  fireEvent.click(screen.getByRole("button", { name }));
/** Configure exact read routes and record any unconfigured request as a teardown failure. */
export function readReplies(
  get: ReturnType<typeof workspace>["get"],
  routes: Record<string, unknown>,
) {
  get.mockImplementation(async (path) => {
    if (!Object.hasOwn(routes, String(path)))
      return unexpectedRequest("Unconfigured screen GET route");
    return etagReply(routes[String(path)]);
  });
}
/** Open and confirm a screen command while flushing already-settled React updates. */
export async function confirmCommand(name: string) {
  click(name);
  const dialog = await screen.findByRole("dialog");
  // Flush settled command updates and effect cleanup before the next action.
  // Deliberately deferred responses remain pending for the calling test.
  await act(async () => {
    fireEvent.click(within(dialog).getByRole("button", { name: "Confirm" }));
  });
}

/** Edit the existing one-value-per-row control through its real buttons and fields. */
export function editValues(label: string, values: string[]) {
  const itemLabel = new Map([
    ["Tags", "Tag"],
    ["Categories", "Category"],
    ["Observation tags", "Observation tag"],
  ]).get(label);
  if (!itemLabel) throw new Error(`Unknown literal-value group: ${label}`);
  const element = screen.getByRole("group", { name: label, hidden: true });
  for (
    let section = element.closest("details");
    section;
    section = section.parentElement?.closest("details") ?? null
  ) {
    if (!section.open) fireEvent.click(section.querySelector("summary")!);
  }
  const group = within(element);
  let remove = group
    .getAllByRole("button")
    .find((item) =>
      item.textContent?.startsWith(`Remove ${itemLabel.toLowerCase()} `),
    );
  while (remove) {
    fireEvent.click(remove);
    remove = group
      .getAllByRole("button")
      .find((item) =>
        item.textContent?.startsWith(`Remove ${itemLabel.toLowerCase()} `),
      );
  }
  for (const [index, value] of values.entries()) {
    fireEvent.click(
      group.getByRole("button", {
        name: `Add ${itemLabel.toLowerCase()}`,
      }),
    );
    fireEvent.change(
      group.getByLabelText(`${itemLabel} ${index + 1}`, { exact: true }),
      { target: { value } },
    );
  }
}
