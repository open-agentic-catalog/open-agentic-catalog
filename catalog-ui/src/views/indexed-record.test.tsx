import { testAccess } from "../../tests/unit/access-fixtures";
// @vitest-environment jsdom
import { afterEach, expect, it, vi } from "vitest";
import { useUnsaved } from "./workspace";
import {
  cleanup,
  fireEvent,
  render,
  screen,
  waitFor,
} from "@testing-library/react";
import IndexedRecord from "./IndexedRecord";
import type { components } from "../api/schema";
import { reply, workspace } from "../../tests/unit/fixtures";

const record: components["schemas"]["CatalogSearchResult"] = {
  id: "indexed-1",
  type: "indexed",
  identifier: "https://example.test/agents/a+b",
  versionPresent: false,
  kind: "agent",
  displayName: "External agent",
  tags: [],
  score: 1,
  indexed: {
    observationGroupId: "observation-group-v1:sha256:" + "a".repeat(64),
    normalizedEntryDigest: "sha256:abc",
    sourceIds: ["00000000-0000-7000-8000-000000000010"],
    sourceCount: 1,
    sourcesTruncated: false,
    conflict: false,
    sameSourceConflict: false,
  },
};

afterEach(() => {
  cleanup();
  vi.restoreAllMocks();
  history.replaceState(null, "", "/");
});

it("requires explicit discard before reloading an indexed record with private edited input", async () => {
  const { props, get } = workspace();
  history.replaceState(
    null,
    "",
    `/unit/indexed-record?identifier=${encodeURIComponent(record.identifier)}&versionPresent=false`,
  );
  get.mockResolvedValue(reply(record));
  function EditedView() {
    useUnsaved(true);
    return <IndexedRecord {...props} />;
  }
  render(<EditedView />);
  await screen.findByRole("heading", { name: record.displayName });
  const discard = vi.spyOn(window, "confirm").mockReturnValue(false);
  fireEvent.click(
    screen.getByRole("button", { name: "Reload indexed record" }),
  );
  expect(discard).toHaveBeenCalledOnce();
  expect(get).toHaveBeenCalledOnce();
  discard.mockReturnValue(true);
  fireEvent.click(
    screen.getByRole("button", { name: "Reload indexed record" }),
  );
  await waitFor(() => expect(get).toHaveBeenCalledTimes(2));
});

it("rereads only the applied exact indexed bookmark and retains its last projection after failure", async () => {
  const { props, get } = workspace();
  history.replaceState(
    null,
    "",
    `/unit/indexed-record?identifier=${encodeURIComponent(record.identifier)}&versionPresent=false`,
  );
  get.mockResolvedValue(reply(record));
  render(<IndexedRecord {...props} />);
  await screen.findByRole("heading", { name: record.displayName });
  get.mockRejectedValueOnce(new Error("unavailable"));
  fireEvent.click(
    screen.getByRole("button", { name: "Reload indexed record" }),
  );
  await screen.findByRole("alert");
  expect(
    screen.getByRole("heading", { name: record.displayName }),
  ).toBeTruthy();
  get.mockResolvedValue(
    reply({ ...record, displayName: "Updated external record" }),
  );
  fireEvent.click(screen.getByRole("button", { name: "Reload" }));
  await screen.findByRole("heading", { name: "Updated external record" });
  expect(get).toHaveBeenCalledTimes(3);
  for (const [path, options] of get.mock.calls) {
    expect(path).toBe("/api/v1/catalog/indexed-record");
    expect(options).toMatchObject({
      params: {
        query: { identifier: record.identifier, versionPresent: false },
      },
    });
  }
});

it.each(["identifier", "presence", "version", "origin"])(
  "refuses a well-shaped projection for a different selected %s",
  async (mismatch) => {
    const { props, get } = workspace();
    history.replaceState(
      null,
      "",
      `/unit/indexed-record?identifier=${encodeURIComponent(record.identifier)}&versionPresent=true&version=`,
    );
    const selected = { ...record, versionPresent: true, version: "" };
    const wrong =
      mismatch === "identifier"
        ? { ...selected, identifier: "urn:air:other.test:agent:another" }
        : mismatch === "presence"
          ? { ...selected, versionPresent: false }
          : mismatch === "version"
            ? { ...selected, version: "1" }
            : {
                ...selected,
                type: "live",
                indexed: undefined,
                live: {
                  sourceId: "00000000-0000-7000-8000-000000000010",
                  source: "https://another.test",
                  entry: {},
                },
              };
    get.mockResolvedValueOnce(reply(wrong)).mockResolvedValue(reply(selected));
    render(<IndexedRecord {...props} />);
    await screen.findByRole("alert");
    expect(
      screen.queryByRole("heading", { name: record.displayName }),
    ).toBeNull();
    fireEvent.click(screen.getByRole("button", { name: "Reload" }));
    await screen.findByRole("heading", { name: record.displayName });
    expect(get).toHaveBeenCalledTimes(2);
  },
);

it("reports a malformed bookmark without displaying a fabricated record", async () => {
  const { props, get } = workspace();
  history.replaceState(null, "", "/unit/indexed-record");
  get.mockResolvedValue({
    error: { title: "Invalid query", status: 400 },
    response: new Response(null, { status: 400 }),
  });
  render(<IndexedRecord {...props} />);
  expect((await screen.findByRole("alert")).textContent).toContain(
    "check your input",
  );
  expect(get).toHaveBeenCalledExactlyOnceWith(
    "/api/v1/catalog/indexed-record",
    {
      params: { query: { identifier: "", versionPresent: false } },
      signal: expect.any(AbortSignal) as unknown,
    },
  );
  expect(screen.queryByText("Current indexed projection")).toBeNull();
  expect(props.onFailure).toHaveBeenCalledExactlyOnceWith(
    expect.objectContaining({ status: 400 }),
  );
});

it("refuses a successful record response when the bookmark has no identifier", async () => {
  const { props, get } = workspace();
  history.replaceState(null, "", "/unit/indexed-record");
  get.mockResolvedValue(reply(record));
  render(<IndexedRecord {...props} />);
  await screen.findByRole("alert");
  expect(
    screen.queryByRole("heading", { name: record.displayName }),
  ).toBeNull();
  expect(props.onFailure).toHaveBeenCalledExactlyOnceWith(
    expect.objectContaining({ code: "invalid_api_response" }),
  );
});

it.each([
  [false, undefined, "No version supplied"],
  [true, "", "Empty version"],
  [true, "1.0+build", "1.0+build"],
] as const)(
  "preserves indexed identity with versionPresent=%s and version=%s",
  async (versionPresent, version, label) => {
    const { props, get } = workspace();
    const query = {
      identifier: record.identifier,
      versionPresent,
      ...(version !== undefined ? { version } : {}),
    };
    const url = new URLSearchParams({
      identifier: record.identifier,
      versionPresent: String(versionPresent),
      ...(version !== undefined ? { version } : {}),
    });
    history.replaceState(null, "", `/unit/indexed-record?${url}`);
    get.mockResolvedValue(
      reply({
        ...record,
        versionPresent,
        ...(version !== undefined ? { version } : {}),
      }),
    );
    render(
      <IndexedRecord
        {...props}
        access={{
          ...props.access,
          ...testAccess(["source_admin"]),
          scopeEnforcement: true,
          scopes: ["sources.read"],
        }}
      />,
    );
    await screen.findByRole("heading", { name: record.displayName });
    const currentRecordGuidance = screen.getByText(
      /This link shows current source metadata/,
    );
    expect(currentRecordGuidance.textContent).toContain(
      "not an immutable approval or permission to execute the resource",
    );
    expect(currentRecordGuidance.closest("details")).toBeNull();
    expect(screen.getByText(label, { selector: "dd" })).toBeTruthy();
    expect(screen.getByText("Not supplied")).toBeTruthy();
    expect(screen.getByText("None reported")).toBeTruthy();
    if (versionPresent && version === "") {
      expect(
        screen.queryByRole("link", { name: "Compare source contributions" }),
      ).toBeNull();
    } else {
      const group = new URL(
        screen
          .getByRole("link", { name: "Compare source contributions" })
          .getAttribute("href")!,
        location.origin,
      );
      expect(group.pathname).toBe("/unit/observation-group");
      expect(Object.fromEntries(group.searchParams)).toEqual({
        identifier: record.identifier,
        versionPresent: String(versionPresent),
        ...(version !== undefined ? { version } : {}),
      });
      expect(group.searchParams.has("observationGroupId")).toBe(false);
    }
    expect(get).toHaveBeenCalledExactlyOnceWith(
      "/api/v1/catalog/indexed-record",
      {
        params: { query },
        signal: expect.any(AbortSignal) as unknown,
      },
    );
    expect(
      screen
        .getByRole("link", { name: "Back to Catalog" })
        .getAttribute("href"),
    ).toBe("/unit/catalog");
  },
);

it("shows bounded attribution and conflicts while keeping untrusted metadata inert", async () => {
  const { props, get } = workspace();
  history.replaceState(
    null,
    "",
    `/unit/indexed-record?identifier=${encodeURIComponent(record.identifier)}`,
  );
  const hostile = '<img src=x onerror="alert(1)">';
  get.mockResolvedValue(
    reply({
      ...record,
      displayName: hostile,
      description: hostile,
      publisher: "External publisher",
      indexed: {
        ...record.indexed,
        sourceCount: 20,
        sourcesTruncated: true,
        conflict: true,
        sameSourceConflict: true,
      },
    }),
  );
  const view = render(<IndexedRecord {...props} />);
  const { container } = view;
  await screen.findByRole("heading", { name: hostile });
  expect(container.querySelector("img")).toBeNull();
  expect(
    screen.getByText(
      "Reported sources: 20. Source IDs shown: 1. Additional source IDs were omitted from this bounded projection.",
    ),
  ).toBeTruthy();
  expect(screen.getByText("Conflicting current variants")).toBeTruthy();
  expect(
    screen.getByText("Entry digest (not a document checksum)"),
  ).toBeTruthy();
  expect(screen.getByText("sha256:abc")).toBeTruthy();
  expect(
    screen
      .getAllByRole("heading", { level: 3 })
      .map((heading) => heading.textContent),
  ).toEqual([
    "Overview",
    "Source provenance",
    "Use this resource",
    "Technical details and evidence",
  ]);

  const kind = screen.getByText("Kind", { selector: "dt" }).nextElementSibling!;
  expect(kind.textContent).toBe("Agent");
  expect(kind.querySelector(".catalog-kind-badge.agent")).toBeTruthy();
  expect(kind.querySelector('svg[aria-hidden="true"]')).toBeTruthy();
  const handoff = screen.getByRole("region", {
    name: "External resource handoff",
  });
  expect(
    handoff.compareDocumentPosition(
      screen.getByText("Current indexed projection"),
    ) & Node.DOCUMENT_POSITION_FOLLOWING,
  ).toBeTruthy();
  expect(
    screen.getByText(/Authentication requirements are not established/),
  ).toBeTruthy();
  expect(
    screen.getByText("External publisher", { selector: "dd" }),
  ).toBeTruthy();
  fireEvent.click(screen.getByText("Current indexed projection"));
  expect(
    screen.getByText("Current indexed projection").parentElement?.textContent,
  ).toContain("00000000-0000-7000-8000-000000000010");
  expect(
    screen.getByText(
      "A source reports multiple current variants for this identity.",
    ),
  ).toBeTruthy();
  for (const access of [
    props.access,
    {
      ...props.access,
      ...testAccess(["source_admin" as const]),
      scopeEnforcement: true,
      scopes: [],
    },
    {
      ...props.access,
      ...testAccess(["tenant_admin" as const]),
      scopeEnforcement: true,
      scopes: [],
    },
  ]) {
    view.rerender(<IndexedRecord {...props} access={access} />);
    expect(
      screen.queryByRole("link", { name: "Compare source contributions" }),
    ).toBeNull();
    expect(screen.queryByRole("link", { name: /Open source/ })).toBeNull();
  }
  view.rerender(
    <IndexedRecord
      {...props}
      access={{
        ...props.access,
        ...testAccess(["tenant_admin"]),
        scopeEnforcement: true,
        scopes: ["sources.read"],
      }}
    />,
  );
  expect(
    screen.getByRole("link", { name: /Open source/ }).getAttribute("href"),
  ).toBe(`/unit/sources/${record.indexed!.sourceIds[0]}`);
  expect(get).toHaveBeenCalledTimes(1);
});

it("drops unavailable bookmark data on identity change and reloads the exact new identity", async () => {
  const { props, get } = workspace();
  history.replaceState(
    null,
    "",
    `/unit/indexed-record?identifier=${encodeURIComponent(record.identifier)}`,
  );
  get
    .mockResolvedValueOnce(reply(record))
    .mockResolvedValueOnce({
      error: { title: "Not found", status: 404, code: "not_found" },
      response: new Response(null, { status: 404 }),
    })
    .mockResolvedValue(
      reply({
        ...record,
        identifier: "second",
        versionPresent: true,
        version: "",
        displayName: "Restored record",
        indexed: record.indexed,
      }),
    );
  const view = render(<IndexedRecord {...props} />);
  await screen.findByRole("heading", { name: record.displayName });
  history.replaceState(
    null,
    "",
    "/unit/indexed-record?identifier=second&versionPresent=true&version=",
  );
  view.rerender(<IndexedRecord {...props} />);
  await screen.findByRole("alert");
  expect(
    screen.queryByRole("heading", { name: record.displayName }),
  ).toBeNull();
  expect(props.onFailure).toHaveBeenCalledTimes(1);
  fireEvent.click(screen.getByRole("button", { name: "Reload" }));
  await screen.findByRole("heading", { name: "Restored record" });
  await waitFor(() => expect(get).toHaveBeenCalledTimes(3));
  expect(get.mock.calls[2]?.[1]).toMatchObject({
    params: {
      query: { identifier: "second", versionPresent: true, version: "" },
    },
  });
  expect(screen.getByText("None reported")).toBeTruthy();
});

it("shows missing attribution honestly and rejects a malformed source reference before handoff", async () => {
  const { props, get } = workspace();
  history.replaceState(
    null,
    "",
    `/unit/indexed-record?identifier=${encodeURIComponent(record.identifier)}&versionPresent=false`,
  );
  get
    .mockResolvedValueOnce(
      reply({
        ...record,
        indexed: { ...record.indexed, sourceIds: ["../sources"] },
      }),
    )
    .mockResolvedValue(
      reply({
        ...record,
        indexed: {
          ...record.indexed,
          sourceIds: [],
          sourceCount: 3,
          sourcesTruncated: true,
        },
      }),
    );
  render(<IndexedRecord {...props} />);
  await screen.findByRole("alert");
  expect(
    screen.queryByRole("region", { name: "External resource handoff" }),
  ).toBeNull();
  expect(
    screen.queryByRole("region", { name: "Indexed source provenance" }),
  ).toBeNull();
  fireEvent.click(screen.getByRole("button", { name: "Reload" }));
  await screen.findByRole("heading", { name: record.displayName });
  expect(screen.getByText("No source IDs supplied.")).toBeTruthy();
  expect(screen.queryByRole("button", { name: "Copy Source ID" })).toBeNull();
  expect(
    screen.getByRole("button", { name: "Copy Observation group ID" }),
  ).toBeTruthy();
  expect(
    screen.queryByRole("link", { name: "Compare source contributions" }),
  ).toBeNull();
  expect(get).toHaveBeenCalledTimes(2);
  expect(props.onFailure).toHaveBeenCalledOnce();
});

it("binds a detail read to the captured route while browser location has already moved", async () => {
  const { props, get } = workspace();
  history.replaceState(null, "", "/unit/catalog");
  get.mockResolvedValue(reply(record));
  render(
    <IndexedRecord
      {...props}
      search={`?identifier=${encodeURIComponent(record.identifier)}&versionPresent=false`}
      restoreSearch
    />,
  );
  await screen.findByRole("heading", { name: record.displayName });
  expect(get).toHaveBeenCalledExactlyOnceWith(
    "/api/v1/catalog/indexed-record",
    expect.objectContaining({
      params: {
        query: { identifier: record.identifier, versionPresent: false },
      },
    }),
  );
});

it("accepts an explicitly selected empty version omitted from the current response", async () => {
  const { props, get } = workspace();
  const empty = { ...record, versionPresent: true };
  get.mockResolvedValue(reply(empty));
  render(
    <IndexedRecord
      {...props}
      search={`?identifier=${encodeURIComponent(record.identifier)}&versionPresent=true&version=`}
    />,
  );
  await screen.findByRole("heading", { name: record.displayName });
  expect(screen.getByText("Empty version")).toBeTruthy();
  expect(screen.queryByRole("alert")).toBeNull();
});

it("keeps saved-search return links and known external sections in the current record", async () => {
  const { props, get } = workspace();
  history.replaceState(
    null,
    "",
    `/unit/indexed-record?identifier=${encodeURIComponent(record.identifier)}&versionPresent=false`,
  );
  get.mockResolvedValue(reply(record));
  render(
    <IndexedRecord
      {...props}
      returnPath="/unit/saved-items/search-one"
      returnLabel="saved search"
      restoreSearch
    />,
  );
  await screen.findByRole("heading", { name: record.displayName });
  expect(
    screen
      .getByRole("link", { name: "Return to saved search" })
      .getAttribute("href"),
  ).toBe("/unit/saved-items/search-one");
  expect(
    screen.getByRole("navigation", { name: "Record sections" }),
  ).toBeTruthy();
  expect(screen.getByText(/Reload the saved external entry/)).toBeTruthy();
  expect(get).toHaveBeenCalledOnce();
});
