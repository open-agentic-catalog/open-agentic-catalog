// @vitest-environment jsdom
// Exact immutable selections remain independent of mutable candidates and external archives.
import { afterEach, expect, it, vi } from "vitest";
import {
  act,
  cleanup,
  fireEvent,
  render,
  screen,
  waitFor,
  within,
} from "@testing-library/react";
import { RevisionComparison, metadataChanges } from "./RevisionComparison";
import { workspace, reply, deferred } from "../../tests/unit/fixtures";
import { administrator } from "../../tests/unit/screens";
import {
  evidence,
  listingRevision,
  representation,
  versionRevision,
} from "../../tests/unit/responses";
import { testAccess } from "../../tests/unit/access-fixtures";
afterEach(() => {
  cleanup();
  vi.restoreAllMocks();
});
function click(name: string) {
  fireEvent.click(screen.getByRole("button", { name }));
}
function choose(name: string, value: string) {
  fireEvent.change(screen.getByLabelText(name), { target: { value } });
}
/** Compare read arguments exactly after checking their opaque cancellation signal. */
function readArguments([path, input]: unknown[]) {
  const { signal, ...options } = input as Record<string, unknown>;
  expect(signal).toBeInstanceOf(AbortSignal);
  return [path, options];
}
const rows = {
  items: [
    { id: "before", revisionNumber: 1, createdBy: "author" },
    { id: "after", revisionNumber: 2, createdBy: "author" },
  ],
};
function fixture(kind: "listing" | "version" = "listing") {
  const f = workspace();
  f.props.access = administrator;
  f.get.mockImplementation(async (path, options) => {
    const opts = options as { params?: { path?: { revisionId?: string } } };
    if (String(path).endsWith("/revisions")) return reply(rows);
    const id = opts.params?.path?.revisionId;
    if (path === "/api/v1/listing-revisions/{revisionId}")
      return reply({
        ...listingRevision,
        id,
        description: id === "before" ? "Old" : "New",
        tags: id === "before" ? ["a", "b"] : ["b", "a"],
      });
    if (path === "/api/v1/version-revisions/{revisionId}")
      return reply({
        namespaceId: "ns-1",
        revision: {
          ...versionRevision,
          id,
          representations: [
            {
              ...representation,
              data: { name: id },
              contentDigest: `sha256:${id}`,
            },
          ],
        },
        evidence,
      });
    throw Error("Unexpected read");
  });
  const props = {
    ...f.props,
    kind,
    ownerId: kind === "listing" ? "listing-1" : "version-1",
    namespaceId: "ns-1",
  };
  return { ...f, props };
}
async function select() {
  click("Choose revisions to compare");
  await screen.findAllByRole("option", { name: "Revision 1 / before" });
  choose("Before revision", "before");
  choose("After revision", "after");
  click("Compare selected revisions");
  await screen.findByLabelText("Compared revisions");
}

async function retainedFixture(
  bytes = new TextEncoder().encode("private document"),
) {
  const f = fixture("version");
  const hash = new Uint8Array(await crypto.subtle.digest("SHA-256", bytes));
  const id = Array.from(hash, (b) => b.toString(16).padStart(2, "0")).join("");
  const artifact = {
    data: bytes.buffer,
    response: new Response(null, {
      headers: {
        "Content-Type": "text/plain",
        "Content-Digest": `sha-256=:${btoa(String.fromCharCode(...hash))}:`,
      },
    }),
  };
  const original = f.get.getMockImplementation()!;
  f.get.mockImplementation(async (path, options) => {
    if (path === "/api/v1/artifacts/{artifactId}/content") return artifact;
    const response = (await original(path, options)) as ReturnType<
      typeof reply
    >;
    if (path !== "/api/v1/version-revisions/{revisionId}") return response;
    const body = response.data as { revision: typeof versionRevision };
    return {
      ...response,
      data: {
        ...body,
        revision: {
          ...body.revision,
          representations: [
            {
              ...representation,
              artifactId: id,
              contentDigest: `sha256:${id}`,
              size: bytes.byteLength,
            },
          ],
        },
      },
    };
  });
  return { ...f, artifact };
}

it("does not read content when only revision metadata is permitted", async () => {
  const f = await retainedFixture();
  render(
    <RevisionComparison
      {...f.props}
      access={{
        ...administrator,
        permissions: ["resource.read", "resource.read_history"],
        availablePermissions: ["resource.read", "resource.read_history"],
      }}
    />,
  );
  expect(
    screen.getByText(
      /Comparison needs resource.read_history and resource.read_content/,
    ),
  ).toBeTruthy();
  expect(
    screen.queryByRole("button", { name: "Choose revisions to compare" }),
  ).toBeNull();
  expect(
    screen.queryByRole("button", { name: "Inspect selected documents" }),
  ).toBeNull();
  expect(
    f.get.mock.calls.some(([path]) => String(path).includes("/artifacts/")),
  ).toBe(false);
});

it.each(["digest", "utf8"])(
  "refuses %s failures before showing documents or change navigation",
  async (failure) => {
    const f = await retainedFixture(
      failure === "utf8" ? new Uint8Array([255]) : undefined,
    );
    if (failure === "digest")
      f.artifact.response.headers.set("Content-Digest", "sha-256=:wrong:");
    render(<RevisionComparison {...f.props} />);
    await select();
    click("Inspect selected documents");
    const alert = await screen.findByRole("alert");
    expect(alert.textContent).toContain(
      failure === "digest"
        ? "Document integrity could not be verified."
        : "A selected document could not be decoded as UTF-8.",
    );
    expect(
      screen.queryByRole("region", { name: "Document changes" }),
    ).toBeNull();
    expect(
      screen.queryByText("Exact retained documents are identical."),
    ).toBeNull();
    expect(screen.queryByText("private document")).toBeNull();
    expect(
      f.get.mock.calls.filter(([path]) => String(path).includes("/artifacts/")),
    ).toHaveLength(1);
  },
);

it("aborts and discards a pending artifact after access changes", async () => {
  const f = await retainedFixture();
  const view = render(<RevisionComparison {...f.props} />);
  await select();
  const pending = deferred<typeof f.artifact>();
  const original = f.get.getMockImplementation()!;
  let signal: AbortSignal | undefined;
  f.get.mockImplementation((path, options) => {
    if (path !== "/api/v1/artifacts/{artifactId}/content")
      return original(path, options);
    signal = (options as { signal: AbortSignal }).signal;
    return pending.promise;
  });
  click("Inspect selected documents");
  await waitFor(() => expect(signal).toBeInstanceOf(AbortSignal));
  view.rerender(
    <RevisionComparison
      {...f.props}
      access={{ ...administrator, subject: "replacement", ...testAccess([]) }}
    />,
  );
  expect(signal!.aborted).toBe(true);
  await act(async () => {
    pending.resolve(f.artifact);
  });
  expect(screen.queryByRole("region", { name: "Document changes" })).toBeNull();
  expect(screen.queryByText("private document")).toBeNull();
  expect(f.props.onFailure).not.toHaveBeenCalled();
});

it("discards both documents when the second verification fails and recovers on explicit reload", async () => {
  const f = await retainedFixture();
  const original = f.get.getMockImplementation()!;
  let artifactCalls = 0;
  f.get.mockImplementation(async (path, options) => {
    const response = await original(path, options);
    if (
      path !== "/api/v1/artifacts/{artifactId}/content" ||
      ++artifactCalls !== 2
    )
      return response;
    const headers = new Headers(f.artifact.response.headers);
    headers.set("Content-Digest", "sha-256=:wrong:");
    return { ...f.artifact, response: new Response(null, { headers }) };
  });
  render(<RevisionComparison {...f.props} />);
  await select();
  click("Inspect selected documents");
  expect((await screen.findByRole("alert")).textContent).toContain(
    "Document integrity could not be verified.",
  );
  expect(artifactCalls).toBe(2);
  expect(screen.queryByText("private document")).toBeNull();
  expect(screen.queryByRole("region", { name: "Document changes" })).toBeNull();
  f.get.mockImplementation(original);
  click("Reload");
  await screen.findByText("Exact retained documents are identical.");
  expect(screen.queryByRole("alert")).toBeNull();
  expect(screen.getAllByRole("group", { name: "Text document" })).toHaveLength(
    2,
  );
});

it("retains transport and authorization error handling without exposing raw dependency text", async () => {
  const f = await retainedFixture();
  const original = f.get.getMockImplementation()!;
  render(<RevisionComparison {...f.props} />);
  await select();
  f.get.mockRejectedValueOnce(new Error("private transport detail"));
  click("Inspect selected documents");
  expect((await screen.findByRole("alert")).textContent).toContain(
    "This page could not be loaded.",
  );
  expect(screen.queryByText(/private transport detail/)).toBeNull();
  expect(f.props.onFailure).toHaveBeenCalledWith(
    expect.objectContaining({ message: "private transport detail" }),
  );
  f.get.mockResolvedValueOnce({
    error: { code: "FORBIDDEN" },
    response: new Response(null, { status: 403 }),
  });
  click("Reload");
  expect((await screen.findByRole("alert")).textContent).toContain(
    "Your current access does not permit this action.",
  );
  expect(f.props.onFailure).toHaveBeenCalledWith(
    expect.objectContaining({ status: 403 }),
  );
  f.get.mockImplementation(original);
  click("Reload");
  await screen.findByText("Exact retained documents are identical.");
});

it.each(["Before", "After"])(
  "clears inspected documents when the %s representation selection changes, without an implicit read",
  async (side) => {
    const f = await retainedFixture();
    const original = f.get.getMockImplementation()!;
    f.get.mockImplementation(async (path, options) => {
      const response = (await original(path, options)) as ReturnType<
        typeof reply
      >;
      if (path !== "/api/v1/version-revisions/{revisionId}") return response;
      const body = response.data as { revision: typeof versionRevision };
      return {
        ...response,
        data: {
          ...body,
          revision: {
            ...body.revision,
            representations: [
              ...body.revision.representations,
              { ...representation, id: "other-document" },
            ],
          },
        },
      };
    });
    render(<RevisionComparison {...f.props} />);
    await select();
    click("Inspect selected documents");
    await screen.findByText("Exact retained documents are identical.");
    const reads = f.get.mock.calls.length;
    choose(`${side} representation`, "1");
    expect(
      screen.queryByRole("region", { name: "Document changes" }),
    ).toBeNull();
    expect(
      screen.queryByText("Exact retained documents are identical."),
    ).toBeNull();
    expect(screen.queryByText("private document")).toBeNull();
    expect(f.get.mock.calls).toHaveLength(reads);
  },
);
it("compares semantic fields with additions, removals and ordered arrays", () => {
  expect(
    metadataChanges(
      { gone: "x", tags: ["a", "b"], same: "fixed" },
      { added: "y", tags: ["b", "a"], same: "fixed" },
    ),
  ).toEqual([
    { field: "added", before: undefined, after: "y", change: "Added" },
    { field: "gone", before: "x", after: undefined, change: "Removed" },
    { field: "tags", before: ["a", "b"], after: ["b", "a"], change: "Changed" },
  ]);
});
it("loads only explicit bounded choices and compares exact Listing IDs without mutations", async () => {
  const f = fixture();
  render(<RevisionComparison {...f.props} />);
  expect(f.get).not.toHaveBeenCalled();
  await select();
  expect(
    screen.getByRole("heading", { name: "Changed: description" }),
  ).toBeTruthy();
  expect(screen.getByRole("heading", { name: "Changed: tags" })).toBeTruthy();
  expect(f.get).toHaveBeenCalledWith(
    "/api/v1/catalog-listings/{listingId}/revisions",
    expect.objectContaining({
      params: { path: { listingId: "listing-1" }, query: { limit: 25 } },
    }),
  );
  choose("After revision", "before");
  click("Compare selected revisions");
  await screen.findByText("No metadata changes.");
  click("Close comparison choices");
  expect(screen.queryByLabelText("Before revision")).toBeNull();
});
it.each(["listing", "version"] as const)(
  "keeps later %s history choices bound to the selected parent without eager content reads",
  async (kind) => {
    const f = fixture(kind);
    const original = f.get.getMockImplementation()!;
    const historyPath =
      kind === "listing"
        ? "/api/v1/catalog-listings/{listingId}/revisions"
        : "/api/v1/resource-versions/{versionId}/revisions";
    const parent =
      kind === "listing"
        ? { listingId: "listing-1" }
        : { versionId: "version-1" };
    const cursor = "next-owned-history-page";
    const mutate = vi.fn(() => {
      throw new Error("Revision comparison must not mutate state");
    });
    Object.assign(f.props.client.api, {
      POST: mutate,
      PATCH: mutate,
      PUT: mutate,
      DELETE: mutate,
    });
    f.get.mockImplementation((path, options) => {
      if (path !== historyPath) return original(path, options);
      const query = (options as { params: { query: { cursor?: string } } })
        .params.query;
      return Promise.resolve(
        query.cursor
          ? reply({ items: [rows.items[1]] })
          : reply({ items: [rows.items[0]], nextCursor: cursor }),
      );
    });
    render(<RevisionComparison {...f.props} fixedAfter="after" />);
    expect(f.get).not.toHaveBeenCalled();
    click("Choose revisions to compare");
    await screen.findByRole("option", { name: "Revision 1 / before" });
    choose("Before revision", "before");
    fireEvent.click(
      within(
        screen.getByRole("group", {
          name: "Before revision choices pagination",
        }),
      ).getByRole("button", { name: "Next page" }),
    );
    await screen.findByRole("option", { name: "Revision 2 / after" });
    expect(screen.getByLabelText("Before revision")).toHaveProperty(
      "value",
      "before",
    );
    expect(f.get.mock.calls.map(readArguments)).toEqual([
      [
        historyPath,
        {
          params: { path: parent, query: { limit: 25 } },
        },
      ],
      [
        historyPath,
        {
          params: { path: parent, query: { limit: 25, cursor } },
        },
      ],
    ]);
    expect(mutate).not.toHaveBeenCalled();
    click("Compare selected revisions");
    await screen.findByLabelText("Compared revisions");
    expect(
      f.get.mock.calls
        .slice(2)
        .map(([path, options]) => [
          path,
          (options as { params: { path: { revisionId: string } } }).params.path,
        ]),
    ).toEqual([
      [`/api/v1/${kind}-revisions/{revisionId}`, { revisionId: "before" }],
      [`/api/v1/${kind}-revisions/{revisionId}`, { revisionId: "after" }],
    ]);
    expect(mutate).not.toHaveBeenCalled();
  },
);
it("fixes the review after-side to submitted evidence and explains decoded document limits", async () => {
  const f = fixture("version");
  render(<RevisionComparison {...f.props} fixedAfter="after" />);
  click("Choose revisions to compare");
  await screen.findAllByRole("option", { name: "Revision 1 / before" });
  choose("Before revision", "before");
  expect(screen.queryByLabelText("After revision")).toBeNull();
  click("Compare selected revisions");
  await screen.findByLabelText("Compared revisions");
  expect(screen.getByText(/After is fixed to the submitted/)).toBeTruthy();
  click("Inspect selected documents");
  await screen.findByText(/Lossless document comparison is unavailable/);
  expect(
    f.get.mock.calls.filter(
      ([p]) => p === "/api/v1/artifacts/{artifactId}/content",
    ),
  ).toHaveLength(0);
  choose("Before representation", "0");
  choose("After representation", "0");
});
it.each(["listing", "version"] as const)(
  "rejects mismatched %s baselines and permits explicit retry",
  async (kind) => {
    const f = fixture(kind);
    const original = f.get.getMockImplementation()!;
    f.get.mockImplementation(async (path, options) => {
      const r = (await original(path, options)) as {
        data: Record<string, unknown>;
        response: Response;
      };
      if (String(path).includes("revisions/{revisionId}"))
        return {
          ...r,
          data:
            kind === "listing"
              ? { ...r.data, listingId: "another" }
              : { ...r.data, namespaceId: "another" },
        };
      return r;
    });
    render(<RevisionComparison {...f.props} />);
    click("Choose revisions to compare");
    await screen.findAllByRole("option", { name: "Revision 1 / before" });
    choose("Before revision", "before");
    choose("After revision", "after");
    click("Compare selected revisions");
    await screen.findByRole("alert");
    expect(screen.queryByLabelText("Compared revisions")).toBeNull();
    f.get.mockImplementation(original);
    click("Reload");
    await screen.findByLabelText("Compared revisions");
  },
);
it("clears an obsolete identity's comparison and fences its pending read", async () => {
  const f = fixture();
  const view = render(<RevisionComparison {...f.props} />);
  const pending = deferred<ReturnType<typeof reply>>();
  const original = f.get.getMockImplementation()!;
  f.get.mockImplementation((p, o) =>
    p === "/api/v1/listing-revisions/{revisionId}"
      ? pending.promise
      : original(p, o),
  );
  click("Choose revisions to compare");
  await screen.findAllByRole("option", { name: "Revision 1 / before" });
  choose("Before revision", "before");
  choose("After revision", "after");
  click("Compare selected revisions");
  view.rerender(
    <RevisionComparison
      {...f.props}
      access={{ ...administrator, ...testAccess([]) }}
    />,
  );
  await act(async () =>
    pending.resolve(reply({ ...listingRevision, id: "before" })),
  );
  expect(screen.queryByLabelText("Compared revisions")).toBeNull();
  expect(
    screen.queryByRole("button", { name: "Choose revisions to compare" }),
  ).toBeNull();
});
it.each([false, true])(
  "compares verified raw text and refuses oversized documents: %s",
  async (oversized) => {
    const f = fixture("version");
    const text = oversized
      ? "x".repeat(2 * 1024 * 1024 + 1)
      : '{ "n":9007199254740993123456789, "literal":"<script>read only</script>" }';
    const bytes = new TextEncoder().encode(text);
    const hash = new Uint8Array(await crypto.subtle.digest("SHA-256", bytes));
    const id = Array.from(hash, (b) => b.toString(16).padStart(2, "0")).join(
      "",
    );
    const original = f.get.getMockImplementation()!;
    f.get.mockImplementation(async (p, o) => {
      if (p === "/api/v1/artifacts/{artifactId}/content")
        return {
          data: bytes.buffer,
          response: new Response(null, {
            headers: {
              "Content-Type": "application/json",
              "Content-Digest": `sha-256=:${btoa(String.fromCharCode(...hash))}:`,
            },
          }),
        };
      const r = (await original(p, o)) as { data: unknown; response: Response };
      if (p === "/api/v1/version-revisions/{revisionId}") {
        const body = r.data as { revision: typeof versionRevision };
        return {
          ...r,
          data: {
            ...body,
            revision: {
              ...body.revision,
              representations: [{ ...representation, artifactId: id }],
            },
          },
        };
      }
      return r;
    });
    render(<RevisionComparison {...f.props} />);
    await select();
    expect(
      f.get.mock.calls.filter(
        ([p]) => p === "/api/v1/artifacts/{artifactId}/content",
      ),
    ).toHaveLength(0);
    click("Inspect selected documents");
    if (oversized) {
      expect((await screen.findByRole("alert")).textContent).toContain(
        "This document exceeds the 2 MiB comparison text limit.",
      );
      expect(
        screen.queryByText("Exact retained documents are identical."),
      ).toBeNull();
      expect(screen.queryByLabelText("JSON document")).toBeNull();
      expect(
        f.get.mock.calls.filter(
          ([path]) => path === "/api/v1/artifacts/{artifactId}/content",
        ),
      ).toHaveLength(1);
      return;
    }
    await screen.findByText("Exact retained documents are identical.");
    await waitFor(() =>
      expect(screen.getAllByLabelText("JSON document")).toHaveLength(2),
    );
    expect(screen.getAllByLabelText("JSON document")[0]!.textContent).toBe(
      text,
    );
    expect(document.querySelector("script")).toBeNull();
  },
);

it.each([
  {
    difference: "whitespace",
    before: '{"n":1.0,"literal":"<script>read only</script>"}',
    after: '{ "n":1.0, "literal":"<script>read only</script>" }',
  },
  {
    difference: "numeric token spelling",
    before: '{"n":1.0,"literal":"<script>read only</script>"}',
    after: '{"n":1e0,"literal":"<script>read only</script>"}',
  },
  {
    difference: "leading BOM",
    before: '\uFEFF{"n":1.0,"literal":"<script>read only</script>"}',
    after: '{"n":1.0,"literal":"<script>read only</script>"}',
  },
  {
    difference: "CRLF line endings",
    before: '{\r\n"n":1.0\r\n}',
    after: '{\n"n":1.0\n}',
  },
  {
    difference: "detailed-work refusal",
    before: '{"n":1.0,"padding":"' + "x".repeat(65536) + '"}',
    after: '{"n":1e0,"padding":"' + "x".repeat(65536) + '"}',
  },
])(
  "preserves different exact documents despite equal decoded JSON: $difference",
  async ({ before, after }) => {
    const f = fixture("version");
    expect(JSON.parse(before.replace(/^\uFEFF/, ""))).toEqual(
      JSON.parse(after),
    );
    const stored = await Promise.all(
      [before, after].map(async (text) => {
        const bytes = new TextEncoder().encode(text);
        const hash = new Uint8Array(
          await crypto.subtle.digest("SHA-256", bytes),
        );
        return {
          bytes,
          id: Array.from(hash, (byte) =>
            byte.toString(16).padStart(2, "0"),
          ).join(""),
          digest: `sha-256=:${btoa(String.fromCharCode(...hash))}:`,
        };
      }),
    );
    expect(stored[0]!.id).not.toBe(stored[1]!.id);
    const original = f.get.getMockImplementation()!;
    const mutate = vi.fn(() => {
      throw new Error("Inspecting retained bytes must not mutate state");
    });
    Object.assign(f.props.client.api, {
      POST: mutate,
      PATCH: mutate,
      PUT: mutate,
      DELETE: mutate,
    });
    f.get.mockImplementation(async (path, options) => {
      if (path === "/api/v1/artifacts/{artifactId}/content") {
        const id = (options as { params: { path: { artifactId: string } } })
          .params.path.artifactId;
        const content = stored.find((item) => item.id === id);
        if (!content) throw new Error("Unexpected retained document");
        return {
          data: content.bytes.buffer,
          response: new Response(null, {
            headers: {
              "Content-Type": "application/json",
              "Content-Digest": content.digest,
            },
          }),
        };
      }
      const result = (await original(path, options)) as {
        data: unknown;
        response: Response;
      };
      if (path !== "/api/v1/version-revisions/{revisionId}") return result;
      const id = (options as { params: { path: { revisionId: string } } })
        .params.path.revisionId;
      const content = stored[id === "before" ? 0 : 1]!;
      const body = result.data as { revision: typeof versionRevision };
      return {
        ...result,
        data: {
          ...body,
          revision: {
            ...body.revision,
            representations: [
              {
                ...representation,
                id: "document-" + id,
                artifactId: content.id,
                contentDigest: "sha256:" + content.id,
                size: content.bytes.byteLength,
              },
            ],
          },
        },
      };
    });
    render(<RevisionComparison {...f.props} />);
    await select();
    const contentReads = () =>
      f.get.mock.calls.filter(
        ([path]) => path === "/api/v1/artifacts/{artifactId}/content",
      );
    expect(contentReads()).toEqual([]);
    expect(mutate).not.toHaveBeenCalled();
    click("Inspect selected documents");
    await screen.findByText(
      "Exact retained documents differ, including any whitespace or numeric token changes.",
    );
    const beforePanel = screen.getByRole("heading", {
      name: "Before document",
    }).parentElement!;
    const afterPanel = screen.getByRole("heading", {
      name: "After document",
    }).parentElement!;
    expect(
      within(beforePanel).getByRole("group", {
        name: before.startsWith("\uFEFF") ? "Text document" : "JSON document",
      }).textContent,
    ).toBe(before);
    expect(
      within(afterPanel).getByRole("group", { name: "JSON document" })
        .textContent,
    ).toBe(after);
    expect(contentReads().map(readArguments)).toEqual(
      stored.map((content) => [
        "/api/v1/artifacts/{artifactId}/content",
        {
          params: { path: { artifactId: content.id } },
          parseAs: "arrayBuffer",
        },
      ]),
    );
    expect(document.querySelector("script")).toBeNull();
    if (before.startsWith("\uFEFF")) {
      expect(
        screen.getByRole("region", { name: "Change 1: Removed text" })
          .textContent,
      ).toContain("[BOM]");
      expect(
        screen.queryByText("Exact retained documents are identical."),
      ).toBeNull();
    }
    if (before.length > 65536) {
      expect(
        screen.getByText(/Detailed change navigation is unavailable/),
      ).toBeTruthy();
      expect(screen.queryByRole("button", { name: "Next change" })).toBeNull();
    }
    expect(mutate).not.toHaveBeenCalled();
  },
);

it("treats object-key order as equal while bounding nested metadata", () => {
  expect(
    metadataChanges(
      { extensions: { a: 1, b: [{ x: true, y: false }] } },
      { extensions: { b: [{ y: false, x: true }], a: 1 } },
    ),
  ).toEqual([]);
  let deep: unknown = {};
  for (let i = 0; i < 66; i++) deep = { nested: deep };
  expect(() => metadataChanges({}, { deep })).toThrow("depth");
});

it.each([false, true])(
  "compares only explicitly requested Listing-linked native revisions, refusing a foreign context: %s",
  async (foreign) => {
    const f = fixture();
    const original = f.get.getMockImplementation()!;
    f.get.mockImplementation(async (p, o) => {
      if (p === "/api/v1/version-revisions/{revisionId}") {
        const opts = o as { params: { path: { revisionId: string } } };
        return reply({
          namespaceId: foreign ? "foreign" : "ns-1",
          revision: {
            ...versionRevision,
            id: opts.params.path.revisionId,
            representations: [
              { ...representation, data: { name: "Retained metadata" } },
            ],
          },
          evidence,
        });
      }
      return original(p, o);
    });
    render(<RevisionComparison {...f.props} fixedAfter="after" />);
    click("Choose revisions to compare");
    await screen.findByRole("option", { name: "Revision 1 / before" });
    choose("Before revision", "before");
    click("Compare selected revisions");
    await screen.findByLabelText("Compared revisions");
    expect(
      f.get.mock.calls.filter(
        ([p]) => p === "/api/v1/version-revisions/{revisionId}",
      ),
    ).toHaveLength(0);
    click("Compare linked Version revisions");
    if (foreign) {
      await screen.findByRole("alert");
      expect(screen.queryByText("Before representations")).toBeNull();
    } else {
      await screen.findByRole("heading", { name: "Before representations" });
      expect(
        screen.getByRole("heading", { name: "After representations" }),
      ).toBeTruthy();
      click("Inspect selected documents");
      await screen.findByText(/Lossless document comparison is unavailable/);
    }
    click("Close linked Version comparison");
    expect(
      screen.queryByRole("heading", { name: "Before representations" }),
    ).toBeNull();
  },
);
