/// <reference types="node" />
import { required } from "../../tests/required";
// @vitest-environment jsdom
/// <reference types="node" />
// Clipboard and retained-download tests use real hashing and test-owned response bytes, never services.
import { webcrypto } from "node:crypto";
import { Blob as NodeBlob } from "node:buffer";
import { afterEach, beforeEach, expect, it, vi } from "vitest";
import {
  act,
  cleanup,
  fireEvent,
  render,
  screen,
  waitFor,
} from "@testing-library/react";
import { CopyReference } from "./CopyReference";
import { Profiler } from "react";
import { ArtifactDownload, verifyArtifact } from "./ArtifactDownload";
import { makeAPI } from "../api/client";
import { deferred, workspace } from "../../tests/unit/fixtures";

beforeEach(() => {
  vi.stubGlobal("crypto", webcrypto);
  vi.stubGlobal("Blob", NodeBlob);
});
afterEach(() => {
  cleanup();
  vi.restoreAllMocks();
  vi.unstubAllGlobals();
});

/** Create a content-addressed response with non-text bytes and valid digest metadata. */
async function artifact(mediaType = "application/octet-stream") {
  const bytes = new Uint8Array([0, 255, 239, 187, 191, 13, 10, 32]).buffer;
  const hash = Buffer.from(await webcrypto.subtle.digest("SHA-256", bytes));
  const id = hash.toString("hex");
  const response = new Response(null, {
    headers: {
      "Content-Type": mediaType,
      "Content-Digest": `sha-256=:${hash.toString("base64")}:`,
    },
  });
  return { id, bytes, response };
}

it("retains clipboard feedback when copying before the initial passive effects", async () => {
  const writeText = vi.fn().mockResolvedValue(undefined);
  vi.stubGlobal("navigator", { clipboard: { writeText } });
  let activated = false;
  // Activate during commit to reproduce interaction before passive effects.
  render(
    <Profiler
      id="early-copy-action"
      onRender={() => {
        const copy = screen.queryByRole("button", { name: "Copy Reference" });
        if (!activated && copy) {
          activated = true;
          copy.click();
        }
      }}
    >
      <CopyReference label="Reference" value="exact-reference" />
    </Profiler>,
  );
  await screen.findByText("Copied.");
  expect(writeText).toHaveBeenCalledExactlyOnceWith("exact-reference");
});

it("copies the exact displayed value and provides a manual fallback for clipboard denial", async () => {
  const writeText = vi.fn().mockResolvedValue(undefined);
  vi.stubGlobal("navigator", { clipboard: { writeText } });
  const view = render(
    <CopyReference
      label="Reference"
      value={'<script>literal</script> "quoted"'}
    />,
  );
  expect(writeText).not.toHaveBeenCalled();
  fireEvent.click(screen.getByRole("button", { name: "Copy Reference" }));
  await screen.findByText("Copied.");
  expect(writeText).toHaveBeenCalledExactlyOnceWith(
    '<script>literal</script> "quoted"',
  );
  expect(view.container.querySelector("script")).toBeNull();
  writeText.mockRejectedValue(new Error("private clipboard message"));
  fireEvent.click(screen.getByRole("button", { name: "Copy Reference" }));
  await screen.findByText(/Copy unavailable/);
  expect(view.container.textContent).not.toContain("private clipboard message");
  view.rerender(<CopyReference label="Reference" value={"x".repeat(8193)} />);
  expect(screen.getByRole("button")).toHaveProperty("disabled", true);
  expect(screen.queryByRole("status")).toBeNull();
});

it.each(["resolve", "reject"])(
  "ignores a stale clipboard %s after the reference changes",
  async (outcome) => {
    const pending = deferred<void>();
    vi.stubGlobal("navigator", {
      clipboard: { writeText: vi.fn().mockReturnValue(pending.promise) },
    });
    const view = render(<CopyReference label="Reference" value="old" />);
    fireEvent.click(screen.getByRole("button"));
    view.rerender(<CopyReference label="Reference" value="new" />);
    await act(async () => {
      if (outcome === "resolve") pending.resolve();
      else pending.reject(new Error("denied"));
    });
    expect(screen.queryByRole("status")).toBeNull();
  },
);

it("keeps a reference selectable when the clipboard API is unavailable", async () => {
  vi.stubGlobal("navigator", {});
  render(<CopyReference label="Reference" value="exact-reference" />);
  fireEvent.click(screen.getByRole("button"));
  await screen.findByText(/Copy unavailable/);
  expect(screen.getByText("exact-reference").tagName).toBe("CODE");
});

it("verifies exact bytes and refuses missing media types, mismatched digests and mismatched IDs", async () => {
  const a = await artifact();
  const blob = await verifyArtifact(a.bytes, a.response, a.id);
  expect(blob.type).toBe("application/octet-stream");
  expect(new Uint8Array(await blob.arrayBuffer())).toEqual(
    new Uint8Array(a.bytes),
  );
  await expect(verifyArtifact(a.bytes, new Response(), a.id)).rejects.toThrow(
    "media type",
  );
  await expect(
    verifyArtifact(
      a.bytes,
      new Response(null, { headers: { "Content-Type": "invalid" } }),
      a.id,
    ),
  ).rejects.toThrow("media type");
  await expect(
    verifyArtifact(a.bytes, a.response, "b".repeat(64)),
  ).rejects.toThrow("checksum");
  a.response.headers.delete("Content-Digest");
  await expect(verifyArtifact(a.bytes, a.response, a.id)).rejects.toThrow(
    "checksum",
  );
});

it("prepares a verified attachment only on request, replaces URLs and cleans up on unmount", async () => {
  const a = await artifact();
  const { props, get } = workspace();
  a.response.headers.set(
    "Content-Disposition",
    'attachment; filename="../../unsafe.html"',
  );
  get.mockResolvedValue({ data: a.bytes, response: a.response });
  const create = vi
    .fn<(blob: Blob) => string>()
    .mockReturnValueOnce("blob:first")
    .mockReturnValueOnce("blob:second");
  const revoke = vi.fn();
  vi.stubGlobal(
    "URL",
    class extends URL {
      static createObjectURL = create;
      static revokeObjectURL = revoke;
    },
  );
  const view = render(<ArtifactDownload {...props} id={a.id} />);
  expect(get).not.toHaveBeenCalled();
  expect(screen.queryByRole("link")).toBeNull();
  fireEvent.click(screen.getByRole("button"));
  const link = await screen.findByRole("link", {
    name: "Save retained document",
  });
  expect(link.getAttribute("download")).toBe(`catalog-document-${a.id}.bin`);
  expect(link.getAttribute("href")).toBe("blob:first");
  expect(get.mock.calls[0]).toMatchObject([
    "/api/v1/artifacts/{artifactId}/content",
    { params: { path: { artifactId: a.id } }, parseAs: "arrayBuffer" },
  ]);
  expect(
    new Uint8Array(await required(create.mock.calls[0])[0].arrayBuffer()),
  ).toEqual(new Uint8Array(a.bytes));
  fireEvent.click(screen.getByRole("button"));
  await waitFor(() =>
    expect(screen.getByRole("link").getAttribute("href")).toBe("blob:second"),
  );
  expect(revoke).toHaveBeenCalledWith("blob:first");
  view.unmount();
  expect(revoke).toHaveBeenCalledWith("blob:second");
});

it.each([403, 500])(
  "reports a %i response safely and permits an explicit retry",
  async (status) => {
    const { props, get } = workspace();
    get.mockResolvedValue({
      response: new Response(null, { status }),
      error: { code: "DENIED" },
    });
    render(<ArtifactDownload {...props} id={"a".repeat(64)} />);
    fireEvent.click(screen.getByRole("button"));
    await screen.findByRole("alert");
    expect(screen.queryByRole("link")).toBeNull();
    expect(props.onFailure).toHaveBeenCalledTimes(1);
    get.mockRejectedValue(new Error("private transport"));
    fireEvent.click(screen.getByRole("button"));
    await screen.findByText(
      "Download could not be prepared or verified. Try again.",
    );
    expect(screen.queryByText("private transport")).toBeNull();
  },
);

it("aborts pending reads on client replacement and revokes prepared files on record replacement", async () => {
  const a = await artifact();
  const first = workspace();
  const second = workspace();
  const pending = deferred<unknown>();
  first.get.mockReturnValue(pending.promise);
  second.get.mockResolvedValue({ data: a.bytes, response: a.response });
  const create = vi
    .fn<(blob: Blob) => string>()
    .mockReturnValue("blob:current");
  const revoke = vi.fn();
  vi.stubGlobal(
    "URL",
    class extends URL {
      static createObjectURL = create;
      static revokeObjectURL = revoke;
    },
  );
  const view = render(<ArtifactDownload {...first.props} id={a.id} />);
  fireEvent.click(screen.getByRole("button"));
  const signal = (
    required(first.get.mock.calls[0])[1] as { signal: AbortSignal }
  ).signal;
  view.rerender(<ArtifactDownload {...second.props} id={a.id} />);
  expect(signal.aborted).toBe(true);
  expect(screen.getByRole("button")).toHaveProperty("disabled", false);
  await act(async () =>
    pending.resolve({ data: a.bytes, response: a.response }),
  );
  expect(create).not.toHaveBeenCalled();
  expect(first.props.onFailure).not.toHaveBeenCalled();
  fireEvent.click(screen.getByRole("button"));
  await screen.findByRole("link");
  expect(create).toHaveBeenCalledTimes(1);
  view.rerender(<ArtifactDownload {...second.props} id={"b".repeat(64)} />);
  expect(revoke).toHaveBeenCalledExactlyOnceWith("blob:current");
  expect(screen.queryByRole("link")).toBeNull();
  expect(second.get).toHaveBeenCalledTimes(1);
});

it("rechecks session authority after asynchronous checksum verification", async () => {
  const a = await artifact();
  const hash = await webcrypto.subtle.digest("SHA-256", a.bytes);
  const pending = deferred<ArrayBuffer>();
  const digest = vi.fn().mockReturnValue(pending.promise);
  vi.stubGlobal("crypto", { subtle: { digest } });
  vi.stubGlobal(
    "fetch",
    vi.fn().mockResolvedValue(
      new Response(a.bytes, {
        headers: a.response.headers,
      }),
    ),
  );
  const { props } = workspace();
  const client = makeAPI(props.runtime);
  render(<ArtifactDownload {...props} client={client} id={a.id} />);
  fireEvent.click(screen.getByRole("button"));
  await waitFor(() => expect(digest).toHaveBeenCalledTimes(1));
  client.invalidate();
  await act(async () => pending.resolve(hash));
  expect(screen.queryByRole("link")).toBeNull();
  expect(props.onFailure).toHaveBeenCalledExactlyOnceWith(
    expect.objectContaining({ name: "AbortError", message: "Session changed" }),
  );
});

it.each(["resolve", "reject"])(
  "discards a stale download %s after unmount",
  async (outcome) => {
    const a = await artifact();
    const { props, get } = workspace();
    const pending = deferred<unknown>();
    get.mockReturnValue(pending.promise);
    const view = render(<ArtifactDownload {...props} id={a.id} />);
    fireEvent.click(screen.getByRole("button"));
    const signal = (required(get.mock.calls[0])[1] as { signal: AbortSignal })
      .signal;
    view.unmount();
    expect(signal.aborted).toBe(true);
    await act(async () => {
      if (outcome === "resolve")
        pending.resolve({ data: a.bytes, response: a.response });
      else pending.reject(new Error("late"));
    });
    expect(screen.queryByRole("link")).toBeNull();
    expect(props.onFailure).not.toHaveBeenCalled();
  },
);

it("offers an isolated HTTPS navigation without changing the exact copied reference", async () => {
  const writeText = vi.fn().mockResolvedValue(undefined);
  vi.stubGlobal("navigator", { clipboard: { writeText } });
  const value = "https://git.example.test/team/repository?ref=main#readme";
  render(<CopyReference label="Git repository" value={value} open />);
  const link = screen.getByRole("link", { name: "Open Git repository" });
  expect(link.getAttribute("href")).toBe(value);
  expect(link.getAttribute("target")).toBe("_blank");
  expect(link.getAttribute("rel")).toBe("noopener noreferrer");
  expect(link.getAttribute("referrerpolicy")).toBe("no-referrer");
  expect(link.getAttribute("title")).toBe(value);
  expect(writeText).not.toHaveBeenCalled();
  fireEvent.click(screen.getByRole("button", { name: "Copy Git repository" }));
  await screen.findByText("Copied.");
  expect(writeText).toHaveBeenCalledExactlyOnceWith(value);
});

it.each([
  "javascript:alert(1)",
  "data:text/html,hello",
  "http://git.example.test/repo",
  "https://user:password@git.example.test/repo",
  "/relative/repository",
  "not a URL",
  "https://git.example.test/\nrepo",
  " https://git.example.test/repo",
  "https://git.example.test/" + "x".repeat(8192),
])("keeps unsafe or oversized navigation copy-only case %#", (value) => {
  render(<CopyReference label="Git repository" value={value} open />);
  expect(screen.queryByRole("link")).toBeNull();
  expect(
    screen.getByRole("button", { name: "Copy Git repository" }),
  ).toBeTruthy();
});

it("never turns an endpoint, OCI pin or commit into a link by inspecting its text", () => {
  const view = render(
    <CopyReference
      label="Declared endpoint"
      value="https://runtime.test/mcp"
    />,
  );
  expect(screen.queryByRole("link")).toBeNull();
  view.rerender(
    <CopyReference label="OCI pin" value="registry.test/repo@sha256:abcd" />,
  );
  expect(screen.queryByRole("link")).toBeNull();
  view.rerender(
    <CopyReference
      label="Documentation"
      value="https://docs.example.test/"
      open
      openLabel="Documentation"
    />,
  );
  expect(screen.getByRole("link", { name: "Documentation" })).toBeTruthy();
});

it.each([
  ["application/json", "json"],
  ["APPLICATION/JSON; charset=UTF-8", "json"],
  ["application/ld+json", "jsonld"],
  ["application/a2a-agent-card+json", "json"],
  ["application/agent-skills+md", "md"],
  ["text/markdown; variant=skill", "md"],
  ["text/plain", "txt"],
  ["application/yaml", "yaml"],
  ["image/svg+xml", "bin"],
  ["application/private+json", "bin"],
])(
  "shows verified media type %s and a fixed safe .%s filename",
  async (mediaType, suffix) => {
    const a = await artifact(mediaType);
    const w = workspace();
    w.get.mockResolvedValue({ data: a.bytes, response: a.response });
    vi.stubGlobal(
      "URL",
      Object.assign(class extends URL {}, {
        createObjectURL: vi.fn(() => "blob:verified-media"),
        revokeObjectURL: vi.fn(),
      }),
    );
    render(<ArtifactDownload {...w.props} id={a.id} />);
    fireEvent.click(
      screen.getByRole("button", {
        name: "Prepare retained document download",
      }),
    );
    const link = await screen.findByRole("link", {
      name: "Save retained document",
    });
    expect(link.getAttribute("download")).toBe(
      `catalog-document-${a.id}.${suffix}`,
    );
    expect(screen.getByText(mediaType.toLowerCase())).toBeTruthy();
    expect(w.get).toHaveBeenCalledOnce();
  },
);
