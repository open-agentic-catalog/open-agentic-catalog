import { testAccess } from "../../tests/unit/access-fixtures";
// @vitest-environment jsdom
import { afterEach, beforeEach, expect, it, vi } from "vitest";
import {
  act,
  cleanup,
  fireEvent,
  render,
  screen,
  waitFor,
  within,
} from "@testing-library/react";
import Service from "./Service";
import type { components } from "../api/schema";
import { screenWorkspace } from "../../tests/unit/screens";
import { job as reindexJob } from "../../tests/unit/responses";
import {
  deferred,
  processSample,
  reply,
  workspace,
  installDialog,
} from "../../tests/unit/fixtures";

type Capabilities = components["schemas"]["Capabilities"];
// The screen iterates limits rather than interpreting individual settings.
// Keep representative numeric limits here; API contracts validate the full set.
const capabilities = {
  resourceKinds: ["agent", "mcp_server", "skill"],
  profiles: {
    ard: ["ard-profile"],
    aiCatalog: ["catalog-profile"],
    agent: ["agent-profile"],
    mcp: ["mcp-profile"],
    skill: ["skill-profile"],
    plugin: ["plugin-profile"],
  },
  operations: ["authoring", "discovery"],
  governance: {
    publisherDomainVerification: "disabled",
    publicationAuthorization: "administrator_approved",
    publisherAuthority: "not_assessed",
    legalRights: "not_assessed",
  },
  search: {
    tier: "core",
    profile: "core-lexical-v1",
    semantic: "disabled",
    degraded: false,
  },
  optionalCapabilities: {
    serviceActivity: "disabled",
    credentialResolution: "unavailable",
    managedMedia: "disabled",
    softwareAssessment: "unavailable",
    webhooks: "enabled",
  },
  limits: { requestBodyBytes: 1048576, publicationConcurrentConstructions: 1 },
  profileBundle: { digest: "sha256:profile-bundle", artifacts: 12 },
  degradations: [],
} satisfies Omit<Capabilities, "limits"> & {
  limits: Partial<Capabilities["limits"]>;
};
const version = {
  ...processSample,
  service: "open-agentic-catalog",
  role: "api",
  version: "unit-build",
  sourceState: "modified",
  commit: "abc123",
  ardProfile: "ard-profile",
  aiCatalogProfile: "catalog-profile",
};

let restoreDialog: () => void;
beforeEach(() => {
  restoreDialog = installDialog();
});
afterEach(() => {
  cleanup();
  vi.useRealTimers();
  vi.restoreAllMocks();
  history.replaceState(null, "", "/");
  restoreDialog();
});

it("keeps a rebuild draft disabled after current rebuild authority is lost", async () => {
  const w = screenWorkspace();
  const access = { ...w.props.access, ...testAccess(["tenant_admin"]) };
  w.get.mockImplementation(async (path) =>
    reply(
      path === "/api/v1/service-information"
        ? version
        : {
            ...capabilities,
            search: { ...capabilities.search, semantic: "enabled" },
          },
    ),
  );
  const view = render(<Service {...w.props} access={access} />);
  fireEvent.change(await screen.findByLabelText("Reason"), {
    target: { value: "Keep this rebuild preparation" },
  });
  view.rerender(
    <Service
      {...w.props}
      access={{
        ...access,
        permissions: access.permissions.filter(
          (permission) => permission !== "operations.reindex",
        ),
      }}
    />,
  );
  const reason = screen.getByLabelText("Reason");
  expect(reason).toHaveProperty("value", "Keep this rebuild preparation");
  expect(reason.matches(":disabled")).toBe(true);
  const send = screen.getByRole("button", {
    name: "Rebuild search index",
  });
  expect(send).toHaveProperty("disabled", true);
  fireEvent.submit(send.closest("form")!);
  expect(w.post).not.toHaveBeenCalled();
  view.rerender(<Service {...w.props} access={access} />);
  expect(screen.getByLabelText("Reason")).toHaveProperty(
    "value",
    "Keep this rebuild preparation",
  );
  expect(screen.getByLabelText("Reason").matches(":disabled")).toBe(false);
});

it.each([false, true])(
  "keeps rebuild input entered during capability refresh, failed=%s",
  async (failed) => {
    const w = screenWorkspace();
    const access = { ...w.props.access, ...testAccess(["tenant_admin"]) };
    const enabled = {
      ...capabilities,
      search: { ...capabilities.search, semantic: "enabled" },
    };
    const pending = deferred<ReturnType<typeof reply<typeof enabled>>>();
    let capabilityReads = 0;
    w.get.mockImplementation(async (path) => {
      if (path === "/api/v1/service-information") return reply(version);
      return ++capabilityReads === 2 ? pending.promise : reply(enabled);
    });
    render(<Service {...w.props} access={access} />);
    await screen.findByLabelText("Reason");
    fireEvent.click(screen.getByRole("button", { name: "Refresh" }));
    await waitFor(() => expect(capabilityReads).toBe(2));
    const reason = screen.getByLabelText("Reason");
    expect(reason.matches(":disabled")).toBe(false);
    fireEvent.change(reason, {
      target: { value: "New input while capabilities load" },
    });
    const send = screen.getByRole("button", {
      name: "Rebuild search index",
    });
    expect(send).toHaveProperty("disabled", true);
    fireEvent.submit(send.closest("form")!);
    expect(w.post).not.toHaveBeenCalled();
    await act(async () => {
      if (failed) pending.reject(new Error("Capability read unavailable"));
      else pending.resolve(reply(enabled));
    });
    expect(screen.getByLabelText("Reason")).toHaveProperty(
      "value",
      "New input while capabilities load",
    );
    expect(
      screen.getByRole("button", { name: "Rebuild search index" }),
    ).toHaveProperty("disabled", failed);
    expect(w.post).not.toHaveBeenCalled();
  },
);

it.each([false, true])(
  "keeps a prepared rebuild through authority loss, uncertain=%s",
  async (uncertain) => {
    const w = screenWorkspace();
    const access = { ...w.props.access, ...testAccess(["tenant_admin"]) };
    w.get.mockImplementation(async (path) =>
      reply(
        path === "/api/v1/service-information"
          ? version
          : {
              ...capabilities,
              search: { ...capabilities.search, semantic: "enabled" },
            },
      ),
    );
    w.post.mockRejectedValueOnce(new Error("Reply lost"));
    const view = render(<Service {...w.props} access={access} />);
    fireEvent.change(await screen.findByLabelText("Reason"), {
      target: { value: "Recover the exact rebuild" },
    });
    fireEvent.click(
      screen.getByRole("button", { name: "Rebuild search index" }),
    );
    if (uncertain) {
      fireEvent.click(screen.getByRole("button", { name: "Confirm" }));
      await screen.findByRole("button", {
        name: "Retry same command",
      });
    }
    view.rerender(
      <Service
        {...w.props}
        access={{
          ...access,
          permissions: access.permissions.filter(
            (permission) => permission !== "operations.reindex",
          ),
        }}
      />,
    );
    const dialog = screen.getByRole("dialog");
    expect(dialog.textContent).toContain("Recover the exact rebuild");
    const send = within(dialog).getByRole("button", {
      name: uncertain ? "Retry same command" : "Confirm",
    });
    expect(send).toHaveProperty("disabled", !uncertain);
    expect(send.className).toBe("warning");
    expect(send.querySelector('svg[aria-hidden="true"]')).toBeTruthy();
    if (uncertain) {
      w.post.mockResolvedValueOnce(reply(reindexJob));
      fireEvent.click(send);
      await screen.findByText("Rebuild request accepted");
      expect(w.post).toHaveBeenCalledTimes(2);
      expect(w.post.mock.calls[1]).toEqual(w.post.mock.calls[0]);
    } else {
      fireEvent.click(send);
      expect(w.post).not.toHaveBeenCalled();
    }
  },
);

it("explains unavailable semantic support without suggesting it can be enabled", async () => {
  const w = workspace();
  w.get.mockResolvedValueOnce(reply(version)).mockResolvedValueOnce(
    reply({
      ...capabilities,
      search: { ...capabilities.search, semantic: "unavailable" },
    }),
  );
  render(<Service {...w.props} />);
  await screen.findByRole("heading", { name: "Capabilities" });
  expect(screen.getByText(/Semantic indexing is unavailable/)).toBeTruthy();
  expect(
    screen.queryByText(
      /enabling this capability requires operator configuration/,
    ),
  ).toBeNull();
});

it("links the operator guides and labels byte, page and expansion limits", async () => {
  const w = workspace();
  w.get.mockResolvedValueOnce(reply(version)).mockResolvedValueOnce(
    reply({
      ...capabilities,
      limits: {
        ...capabilities.limits,
        catalogPageSize: 50,
        skillPackageExpansionRatio: 20,
      },
    }),
  );
  render(
    <Service
      {...w.props}
      runtime={{ ...w.props.runtime, documentationUrl: "/docs/" }}
    />,
  );
  expect(
    (
      await screen.findByRole("link", { name: /Service observations guide/ })
    ).getAttribute("href"),
  ).toBe("/docs/operations/observability/");
  expect(
    screen
      .getByRole("link", { name: /Search indexing guide/ })
      .getAttribute("href"),
  ).toBe("/docs/operations/hybrid-search/");
  await screen.findByRole("heading", { name: "Capabilities" });
  expect(screen.getByText("50 items per page")).toBeTruthy();
  expect(screen.getByText("20:1")).toBeTruthy();
});

it("focuses a newly opened indexing fragment without another server read", async () => {
  const w = workspace();
  w.get
    .mockResolvedValueOnce(reply(version))
    .mockResolvedValueOnce(reply(capabilities));
  render(<Service {...w.props} />);
  await screen.findByRole("heading", { name: "Capabilities" });
  const target = screen.getByRole("region", { name: "Search indexing" });
  target.scrollIntoView = vi.fn();
  history.replaceState(null, "", "/service#other");
  fireEvent(window, new HashChangeEvent("hashchange"));
  expect(target.scrollIntoView).not.toHaveBeenCalled();
  history.replaceState(null, "", "/service#search-indexing");
  fireEvent(window, new HashChangeEvent("hashchange"));
  expect(document.activeElement).toBe(target);
  expect(target.scrollIntoView).toHaveBeenCalledExactlyOnceWith({
    block: "start",
  });
  expect(w.get).toHaveBeenCalledTimes(2);
});

it.each(["unknown", "healthy", "degraded"] as const)(
  "shows only the reported %s provider observation and copies loaded diagnostics without another read",
  async (providerState) => {
    const w = workspace();
    w.get.mockResolvedValueOnce(reply(version)).mockResolvedValueOnce(
      reply({
        ...capabilities,
        search: { ...capabilities.search, providerState },
      }),
    );
    const writeText = vi.fn().mockResolvedValue(undefined);
    Object.defineProperty(navigator, "clipboard", {
      configurable: true,
      value: { writeText },
    });
    render(<Service {...w.props} />);
    await waitFor(() =>
      expect(
        screen.getByText("Last reported embedding provider status")
          .nextElementSibling?.textContent,
      ).toBe(
        { healthy: "Healthy", degraded: "Degraded", unknown: "Unknown" }[
          providerState
        ],
      ),
    );
    fireEvent.click(
      screen.getByRole("button", { name: "Copy API instance ID" }),
    );
    await waitFor(() =>
      expect(writeText).toHaveBeenCalledWith(version.instanceId),
    );
    fireEvent.click(
      screen.getByRole("button", { name: "Copy support details" }),
    );
    await waitFor(() => expect(writeText).toHaveBeenCalledTimes(2));
    expect(JSON.parse(String(writeText.mock.calls[1]?.[0]))).toMatchObject({
      formatVersion: 1,
      apiBuild: {
        version: "unit-build",
        commit: "abc123",
        sourceState: "modified",
      },
    });
    expect(String(writeText.mock.calls[1]?.[0])).not.toContain("instanceId");
    expect(w.get).toHaveBeenCalledTimes(2);
    expect(w.processInfo).toHaveBeenCalledOnce();
  },
);

it.each(["Reason", "Search index"])(
  "protects a reindex draft edited through %s before Service refresh",
  async (field) => {
    const { props, get, post } = screenWorkspace();
    get.mockImplementation(async (path) =>
      reply(
        path === "/api/v1/service-information"
          ? version
          : {
              ...capabilities,
              search: { ...capabilities.search, semantic: "enabled" },
            },
      ),
    );
    const discard = vi.spyOn(window, "confirm").mockReturnValue(false);
    render(
      <Service
        {...props}
        access={{ ...props.access, ...testAccess(["tenant_admin"]) }}
      />,
    );
    const control = await screen.findByLabelText(field);
    fireEvent.change(control, {
      target: { value: field === "Reason" ? "Keep rebuild reason" : "public" },
    });
    const reads = get.mock.calls.length;
    fireEvent.click(screen.getByRole("button", { name: "Refresh" }));
    expect(discard).toHaveBeenCalledOnce();
    expect(get).toHaveBeenCalledTimes(reads);
    expect(screen.getByLabelText(field)).toHaveProperty(
      "value",
      field === "Reason" ? "Keep rebuild reason" : "public",
    );
    expect(post).not.toHaveBeenCalled();
    discard.mockReturnValue(true);
    fireEvent.click(screen.getByRole("button", { name: "Refresh" }));
    await waitFor(() => expect(get).toHaveBeenCalledTimes(reads + 2));
    expect(await screen.findByLabelText(field)).toHaveProperty(
      "value",
      field === "Reason" ? "" : "control",
    );
  },
);

it.each([
  { admin: true, scoped: false, permitted: true, job: "indexing-job" },
  { admin: true, scoped: true, permitted: false, job: "" },
  { admin: false, scoped: false, permitted: false, job: "" },
])(
  "shows safe indexing status and role/scope-gated administration: %j",
  async ({ admin, scoped, permitted, job }) => {
    const { props, get } = workspace();
    get.mockResolvedValueOnce(reply(version)).mockResolvedValueOnce(
      reply({
        ...capabilities,
        search: {
          ...capabilities.search,
          semantic: "enabled",
          indexing: {
            state: "ready",
            indexed: 3,
            failed: 1,
            ...(job ? { jobId: job } : {}),
          },
        },
      }),
    );
    render(
      <Service
        {...props}
        access={{
          ...props.access,
          ...testAccess(admin ? ["tenant_admin"] : ["reader"]),
          scopeEnforcement: scoped,
          scopes: [],
        }}
      />,
    );
    await screen.findByRole("heading", { name: "Capabilities" });
    expect(
      screen
        .getByRole("heading", { name: "Search indexing" })
        .closest("section")?.id,
    ).toBe("search-indexing");
    expect(
      !!screen.queryByText(/Rebuilding requires operations.reindex permission/),
    ).toBe(!permitted);
    expect(!!screen.queryByText(/3 items indexed/)).toBe(permitted);
    expect(
      !!screen.queryByRole("heading", { name: "Rebuild search index" }),
    ).toBe(permitted);
    expect(
      !!screen.queryByRole("link", { name: "View current indexing Job" }),
    ).toBe(!!job);
  },
);

it("loads build and capability information independently and displays supported limits without edit controls", async () => {
  const { props, get } = workspace();
  const build = deferred<ReturnType<typeof reply<typeof version>>>();
  const caps = deferred<ReturnType<typeof reply<typeof capabilities>>>();
  get.mockImplementation((path) => {
    if (path === "/api/v1/service-information") return build.promise;
    if (path === "/api/v1/capabilities") return caps.promise;
    throw new Error("Unexpected service request");
  });
  render(<Service {...props} />);
  const refresh = screen.getByRole("button", {
    name: "Refresh",
  }) as HTMLButtonElement;
  expect(refresh.disabled).toBe(true);
  expect(
    screen.getByText(/Indexing controls require a successful capability read/),
  ).toBeTruthy();
  await act(async () => build.resolve(reply(version)));
  expect(screen.getByText("unit-build")).toBeTruthy();
  expect(refresh.disabled).toBe(true);
  await act(async () => caps.resolve(reply(capabilities)));
  expect(refresh.disabled).toBe(false);
  expect(screen.getByText(/Semantic indexing is turned off/)).toBeTruthy();
  expect(screen.getByText("Agent, MCP Server, Skill")).toBeTruthy();
  expect(screen.getByText("No")).toBeTruthy();
  expect(
    screen.getByText("Service activity API").nextElementSibling?.textContent,
  ).toBe("Turned off");
  expect(screen.queryByText("serviceActivity")).toBeNull();
  expect(screen.getByText("Webhooks").nextElementSibling?.textContent).toBe(
    "Enabled",
  );
  expect(
    screen.getByText("Credential resolution").nextElementSibling?.textContent,
  ).toBe("Unavailable");
  expect(
    screen.getByText("Publication authorization").nextElementSibling
      ?.textContent,
  ).toBe("Administrator approved");
  fireEvent.click(screen.getByText("Configured limits"));
  expect(screen.getByText("Request body").nextElementSibling?.textContent).toBe(
    "1048576 bytes",
  );
  fireEvent.click(screen.getByText("Supported operation families"));
  expect(screen.getByText("discovery", { selector: "li" })).toBeTruthy();
  expect(screen.getByText("sha256:profile-bundle")).toBeTruthy();
  expect(screen.queryByRole("heading", { name: "Degradations" })).toBeNull();
  expect(
    screen
      .getByRole("link", { name: "Browse API reference" })
      .getAttribute("href"),
  ).toBe("/unit/api-reference");
  expect(screen.queryByRole("textbox")).toBeNull();
  expect(get).toHaveBeenCalledTimes(2);
  for (const call of get.mock.calls)
    expect(call[1]).toEqual({ signal: expect.any(AbortSignal) as unknown });
});

it("refreshes both panels, isolates failures, and renders degradation text without HTML", async () => {
  const { props, get } = workspace();
  get
    .mockResolvedValueOnce(reply(version))
    .mockResolvedValueOnce(reply(capabilities));
  const { container } = render(<Service {...props} />);
  await screen.findByRole("heading", { name: "Capabilities" });
  const hostile = '<script>alert("unsafe")</script>';
  get
    .mockResolvedValueOnce({
      error: { title: "Unavailable", status: 503 },
      response: new Response(null, { status: 503 }),
    })
    .mockResolvedValueOnce(
      reply({
        ...capabilities,
        search: { ...capabilities.search, degraded: true },
        degradations: [{ component: "search", reason: hostile }],
      }),
    );
  fireEvent.click(screen.getByRole("button", { name: "Refresh" }));
  await screen.findByRole("alert");
  await screen.findByRole("heading", { name: "Degradations" });
  expect(screen.queryByRole("heading", { name: "Catalog API" })).toBeNull();
  expect(screen.getByRole("heading", { name: "Capabilities" })).toBeTruthy();
  expect(screen.getByText("Yes")).toBeTruthy();
  expect(screen.getByText(`search: ${hostile}`)).toBeTruthy();
  expect(container.querySelector("script")).toBeNull();
  expect(props.onFailure).toHaveBeenCalledTimes(1);
  get.mockResolvedValueOnce(reply(version));
  fireEvent.click(screen.getByRole("button", { name: "Reload" }));
  await screen.findByRole("heading", { name: "Catalog API" });
  expect(get).toHaveBeenCalledTimes(5);
  expect(get.mock.calls[4]?.[0]).toBe("/api/v1/service-information");

  get
    .mockResolvedValueOnce(reply(version))
    .mockRejectedValueOnce(new Error("transport failure"));
  fireEvent.click(screen.getByRole("button", { name: "Refresh" }));
  await screen.findByRole("alert");
  expect(screen.queryByRole("heading", { name: "Capabilities" })).toBeNull();
  expect(screen.getByRole("heading", { name: "Catalog API" })).toBeTruthy();
  get.mockResolvedValueOnce(reply(capabilities));
  fireEvent.click(screen.getByRole("button", { name: "Reload" }));
  await screen.findByRole("heading", { name: "Capabilities" });
  await waitFor(() => expect(get).toHaveBeenCalledTimes(8));
  expect(get.mock.calls[7]?.[0]).toBe("/api/v1/capabilities");
  expect(screen.queryByRole("heading", { name: "Degradations" })).toBeNull();
});

it.each([
  { anchor: true, buildFirst: true },
  { anchor: true, buildFirst: false },
  { anchor: false, buildFirst: false },
])(
  "aligns only an explicit indexing anchor after both reads settle and leaves refresh alone: %j",
  async ({ anchor, buildFirst }) => {
    history.replaceState(
      null,
      "",
      anchor ? "/service#search-indexing" : "/service",
    );
    const { props, get } = workspace();
    const build = deferred<ReturnType<typeof reply<typeof version>>>();
    const caps = deferred<ReturnType<typeof reply<typeof capabilities>>>();
    get.mockReturnValueOnce(build.promise).mockReturnValueOnce(caps.promise);
    render(<Service {...props} />);
    const section = screen
      .getByRole("heading", { name: "Search indexing" })
      .closest("section")!;
    const scroll = vi.fn();
    section.scrollIntoView = scroll;
    expect(scroll).not.toHaveBeenCalled();
    const finishBuild = () => build.resolve(reply(version));
    const finishCaps = () => caps.resolve(reply(capabilities));
    await act(async () => (buildFirst ? finishBuild() : finishCaps()));
    expect(scroll).not.toHaveBeenCalled();
    await act(async () => (buildFirst ? finishCaps() : finishBuild()));
    expect(scroll).toHaveBeenCalledTimes(anchor ? 1 : 0);
    if (anchor) expect(scroll).toHaveBeenCalledWith({ block: "start" });
    if (anchor) expect(document.activeElement).toBe(section);
    get
      .mockResolvedValueOnce(reply(version))
      .mockResolvedValueOnce(reply(capabilities));
    fireEvent.click(screen.getByRole("button", { name: "Refresh" }));
    await waitFor(() =>
      expect(screen.getByRole("button", { name: "Refresh" })).toHaveProperty(
        "disabled",
        false,
      ),
    );
    expect(scroll).toHaveBeenCalledTimes(anchor ? 1 : 0);
    expect(get).toHaveBeenCalledTimes(4);
  },
);

it("keeps UI and API builds independent and refreshes both", async () => {
  const { props, get, processInfo } = workspace();
  get
    .mockResolvedValueOnce(reply(version))
    .mockResolvedValueOnce(reply(capabilities));
  processInfo.mockResolvedValueOnce({
    ...processSample,
    role: "ui",
    version: "independent-ui",
    commit: "own-ui-sha",
    sourceState: "unknown",
  });
  render(<Service {...props} />);
  await screen.findByText("independent-ui");
  expect(screen.getByText("own-ui-sha")).toBeTruthy();
  expect(screen.getByText("unit-build")).toBeTruthy();
  get
    .mockResolvedValueOnce(reply(version))
    .mockResolvedValueOnce(reply(capabilities));
  fireEvent.click(screen.getByRole("button", { name: "Refresh" }));
  await waitFor(() => expect(processInfo).toHaveBeenCalledTimes(2));
});

it("keeps Catalog information available while retrying only a failed UI process read", async () => {
  const { props, get, processInfo } = workspace();
  get
    .mockResolvedValueOnce(reply(version))
    .mockResolvedValueOnce(reply(capabilities));
  processInfo.mockRejectedValueOnce(new Error("UI build unavailable"));
  render(<Service {...props} />);
  await screen.findByRole("alert");
  expect(screen.getByRole("heading", { name: "Catalog API" })).toBeTruthy();
  expect(screen.queryByRole("heading", { name: "UI host" })).toBeNull();
  const pending = deferred<Awaited<ReturnType<typeof processInfo>>>();
  processInfo.mockReturnValueOnce(pending.promise);
  fireEvent.click(screen.getByRole("button", { name: "Reload" }));
  await waitFor(() => expect(processInfo).toHaveBeenCalledTimes(2));
  expect(screen.queryByRole("alert")).toBeNull();
  expect(screen.queryByRole("heading", { name: "UI host" })).toBeNull();
  expect(screen.getByText("unit-build")).toBeTruthy();
  expect(screen.getByRole("button", { name: "Refresh" })).toHaveProperty(
    "disabled",
    true,
  );
  await act(async () => {
    pending.resolve({
      ...processSample,
      instanceId: "00000000-0000-7000-8000-000000000002",
      role: "ui",
      version: "recovered-ui",
      commit: "own-ui-sha",
      sourceState: "unknown",
    });
  });
  expect(screen.getByText("recovered-ui")).toBeTruthy();
  expect(screen.getByText("own-ui-sha")).toBeTruthy();
  expect(screen.getByText("unit-build")).toBeTruthy();
  expect(get).toHaveBeenCalledTimes(2);
  expect(props.onFailure).toHaveBeenCalledTimes(1);
});

it("replaces one API replica observation coherently while preserving the independent UI process", async () => {
  const { props, get, processInfo } = workspace();
  get
    .mockResolvedValueOnce(reply(version))
    .mockResolvedValueOnce(reply(capabilities));
  render(<Service {...props} />);
  const section = (name: string) =>
    within(screen.getByRole("heading", { name }).closest("section")!);
  await screen.findByText("unit-build");
  expect(
    section("Catalog API").getByText(processSample.instanceId),
  ).toBeTruthy();
  expect(section("Catalog API").getByText("1 minute 30 seconds")).toBeTruthy();
  expect(
    section("UI host").getByText("00000000-0000-7000-8000-000000000002"),
  ).toBeTruthy();
  get
    .mockResolvedValueOnce(
      reply({
        ...version,
        instanceId: "00000000-0000-7000-8000-000000000003",
        startedAt: "2026-09-01T11:00:00Z",
        observedAt: "2026-09-01T10:00:00Z",
        uptimeSeconds: 86401,
        version: "other-replica",
      }),
    )
    .mockResolvedValueOnce(reply(capabilities));
  fireEvent.click(screen.getByRole("button", { name: "Refresh" }));
  await screen.findByText("other-replica");
  expect(
    screen.getByText(/Uptime was measured with the last response/),
  ).toBeTruthy();
  expect(screen.getByText(/Refresh may reach another/)).toBeTruthy();
  expect(screen.getByText(/A process start time is not an/)).toBeTruthy();
  expect(
    section("Catalog API").queryByText(processSample.instanceId),
  ).toBeNull();
  expect(
    section("Catalog API").getByText("00000000-0000-7000-8000-000000000003"),
  ).toBeTruthy();
  expect(section("Catalog API").getByText("1 day 1 second")).toBeTruthy();
  expect(
    section("Catalog API").getByText(
      (_, element) =>
        element?.tagName === "TIME" &&
        element.getAttribute("datetime") === "2026-09-01T11:00:00Z",
    ),
  ).toBeTruthy();
  expect(
    section("UI host").getByText("00000000-0000-7000-8000-000000000002"),
  ).toBeTruthy();
  expect(processInfo).toHaveBeenCalledTimes(2);
  get
    .mockResolvedValueOnce(reply({ ...version, uptimeSeconds: -1 }))
    .mockResolvedValueOnce(reply(capabilities));
  fireEvent.click(screen.getByRole("button", { name: "Refresh" }));
  await screen.findByRole("alert");
  expect(screen.queryByRole("heading", { name: "Catalog API" })).toBeNull();
  expect(screen.getByRole("heading", { name: "UI host" })).toBeTruthy();
  expect(props.onFailure).toHaveBeenCalledTimes(1);
});

it("ages received observations without polling and resets age after Refresh", async () => {
  vi.useFakeTimers();
  const clock = vi.spyOn(performance, "now").mockReturnValue(0);
  const { props, get } = workspace();
  get
    .mockResolvedValueOnce(reply(version))
    .mockResolvedValueOnce(reply(capabilities));
  const view = await act(async () => render(<Service {...props} />));
  expect(screen.getAllByText("Less than a minute ago")).toHaveLength(2);
  clock.mockReturnValue(60000);
  act(() => {
    vi.advanceTimersByTime(60000);
  });
  expect(screen.getAllByText("1 minute ago")).toHaveLength(2);
  clock.mockReturnValue(120000);
  act(() => {
    vi.advanceTimersByTime(60000);
  });
  expect(screen.getAllByText("2 minutes ago")).toHaveLength(2);
  expect(get).toHaveBeenCalledTimes(2);
  get
    .mockResolvedValueOnce(reply(version))
    .mockResolvedValueOnce(reply(capabilities));
  await act(async () =>
    fireEvent.click(screen.getByRole("button", { name: "Refresh" })),
  );
  expect(screen.getAllByText("Less than a minute ago")).toHaveLength(2);
  view.unmount();
  expect(vi.getTimerCount()).toBe(0);
});

it("ages API and UI observations from their independent receipt times", async () => {
  vi.useFakeTimers();
  const clock = vi.spyOn(performance, "now").mockReturnValue(0);
  const { props, get, processInfo } = workspace();
  const pending = deferred<Awaited<ReturnType<typeof processInfo>>>();
  get
    .mockResolvedValueOnce(reply(version))
    .mockResolvedValueOnce(reply(capabilities));
  processInfo.mockReturnValueOnce(pending.promise);
  const view = await act(async () => render(<Service {...props} />));
  const catalog = within(
    screen.getByRole("heading", { name: "Catalog API" }).closest("section")!,
  );
  expect(catalog.getByText("Less than a minute ago")).toBeTruthy();
  expect(screen.queryByRole("heading", { name: "UI host" })).toBeNull();
  clock.mockReturnValue(120000);
  act(() => {
    vi.advanceTimersByTime(120000);
  });
  await act(async () => {
    pending.resolve({
      ...processSample,
      instanceId: "00000000-0000-7000-8000-000000000002",
      role: "ui",
      version: "late-ui",
      commit: "fixture",
      sourceState: "unknown",
    });
  });
  const ui = within(
    screen.getByRole("heading", { name: "UI host" }).closest("section")!,
  );
  expect(catalog.getByText("2 minutes ago")).toBeTruthy();
  expect(ui.getByText("Less than a minute ago")).toBeTruthy();
  // The server's captured uptime does not grow with local observation age.
  expect(catalog.getByText("1 minute 30 seconds")).toBeTruthy();
  expect(ui.getByText("1 minute 30 seconds")).toBeTruthy();
  expect(get).toHaveBeenCalledTimes(2);
  expect(processInfo).toHaveBeenCalledTimes(1);
  view.unmount();
  expect(vi.getTimerCount()).toBe(0);
});

it.each([
  ["unknown", "neutral", "pending", "neutral"],
  ["healthy", "success", "ready", "success"],
  ["degraded", "warning", "building", "info"],
  ["unknown", "neutral", "failed", "danger"],
] as const)(
  "keeps reported provider %s separate from index %s",
  async (providerState, providerTone, state, indexTone) => {
    const w = screenWorkspace();
    w.get.mockImplementation(async (path) =>
      reply(
        path === "/api/v1/service-information"
          ? version
          : {
              ...capabilities,
              search: {
                ...capabilities.search,
                providerState,
                indexing: { state, indexed: 5, failed: 0 },
              },
            },
      ),
    );
    render(
      <Service
        {...w.props}
        access={{ ...w.props.access, ...testAccess(["tenant_admin"]) }}
      />,
    );
    const field = await screen.findByText(
      "Last reported embedding provider status",
    );
    expect(
      field.nextElementSibling
        ?.querySelector("[data-tone]")
        ?.getAttribute("data-tone"),
    ).toBe(providerTone);
    const section = screen.getByRole("region", {
      name: "Catalog index status",
    });
    expect(
      section.querySelector("[data-tone]")?.getAttribute("data-tone"),
    ).toBe(indexTone);
    expect(
      screen
        .getByText("Semantic search")
        .nextElementSibling?.querySelector("[data-tone]")
        ?.getAttribute("data-tone"),
    ).toBe("neutral");
    expect(screen.getByText(/Provider status is the API process/)).toBeTruthy();
    expect(w.post).not.toHaveBeenCalled();
  },
);
