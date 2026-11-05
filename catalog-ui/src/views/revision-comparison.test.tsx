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
      await screen.findByRole("alert");
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
