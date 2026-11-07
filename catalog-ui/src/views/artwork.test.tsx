// @vitest-environment jsdom
import { createHash, webcrypto } from "node:crypto";
import { Blob as NodeBlob } from "node:buffer";
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
import {
  artworkInput,
  artworkIRI,
  imageURL,
  isArtwork,
  nativeIcons,
  type ArtworkDescriptor,
} from "./artwork";
import { ArtworkEditor } from "./ArtworkEditor";
import { ArtworkGallery, ArtworkIcon, artworkPresentation } from "./Artwork";
import { foreignArtworkTarget, SourceArtworkControls } from "./SourceArtwork";
import { deferred, workspace } from "../../tests/unit/fixtures";
import { makeAPI, type Client } from "../api/client";
import { flushSync } from "react-dom";

const id = "00000000-0000-7000-8000-000000000001";
const asset = {
  url: "https://images.example/icon.png",
  alt: "A red robot",
  rights: { basis: "owned" as const, attribution: "Example team" },
};
const descriptor: ArtworkDescriptor = {
  schemaVersion: 1,
  icon: asset,
  screenshots: [
    { ...asset, alt: "Robot dashboard", caption: "Dashboard" },
    { ...asset, alt: "Robot settings" },
  ],
};
const gallery = {
  listingRevision: {
    id,
    extensions: {
      [artworkIRI]: { schemaVersion: 1, screenshots: descriptor.screenshots },
    },
  },
};
const local = {
  listingRevision: { id, extensions: { [artworkIRI]: descriptor } },
};
beforeEach(() => {
  vi.stubGlobal("crypto", webcrypto);
  vi.stubGlobal("Blob", NodeBlob);
});
afterEach(() => {
  cleanup();
  vi.restoreAllMocks();
  vi.unstubAllGlobals();
});
function attachments() {
  const create = vi.fn().mockReturnValue("blob:owned");
  const revoke = vi.fn();
  vi.stubGlobal(
    "URL",
    class extends URL {
      static createObjectURL = create;
      static revokeObjectURL = revoke;
    },
  );
  return { create, revoke };
}
function transport() {
  const { props } = workspace();
  const post = vi.fn(async (..._args: unknown[]) => ({
    data: new Uint8Array([1, 2, 3]).buffer,
    response: new Response(null, {
      headers: {
        "Content-Type": "image/png",
        "Content-Digest": `sha-256=:${createHash("sha256")
          .update(new Uint8Array([1, 2, 3]))
          .digest("base64")}:`,
      },
    }),
  }));
  props.client.api.POST = post as unknown as Client["api"]["POST"];
  return { props, post };
}
it("guards HTTPS references and strips backend facts from author inputs", () => {
  for (const url of [
    "http://images.example/a",
    "https://name:password@images.example/a",
    "https://images.example/a#secret",
    "https://images.example/a#",
    "https://images.example/a\n",
    "invalid",
    "https://images.example/" + "x".repeat(2048),
  ])
    expect(imageURL(url)).toBe(false);
  const sealed = {
    ...asset,
    digest: "sha256:" + "a".repeat(64),
    mediaType: "image/png",
    size: 3,
    width: 20,
    height: 20,
  };
  expect(artworkInput({ schemaVersion: 1, icon: sealed })).toEqual({
    schemaVersion: 1,
    icon: asset,
  });
  expect(isArtwork({ schemaVersion: 1, icon: sealed })).toBe(true);
  for (const value of [
    null,
    { schemaVersion: 2 },
    { schemaVersion: 1, icon: { ...asset, digest: "wrong" } },
    { schemaVersion: 1, screenshots: Array(9).fill(asset) },
    {
      schemaVersion: 1,
      icon: { ...asset, rights: { basis: "unverified", attribution: "x" } },
    },
    {
      schemaVersion: 1,
      icon: {
        ...asset,
        rights: { ...asset.rights, reference: "http://images.example/license" },
      },
    },
  ])
    expect(isArtwork(value)).toBe(false);
  expect(() =>
    artworkInput({
      schemaVersion: 1,
      icon: { ...asset, url: "http://bad.example/a" },
    }),
  ).toThrow(/HTTPS/);
  expect(() =>
    artworkInput({
      schemaVersion: 1,
      screenshots: Array.from({ length: 9 }, () => asset),
    }),
  ).toThrow(/eight/);
  expect(() =>
    artworkInput({
      schemaVersion: 1,
      icon: {
        ...asset,
        rights: { ...asset.rights, reference: "http://bad.example" },
      },
    }),
  ).toThrow(/reference/);
});
it("bounds native choices and ignores unsupported or malformed hints without requests", () => {
  expect(nativeIcons(null)).toEqual([]);
  expect(
    nativeIcons({ iconUrl: "https://images.example/a.png", icons: null }),
  ).toEqual([
    { url: "https://images.example/a.png", label: "Declared native icon" },
  ]);
  expect(
    nativeIcons({
      iconUrl: "https://images.example/a.png",
      icons: [
        {
          src: "https://images.example/dark.png",
          theme: "dark",
          sizes: ["32x32"],
        },
        { src: "data:image/png;base64,x" },
        { src: "https://images.example/a.svg", mimeType: "image/svg+xml" },
      ],
    }),
  ).toEqual([
    { url: "https://images.example/a.png", label: "Declared native icon" },
    { url: "https://images.example/dark.png", label: "dark; 32x32" },
  ]);
  expect(
    nativeIcons({
      icons: Array(20).fill({ src: "https://images.example/a.png" }),
    }),
  ).toHaveLength(16);
});
it.each([
  ["omitted", undefined, true],
  ["PNG", "image/png", true],
  ["JPEG", "image/jpeg", true],
  ["unsupported string", "image/svg+xml", false],
  ["null", null, false],
  ["number", 1, false],
  ["array", ["image/png"], false],
  ["object", {}, false],
  ["shadowed toString", { toString: "ordinary JSON field" }, false],
])(
  "checks the %s MIME hint without coercing it or losing valid neighbors",
  (_label, mimeType, accepted) => {
    const declared = {
      url: "https://images.example/native.png",
      label: "Declared native icon",
    };
    const candidate = {
      url: "https://images.example/candidate.png",
      label: "dark; 32x32",
    };
    const neighbor = {
      url: "https://images.example/neighbor.jpg",
      label: "any theme; size unspecified",
    };
    expect(
      nativeIcons({
        iconUrl: declared.url,
        icons: [
          {
            src: candidate.url,
            ...(mimeType === undefined ? {} : { mimeType }),
            theme: "dark",
            sizes: ["32x32"],
          },
          { src: neighbor.url, mimeType: "image/jpeg" },
        ],
      }),
    ).toEqual(
      accepted ? [declared, candidate, neighbor] : [declared, neighbor],
    );
  },
);
it("binds exact local, indexed and live source authority and rejects malformed projection", () => {
  expect(artworkPresentation(local)?.target).toEqual({
    origin: "local",
    listingRevisionId: id,
  });
  const indexed = {
    type: "indexed",
    identifier: "https://example.test/air",
    versionPresent: false,
    kind: "agent",
    indexed: { presentationSourceId: id, presentationGeneration: 2 },
    artwork: { descriptor },
  };
  expect(artworkPresentation(indexed)?.target).toMatchObject({
    origin: "indexed",
    target: { sourceId: id, presentationGeneration: 2, versionPresent: false },
  });
  expect(foreignArtworkTarget(indexed)).not.toHaveProperty("version");
  for (const version of ["", "1.0.0"]) {
    expect(
      artworkPresentation({ ...indexed, versionPresent: true, version })
        ?.target,
    ).toEqual({
      origin: "indexed",
      target: {
        sourceId: id,
        presentationGeneration: 2,
        identifier: indexed.identifier,
        versionPresent: true,
        version,
        kind: "agent",
      },
    });
  }
  expect(
    foreignArtworkTarget({ ...indexed, versionPresent: true }),
  ).toMatchObject({ versionPresent: true, version: "" });
  expect(
    foreignArtworkTarget({ ...indexed, versionPresent: true, version: null }),
  ).toBeUndefined();
  expect(
    artworkPresentation({
      ...indexed,
      indexed: { presentationSourceId: "group" },
    }),
  ).toBeUndefined();
  expect(
    artworkPresentation({
      type: "live",
      artwork: { descriptor, receipt: "signed" },
    })?.target,
  ).toEqual({ origin: "live", receipt: "signed", descriptor });
  expect(
    artworkPresentation({ type: "live", artwork: { descriptor, receipt: "" } }),
  ).toBeUndefined();
  expect(
    artworkPresentation({ artwork: { descriptor: { schemaVersion: 3 } } }),
  ).toBeUndefined();
  expect(artworkPresentation(null)).toBeUndefined();
});
it("edits ordered artwork with rights, native selection and explicit removal", () => {
  vi.spyOn(window, "confirm").mockReturnValue(true);
  const onChange = vi.fn<(value: ArtworkDescriptor) => void>();
  const view = render(
    <ArtworkEditor value={{ schemaVersion: 1 }} onChange={onChange} />,
  );
  fireEvent.click(screen.getByRole("button", { name: "Add screenshot" }));
  expect(onChange).toHaveBeenLastCalledWith({
    schemaVersion: 1,
    screenshots: [
      { url: "", alt: "", rights: { basis: "owned", attribution: "" } },
    ],
  });
  fireEvent.click(screen.getByRole("button", { name: "Add icon" }));
  expect(onChange.mock.lastCall?.[0].icon?.rights).toEqual({
    basis: "owned",
    attribution: "",
  });
  view.rerender(
    <ArtworkEditor
      value={descriptor}
      onChange={onChange}
      candidates={[
        { url: "https://images.example/native.png", label: "dark 32x32" },
      ]}
    />,
  );
  fireEvent.change(screen.getByLabelText("Use a declared native icon"), {
    target: { value: "https://images.example/native.png" },
  });
  expect(onChange.mock.lastCall?.[0].icon?.url).toBe(
    "https://images.example/native.png",
  );
  fireEvent.change(screen.getByLabelText("Icon alternative text"), {
    target: { value: "Different robot" },
  });
  fireEvent.change(screen.getByLabelText("Icon rights basis"), {
    target: { value: "licensed" },
  });
  fireEvent.change(screen.getByLabelText("Icon attribution"), {
    target: { value: "Licensed team" },
  });
  fireEvent.change(screen.getByLabelText("Icon rights reference (optional)"), {
    target: { value: "https://images.example/license" },
  });
  fireEvent.change(screen.getByLabelText("Icon caption (optional)"), {
    target: { value: "A caption" },
  });
  fireEvent.change(screen.getByLabelText("Icon HTTPS URL"), {
    target: { value: "https://images.example/new.png" },
  });
  fireEvent.click(
    screen.getByRole("button", { name: "Move screenshot 2 earlier" }),
  );
  expect(onChange).toHaveBeenLastCalledWith(
    expect.objectContaining({
      screenshots: [descriptor.screenshots![1], descriptor.screenshots![0]],
    }),
  );
  fireEvent.change(screen.getByLabelText("Screenshot 1 alternative text"), {
    target: { value: "Edited screenshot" },
  });
  expect(onChange.mock.lastCall?.[0].screenshots?.[0]?.alt).toBe(
    "Edited screenshot",
  );
  fireEvent.click(screen.getByRole("button", { name: "Add screenshot" }));
  expect(onChange.mock.lastCall?.[0].screenshots).toHaveLength(3);
  fireEvent.click(screen.getByRole("button", { name: "Remove screenshot 1" }));
  expect(onChange).toHaveBeenLastCalledWith(
    expect.objectContaining({ screenshots: [descriptor.screenshots![1]] }),
  );
  fireEvent.click(screen.getByRole("button", { name: "Remove icon" }));
  expect(onChange.mock.lastCall?.[0]).not.toHaveProperty("icon");
});
it("loads gallery images only through binary POST, replaces attachments and preserves attribution", async () => {
  const { props, post } = transport();
  const { revoke } = attachments();
  const view = render(<ArtworkGallery client={props.client} item={gallery} />);
  expect(
    (await screen.findByAltText("Robot dashboard")).getAttribute("src"),
  ).toBe("blob:owned");
  expect(post).toHaveBeenCalledWith(
    "/api/v1/artwork/read",
    expect.objectContaining({
      body: {
        origin: "local",
        listingRevisionId: id,
        slot: "screenshot",
        index: 0,
      },
      parseAs: "arrayBuffer",
    }),
  );
  expect(screen.getByText(/Example team/)).toBeTruthy();
  fireEvent.click(screen.getByRole("button", { name: "Next screenshot" }));
  await screen.findByAltText("Robot settings");
  expect(revoke).toHaveBeenCalledWith("blob:owned");
  fireEvent.click(screen.getByRole("button", { name: "Previous screenshot" }));
  await screen.findByAltText("Robot dashboard");
  view.unmount();
  expect(revoke).toHaveBeenCalledTimes(3);
});
it("uses kind fallback for cards until intersecting and never assigns a remote image URL", async () => {
  const { props, post } = transport();
  attachments();
  let observe!: IntersectionObserverCallback;
  vi.stubGlobal(
    "IntersectionObserver",
    class {
      constructor(callback: IntersectionObserverCallback) {
        observe = callback;
      }
      observe() {}
      disconnect() {}
    },
  );
  render(<ArtworkIcon client={props.client} item={local} kind="agent" />);
  expect(post).not.toHaveBeenCalled();
  act(() =>
    observe(
      [{ isIntersecting: false } as IntersectionObserverEntry],
      {} as IntersectionObserver,
    ),
  );
  expect(post).not.toHaveBeenCalled();
  act(() =>
    observe(
      [{ isIntersecting: true } as IntersectionObserverEntry],
      {} as IntersectionObserver,
    ),
  );
  expect((await screen.findByAltText("A red robot")).getAttribute("src")).toBe(
    "blob:owned",
  );
});
it("keeps a lazy kind fallback without reading image bytes when intersection observation is unavailable", async () => {
  const { props, post } = transport();
  const { create } = attachments();
  vi.stubGlobal("IntersectionObserver", undefined);
  render(<ArtworkIcon client={props.client} item={local} kind="agent" />);
  await act(async () => {});
  expect(screen.queryByRole("img")).toBeNull();
  expect(post).not.toHaveBeenCalled();
  expect(create).not.toHaveBeenCalled();
});
it("refuses unsupported response media and retries only explicitly", async () => {
  const { props, post } = transport();
  attachments();
  post.mockResolvedValue({
    data: new Uint8Array([1, 2, 3]).buffer,
    response: new Response(null, {
      headers: { "Content-Type": "image/svg+xml" },
    }),
  });
  render(<ArtworkGallery client={props.client} item={gallery} />);
  await screen.findByText("Image unavailable");
  expect(post).toHaveBeenCalledTimes(1);
  post.mockResolvedValue({
    data: new Uint8Array([1, 2, 3]).buffer,
    response: new Response(null, {
      headers: {
        "Content-Type": "image/png",
        "Content-Digest": `sha-256=:${createHash("sha256")
          .update(new Uint8Array([1, 2, 3]))
          .digest("base64")}:`,
      },
    }),
  });
  fireEvent.click(screen.getByRole("button", { name: "Retry image" }));
  await screen.findByAltText("Robot dashboard");
});
it.each([
  { name: "missing media type", mediaType: "", digest: "valid", size: 3 },
  {
    name: "missing transport digest",
    mediaType: "image/png",
    digest: "",
    size: 3,
  },
  {
    name: "mismatched transport digest",
    mediaType: "image/png",
    digest: `sha-256=:${Buffer.alloc(32).toString("base64")}:`,
    size: 3,
  },
  {
    name: "oversized encoded response",
    mediaType: "image/png",
    digest: "valid",
    size: 2 * 1024 * 1024 + 1,
  },
])("rejects $name before creating an image attachment", async (testCase) => {
  const { props, post } = transport();
  const { create } = attachments();
  const bytes = new Uint8Array(testCase.size);
  const digest = `sha-256=:${createHash("sha256").update(bytes).digest("base64")}:`;
  post.mockResolvedValue({
    data: bytes.buffer,
    response: new Response(null, {
      headers: {
        ...(testCase.mediaType ? { "Content-Type": testCase.mediaType } : {}),
        ...(testCase.digest
          ? {
              "Content-Digest":
                testCase.digest === "valid" ? digest : testCase.digest,
            }
          : {}),
      },
    }),
  });
  render(
    <ArtworkGallery
      client={props.client}
      item={gallery}
      onFailure={props.onFailure}
    />,
  );
  await screen.findByText("Image unavailable");
  expect(create).not.toHaveBeenCalled();
  expect(post).toHaveBeenCalledOnce();
  expect(props.onFailure).not.toHaveBeenCalled();
});
it("discards late image replies after unmount and aborts the binary request", async () => {
  const { props, post } = transport();
  const { create } = attachments();
  const pending = deferred<{ data: ArrayBuffer; response: Response }>();
  post.mockReturnValue(pending.promise);
  const view = render(<ArtworkGallery client={props.client} item={gallery} />);
  await waitFor(() => expect(post).toHaveBeenCalledTimes(1));
  const signal = (post.mock.calls[0]![1] as { signal: AbortSignal }).signal;
  view.unmount();
  expect(signal.aborted).toBe(true);
  // The late reply passes both content checks. Only the lifetime guard may
  // prevent its Blob attachment; an invalid digest would mask that defect.
  await act(async () =>
    pending.resolve({
      data: new Uint8Array([1, 2, 3]).buffer,
      response: new Response(null, {
        headers: {
          "Content-Type": "image/png",
          "Content-Digest": `sha-256=:${createHash("sha256")
            .update(new Uint8Array([1, 2, 3]))
            .digest("base64")}:`,
        },
      }),
    }),
  );
  expect(create).not.toHaveBeenCalled();
});
it("keeps source administration hidden from ordinary readers", () => {
  const { props, get } = workspace();
  render(
    <SourceArtworkControls
      {...props}
      item={{
        type: "indexed",
        identifier: "air",
        versionPresent: false,
        kind: "agent",
        indexed: { presentationSourceId: id, presentationGeneration: 1 },
      }}
    />,
  );
  expect(screen.queryByText("Manage local artwork")).toBeNull();
  expect(get).not.toHaveBeenCalled();
});
it("the actual session transport adds its host header to image POSTs", async () => {
  attachments();
  const fetch = vi.fn(
    async (_input: RequestInfo | URL) =>
      new Response(new Uint8Array([1, 2, 3]), {
        headers: {
          "Content-Type": "image/png",
          "Content-Digest": `sha-256=:${createHash("sha256")
            .update(new Uint8Array([1, 2, 3]))
            .digest("base64")}:`,
        },
      }),
  );
  vi.stubGlobal("fetch", fetch);
  const client = makeAPI({
    basePath: "/unit",
    authMode: "development",
    documentationUrl: "",
  });
  render(<ArtworkGallery client={client} item={gallery} />);
  await screen.findByAltText("Robot dashboard");
  const request = fetch.mock.calls[0]![0] as unknown as Request;
  expect(request.url).toContain("/unit/_catalog/api/v1/artwork/read");
  expect(request.headers.get("X-Catalog-Request")).toBe("1");
  expect(request.method).toBe("POST");
});
it("caps concurrent image reads and cancels queued cards on unmount", async () => {
  const { props, post } = transport();
  const { create } = attachments();
  const pending = deferred<{ data: ArrayBuffer; response: Response }>();
  post.mockReturnValue(pending.promise);
  const view = render(
    <>
      {Array.from({ length: 9 }, (_, index) => (
        <ArtworkGallery key={index} client={props.client} item={gallery} />
      ))}
    </>,
  );
  await waitFor(() => expect(post).toHaveBeenCalledTimes(4));
  view.unmount();
  await act(async () =>
    pending.resolve({
      data: new Uint8Array([1, 2, 3]).buffer,
      response: new Response(null, {
        headers: { "Content-Type": "image/png" },
      }),
    }),
  );
  expect(post).toHaveBeenCalledTimes(4);
  expect(create).not.toHaveBeenCalled();
});
it("releases a queued image's handed-off slot when its card is removed before the read starts", async () => {
  const { props, post } = transport();
  const { create } = attachments();
  const first = deferred<Awaited<ReturnType<typeof post>>>();
  const rest = deferred<Awaited<ReturnType<typeof post>>>();
  post
    .mockImplementation(() => rest.promise)
    .mockImplementationOnce(() => first.promise);
  const images = (keys: number[]) => (
    <>
      {keys.map((key) => (
        <ArtworkGallery key={key} client={props.client} item={gallery} />
      ))}
    </>
  );
  const view = render(images([0, 1, 2, 3, 4]));
  await waitFor(() => expect(post).toHaveBeenCalledTimes(4));
  await act(async () => {
    first.reject(new Error("Controlled first image failure"));
    // A completed request releases a slot before the queued await resumes.
    // Remove the card through a normal parent render in that microtask gap.
    await new Promise<void>((resolve) => {
      queueMicrotask(() => {
        flushSync(() => view.rerender(images([0, 1, 2, 3])));
        resolve();
      });
    });
  });
  expect(post).toHaveBeenCalledTimes(4);
  expect(create).not.toHaveBeenCalled();
  // Three requests still own slots. One fresh card may read; the second must
  // wait, proving the cancelled handoff released exactly one slot.
  view.rerender(images([0, 1, 2, 3, 5, 6]));
  await waitFor(() => expect(post).toHaveBeenCalledTimes(5));
  await act(async () => Promise.resolve());
  expect(post).toHaveBeenCalledTimes(5);
  expect(create).not.toHaveBeenCalled();
  view.unmount();
  await act(async () =>
    rest.resolve({
      data: new Uint8Array([1, 2, 3]).buffer,
      response: new Response(null, {
        headers: { "Content-Type": "image/svg+xml" },
      }),
    }),
  );
  expect(post).toHaveBeenCalledTimes(5);
  expect(create).not.toHaveBeenCalled();
  const fresh = transport();
  render(<ArtworkGallery client={fresh.props.client} item={gallery} />);
  await screen.findByAltText("Robot dashboard");
  expect(fresh.post).toHaveBeenCalledOnce();
});
it("clears already displayed private image attachments when the session is invalidated", async () => {
  const { revoke } = attachments();
  vi.stubGlobal(
    "fetch",
    vi.fn(
      async () =>
        new Response(new Uint8Array([1, 2, 3]), {
          headers: {
            "Content-Type": "image/png",
            "Content-Digest": `sha-256=:${createHash("sha256")
              .update(new Uint8Array([1, 2, 3]))
              .digest("base64")}:`,
          },
        }),
    ),
  );
  const client = makeAPI({
    basePath: "/unit",
    authMode: "development",
    documentationUrl: "",
  });
  render(<ArtworkGallery client={client} item={gallery} />);
  await screen.findByAltText("Robot dashboard");
  act(() => client.invalidate());
  expect(screen.queryByAltText("Robot dashboard")).toBeNull();
  expect(revoke).toHaveBeenCalledWith("blob:owned");
});
it("invalidates an unfinished image read and discards its digest-valid late reply without a Blob", async () => {
  const { props } = workspace();
  const { create, revoke } = attachments();
  const pending = deferred<Response>();
  const fetch = vi.fn<(...args: unknown[]) => Promise<Response>>(
    () => pending.promise,
  );
  vi.stubGlobal("fetch", fetch);
  const client = makeAPI({
    basePath: "/unit",
    authMode: "development",
    documentationUrl: "",
  });
  render(
    <ArtworkGallery
      client={client}
      item={gallery}
      onFailure={props.onFailure}
    />,
  );
  await waitFor(() => expect(fetch).toHaveBeenCalledOnce());
  const request = fetch.mock.calls[0]![0] as Request;
  expect(request.url).toBe(
    location.origin + "/unit/_catalog/api/v1/artwork/read",
  );
  expect(request.signal.aborted).toBe(false);
  act(() => client.invalidate());
  expect(request.signal.aborted).toBe(true);
  expect(create).not.toHaveBeenCalled();
  expect(revoke).not.toHaveBeenCalled();
  // The transport ignores cancellation and returns valid bytes; only current
  // session/lifetime checks may refuse this otherwise admissible attachment.
  await act(async () =>
    pending.resolve(
      new Response(new Uint8Array([1, 2, 3]), {
        headers: {
          "Content-Type": "image/png",
          "Content-Digest": `sha-256=:${createHash("sha256")
            .update(new Uint8Array([1, 2, 3]))
            .digest("base64")}:`,
        },
      }),
    ),
  );
  expect(screen.queryByAltText("Robot dashboard")).toBeNull();
  expect(create).not.toHaveBeenCalled();
  expect(revoke).not.toHaveBeenCalled();
  expect(props.onFailure).not.toHaveBeenCalled();
  expect(fetch).toHaveBeenCalledOnce();
});
it.each([401, 403])(
  "reports %s for shell reconciliation without automatic image retries",
  async (status) => {
    const { props, post } = transport();
    attachments();
    post.mockResolvedValue({
      data: undefined as unknown as ArrayBuffer,
      response: new Response(null, { status }),
    });
    render(
      <ArtworkGallery
        client={props.client}
        item={gallery}
        onFailure={props.onFailure}
      />,
    );
    await screen.findByText("Image unavailable");
    expect(props.onFailure).toHaveBeenCalledOnce();
    expect(post).toHaveBeenCalledOnce();
  },
);
it("renders source-provided mutable native icons without inventing rights or sealed facts", async () => {
  const { props } = transport();
  attachments();
  const native = {
    type: "indexed",
    identifier: "urn:air:example.test:team:agent",
    versionPresent: false,
    kind: "agent",
    indexed: { presentationSourceId: id, presentationGeneration: 2 },
    artwork: {
      mutableIcon: true,
      iconSource: "native",
      descriptor: {
        schemaVersion: 1,
        icon: {
          url: asset.url,
          alt: "Native robot",
          digest: "",
          mediaType: "",
          size: 0,
          width: 0,
          height: 0,
          rightsAssessment: "",
        },
      },
    },
  };
  expect(artworkPresentation(native)).toBeTruthy();
  render(<ArtworkGallery client={props.client} item={native} />);
  await screen.findByAltText("Native robot");
  expect(
    screen.getByText(
      "Source-provided native icon. Rights have not been assessed.",
    ),
  ).toBeTruthy();
  expect(screen.getByText(/Native icon bytes may change/)).toBeTruthy();
});
it("refuses a source image whose encoded bytes disagree with its sealed digest", async () => {
  const { props } = transport();
  const { create } = attachments();
  const changed = {
    listingRevision: {
      id,
      extensions: {
        [artworkIRI]: {
          schemaVersion: 1,
          screenshots: [{ ...asset, digest: "sha256:" + "0".repeat(64) }],
        },
      },
    },
  };
  render(<ArtworkGallery client={props.client} item={changed} />);
  await screen.findByText("Image unavailable");
  expect(create).not.toHaveBeenCalled();
});
it("releases queued image reads after successful completion and handles a browser decode failure", async () => {
  const { props, post } = transport();
  attachments();
  render(
    <>
      {Array.from({ length: 6 }, (_, index) => (
        <ArtworkGallery key={index} client={props.client} item={gallery} />
      ))}
    </>,
  );
  await waitFor(() =>
    expect(screen.getAllByAltText("Robot dashboard")).toHaveLength(6),
  );
  expect(post).toHaveBeenCalledTimes(6);
  fireEvent.error(screen.getAllByAltText("Robot dashboard")[0]!);
  expect(screen.getByText("Image unavailable")).toBeTruthy();
  fireEvent.click(screen.getByRole("button", { name: "Retry image" }));
  await waitFor(() => expect(post).toHaveBeenCalledTimes(7));
});
it("bounds waiting artwork requests and allows explicit retry after the queue drains", async () => {
  const { props, post } = transport();
  const pending = deferred<Awaited<ReturnType<typeof post>>>();
  post.mockImplementation(() => pending.promise);
  attachments();
  const view = render(
    <>
      {Array.from({ length: 133 }, (_, index) => (
        <ArtworkGallery key={index} client={props.client} item={gallery} />
      ))}
    </>,
  );
  await screen.findByText("Image unavailable");
  expect(post).toHaveBeenCalledTimes(4);
  view.unmount();
  await act(async () =>
    pending.resolve({
      data: new Uint8Array([1, 2, 3]).buffer,
      response: new Response(null, {
        headers: { "Content-Type": "image/png" },
      }),
    }),
  );
});
it("rejects incomplete rights and invalid author image references before confirmation", () => {
  expect(() =>
    artworkInput({
      ...descriptor,
      icon: { ...asset, url: "http://images.example/icon.png" },
    }),
  ).toThrow(/HTTPS/);
  expect(() =>
    artworkInput({
      ...descriptor,
      icon: {
        ...asset,
        rights: { ...asset.rights, reference: "http://images.example/license" },
      },
    }),
  ).toThrow(/Rights reference/);
  expect(() =>
    artworkInput({
      ...descriptor,
      icon: {
        ...asset,
        rights: { ...asset.rights, basis: "unknown" as "owned" },
      },
    }),
  ).toThrow(/rights basis/);
  expect(() =>
    artworkInput({
      ...descriptor,
      screenshots: Array.from({ length: 9 }, () => asset),
    }),
  ).toThrow(/eight/);
  expect(
    artworkInput({
      ...descriptor,
      icon: {
        ...asset,
        caption: "Approved image",
        rights: {
          ...asset.rights,
          reference: "https://images.example/license",
        },
      },
    }).icon,
  ).toMatchObject({
    caption: "Approved image",
    rights: { reference: "https://images.example/license" },
  });
  expect(foreignArtworkTarget(null)).toBeUndefined();
  expect(
    foreignArtworkTarget({
      ...local,
      identifier: "urn:example:agent",
      versionPresent: false,
      kind: "agent",
    }),
  ).toBeUndefined();
  expect(
    foreignArtworkTarget({
      identifier: "urn:example:agent",
      versionPresent: false,
      kind: "agent",
      live: { sourceId: id, presentationGeneration: 1 },
    }),
  ).not.toHaveProperty("version");
});
it.each(["publisher", "local_override"])(
  "shows %s screenshot attribution independently of the original declared rights",
  async (screenshotsSource) => {
    const { props, post } = transport();
    const screenshotReady = deferred<void>();
    const reply = post.getMockImplementation()!;
    post.mockImplementation(async (...args: unknown[]) => {
      if ((args[1] as { body: { slot: string } }).body.slot === "screenshot")
        await screenshotReady.promise;
      return reply(...args);
    });
    attachments();
    render(
      <ArtworkGallery
        client={props.client}
        item={{
          type: "live",
          artwork: {
            descriptor,
            receipt: "proof",
            iconSource: "local_override",
            screenshotsSource,
          },
        }}
      />,
    );
    await screen.findByAltText("A red robot");
    expect(screen.getAllByText("Local consumer override")).toHaveLength(
      screenshotsSource === "local_override" ? 2 : 1,
    );
    expect(screen.queryByText("Source-provided artwork") !== null).toBe(
      screenshotsSource === "publisher",
    );
    expect(screen.queryByAltText("Robot dashboard")).toBeNull();
    screenshotReady.resolve();
    expect(await screen.findByAltText("Robot dashboard")).toBeTruthy();
    expect(screen.getByText(/Image attribution: Example team/)).toBeTruthy();
  },
);

it.each([5, 8])(
  "preserves all %s retained screenshots while preparing author input",
  (count) => {
    const screenshots = Array.from({ length: count }, (_, index) => ({
      ...asset,
      alt: `Screenshot ${index + 1}`,
      digest: `sha256:${"a".repeat(64)}`,
      mediaType: "image/png",
      size: 100,
      width: 10,
      height: 10,
    }));
    const prepared = artworkInput({ schemaVersion: 1, screenshots });
    expect(prepared.screenshots).toEqual(
      screenshots.map(({ url, alt, rights }) => ({ url, alt, rights })),
    );
    const onChange = vi.fn();
    render(
      <ArtworkEditor
        value={{ schemaVersion: 1, screenshots }}
        onChange={onChange}
      />,
    );
    expect(
      screen.getByRole("button", { name: "Add screenshot" }),
    ).toHaveProperty("disabled", count === 8);
    expect(
      screen.getAllByLabelText(/Screenshot \d alternative text/),
    ).toHaveLength(count);
  },
);

it("preserves retained artwork descriptions at supported hard bounds and rejects over-bound input", () => {
  const icon = {
    ...asset,
    alt: "🙂".repeat(256),
    caption: "🙂".repeat(512),
    rights: { ...asset.rights, attribution: "🙂".repeat(1024) },
  };
  expect(artworkInput({ schemaVersion: 1, icon }).icon).toEqual(icon);
  for (const invalid of [
    { ...icon, alt: icon.alt + "x" },
    { ...icon, caption: icon.caption + "x" },
    {
      ...icon,
      rights: { ...icon.rights, attribution: icon.rights.attribution + "x" },
    },
  ])
    expect(() => artworkInput({ schemaVersion: 1, icon: invalid })).toThrow(
      /characters/,
    );
});

it("associates artwork rights guidance without fetching references or editing artwork", () => {
  const onChange = vi.fn();
  const fetch = vi.fn();
  vi.stubGlobal("fetch", fetch);
  render(<ArtworkEditor value={descriptor} onChange={onChange} />);
  const disclosure = screen
    .getByText("Icon and screenshots")
    .closest("details")!;
  disclosure.open = true;
  const imageURL = screen.getByLabelText("Icon HTTPS URL");
  expect(
    document.getElementById(imageURL.getAttribute("aria-describedby")!)
      ?.textContent,
  ).toContain("static PNG or JPEG from an allowed HTTPS origin");
  expect(
    document.getElementById(imageURL.getAttribute("aria-describedby")!)
      ?.textContent,
  ).toContain("credentials, spaces or a fragment (#)");
  const attribution = screen.getByLabelText("Icon attribution");
  const attributionHint = document.getElementById(
    attribution.getAttribute("aria-describedby")!,
  )!;
  expect(attributionHint.textContent).toContain(
    "Required even for an owned image",
  );
  expect(screen.getByText(/Validation does not establish/)).toBeTruthy();
  const reference = screen.getByLabelText("Icon rights reference (optional)");
  const initialDescription = reference.getAttribute("aria-describedby");
  expect(document.getElementById(initialDescription!)?.textContent).toContain(
    "Do not include private proof or secrets",
  );
  const about = screen.getByRole("button", {
    name: "About icon rights reference (optional)",
  });
  expect(about.getAttribute("aria-expanded")).toBe("false");
  fireEvent.click(about);
  const help = within(screen.getByRole("group", { name: "Icon" })).getByText(
    /This is a reference link, not an evidence upload/,
  );
  expect(help).toHaveProperty("hidden", false);
  expect(reference.getAttribute("aria-describedby")?.split(" ")).toContain(
    help.id,
  );
  fireEvent.click(about);
  expect(help).toHaveProperty("hidden", true);
  expect(reference.getAttribute("aria-describedby")).toBe(initialDescription);
  expect(screen.getByLabelText("Icon rights basis")).toHaveProperty(
    "value",
    "owned",
  );
  expect(onChange).not.toHaveBeenCalled();
  expect(fetch).not.toHaveBeenCalled();
});
