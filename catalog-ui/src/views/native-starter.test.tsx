// @vitest-environment jsdom
import { afterEach, beforeEach, expect, it, vi } from "vitest";
import { useState } from "react";
import { fireEvent, render, screen } from "@testing-library/react";
import { screenLifecycle } from "../../tests/unit/screens";
import { NativeDocumentPreview, NativeStarter } from "./NativeStarter";
import {
  newRepresentation,
  RepresentationEditor,
  type RepresentationDraft,
} from "./representations";
import { nativeStarters } from "./native-starters";

screenLifecycle("/unit/versions/new");
const create = vi.fn((_blob: Blob) => "blob:local-document");
const revoke = vi.fn();
const fetch = vi.fn();
beforeEach(() => {
  create.mockClear();
  revoke.mockClear();
  fetch.mockClear();
  vi.stubGlobal("fetch", fetch);
  vi.stubGlobal(
    "URL",
    Object.assign(class extends URL {}, {
      createObjectURL: create,
      revokeObjectURL: revoke,
    }),
  );
});
afterEach(() => {
  expect(fetch).not.toHaveBeenCalled();
  vi.unstubAllGlobals();
});

it("applies a local example and suggests its version without preparing a command", () => {
  const onUseExample = vi.fn();
  const onVersionChange = vi.fn();
  const confirm = vi.spyOn(window, "confirm");
  const value = newRepresentation("agent");
  render(
    <NativeStarter
      kind="agent"
      value={value}
      onUseExample={onUseExample}
      version=""
      onVersionChange={onVersionChange}
    />,
  );
  expect(create).not.toHaveBeenCalled();
  fireEvent.click(screen.getByRole("button", { name: "Use example" }));
  expect(onUseExample).toHaveBeenCalledExactlyOnceWith(
    expect.objectContaining({
      editorID: value.editorID,
      mediaType: "application/a2a-agent-card+json",
      content: nativeStarters("agent")[0]!.content,
    }),
  );
  expect(onVersionChange).toHaveBeenCalledExactlyOnceWith("1.0.0");
  expect(confirm).not.toHaveBeenCalled();
});

it("protects staged replacement and separately confirms a different version label", () => {
  const onUseExample = vi.fn();
  const onVersionChange = vi.fn();
  const confirm = vi.spyOn(window, "confirm").mockReturnValueOnce(false);
  const value = { ...newRepresentation("agent"), documentStaged: true };
  render(
    <NativeStarter
      kind="agent"
      value={value}
      onUseExample={onUseExample}
      version="draft-label"
      onVersionChange={onVersionChange}
    />,
  );
  const use = screen.getByRole("button", { name: "Use example" });
  fireEvent.click(use);
  expect(onUseExample).not.toHaveBeenCalled();
  expect(onVersionChange).not.toHaveBeenCalled();
  confirm.mockReturnValueOnce(true).mockReturnValueOnce(false);
  fireEvent.click(use);
  expect(onUseExample).toHaveBeenCalledOnce();
  expect(onUseExample.mock.calls[0]![0]).not.toHaveProperty("documentStaged");
  expect(onVersionChange).not.toHaveBeenCalled();
  expect(confirm.mock.calls[2]![0]).toContain(
    "Replace Version label draft-label",
  );
  confirm.mockReturnValue(true);
  fireEvent.click(use);
  expect(onVersionChange).toHaveBeenCalledExactlyOnceWith("1.0.0");
});

it("selects an explicit MCP format and preserves other row settings", () => {
  const onUseExample = vi.fn();
  render(
    <NativeStarter
      kind="mcp_server"
      value={{
        ...newRepresentation("mcp_server"),
        role: "metadata",
        profileId: "profile",
      }}
      onUseExample={onUseExample}
    />,
  );
  fireEvent.change(screen.getByLabelText("Native example"), {
    target: { value: "mcp-registry-npm" },
  });
  expect(screen.getByText(/Package version is separate/)).toBeTruthy();
  fireEvent.click(screen.getByRole("button", { name: "Use example" }));
  expect(onUseExample).toHaveBeenCalledExactlyOnceWith(
    expect.objectContaining({
      role: "metadata",
      profileId: "profile",
      mediaType: "application/vnd.mcp.server+json",
      content: nativeStarters("mcp_server")[2]!.content,
    }),
  );
});

it("previews and downloads the current controlled text with owned Blob URLs", async () => {
  const onUseExample = vi.fn();
  const content =
    '{  "name":"Agent", "description":"<img data-injection src=x>", "supportedInterfaces":[], "version":"1.0.0" }';
  const value = { ...newRepresentation("agent"), content };
  const view = render(
    <NativeStarter kind="agent" value={value} onUseExample={onUseExample} />,
  );
  fireEvent.click(
    screen.getByRole("button", { name: "Preview and download document" }),
  );
  expect(screen.getByRole("group", { name: "JSON document" }).textContent).toBe(
    content,
  );
  expect(view.container.querySelector("[data-injection]")).toBeNull();
  const link = screen.getByRole("link", { name: "Download agent-card.json" });
  expect(link.getAttribute("href")).toBe("blob:local-document");
  expect(link.getAttribute("download")).toBe("agent-card.json");
  const blob = create.mock.calls[0]![0];
  expect(blob.type).toBe("text/plain;charset=utf-8");
  const decoded = await new Promise((resolve) => {
    const reader = new FileReader();
    reader.onload = () => resolve(reader.result);
    reader.readAsText(blob);
  });
  expect(decoded).toBe(content);
  view.rerender(
    <NativeStarter
      kind="agent"
      value={{ ...value, content: content + "\n" }}
      onUseExample={onUseExample}
    />,
  );
  expect(screen.getByRole("group", { name: "JSON document" }).textContent).toBe(
    content + "\n",
  );
  expect(revoke).toHaveBeenCalledExactlyOnceWith("blob:local-document");
  fireEvent.click(
    screen.getByRole("button", { name: "Hide document preview" }),
  );
  expect(screen.queryByRole("link")).toBeNull();
  expect(revoke).toHaveBeenCalledTimes(2);
  expect(onUseExample).not.toHaveBeenCalled();
});

it("supports a controlled preview toggle without inserting an inline duplicate", () => {
  const onPreviewChange = vi.fn();
  const onUseExample = vi.fn();
  const value = {
    ...newRepresentation("agent"),
    content: nativeStarters("agent")[0]!.content,
  };
  const view = render(
    <NativeStarter
      kind="agent"
      value={value}
      onUseExample={onUseExample}
      previewOpen={false}
      onPreviewChange={onPreviewChange}
      inlinePreview={false}
    />,
  );
  fireEvent.click(
    screen.getByRole("button", { name: "Preview and download document" }),
  );
  expect(onPreviewChange).toHaveBeenCalledExactlyOnceWith(true);
  expect(
    screen.queryByRole("region", { name: "Current document preview" }),
  ).toBeNull();
  expect(create).not.toHaveBeenCalled();
  view.rerender(
    <NativeStarter
      kind="agent"
      value={value}
      onUseExample={onUseExample}
      previewOpen
      onPreviewChange={onPreviewChange}
      inlinePreview={false}
    />,
  );
  fireEvent.click(
    screen.getByRole("button", { name: "Hide document preview" }),
  );
  expect(onPreviewChange).toHaveBeenLastCalledWith(false);
  expect(create).not.toHaveBeenCalled();
  expect(onUseExample).not.toHaveBeenCalled();
});

it("places one live preview after editable fields and revokes replaced downloads", async () => {
  const changed = vi.fn();
  const original = "  " + nativeStarters("agent")[0]!.content + "\n";
  const initial = { ...newRepresentation("agent"), content: original };
  create
    .mockReturnValueOnce("blob:original")
    .mockReturnValueOnce("blob:changed");
  function Editor() {
    const [value, setValue] = useState<RepresentationDraft>(initial);
    return (
      <RepresentationEditor
        kind="agent"
        index={0}
        value={value}
        onChange={(next) => {
          changed(next);
          setValue(next);
        }}
      />
    );
  }
  const view = render(<Editor />);
  expect(create).not.toHaveBeenCalled();
  fireEvent.click(
    screen.getByRole("button", { name: "Preview and download document" }),
  );
  const preview = screen.getByRole("region", {
    name: "Current document preview",
  });
  const editor = screen.getByRole("textbox", {
    name: "Standards document (JSON)",
  });
  expect(
    editor.compareDocumentPosition(preview) & Node.DOCUMENT_POSITION_FOLLOWING,
  ).toBeTruthy();
  expect(
    screen.getAllByRole("region", { name: "Current document preview" }),
  ).toHaveLength(1);
  expect(screen.getByRole("group", { name: "JSON document" }).textContent).toBe(
    original,
  );
  expect(
    screen
      .getByRole("link", { name: "Download agent-card.json" })
      .getAttribute("href"),
  ).toBe("blob:original");
  expect(changed).not.toHaveBeenCalled();
  const updated =
    original.replace("Order exception Agent", "Updated Agent") + "\n";
  fireEvent.change(editor, { target: { value: updated } });
  expect(screen.getByRole("group", { name: "JSON document" }).textContent).toBe(
    updated,
  );
  expect(
    screen
      .getByRole("link", { name: "Download agent-card.json" })
      .getAttribute("href"),
  ).toBe("blob:changed");
  expect(revoke).toHaveBeenCalledExactlyOnceWith("blob:original");
  const blob = create.mock.calls[1]![0];
  const decoded = await new Promise((resolve) => {
    const reader = new FileReader();
    reader.onload = () => resolve(reader.result);
    reader.readAsText(blob);
  });
  expect(decoded).toBe(updated);
  view.unmount();
  expect(revoke).toHaveBeenLastCalledWith("blob:changed");
  expect(changed).toHaveBeenCalledOnce();
});

it("removes a detached preview when its current draft becomes a remote source", () => {
  const value = {
    ...newRepresentation("agent"),
    content: nativeStarters("agent")[0]!.content,
  };
  const view = render(
    <NativeDocumentPreview kind="agent" value={value} open={false} />,
  );
  expect(create).not.toHaveBeenCalled();
  view.rerender(<NativeDocumentPreview kind="agent" value={value} open />);
  expect(
    screen.getByRole("region", { name: "Current document preview" }),
  ).toBeTruthy();
  view.rerender(
    <NativeDocumentPreview
      kind="agent"
      value={{ ...value, mode: "url", url: "https://example.com/card.json" }}
      open
    />,
  );
  expect(
    screen.queryByRole("region", { name: "Current document preview" }),
  ).toBeNull();
  expect(screen.queryByRole("link")).toBeNull();
  expect(revoke).toHaveBeenCalledExactlyOnceWith("blob:local-document");
});

it("offers imported native versions with explicit replacement and no document edits", () => {
  const onUseExample = vi.fn();
  const onVersionChange = vi.fn();
  const value = {
    ...newRepresentation("agent"),
    content: nativeStarters("agent")[0]!.content,
  };
  const confirm = vi
    .spyOn(window, "confirm")
    .mockReturnValueOnce(false)
    .mockReturnValueOnce(true);
  const view = render(
    <NativeStarter
      kind="agent"
      value={value}
      onUseExample={onUseExample}
      version="previous"
      onVersionChange={onVersionChange}
    />,
  );
  const use = screen.getByRole("button", {
    name: "Use document version 1.0.0 as Version label",
  });
  fireEvent.click(use);
  expect(onVersionChange).not.toHaveBeenCalled();
  fireEvent.click(use);
  expect(onVersionChange).toHaveBeenCalledExactlyOnceWith("1.0.0");
  expect(onUseExample).not.toHaveBeenCalled();
  view.rerender(
    <NativeStarter
      kind="agent"
      value={value}
      onUseExample={onUseExample}
      version="1.0.0"
      onVersionChange={onVersionChange}
    />,
  );
  expect(
    screen.queryByRole("button", { name: /Use document version/ }),
  ).toBeNull();
  confirm.mockReturnValue(true);
  fireEvent.click(screen.getByRole("button", { name: "Use example" }));
  expect(onVersionChange).toHaveBeenCalledOnce();
});

it("keeps wrongly shaped JSON visible and explains media-type mismatch without converting it", () => {
  const onUseExample = vi.fn();
  const onVersionChange = vi.fn();
  const value = {
    ...newRepresentation("mcp_server"),
    mediaType: "application/vnd.mcp.server+json",
    content: nativeStarters("mcp_server")[0]!.content,
  };
  const view = render(
    <NativeStarter
      kind="mcp_server"
      value={value}
      onUseExample={onUseExample}
      onVersionChange={onVersionChange}
    />,
  );
  expect(
    screen.getByText(/declared document schema does not match/),
  ).toBeTruthy();
  expect(
    screen.queryByRole("button", { name: /Use document version/ }),
  ).toBeNull();
  view.rerender(
    <NativeStarter
      kind="mcp_server"
      value={{ ...value, content: '{"jsonrpc":"2.0","result":{}}' }}
      onUseExample={onUseExample}
      onVersionChange={onVersionChange}
    />,
  );
  expect(
    screen.getByText(/ARD wrappers and MCP protocol responses/),
  ).toBeTruthy();
  fireEvent.click(
    screen.getByRole("button", { name: "Preview and download document" }),
  );
  expect(screen.getByRole("group", { name: "JSON document" }).textContent).toBe(
    '{"jsonrpc":"2.0","result":{}}',
  );
  expect(onUseExample).not.toHaveBeenCalled();
  expect(onVersionChange).not.toHaveBeenCalled();
});

it("leaves Plugin file preparation to the lazy archive editor", () => {
  render(
    <NativeStarter
      kind="plugin"
      value={newRepresentation("plugin")}
      onUseExample={vi.fn()}
    />,
  );
  expect(screen.queryByRole("button", { name: "Use example" })).toBeNull();
  expect(create).not.toHaveBeenCalled();
});

it("does not preview URL or archive contents and protects replacing existing sources", () => {
  const onUseExample = vi.fn();
  const confirm = vi.spyOn(window, "confirm").mockReturnValue(false);
  render(
    <NativeStarter
      kind="skill"
      value={{
        ...newRepresentation("skill"),
        mode: "package",
        packageData: "package",
        handoff: "handoff",
      }}
      onUseExample={onUseExample}
    />,
  );
  expect(
    screen.queryByRole("button", { name: "Preview and download document" }),
  ).toBeNull();
  fireEvent.click(screen.getByRole("button", { name: "Use example" }));
  expect(confirm).toHaveBeenCalledOnce();
  expect(onUseExample).not.toHaveBeenCalled();
});

it("moves focus to a narrow preview and back to its own editor without changing history or repeating focus during edits", () => {
  vi.mocked(window.matchMedia).mockReturnValue({
    matches: true,
  } as MediaQueryList);
  const value = {
    ...newRepresentation("agent"),
    content: nativeStarters("agent")[0]!.content,
  };
  const changed = vi.fn();
  const view = render(
    <NativeStarter kind="agent" value={value} onUseExample={changed} />,
  );
  const toggle = screen.getByRole("button", {
    name: "Preview and download document",
  });
  const location = window.location.href;
  fireEvent.click(toggle);
  const panel = screen.getByRole("region", {
    name: "Current document preview",
  });
  expect(document.activeElement).toBe(panel);
  expect(
    screen.getByRole("heading", { name: "Document preview" }),
  ).toBeTruthy();
  expect(panel.textContent).toContain("agent-card.json");
  expect(toggle.getAttribute("aria-controls")).toBe(panel.id);
  fireEvent.click(screen.getByRole("button", { name: "Back to editor" }));
  expect(document.activeElement).toBe(toggle);
  view.rerender(
    <NativeStarter
      kind="agent"
      value={{
        ...value,
        content: value.content.replace(
          "Order exception Agent",
          "Updated Agent",
        ),
      }}
      onUseExample={changed}
    />,
  );
  expect(document.activeElement).toBe(toggle);
  expect(window.location.href).toBe(location);
  expect(changed).not.toHaveBeenCalled();
});
