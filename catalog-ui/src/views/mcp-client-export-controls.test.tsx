// @vitest-environment jsdom
/// <reference types="node" />
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
import type { RecordDetail } from "../api/client";
import { MCPClientExport } from "./MCPClientExport";
import { deferred, workspace } from "../../tests/unit/fixtures";

const releaseID = "00000000-0000-7000-8000-000000000001";
const document = {
  name: "org.example/server",
  version: "1.0.0",
  remotes: [
    {
      type: "streamable-http",
      url: "https://mcp.test/route?a=%2F",
      headers: [
        { name: "Authorization", isRequired: true, value: "PRIVATE_TOKEN" },
      ],
    },
  ],
};
function record(data: unknown = document): RecordDetail {
  return {
    resource: { kind: "mcp_server", namespaceId: "namespace" },
    release: { id: releaseID, state: "published" },
    versionRevision: {
      id: "revision",
      representations: [
        {
          id: "rep",
          role: "primary",
          validationState: "valid",
          profileId: "mcp-registry-server-1.8.1-f52dc85",
          mediaType: "application/vnd.mcp.server+json",
          contentDigest: `sha256:${"a".repeat(64)}`,
          size: 100,
          data,
        },
      ],
    },
  } as unknown as RecordDetail;
}
let blobs: Blob[];
beforeEach(() => {
  blobs = [];
  vi.stubGlobal("crypto", webcrypto);
  vi.stubGlobal("Blob", NodeBlob);
  vi.stubGlobal("requestAnimationFrame", (callback: FrameRequestCallback) => {
    callback(0);
    return 1;
  });
  vi.stubGlobal(
    "URL",
    Object.assign(URL, {
      createObjectURL: vi.fn((blob: Blob) => {
        blobs.push(blob);
        return `blob:config-${blobs.length}`;
      }),
      revokeObjectURL: vi.fn(),
    }),
  );
});
afterEach(() => {
  cleanup();
  vi.restoreAllMocks();
  vi.unstubAllGlobals();
});

async function artifact() {
  const bytes = new TextEncoder().encode(JSON.stringify(document));
  const hash = Buffer.from(await webcrypto.subtle.digest("SHA-256", bytes));
  const current = record();
  const rep = current.versionRevision.representations[0]!;
  delete rep.data;
  rep.artifactId = hash.toString("hex");
  rep.contentDigest = `sha256:${rep.artifactId}`;
  rep.size = bytes.byteLength;
  const response = new Response(null, {
    headers: {
      "Content-Type": rep.mediaType,
      "Content-Length": String(bytes.byteLength),
      "Content-Digest": `sha-256=:${hash.toString("base64")}:`,
    },
  });
  return { current, response, bytes: bytes.buffer };
}

it("prepares inert inline configuration only on request, omits secrets and resets downloads on format changes", async () => {
  const { props, get } = workspace();
  const current = record();
  const view = render(<MCPClientExport {...props} record={current} />);
  expect(get).not.toHaveBeenCalled();
  expect(screen.queryByRole("link")).toBeNull();
  fireEvent.click(
    screen.getByRole("button", { name: "Prepare client configuration" }),
  );
  const link = await screen.findByRole("link", {
    name: "Download client configuration",
  });
  expect(link.getAttribute("download")).toBe("mcp.json");
  expect(screen.getByText(/Authorization \(required\)/)).toBeTruthy();
  expect(view.container.textContent).not.toContain("PRIVATE_TOKEN");
  const first = await blobs[0]!.text();
  expect(JSON.parse(first)).toEqual({
    servers: {
      [`catalog-${releaseID}`]: { type: "http", url: document.remotes[0]!.url },
    },
  });
  fireEvent.change(screen.getByLabelText("MCP client"), {
    target: { value: "mcp-cursor" },
  });
  expect(screen.queryByRole("link")).toBeNull();
  expect(URL.revokeObjectURL).toHaveBeenCalledWith("blob:config-1");
  fireEvent.click(
    screen.getByRole("button", { name: "Prepare client configuration" }),
  );
  await screen.findByRole("link");
  expect(get).not.toHaveBeenCalled();
  expect(JSON.parse(await blobs[1]!.text())).toEqual({
    mcpServers: { [`catalog-${releaseID}`]: { url: document.remotes[0]!.url } },
  });
});

it("requires an original remote selection and makes unavailable entries explicit", async () => {
  const { props } = workspace();
  render(
    <MCPClientExport
      {...props}
      record={record({
        remotes: [
          { type: "stdio", url: "https://mcp.test" },
          ...document.remotes,
        ],
      })}
    />,
  );
  expect(
    screen.getByRole("option", { name: /Remote 1: unavailable/ }),
  ).toHaveProperty("disabled", true);
  fireEvent.click(
    screen.getByRole("button", { name: "Prepare client configuration" }),
  );
  await screen.findByText(/Choose a remote connection/);
  expect(screen.queryByRole("link")).toBeNull();
  fireEvent.change(screen.getByLabelText("Remote connection"), {
    target: { value: "2" },
  });
  fireEvent.click(
    screen.getByRole("button", { name: "Prepare client configuration" }),
  );
  await screen.findByRole("link");
});

it("fetches and verifies retained bytes explicitly, then reuses them across client formats", async () => {
  const { props, get } = workspace();
  const sample = await artifact();
  get.mockResolvedValue({ data: sample.bytes, response: sample.response });
  render(<MCPClientExport {...props} record={sample.current} />);
  expect(get).not.toHaveBeenCalled();
  fireEvent.click(
    screen.getByRole("button", { name: "Prepare client configuration" }),
  );
  await screen.findByRole("link");
  expect(get).toHaveBeenCalledTimes(1);
  fireEvent.change(screen.getByLabelText("MCP client"), {
    target: { value: "mcp-claude-code" },
  });
  fireEvent.click(
    screen.getByRole("button", { name: "Prepare client configuration" }),
  );
  expect((await screen.findByRole("link")).getAttribute("download")).toBe(
    "mcp.json",
  );
  expect(get).toHaveBeenCalledTimes(1);
});

it.each([
  "media",
  "size",
  "digest",
  "representation digest",
  "encoding",
  "JSON",
  "UTF-8",
  "BOM",
  "denied",
])("refuses invalid retained %s without a download", async (fault) => {
  const { props, get } = workspace();
  const sample = await artifact();
  if (fault === "media")
    sample.response.headers.set("Content-Type", "application/json");
  if (fault === "size") sample.response.headers.set("Content-Length", "1");
  if (fault === "digest")
    sample.response.headers.set("Content-Digest", "sha-256=:bad=:");
  if (fault === "representation digest")
    sample.current.versionRevision.representations[0]!.contentDigest = `sha256:${"0".repeat(64)}`;
  if (fault === "encoding")
    sample.response.headers.set("Content-Encoding", "gzip");
  if (["JSON", "UTF-8", "BOM"].includes(fault)) {
    const encode = (value: string) => new TextEncoder().encode(value);
    const raw =
      fault === "JSON"
        ? encode('{"remotes":[],"remotes":[]}')
        : fault === "BOM"
          ? new Uint8Array([
              0xef,
              0xbb,
              0xbf,
              ...encode(JSON.stringify(document)),
            ])
          : new Uint8Array([
              ...encode('{"extra":"'),
              0xff,
              ...encode('",' + JSON.stringify(document).slice(1)),
            ]);
    const hash = Buffer.from(await webcrypto.subtle.digest("SHA-256", raw));
    const rep = sample.current.versionRevision.representations[0]!;
    rep.artifactId = hash.toString("hex");
    rep.contentDigest = `sha256:${rep.artifactId}`;
    rep.size = raw.byteLength;
    sample.bytes = raw.buffer;
    sample.response.headers.set("Content-Length", String(raw.byteLength));
    sample.response.headers.set(
      "Content-Digest",
      `sha-256=:${hash.toString("base64")}:`,
    );
  }
  get.mockResolvedValue(
    fault === "denied"
      ? {
          error: { code: "FORBIDDEN", detail: "Content access is required." },
          response: new Response(null, { status: 403 }),
        }
      : { data: sample.bytes, response: sample.response },
  );
  render(<MCPClientExport {...props} record={sample.current} />);
  fireEvent.click(
    screen.getByRole("button", { name: "Prepare client configuration" }),
  );
  const alert = await screen.findByRole("alert");
  if (["UTF-8", "BOM"].includes(fault))
    expect(alert.textContent).not.toMatch(/checksum|type, size/);
  expect(screen.queryByRole("link")).toBeNull();
  expect(URL.createObjectURL).not.toHaveBeenCalled();
  expect(props.onFailure).toHaveBeenCalledTimes(1);
});

it("discards late artifact responses after record replacement", async () => {
  const { props, get } = workspace();
  const sample = await artifact();
  const pending = deferred<unknown>();
  get.mockReturnValue(pending.promise);
  const view = render(<MCPClientExport {...props} record={sample.current} />);
  fireEvent.click(
    screen.getByRole("button", { name: "Prepare client configuration" }),
  );
  view.rerender(<MCPClientExport {...props} record={record()} />);
  await act(async () => {
    pending.resolve({ data: sample.bytes, response: sample.response });
  });
  expect(screen.queryByRole("link")).toBeNull();
  expect(URL.createObjectURL).not.toHaveBeenCalled();
});

it("removes prepared private output on session invalidation", async () => {
  const { props } = workspace();
  const current = record();
  const lifetime = new AbortController();
  Object.defineProperty(props.client, "signal", { get: () => lifetime.signal });
  render(<MCPClientExport {...props} record={current} />);
  fireEvent.click(
    screen.getByRole("button", { name: "Prepare client configuration" }),
  );
  await screen.findByRole("link");
  act(() => lifetime.abort());
  await waitFor(() => expect(screen.queryByRole("link")).toBeNull());
  expect(URL.revokeObjectURL).toHaveBeenCalled();
});

it("revokes prepared output on known permission loss without a session abort", async () => {
  const { props } = workspace();
  const current = record();
  const view = render(<MCPClientExport {...props} record={current} />);
  fireEvent.click(
    screen.getByRole("button", { name: "Prepare client configuration" }),
  );
  const link = await screen.findByRole("link");
  const previous = link.getAttribute("href");
  view.rerender(
    <MCPClientExport
      {...props}
      record={current}
      permissionState={{
        access: { ...props.access, permissions: [], namespaceGrants: [] },
        busy: false,
        error: "",
        refresh: vi.fn(),
      }}
    />,
  );
  expect(screen.queryByRole("link")).toBeNull();
  expect(URL.revokeObjectURL).toHaveBeenCalledWith(previous);
  expect(screen.queryByLabelText("MCP client configuration")).toBeNull();
});

it("requires a representation choice and excludes non-current or non-MCP records", () => {
  const { props } = workspace();
  const current = record();
  current.versionRevision.representations.push({
    ...current.versionRevision.representations[0]!,
    id: "second",
  });
  const view = render(<MCPClientExport {...props} record={current} />);
  expect(screen.getByRole("button")).toHaveProperty("disabled", true);
  fireEvent.change(screen.getByLabelText("MCP representation"), {
    target: { value: "second" },
  });
  expect(screen.getByRole("button")).toHaveProperty("disabled", false);
  view.rerender(
    <MCPClientExport
      {...props}
      record={{ ...current, release: { ...current.release, state: "retired" } }}
    />,
  );
  expect(screen.queryByRole("button")).toBeNull();
});

it("does not expose a download when the session changes during asynchronous hashing", async () => {
  const { props, get } = workspace();
  const sample = await artifact();
  const lifetime = new AbortController();
  Object.defineProperty(props.client, "signal", { get: () => lifetime.signal });
  get.mockResolvedValue({ data: sample.bytes, response: sample.response });
  const actual = webcrypto.subtle.digest.bind(webcrypto.subtle);
  const hash = await actual("SHA-256", sample.bytes);
  const pending = deferred<ArrayBuffer>();
  const digest = vi
    .spyOn(webcrypto.subtle, "digest")
    .mockReturnValueOnce(pending.promise);
  render(<MCPClientExport {...props} record={sample.current} />);
  fireEvent.click(
    screen.getByRole("button", { name: "Prepare client configuration" }),
  );
  await waitFor(() => expect(digest).toHaveBeenCalledTimes(1));
  await act(async () => {
    lifetime.abort();
    pending.resolve(hash);
  });
  expect(screen.queryByRole("link")).toBeNull();
  expect(URL.createObjectURL).not.toHaveBeenCalled();
  expect(props.onFailure).not.toHaveBeenCalled();
});

it.each(["profile", "package only", "missing document"])(
  "explains unavailable exports: %s",
  (kind) => {
    const { props, get } = workspace();
    const current = record(
      kind === "package only" ? { name: "package-only" } : document,
    );
    const rep = current.versionRevision.representations[0]!;
    if (kind === "profile") rep.profileId = "private-mcp-profile";
    if (kind === "missing document") delete rep.data;
    render(<MCPClientExport {...props} record={current} />);
    expect(
      screen.getByRole("button", { name: "Prepare client configuration" }),
    ).toHaveProperty("disabled", true);
    expect(get).not.toHaveBeenCalled();
    expect(screen.queryByRole("link")).toBeNull();
  },
);

it.each([new TypeError("PRIVATE_TRANSPORT"), "PRIVATE_TRANSPORT"])(
  "keeps unexpected transport failures private",
  async (failure) => {
    const { props, get } = workspace();
    const sample = await artifact();
    get.mockRejectedValue(failure);
    render(<MCPClientExport {...props} record={sample.current} />);
    fireEvent.click(
      screen.getByRole("button", { name: "Prepare client configuration" }),
    );
    expect((await screen.findByRole("alert")).textContent).toContain(
      "Configuration could not be prepared",
    );
    expect(screen.queryByText(/PRIVATE_TRANSPORT/)).toBeNull();
  },
);

it("lists optional header names and allows no-header connections without inventing authentication", async () => {
  const { props } = workspace();
  const current = record({
    remotes: [
      {
        type: "sse",
        url: "https://mcp.test/one",
        headers: [{ name: "X-Optional", value: "PRIVATE" }],
      },
      { type: "sse", url: "https://mcp.test/two" },
    ],
  });
  render(<MCPClientExport {...props} record={current} />);
  fireEvent.change(screen.getByLabelText("Remote connection"), {
    target: { value: "1" },
  });
  fireEvent.click(
    screen.getByRole("button", { name: "Prepare client configuration" }),
  );
  await screen.findByRole("link");
  expect(screen.getByText(/X-Optional \(optional\)/)).toBeTruthy();
  fireEvent.change(screen.getByLabelText("Remote connection"), {
    target: { value: "2" },
  });
  fireEvent.click(
    screen.getByRole("button", { name: "Prepare client configuration" }),
  );
  await screen.findByRole("link");
  expect(screen.queryByText(/Header values are omitted/)).toBeNull();
});

it("ignores a late artifact rejection after a client-format change", async () => {
  const { props, get } = workspace();
  const sample = await artifact();
  const pending = deferred<unknown>();
  get.mockReturnValue(pending.promise);
  render(<MCPClientExport {...props} record={sample.current} />);
  fireEvent.click(
    screen.getByRole("button", { name: "Prepare client configuration" }),
  );
  fireEvent.change(screen.getByLabelText("MCP client"), {
    target: { value: "mcp-cursor" },
  });
  await act(async () => {
    pending.reject(new Error("PRIVATE_STALE"));
  });
  expect(screen.queryByRole("alert")).toBeNull();
  expect(screen.queryByRole("link")).toBeNull();
  expect(props.onFailure).not.toHaveBeenCalled();
});

it("uses a subordinate heading in the compact record preview", () => {
  const { props } = workspace();
  render(<MCPClientExport {...props} compact record={record()} />);
  expect(
    screen.getByRole("heading", { name: "MCP client configuration", level: 4 }),
  ).toBeTruthy();
});

it("refuses stored bytes when the response omits the declared length", async () => {
  const { props, get } = workspace();
  const sample = await artifact();
  sample.response.headers.delete("Content-Length");
  get.mockResolvedValue({ data: sample.bytes, response: sample.response });
  render(<MCPClientExport {...props} record={sample.current} />);
  fireEvent.click(
    screen.getByRole("button", { name: "Prepare client configuration" }),
  );
  expect((await screen.findByRole("alert")).textContent).toContain(
    "does not match",
  );
  expect(screen.queryByRole("link")).toBeNull();
});

it("does not move focus to a preview replaced before the animation frame", async () => {
  const { props } = workspace();
  let frame: FrameRequestCallback | undefined;
  vi.stubGlobal("requestAnimationFrame", (callback: FrameRequestCallback) => {
    frame = callback;
    return 1;
  });
  render(<MCPClientExport {...props} record={record()} />);
  fireEvent.click(
    screen.getByRole("button", { name: "Prepare client configuration" }),
  );
  await screen.findByRole("link");
  fireEvent.change(screen.getByLabelText("MCP client"), {
    target: { value: "mcp-cursor" },
  });
  act(() => frame?.(0));
  expect(screen.queryByLabelText("Prepared MCP configuration")).toBeNull();
});

it("keeps the new representation output when an old representation read completes late", async () => {
  const { props, get } = workspace();
  const sample = await artifact();
  const other = record({
    ...document,
    remotes: [{ type: "sse", url: "https://second.test/events" }],
  }).versionRevision.representations[0]!;
  sample.current.versionRevision.representations.push({
    ...other,
    id: "second",
  });
  const pending = deferred<unknown>();
  get.mockReturnValue(pending.promise);
  render(<MCPClientExport {...props} record={sample.current} />);
  fireEvent.change(screen.getByLabelText("MCP representation"), {
    target: { value: "rep" },
  });
  fireEvent.click(
    screen.getByRole("button", { name: "Prepare client configuration" }),
  );
  fireEvent.change(screen.getByLabelText("MCP representation"), {
    target: { value: "second" },
  });
  fireEvent.click(
    screen.getByRole("button", { name: "Prepare client configuration" }),
  );
  const href = (await screen.findByRole("link")).getAttribute("href");
  await act(async () =>
    pending.resolve({ data: sample.bytes, response: sample.response }),
  );
  expect(screen.getByRole("link").getAttribute("href")).toBe(href);
  expect(blobs).toHaveLength(1);
  expect(JSON.parse(await blobs[0]!.text())).toEqual({
    servers: {
      [`catalog-${releaseID}`]: {
        type: "sse",
        url: "https://second.test/events",
      },
    },
  });
  expect(get).toHaveBeenCalledTimes(1);
  expect(props.onFailure).not.toHaveBeenCalled();
});
