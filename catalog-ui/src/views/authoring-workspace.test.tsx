// @vitest-environment jsdom
// Only transport and missing dialog primitives are substituted; no Catalog is started.
import { expect, it, vi } from "vitest";
import {
  act,
  fireEvent,
  render,
  screen,
  waitFor,
  within,
} from "@testing-library/react";
import { APIError } from "../api/client";
import { AuthoringWorkspace } from "./AuthoringWorkspace";
import { confirmDiscard } from "./workspace";
import { deferred } from "../../tests/unit/fixtures";
import { evidence, versionRevision } from "../../tests/unit/responses";
import { required } from "../../tests/required";
import {
  administrator,
  change,
  click,
  confirmCommand,
  etagReply,
  namespace,
  readReplies,
  resource,
  screenLifecycle,
  screenWorkspace,
  version,
} from "../../tests/unit/screens";

screenLifecycle("/unit/resources/new");

const source = '{"name":"Unchanged imported Agent","extension":{"x":true}}';
const sourceData = {
  name: "Unchanged imported Agent",
  extension: { x: true },
};
const savedVersion = {
  version: { ...version, candidateRevisionId: versionRevision.id },
  revision: versionRevision,
  evidence,
};

async function newItem() {
  const workspace = screenWorkspace();
  readReplies(workspace.get, {
    "/api/v1/namespaces": { items: [namespace] },
  });
  const view = render(
    <AuthoringWorkspace {...workspace.props} access={administrator} />,
  );
  await screen.findByRole("option", {
    name: `${namespace.displayName} / ${namespace.slug} / ${namespace.id}`,
  });
  change("Namespace", namespace.id);
  change("Canonical resource identifier", resource.canonicalIdentifier);
  return { ...workspace, view };
}

function enterContent(text = source) {
  click("Next: Content");
  change("Version label", version.version);
  change("Standards document (JSON)", text);
}

function commandKey(call: unknown[] | undefined) {
  const input = required(call)[1] as {
    params: { header: Record<string, string> };
  };
  return input.params.header["Idempotency-Key"];
}

it("checks Version labels in code points and clears field errors without changing imported content", async () => {
  const workspace = await newItem();
  enterContent();
  const input = screen.getByLabelText("Version label") as HTMLInputElement;
  change("Version label", "\u0085 ");
  fireEvent.invalid(input);
  expect(input.getAttribute("aria-invalid")).toBe("true");
  expect(input.validationMessage).toBe("Enter a version label.");
  change("Version label", "🙂".repeat(128));
  expect(input.validity.valid).toBe(true);
  expect(input.hasAttribute("aria-invalid")).toBe(false);
  change("Version label", "🙂".repeat(129));
  fireEvent.blur(input);
  expect(input.getAttribute("aria-invalid")).toBe("true");
  expect(input.validationMessage).toContain("128");
  change("Version label", "\uFEFF");
  expect(input.validity.valid).toBe(true);
  expect(screen.getByLabelText("Standards document (JSON)")).toHaveProperty(
    "value",
    source,
  );
  expect(workspace.post).not.toHaveBeenCalled();
});

it("keeps malformed advanced JSON visible before Listing and preserves backend validation for valid JSON", async () => {
  const workspace = await newItem();
  enterContent("{broken");
  fireEvent.click(screen.getByLabelText("Include Listing details"));
  click("Next: Listing details");
  const document = screen.getByLabelText("Standards document (JSON)");
  await waitFor(() =>
    expect(document.getAttribute("aria-invalid")).toBe("true"),
  );
  expect(screen.getByRole("alert").textContent).toContain("Enter valid JSON");
  expect(
    screen.getByLabelText("Version label").closest("fieldset")?.hidden,
  ).toBe(false);
  expect(workspace.post).not.toHaveBeenCalled();
  change("Standards document (JSON)", source);
  expect(document.hasAttribute("aria-invalid")).toBe(false);
  click("Next: Listing details");
  expect(screen.getByRole("button", { name: "Back: Content" })).toBeTruthy();
  expect(workspace.post).not.toHaveBeenCalled();
});

it("clears an archive error when Plugin preparation removes that field, then checks it again on return", async () => {
  const workspace = await newItem();
  readReplies(workspace.get, {
    "/api/v1/namespaces": { items: [namespace] },
    "/api/v1/publishers": { items: [] },
  });
  change("Kind", "plugin");
  click("Next: Content");
  await screen.findByLabelText("Plugin archive URL");
  change("Version label", "1.0.0");
  change("Plugin archive URL", "http://example.test/plugin.zip");
  fireEvent.click(screen.getByLabelText("Include Listing details"));
  click("Next: Listing details");
  expect((await screen.findByRole("alert")).textContent).toContain(
    "Use a credential-free HTTPS URL",
  );
  change("Plugin input", "prepare");
  await screen.findByLabelText("Plugin name");
  await waitFor(() => expect(screen.queryByRole("alert")).toBeNull());
  change("Plugin input", "archive");
  expect(screen.getByLabelText("Plugin archive URL")).toHaveProperty(
    "value",
    "http://example.test/plugin.zip",
  );
  click("Next: Listing details");
  expect((await screen.findByRole("alert")).textContent).toContain(
    "Use a credential-free HTTPS URL",
  );
  change("Plugin archive URL", "https://example.test/plugin.zip");
  click("Next: Listing details");
  expect(screen.getByRole("button", { name: "Back: Content" })).toBeTruthy();
  expect(workspace.post).not.toHaveBeenCalled();
});

it("moves Next and Back without mutations or replacing controlled content and staged files", async () => {
  const workspace = await newItem();
  enterContent();
  const editor = screen.getByLabelText("Standards document (JSON)");
  const bytes = new TextEncoder().encode('{"name":"Staged replacement"}');
  fireEvent.change(screen.getByLabelText("Document file"), {
    target: {
      files: [
        {
          name: "staged-card.json",
          size: bytes.length,
          arrayBuffer: async () => bytes.buffer,
        },
      ],
    },
  });
  await screen.findByRole("button", { name: "Use loaded document" });
  click("Back: Basics");
  expect(screen.getByLabelText("Canonical resource identifier")).toHaveProperty(
    "value",
    resource.canonicalIdentifier,
  );
  click("Next: Content");
  expect(screen.getByLabelText("Standards document (JSON)")).toBe(editor);
  expect(editor).toHaveProperty("value", source);
  expect(screen.getByLabelText("Version label")).toHaveProperty(
    "value",
    version.version,
  );
  expect(
    screen.getByRole("button", { name: "Use loaded document" }),
  ).toBeTruthy();
  expect(screen.getByText("staged-card.json")).toBeTruthy();
  expect(workspace.post).not.toHaveBeenCalled();
  expect(workspace.patch).not.toHaveBeenCalled();
  expect(workspace.remove).not.toHaveBeenCalled();
});

it.each([version.version, `\u0085${version.version}\u0085`])(
  "captures a combined save with separate keys and acknowledgement using label %j",
  async (labelInput) => {
    const workspace = await newItem();
    const resourceReply = deferred<unknown>();
    const versionReply = deferred<unknown>();
    workspace.post
      .mockReturnValueOnce(resourceReply.promise)
      .mockReturnValueOnce(versionReply.promise);
    enterContent();
    change("Version label", labelInput);
    click("Save content draft");
    const dialog = await screen.findByRole("dialog");
    expect(
      within(dialog).getByLabelText("Command summary").textContent,
    ).toContain(resource.canonicalIdentifier);
    expect(
      within(dialog).getByLabelText("Command summary").textContent,
    ).toContain(version.version);
    expect(workspace.post).not.toHaveBeenCalled();
    click("Cancel");
    expect(screen.getByLabelText("Standards document (JSON)")).toHaveProperty(
      "value",
      source,
    );
    await confirmCommand("Save content draft");
    expect(workspace.post).toHaveBeenCalledTimes(1);
    expect(workspace.post.mock.calls[0]).toMatchObject([
      "/api/v1/resources",
      {
        body: {
          namespaceId: namespace.id,
          canonicalIdentifier: resource.canonicalIdentifier,
          kind: "agent",
        },
      },
    ]);
    await act(async () => resourceReply.resolve(etagReply(resource)));
    expect(workspace.post).toHaveBeenCalledTimes(2);
    expect(workspace.post.mock.calls[1]).toMatchObject([
      "/api/v1/resources/{resourceId}/versions",
      {
        params: { path: { resourceId: resource.id } },
        body: {
          version: version.version,
          representations: [
            {
              role: "primary",
              mediaType: "application/a2a-agent-card+json",
              data: {
                name: "Unchanged imported Agent",
                extension: { x: true },
              },
            },
          ],
        },
      },
    ]);
    const resourceKey = commandKey(workspace.post.mock.calls[0]);
    const versionKey = commandKey(workspace.post.mock.calls[1]);
    expect(resourceKey).toEqual(expect.any(String));
    expect(versionKey).toEqual(expect.any(String));
    expect(versionKey).not.toBe(resourceKey);
    await act(async () => versionReply.resolve(etagReply(savedVersion)));
    expect(
      await screen.findByRole("link", { name: "Open saved version" }),
    ).toHaveProperty("href", `${location.origin}/unit/versions/${version.id}`);
  },
);

it("retains acknowledged Resource progress and content after a definitive Version failure", async () => {
  const workspace = await newItem();
  workspace.post
    .mockResolvedValueOnce(etagReply(resource))
    .mockRejectedValueOnce(new APIError(400, "INVALID_ARGUMENT"))
    .mockResolvedValueOnce(etagReply(savedVersion));
  enterContent();
  await confirmCommand("Save content draft");
  const dialog = screen.getByRole("dialog");
  await within(dialog).findByRole("alert");
  expect(
    within(dialog).queryByRole("button", { name: "Retry same command" }),
  ).toBeNull();
  expect(
    screen.getByRole("link", { name: "Open created resource" }),
  ).toHaveProperty("href", `${location.origin}/unit/resources/${resource.id}`);
  click("Back to form");
  expect(screen.getByLabelText("Standards document (JSON)")).toHaveProperty(
    "value",
    source,
  );
  expect(screen.getByLabelText("Version label")).toHaveProperty(
    "value",
    version.version,
  );
  click("Back: Basics");
  expect(screen.getByRole("group", { name: "Basics" })).toHaveProperty(
    "disabled",
    true,
  );
  expect(screen.getByLabelText("Canonical resource identifier")).toHaveProperty(
    "value",
    resource.canonicalIdentifier,
  );
  expect(screen.getByRole("button", { name: "Next: Content" })).toHaveProperty(
    "disabled",
    false,
  );
  click("Next: Content");
  await confirmCommand("Save content draft");
  await screen.findByRole("link", { name: "Open saved version" });
  expect(workspace.post.mock.calls.map(([path]) => path)).toEqual([
    "/api/v1/resources",
    "/api/v1/resources/{resourceId}/versions",
    "/api/v1/resources/{resourceId}/versions",
  ]);
  expect(workspace.post.mock.calls[2]).toMatchObject([
    "/api/v1/resources/{resourceId}/versions",
    {
      params: { path: { resourceId: resource.id } },
      body: {
        version: version.version,
        representations: [{ data: sourceData }],
      },
    },
  ]);
});

it.each([
  ["namespace target", { namespaceId: "another-namespace" }],
  [
    "canonical target",
    { canonicalIdentifier: "urn:air:example.com:agents:other" },
  ],
  ["tenant", { tenantId: "another-tenant" }],
  ["missing tenant", { tenantId: undefined }],
  ["kind", { kind: "skill" }],
  ["unsafe ID", { id: "../another-resource" }],
] as const)(
  "treats an acknowledged Resource with a malformed %s as uncertain and recovers the same command",
  async (_name, mismatch) => {
    const workspace = await newItem();
    workspace.post
      .mockResolvedValueOnce(etagReply({ ...resource, ...mismatch }))
      .mockResolvedValueOnce(etagReply(resource))
      .mockResolvedValueOnce(etagReply(savedVersion));
    enterContent();
    await confirmCommand("Save content draft");
    const dialog = screen.getByRole("dialog");
    await within(dialog).findByRole("button", { name: "Retry same command" });
    expect(within(dialog).getByRole("alert").textContent).toContain(
      "outcome is unknown",
    );
    expect(
      within(dialog).getByRole("button", { name: "Back to form" }),
    ).toHaveProperty("disabled", true);
    expect(
      screen.queryByRole("link", { name: "Open created resource" }),
    ).toBeNull();
    expect(workspace.post).toHaveBeenCalledTimes(1);
    expect(workspace.props.onFailure).toHaveBeenCalledWith(
      expect.objectContaining({ code: "invalid_api_response" }),
    );
    await act(async () => click("Retry same command"));
    await screen.findByRole("link", { name: "Open saved version" });
    expect(workspace.post.mock.calls[1]).toEqual(workspace.post.mock.calls[0]);
    expect(workspace.post.mock.calls[2]).toMatchObject([
      "/api/v1/resources/{resourceId}/versions",
      { params: { path: { resourceId: resource.id } } },
    ]);
    expect(commandKey(workspace.post.mock.calls[2])).not.toBe(
      commandKey(workspace.post.mock.calls[0]),
    );
    expect(screen.getByLabelText("Standards document (JSON)")).toHaveProperty(
      "value",
      source,
    );
  },
);

it("preserves mixed representations across steps and resets item content and keys for Add another", async () => {
  const workspace = await newItem();
  const nextIdentifier = "urn:air:example.com:agents:second";
  const nextResource = {
    ...resource,
    id: "resource-2",
    canonicalIdentifier: nextIdentifier,
  };
  workspace.post
    .mockResolvedValueOnce(etagReply(resource))
    .mockResolvedValueOnce(etagReply(savedVersion))
    .mockResolvedValueOnce(etagReply(nextResource))
    .mockResolvedValueOnce(
      etagReply({
        ...savedVersion,
        version: {
          ...version,
          id: "version-2",
          resourceId: nextResource.id,
          candidateRevisionId: versionRevision.id,
        },
        revision: { ...versionRevision, resourceVersionId: "version-2" },
      }),
    );
  enterContent();
  click("Add representation");
  const secondary = within(screen.getByRole("group", { name: /content 2$/ }));
  fireEvent.change(secondary.getByLabelText("Content source"), {
    target: { value: "content" },
  });
  fireEvent.change(secondary.getByLabelText("Representation role"), {
    target: { value: "documentation" },
  });
  fireEvent.change(secondary.getByLabelText("Media type"), {
    target: { value: "text/markdown" },
  });
  fireEvent.change(secondary.getByLabelText("Profile ID (optional)"), {
    target: { value: "retained-document-profile" },
  });
  fireEvent.change(secondary.getByLabelText("Text content"), {
    target: { value: "# Exact supporting document\n" },
  });
  click("Back: Basics");
  click("Next: Content");
  expect(secondary.getByLabelText("Text content")).toHaveProperty(
    "value",
    "# Exact supporting document\n",
  );
  await confirmCommand("Save content draft");
  await screen.findByRole("link", { name: "Open saved version" });
  expect(workspace.post.mock.calls[1]).toMatchObject([
    "/api/v1/resources/{resourceId}/versions",
    {
      body: {
        representations: [
          { role: "primary", data: sourceData },
          {
            role: "documentation",
            mediaType: "text/markdown",
            profileId: "retained-document-profile",
            content: "# Exact supporting document\n",
          },
        ],
      },
    },
  ]);
  click("Add another item");
  expect(screen.getByLabelText("Namespace")).toHaveProperty(
    "value",
    namespace.id,
  );
  expect(screen.getByLabelText("Canonical resource identifier")).toHaveProperty(
    "value",
    "",
  );
  expect(screen.getByLabelText("Version label")).toHaveProperty("value", "");
  expect(screen.getByLabelText("Standards document (JSON)")).toHaveProperty(
    "value",
    "",
  );
  expect(screen.queryByLabelText("Text content")).toBeNull();
  expect(screen.getAllByLabelText("Representation role")).toHaveLength(1);
  expect(
    screen.getByRole("link", { name: "Open saved version" }),
  ).toHaveProperty("href", `${location.origin}/unit/versions/${version.id}`);
  change("Canonical resource identifier", nextIdentifier);
  enterContent('{"name":"Second item"}');
  await confirmCommand("Save content draft");
  expect(workspace.post).toHaveBeenCalledTimes(4);
  expect(workspace.post.mock.calls[2]).toMatchObject([
    "/api/v1/resources",
    { body: { canonicalIdentifier: nextIdentifier } },
  ]);
  expect(workspace.post.mock.calls[3]).toMatchObject([
    "/api/v1/resources/{resourceId}/versions",
    {
      params: { path: { resourceId: nextResource.id } },
      body: { representations: [{ data: { name: "Second item" } }] },
    },
  ]);
  expect(new Set(workspace.post.mock.calls.map(commandKey)).size).toBe(4);
});

it("creates a Version for a known Resource without repeating identity creation or directory reads", async () => {
  const workspace = screenWorkspace();
  workspace.post.mockResolvedValue(etagReply(savedVersion));
  render(
    <AuthoringWorkspace
      {...workspace.props}
      access={administrator}
      resource={resource}
    />,
  );
  change("Version label", version.version);
  change("Standards document (JSON)", source);
  expect(screen.queryByRole("button", { name: "Next: Content" })).toBeNull();
  expect(screen.queryByRole("button", { name: "Back: Basics" })).toBeNull();
  await confirmCommand("Create version");
  await screen.findByRole("link", { name: "Open saved version" });
  expect(workspace.post).toHaveBeenCalledExactlyOnceWith(
    "/api/v1/resources/{resourceId}/versions",
    expect.objectContaining({
      params: {
        path: { resourceId: resource.id },
        header: { "Idempotency-Key": expect.any(String) as unknown },
      },
    }),
  );
  expect(workspace.get).not.toHaveBeenCalled();
});

it("creates identity only, then continues with the retained content and without repeating Resource creation", async () => {
  const workspace = await newItem();
  workspace.post
    .mockResolvedValueOnce(etagReply(resource))
    .mockResolvedValueOnce(etagReply(savedVersion));
  enterContent();
  click("Back: Basics");
  await waitFor(() =>
    expect(screen.getByLabelText("Namespace ID")).toBe(document.activeElement),
  );
  await confirmCommand("Create resource");
  const continuation = await screen.findByRole("button", {
    name: "Continue to content",
  });
  const confirmation = vi.spyOn(window, "confirm").mockReturnValue(false);
  expect(confirmDiscard()).toBe(false);
  expect(confirmation).toHaveBeenCalledOnce();
  expect(screen.getByLabelText("Standards document (JSON)")).toHaveProperty(
    "value",
    source,
  );
  expect(continuation).toHaveProperty("disabled", false);
  expect(workspace.post.mock.calls.map(([path]) => path)).toEqual([
    "/api/v1/resources",
  ]);
  fireEvent.click(continuation);
  await waitFor(() =>
    expect(screen.getByLabelText("Version label")).toBe(document.activeElement),
  );
  expect(screen.getByLabelText("Version label")).toHaveProperty(
    "value",
    version.version,
  );
  expect(screen.getByLabelText("Standards document (JSON)")).toHaveProperty(
    "value",
    source,
  );
  await confirmCommand("Save content draft");
  await screen.findByRole("link", { name: "Open saved version" });
  expect(workspace.post.mock.calls.map(([path]) => path)).toEqual([
    "/api/v1/resources",
    "/api/v1/resources/{resourceId}/versions",
  ]);
  expect(commandKey(workspace.post.mock.calls[1])).not.toBe(
    commandKey(workspace.post.mock.calls[0]),
  );
  expect(confirmDiscard()).toBe(true);
  expect(confirmation).toHaveBeenCalledOnce();
});

it("keeps empty content continuation clean after identity-only creation and protects later Version edits", async () => {
  const workspace = await newItem();
  workspace.post.mockResolvedValueOnce(etagReply(resource));
  await confirmCommand("Create resource");
  const continuation = await screen.findByRole("button", {
    name: "Continue to content",
  });
  fireEvent.click(continuation);
  await waitFor(() =>
    expect(screen.getByLabelText("Version label")).toBe(document.activeElement),
  );
  const confirmation = vi.spyOn(window, "confirm").mockReturnValue(false);
  expect(screen.getByLabelText("Version label")).toHaveProperty("value", "");
  expect(screen.getByLabelText("Standards document (JSON)")).toHaveProperty(
    "value",
    "",
  );
  expect(confirmDiscard()).toBe(true);
  expect(confirmation).not.toHaveBeenCalled();
  change("Version label", version.version);
  expect(confirmDiscard()).toBe(false);
  change("Version label", "");
  expect(confirmDiscard()).toBe(true);
  change("Standards document (JSON)", source);
  expect(confirmDiscard()).toBe(false);
  expect(confirmation).toHaveBeenCalledTimes(2);
  expect(workspace.post.mock.calls.map(([path]) => path)).toEqual([
    "/api/v1/resources",
  ]);
});

it.each(["", "urn:air:localhost:helper", "x".repeat(513)])(
  "keeps invalid Basics visible, focuses the error and permits correction before Next: %s",
  async (identifier) => {
    const workspace = await newItem();
    change("Canonical resource identifier", identifier);
    click("Next: Content");
    const control = screen.getByLabelText("Canonical resource identifier");
    await waitFor(() =>
      expect(control.getAttribute("aria-invalid")).toBe("true"),
    );
    expect(control).toBe(document.activeElement);
    expect((control as HTMLInputElement).validationMessage).not.toBe("");
    expect(
      document.getElementById(
        control.getAttribute("aria-describedby")!.split(" ")[0]!,
      )?.textContent,
    ).toBe((control as HTMLInputElement).validationMessage);
    expect(screen.getByRole("group", { name: "Basics" })).toBeTruthy();
    expect(workspace.post).not.toHaveBeenCalled();
    change("Canonical resource identifier", resource.canonicalIdentifier);
    click("Next: Content");
    await waitFor(() =>
      expect(screen.getByLabelText("Version label")).toBe(
        document.activeElement,
      ),
    );
    expect(screen.queryByRole("alert")).toBeNull();
    expect(workspace.post).not.toHaveBeenCalled();
  },
);

it("keeps unsaved content protected at an identity-only receipt when edit permission is removed", async () => {
  const workspace = await newItem();
  workspace.post.mockResolvedValueOnce(etagReply(resource));
  enterContent();
  click("Back: Basics");
  await confirmCommand("Create resource");
  await screen.findByRole("button", { name: "Continue to content" });
  workspace.view.rerender(
    <AuthoringWorkspace
      {...workspace.props}
      access={{
        ...administrator,
        permissions: ["resource.create"],
        availablePermissions: ["resource.create"],
      }}
    />,
  );
  const continuation = screen.getByRole("button", {
    name: "Continue to content",
  });
  expect(continuation).toHaveProperty("disabled", true);
  expect(
    screen.getByText(
      "The resource.edit permission is required in this namespace.",
    ),
  ).toBeTruthy();
  expect(screen.queryByRole("button", { name: "Add another item" })).toBeNull();
  const confirmation = vi.spyOn(window, "confirm").mockReturnValue(false);
  expect(confirmDiscard()).toBe(false);
  expect(confirmation).toHaveBeenCalledOnce();
  fireEvent.click(continuation);
  expect(screen.getByLabelText("Standards document (JSON)")).toHaveProperty(
    "value",
    source,
  );
  expect(screen.getByLabelText("Version label")).toHaveProperty(
    "value",
    version.version,
  );
  expect(workspace.post).toHaveBeenCalledTimes(1);
  workspace.view.rerender(
    <AuthoringWorkspace {...workspace.props} access={administrator} />,
  );
  expect(continuation).toHaveProperty("disabled", false);
  fireEvent.click(continuation);
  await waitFor(() =>
    expect(screen.getByLabelText("Version label")).toBe(document.activeElement),
  );
  expect(screen.getByLabelText("Standards document (JSON)")).toHaveProperty(
    "value",
    source,
  );
  expect(workspace.post).toHaveBeenCalledTimes(1);
});

it("changes a clean kind without prompting, then protects populated rows until replacement is confirmed", async () => {
  const workspace = await newItem();
  const confirmation = vi.spyOn(window, "confirm").mockReturnValue(false);
  change("Kind", "agent");
  change("Kind", "skill");
  expect(confirmation).not.toHaveBeenCalled();
  click("Next: Content");
  change("Version label", "skill-release");
  change(
    "Text content",
    "---\nname: helper\ndescription: Useful instructions.\n---\nOriginal instructions.\n",
  );
  click("Add representation");
  const first = within(screen.getByRole("group", { name: /content 1$/ }));
  const second = within(screen.getByRole("group", { name: /content 2$/ }));
  fireEvent.change(second.getByLabelText("Text content"), {
    target: { value: "# Supporting instructions\n" },
  });
  click("Back: Basics");
  change("Kind", "agent");
  expect(confirmation).toHaveBeenCalledOnce();
  expect(screen.getByLabelText("Kind")).toHaveProperty("value", "skill");
  click("Next: Content");
  expect(first.getByLabelText("Text content")).toHaveProperty(
    "value",
    "---\nname: helper\ndescription: Useful instructions.\n---\nOriginal instructions.\n",
  );
  expect(second.getByLabelText("Text content")).toHaveProperty(
    "value",
    "# Supporting instructions\n",
  );
  click("Back: Basics");
  confirmation.mockReturnValue(true);
  change("Kind", "agent");
  click("Next: Content");
  expect(screen.getAllByLabelText("Representation role")).toHaveLength(1);
  expect(screen.getByLabelText("Standards document (JSON)")).toHaveProperty(
    "value",
    "",
  );
  expect(screen.getByLabelText("Media type")).toHaveProperty(
    "value",
    "application/a2a-agent-card+json",
  );
  expect(workspace.post).not.toHaveBeenCalled();
});

it("refuses a content save without exactly one primary and preserves the correction input", async () => {
  const workspace = await newItem();
  enterContent();
  await waitFor(() =>
    expect(screen.getByLabelText("Version label")).toBe(document.activeElement),
  );
  change("Representation role", "metadata");
  click("Save content draft");
  const error = await screen.findByRole("alert");
  expect(error.textContent).toBe("Include exactly one primary representation.");
  expect(error).toBe(document.activeElement);
  expect(workspace.post).not.toHaveBeenCalled();
  expect(screen.getByLabelText("Standards document (JSON)")).toHaveProperty(
    "value",
    source,
  );
  change("Representation role", "primary");
  click("Save content draft");
  await screen.findByRole("dialog");
  click("Cancel");
  expect(workspace.post).not.toHaveBeenCalled();
});

it("lets a creation-only author add another identity with a fresh key and retained Namespace", async () => {
  const workspace = screenWorkspace();
  const nextIdentifier = "private-second-resource";
  workspace.post
    .mockResolvedValueOnce(etagReply(resource))
    .mockResolvedValueOnce(
      etagReply({
        ...resource,
        id: "resource-2",
        canonicalIdentifier: nextIdentifier,
      }),
    );
  render(
    <AuthoringWorkspace
      {...workspace.props}
      access={{
        ...administrator,
        permissions: ["resource.create"],
        availablePermissions: ["resource.create"],
      }}
    />,
  );
  change("Namespace ID", namespace.id);
  change("Canonical resource identifier", resource.canonicalIdentifier);
  expect(screen.queryByRole("button", { name: "Next: Content" })).toBeNull();
  await confirmCommand("Create resource");
  const another = await screen.findByRole("button", {
    name: "Add another item",
  });
  expect(another).toHaveProperty("disabled", false);
  fireEvent.click(another);
  expect(screen.getByLabelText("Canonical resource identifier")).toHaveProperty(
    "value",
    "",
  );
  expect(screen.getByLabelText("Namespace ID")).toHaveProperty(
    "value",
    namespace.id,
  );
  await waitFor(() =>
    expect(screen.getByLabelText("Namespace ID")).toBe(document.activeElement),
  );
  change("Canonical resource identifier", nextIdentifier);
  await confirmCommand("Create resource");
  expect(workspace.post.mock.calls.map(([path]) => path)).toEqual([
    "/api/v1/resources",
    "/api/v1/resources",
  ]);
  expect(commandKey(workspace.post.mock.calls[1])).not.toBe(
    commandKey(workspace.post.mock.calls[0]),
  );
  expect(workspace.get).not.toHaveBeenCalled();
});

it("creates another Version for the same known Resource with blank content and restored focus", async () => {
  const workspace = screenWorkspace();
  const nextVersion = { ...version, id: "version-2", version: "release-two" };
  workspace.post
    .mockResolvedValueOnce(etagReply(savedVersion))
    .mockResolvedValueOnce(
      etagReply({ ...savedVersion, version: nextVersion }),
    );
  render(
    <AuthoringWorkspace
      {...workspace.props}
      access={administrator}
      resource={resource}
    />,
  );
  change("Version label", version.version);
  change("Standards document (JSON)", source);
  await confirmCommand("Create version");
  await screen.findByRole("link", { name: "Open saved version" });
  click("Create another version");
  const confirmation = vi.spyOn(window, "confirm").mockReturnValue(false);
  expect(confirmDiscard()).toBe(true);
  expect(confirmation).not.toHaveBeenCalled();
  expect(screen.getByLabelText("Version label")).toHaveProperty("value", "");
  expect(screen.getByLabelText("Standards document (JSON)")).toHaveProperty(
    "value",
    "",
  );
  await waitFor(() =>
    expect(screen.getByLabelText("Version label")).toBe(document.activeElement),
  );
  expect(screen.queryByLabelText("Namespace")).toBeNull();
  change("Version label", nextVersion.version);
  change("Standards document (JSON)", '{"name":"New content"}');
  await confirmCommand("Create version");
  expect(workspace.post.mock.calls.map(([path]) => path)).toEqual([
    "/api/v1/resources/{resourceId}/versions",
    "/api/v1/resources/{resourceId}/versions",
  ]);
  expect(workspace.post.mock.calls[1]).toMatchObject([
    "/api/v1/resources/{resourceId}/versions",
    {
      params: { path: { resourceId: resource.id } },
      body: {
        version: nextVersion.version,
        representations: [{ data: { name: "New content" } }],
      },
    },
  ]);
  expect(commandKey(workspace.post.mock.calls[1])).not.toBe(
    commandKey(workspace.post.mock.calls[0]),
  );
  expect(workspace.get).not.toHaveBeenCalled();
});

it("acknowledges a saved invalid draft and retains its Version without treating validation as a failed write", async () => {
  const workspace = await newItem();
  workspace.post
    .mockResolvedValueOnce(etagReply(resource))
    .mockResolvedValueOnce(
      etagReply({
        ...savedVersion,
        evidence: { ...evidence, state: "blocked" },
      }),
    );
  enterContent();
  await confirmCommand("Save content draft");
  expect((await screen.findByRole("status")).textContent).toContain(
    "Draft saved, corrections needed.",
  );
  expect(
    screen.getByRole("link", { name: "Open saved version" }),
  ).toHaveProperty("href", `${location.origin}/unit/versions/${version.id}`);
  expect(workspace.props.onFailure).not.toHaveBeenCalled();
  expect(workspace.post).toHaveBeenCalledTimes(2);
});

it("treats the retained Namespace after Add another as clean while protecting later identity and content edits", async () => {
  const workspace = await newItem();
  workspace.post
    .mockResolvedValueOnce(etagReply(resource))
    .mockResolvedValueOnce(etagReply(savedVersion));
  enterContent();
  await confirmCommand("Save content draft");
  await screen.findByRole("link", { name: "Open saved version" });
  click("Add another item");
  const confirmation = vi.spyOn(window, "confirm").mockReturnValue(false);
  expect(screen.getByLabelText("Namespace ID")).toHaveProperty(
    "value",
    namespace.id,
  );
  expect(confirmDiscard()).toBe(true);
  expect(confirmation).not.toHaveBeenCalled();

  change("Kind", "skill");
  expect(confirmDiscard()).toBe(false);
  change("Kind", "agent");
  expect(confirmDiscard()).toBe(true);

  change("Namespace ID", "another-namespace");
  expect(
    screen.getByRole("link", { name: "Open saved version" }),
  ).toHaveProperty("href", `${location.origin}/unit/versions/${version.id}`);
  expect(confirmDiscard()).toBe(false);
  change("Namespace ID", namespace.id);
  expect(confirmDiscard()).toBe(true);

  change("Canonical resource identifier", "new private identity");
  expect(confirmDiscard()).toBe(false);
  change("Canonical resource identifier", "");
  expect(confirmDiscard()).toBe(true);

  change("Canonical resource identifier", "new private identity");
  click("Next: Content");
  change("Standards document (JSON)", '{"name":"New unsaved content"}');
  click("Back: Basics");
  change("Canonical resource identifier", "");
  expect(confirmDiscard()).toBe(false);
  expect(confirmation).toHaveBeenCalledTimes(4);
  expect(workspace.post).toHaveBeenCalledTimes(2);
});

it("retains an editable unsaved draft after creation and editing permissions are removed", async () => {
  const workspace = await newItem();
  enterContent();
  workspace.view.rerender(
    <AuthoringWorkspace
      {...workspace.props}
      access={{
        ...administrator,
        permissions: [],
        availablePermissions: [],
      }}
    />,
  );
  expect(screen.getByLabelText("Standards document (JSON)")).toHaveProperty(
    "value",
    source,
  );
  expect(
    screen.getByRole("button", { name: "Save content draft" }),
  ).toHaveProperty("disabled", true);
  expect(
    window.dispatchEvent(new Event("catalog-ui:leave", { cancelable: true })),
  ).toBe(false);
  expect(workspace.post).not.toHaveBeenCalled();
});

it("continues an acknowledged Resource with editing authority after creation authority is removed", async () => {
  const workspace = await newItem();
  workspace.post
    .mockResolvedValueOnce(etagReply(resource))
    .mockRejectedValueOnce(new APIError(400, "INVALID_ARGUMENT"))
    .mockResolvedValueOnce(etagReply(savedVersion));
  enterContent();
  await confirmCommand("Save content draft");
  await within(screen.getByRole("dialog")).findByRole("alert");
  click("Back to form");
  workspace.view.rerender(
    <AuthoringWorkspace
      {...workspace.props}
      access={{
        ...administrator,
        permissions: ["resource.edit"],
        availablePermissions: ["resource.edit"],
      }}
    />,
  );
  expect(
    screen.getByRole("button", { name: "Save content draft" }),
  ).toHaveProperty("disabled", false);
  await confirmCommand("Save content draft");
  expect(workspace.post.mock.calls.map(([path]) => path)).toEqual([
    "/api/v1/resources",
    "/api/v1/resources/{resourceId}/versions",
    "/api/v1/resources/{resourceId}/versions",
  ]);
});

it.each([
  ["null", undefined],
  ["42", undefined],
  ["{}", undefined],
  ['{"name":42}', undefined],
  ['{"name":"Café Helper"}', "Use suggested name: cafe-helper"],
] as const)(
  "keeps raw input %s unchanged and suggests an AIR name only from a native string name",
  async (text, suggestion) => {
    const workspace = await newItem();
    enterContent(text);
    click("Back: Basics");
    fireEvent.click(screen.getByText("Build an AIR identifier"));
    if (suggestion)
      expect(screen.getByRole("button", { name: suggestion })).toBeTruthy();
    else
      expect(
        screen.queryByRole("button", { name: /^Use suggested name:/ }),
      ).toBeNull();
    expect(
      screen.getByLabelText("Canonical resource identifier"),
    ).toHaveProperty("value", resource.canonicalIdentifier);
    click("Next: Content");
    expect(screen.getByLabelText("Standards document (JSON)")).toHaveProperty(
      "value",
      text,
    );
    expect(workspace.post).not.toHaveBeenCalled();
  },
);

it.each([
  ["Representation role", "metadata", "primary"],
  [
    "Media type",
    "application/vnd.open-agentic-catalog.agent+json",
    "application/a2a-agent-card+json",
  ],
  ["Profile ID (optional)", "selected-profile", ""],
] as const)(
  "protects an Add-version draft edited only through %s and clears protection when reverted",
  (field, changed, original) => {
    const workspace = screenWorkspace();
    render(
      <AuthoringWorkspace
        {...workspace.props}
        access={administrator}
        resource={resource}
      />,
    );
    const confirmation = vi.spyOn(window, "confirm").mockReturnValue(false);
    expect(confirmDiscard()).toBe(true);
    change(field, changed);
    expect(screen.getByLabelText("Version label")).toHaveProperty("value", "");
    expect(screen.getByLabelText("Standards document (JSON)")).toHaveProperty(
      "value",
      "",
    );
    expect(confirmDiscard()).toBe(false);
    expect(confirmation).toHaveBeenCalledOnce();
    change(field, original);
    expect(confirmDiscard()).toBe(true);
    expect(confirmation).toHaveBeenCalledOnce();
    expect(workspace.post).not.toHaveBeenCalled();
    expect(workspace.get).not.toHaveBeenCalled();
  },
);

it("protects an added blank representation before label or content entry and becomes clean after removing it", () => {
  const workspace = screenWorkspace();
  render(
    <AuthoringWorkspace
      {...workspace.props}
      access={administrator}
      resource={resource}
    />,
  );
  const confirmation = vi.spyOn(window, "confirm").mockReturnValue(false);
  expect(confirmDiscard()).toBe(true);
  click("Add representation");
  expect(screen.getByLabelText("Version label")).toHaveProperty("value", "");
  for (const editor of screen.getAllByLabelText("Standards document (JSON)"))
    expect(editor).toHaveProperty("value", "");
  expect(confirmDiscard()).toBe(false);
  expect(confirmation).toHaveBeenCalledOnce();
  click("Remove representation 2");
  expect(screen.getAllByLabelText("Representation role")).toHaveLength(1);
  expect(confirmDiscard()).toBe(true);
  expect(confirmation).toHaveBeenCalledOnce();
  expect(workspace.post).not.toHaveBeenCalled();
});

it("protects a checksum-only Plugin draft and resets the checksum and delivery input for another Version", async () => {
  const workspace = screenWorkspace();
  const plugin = {
    ...resource,
    kind: "plugin" as const,
    canonicalIdentifier: "urn:air:example.com:plugins:helper",
  };
  workspace.post.mockResolvedValue(etagReply(savedVersion));
  render(
    <AuthoringWorkspace
      {...workspace.props}
      access={administrator}
      resource={plugin}
    />,
  );
  const confirmation = vi.spyOn(window, "confirm").mockReturnValue(false);
  expect(confirmDiscard()).toBe(true);
  const digest = `sha256:${"a".repeat(64)}`;
  await screen.findByLabelText("Expected archive checksum (optional)");
  change("Expected archive checksum (optional)", digest);
  expect(screen.getByLabelText("Plugin archive URL")).toHaveProperty(
    "value",
    "",
  );
  expect(screen.getByLabelText("Version label")).toHaveProperty("value", "");
  expect(confirmDiscard()).toBe(false);
  expect(confirmation).toHaveBeenCalledOnce();
  change("Plugin archive URL", "https://packages.example.com/plugin.zip");
  change("Version label", version.version);
  await confirmCommand("Create version");
  await screen.findByRole("link", { name: "Open saved version" });
  expect(workspace.post.mock.calls[0]).toMatchObject([
    "/api/v1/resources/{resourceId}/versions",
    { body: { representations: [{ expectedDigest: digest }] } },
  ]);
  click("Create another version");
  await screen.findByLabelText("Plugin archive URL");
  expect(
    screen.getByLabelText("Expected archive checksum (optional)"),
  ).toHaveProperty("value", "");
  expect(screen.getByLabelText("Plugin archive URL")).toHaveProperty(
    "value",
    "",
  );
  expect(confirmDiscard()).toBe(true);
  expect(confirmation).toHaveBeenCalledOnce();
});

it("protects unapplied AIR-builder fields, becomes clean when cleared, and resets builder state for Add another", async () => {
  const workspace = await newItem();
  workspace.post
    .mockResolvedValueOnce(etagReply(resource))
    .mockResolvedValueOnce(etagReply(savedVersion));
  fireEvent.click(screen.getByText("Build an AIR identifier"));
  change("Publisher domain", "example.com");
  change("AIR namespace segments (optional)", "agents");
  change("AIR resource name", "helper");
  enterContent();
  await confirmCommand("Save content draft");
  await screen.findByRole("link", { name: "Open saved version" });
  click("Add another item");
  fireEvent.click(screen.getByText("Build an AIR identifier"));
  for (const field of [
    "Publisher domain",
    "AIR namespace segments (optional)",
    "AIR resource name",
  ])
    expect(screen.getByLabelText(field)).toHaveProperty("value", "");
  const confirmation = vi.spyOn(window, "confirm").mockReturnValue(false);
  expect(confirmDiscard()).toBe(true);
  change("Publisher domain", "example.com");
  change("AIR namespace segments (optional)", "agents");
  change("AIR resource name", "unapplied-name");
  expect(screen.getByLabelText("Canonical resource identifier")).toHaveProperty(
    "value",
    "",
  );
  expect(confirmDiscard()).toBe(false);
  expect(confirmation).toHaveBeenCalledOnce();
  change("Publisher domain", "");
  change("AIR namespace segments (optional)", "");
  change("AIR resource name", "");
  expect(confirmDiscard()).toBe(true);
  expect(confirmation).toHaveBeenCalledOnce();
  expect(workspace.post).toHaveBeenCalledTimes(2);
});

it.each(["mcp_server", "skill", "plugin"] as const)(
  "keeps a creation-only %s identity receipt clean and retains matching blank defaults for Add another",
  async (kind) => {
    const workspace = screenWorkspace();
    const identifier = `urn:air:example.com:resources:${kind}`;
    workspace.post.mockResolvedValueOnce(
      etagReply({ ...resource, kind, canonicalIdentifier: identifier }),
    );
    render(
      <AuthoringWorkspace
        {...workspace.props}
        access={{
          ...administrator,
          permissions: ["resource.create"],
          availablePermissions: ["resource.create"],
        }}
      />,
    );
    change("Namespace ID", namespace.id);
    change("Canonical resource identifier", identifier);
    change("Kind", kind);
    await confirmCommand("Create resource");
    await screen.findByText("Resource created.");
    const confirmation = vi.spyOn(window, "confirm").mockReturnValue(false);
    expect(confirmDiscard()).toBe(true);
    expect(confirmation).not.toHaveBeenCalled();
    const another = screen.getByRole("button", { name: "Add another item" });
    expect(another).toHaveProperty("disabled", false);
    fireEvent.click(another);
    expect(screen.getByLabelText("Kind")).toHaveProperty("value", kind);
    expect(screen.getByLabelText("Namespace ID")).toHaveProperty(
      "value",
      namespace.id,
    );
    expect(
      screen.getByLabelText("Canonical resource identifier"),
    ).toHaveProperty("value", "");
    expect(screen.getByLabelText("Media type")).toHaveProperty(
      "value",
      {
        mcp_server: "application/mcp-server-card+json",
        skill: "application/agent-skills+md",
        plugin: "application/agent-plugins+zip",
      }[kind],
    );
    expect(screen.getByLabelText("Version label")).toHaveProperty("value", "");
    expect(
      screen.getByLabelText(
        {
          mcp_server: "Standards document (JSON)",
          skill: "Text content",
          plugin: "Plugin archive URL",
        }[kind],
      ),
    ).toHaveProperty("value", "");
    expect(confirmDiscard()).toBe(true);
    expect(confirmation).not.toHaveBeenCalled();
    expect(workspace.post).toHaveBeenCalledTimes(1);
    expect(workspace.get).not.toHaveBeenCalled();
  },
);

it("protects advanced-only representation edits when replacing Kind and resets them only after confirmation", async () => {
  const workspace = await newItem();
  click("Next: Content");
  change("Profile ID (optional)", "unapplied-profile-choice");
  click("Back: Basics");
  const confirmation = vi.spyOn(window, "confirm").mockReturnValue(false);
  change("Kind", "skill");
  expect(confirmation).toHaveBeenCalledOnce();
  expect(screen.getByLabelText("Kind")).toHaveProperty("value", "agent");
  expect(screen.getByLabelText("Profile ID (optional)")).toHaveProperty(
    "value",
    "unapplied-profile-choice",
  );
  confirmation.mockReturnValue(true);
  change("Kind", "skill");
  expect(confirmation).toHaveBeenCalledTimes(2);
  expect(screen.getByLabelText("Kind")).toHaveProperty("value", "skill");
  expect(screen.getByLabelText("Profile ID (optional)")).toHaveProperty(
    "value",
    "",
  );
  click("Next: Content");
  expect(screen.getByLabelText("Version label")).toHaveProperty("value", "");
  expect(screen.getByLabelText("Text content")).toHaveProperty("value", "");
  expect(screen.getByLabelText("Media type")).toHaveProperty(
    "value",
    "application/agent-skills+md",
  );
  expect(workspace.post).not.toHaveBeenCalled();
});
