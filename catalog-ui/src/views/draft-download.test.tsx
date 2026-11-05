// @vitest-environment jsdom
import { webcrypto, createHash } from "node:crypto";
import { afterEach, beforeEach, it, expect, vi } from "vitest";
import {
  act,
  fireEvent,
  render,
  screen,
  waitFor,
} from "@testing-library/react";
import { DraftDownload } from "./DraftDownload";
import { editorDraft } from "./draft-editor";
import { newRepresentation } from "./representations";
import { writeDraftFile } from "./draft-file";
import {
  screenLifecycle,
  screenWorkspace,
  administrator,
  click,
  change,
} from "../../tests/unit/screens";
import { deferred } from "../../tests/unit/fixtures";

screenLifecycle("/unit/version-revisions/revision");
const create = vi.fn((_blob: Blob) => "blob:retained-draft");
const revoke = vi.fn();
beforeEach(() => {
  create.mockClear();
  revoke.mockClear();
  vi.stubGlobal("crypto", webcrypto);
  vi.stubGlobal(
    "URL",
    class extends URL {
      static createObjectURL = create;
      static revokeObjectURL = revoke;
    },
  );
});
afterEach(() => vi.unstubAllGlobals());
const id = "00000000-0000-7000-8000-000000000001";
const listing = "00000000-0000-7000-8000-000000000002";
function response(
  source = writeDraftFile(
    editorDraft("agent", "", "", [newRepresentation("agent")]),
  ),
  type = "application/json",
) {
  const data = new TextEncoder().encode(source).buffer;
  return {
    data,
    response: new Response(null, {
      headers: {
        "Content-Type": type,
        "Content-Digest": `sha-256=:${createHash("sha256").update(source).digest("base64")}:`,
      },
    }),
  };
}
it("requires current metadata and content permission, independent of history", async () => {
  const { props, get } = screenWorkspace();
  get.mockResolvedValue(response());
  const args = {
    ...props,
    access: {
      ...administrator,
      permissions: [
        "resource.read",
        "resource.read_content",
      ] as typeof administrator.permissions,
    },
    namespaceId: "ns",
    revisionId: id,
  };
  const view = render(<DraftDownload {...args} />);
  expect(get).not.toHaveBeenCalled();
  click("Prepare draft download");
  const link = await screen.findByRole("link", {
    name: "Save catalog-draft.json",
  });
  await waitFor(() => expect(document.activeElement).toBe(link));
  expect(link.getAttribute("download")).toBe("catalog-draft.json");
  expect(get.mock.calls[0]).toMatchObject([
    "/api/v1/version-revisions/{revisionId}/export",
    { params: { path: { revisionId: id } }, parseAs: "arrayBuffer" },
  ]);
  view.rerender(
    <DraftDownload
      {...args}
      access={{ ...args.access, permissions: ["resource.read"] }}
    />,
  );
  expect(screen.queryByRole("link")).toBeNull();
  expect(revoke).toHaveBeenCalledWith("blob:retained-draft");
  view.rerender(
    <DraftDownload
      {...args}
      access={{ ...args.access, permissions: ["resource.read_content"] }}
    />,
  );
  expect(screen.queryByRole("button")).toBeNull();
});
it("captures optional exact Listing metadata and clears stale downloads when fields change", async () => {
  const { props, get } = screenWorkspace();
  get.mockResolvedValue(response());
  const view = render(
    <DraftDownload
      {...props}
      access={administrator}
      namespaceId="ns"
      revisionId={id}
    />,
  );
  fireEvent.click(screen.getByLabelText("Include Listing metadata"));
  click("Prepare draft download");
  expect(screen.getByRole("alert").textContent).toContain(
    "exact ListingRevision",
  );
  change("Exact ListingRevision ID", listing);
  click("Prepare draft download");
  await screen.findByRole("link");
  expect(get.mock.calls[0]).toMatchObject([
    expect.any(String),
    { params: { query: { listingRevisionId: listing } } },
  ]);
  fireEvent.click(screen.getByLabelText("Include Listing metadata"));
  expect(screen.queryByRole("link")).toBeNull();
  view.unmount();
  const fixed = render(
    <DraftDownload
      heading="h3"
      {...props}
      access={administrator}
      namespaceId="ns"
      revisionId={id}
      listingRevisionId={listing}
    />,
  );
  fireEvent.click(screen.getByLabelText("Include Listing metadata"));
  expect(
    (screen.getByLabelText("Exact ListingRevision ID") as HTMLInputElement)
      .readOnly,
  ).toBe(true);
  fixed.unmount();
});
it("rejects bad checksums, media, file contracts and API errors with focused errors", async () => {
  const { props, get } = screenWorkspace();
  render(
    <DraftDownload
      {...props}
      access={administrator}
      namespaceId="ns"
      revisionId={id}
    />,
  );
  const checksum = response();
  checksum.response.headers.delete("Content-Digest");
  for (const reply of [
    checksum,
    response("{}", "text/plain"),
    response("{}"),
    { data: new ArrayBuffer(64 * 1024 * 1024 + 1), response: new Response() },
    { data: new ArrayBuffer(0), response: new Response() },
    {
      error: { code: "FORBIDDEN" },
      response: new Response(null, { status: 403 }),
    },
  ]) {
    get.mockResolvedValueOnce(reply);
    click("Prepare draft download");
    await waitFor(() =>
      expect(document.activeElement).toBe(screen.getByRole("alert")),
    );
    expect(screen.queryByRole("link")).toBeNull();
  }
  expect(props.onFailure).toHaveBeenCalledTimes(6);
});
it("fences responses and hashes after target changes and unmount", async () => {
  const { props, get } = screenWorkspace();
  const pending = deferred<ReturnType<typeof response>>();
  get.mockReturnValueOnce(pending.promise);
  const args = {
    ...props,
    access: administrator,
    namespaceId: "ns",
    revisionId: id,
  };
  const view = render(<DraftDownload {...args} />);
  click("Prepare draft download");
  view.rerender(<DraftDownload {...args} revisionId="new" />);
  await act(async () => pending.resolve(response()));
  expect(create).not.toHaveBeenCalled();
  const failed = deferred<ReturnType<typeof response>>();
  get.mockReturnValueOnce(failed.promise);
  click("Prepare draft download");
  view.unmount();
  await act(async () => failed.reject(new Error("obsolete")));
  expect(props.onFailure).not.toHaveBeenCalled();
});
