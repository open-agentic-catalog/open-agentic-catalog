import { testAccess } from "./access-fixtures";
// Shared fixtures substitute transport and missing DOM primitives while retaining real component behavior.
import { vi } from "vitest";
import type { Access, Client } from "../../src/api/client";
import type { WorkspaceProps } from "../../src/views/workspace";

export const reader: Access = {
  tenantId: "tenant-a",
  subject: "unit-reader",
  membershipId: "member-a",
  realmId: "realm-a",
  actorType: "human",
  ...testAccess(["reader"]),
  namespaceGrants: [],
  scopeEnforcement: false,
  scopes: [],
};

// Host replies use actual UUID-shaped identities, unlike isolated screen labels.
export const sessionAccess: Access = {
  ...reader,
  tenantId: "00000000-0000-0000-0000-000000000001",
  membershipId: "00000000-0000-0000-0000-000000000002",
  realmId: "00000000-0000-0000-0000-000000000003",
};

/** Settle a request explicitly to test races without sleeps or real clocks. */
export function deferred<T>() {
  let resolve!: (value: T) => void;
  let reject!: (reason: unknown) => void;
  const promise = new Promise<T>((yes, no) => {
    resolve = yes;
    reject = no;
  });
  return { promise, resolve, reject };
}

export const reply = <T>(data: T) => ({ data, response: new Response() });

export const processSample = {
  instanceId: "00000000-0000-7000-8000-000000000001",
  startedAt: "2026-09-01T12:00:00Z",
  observedAt: "2026-09-01T12:01:30Z",
  uptimeSeconds: 90,
};

// Only the typed transport is substituted; components and authorization run.
// Unspecified calls fail rather than returning permissive placeholder data.
export function workspace() {
  const get = vi.fn(async (..._args: unknown[]): Promise<unknown> => {
    if (_args[0] === "/api/v1/access/self")
      return reply({ items: [], authorizationRevision: 1 });
    throw new Error("Unexpected GET in unit test");
  });
  const buildInfo = vi.fn(async () => ({
    version: "ui-unit",
    commit: "ui-commit",
    sourceState: "unknown" as const,
  }));
  const processInfo = vi.fn(async () => ({
    ...processSample,
    instanceId: "00000000-0000-7000-8000-000000000002",
    role: "ui" as const,
    version: "ui-unit",
    commit: "ui-commit",
    sourceState: "unknown" as const,
  }));
  const client = {
    processInfo,
    buildInfo,
    api: { GET: get },
  } as unknown as Client;
  return {
    get,
    buildInfo,
    processInfo,
    props: {
      client,
      access: reader,
      runtime: {
        basePath: "/unit",
        authMode: "development" as const,
        documentationUrl: "",
      },
      id: "",
      onFailure: vi.fn(),
    } satisfies WorkspaceProps,
  };
}

// jsdom has no modal-dialog implementation. This models only the open state;
// focus trapping, Escape and browser navigation remain Playwright assertions.
export function installDialog() {
  const methods = ["show", "showModal", "close"] as const;
  const previous = methods.map((method) =>
    Object.getOwnPropertyDescriptor(HTMLDialogElement.prototype, method),
  );
  for (const method of methods) {
    Object.defineProperty(HTMLDialogElement.prototype, method, {
      configurable: true,
      value(this: HTMLDialogElement) {
        if (method === "close") this.removeAttribute("open");
        else this.setAttribute("open", "");
      },
    });
  }
  return () => {
    methods.forEach((method, index) => {
      const descriptor = previous[index];
      if (descriptor)
        Object.defineProperty(HTMLDialogElement.prototype, method, descriptor);
      else Reflect.deleteProperty(HTMLDialogElement.prototype, method);
    });
  };
}
