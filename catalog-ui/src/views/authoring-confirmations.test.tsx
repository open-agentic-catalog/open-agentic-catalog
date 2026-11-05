// @vitest-environment jsdom
import { expect, it } from "vitest";
import { fireEvent, render, screen, within } from "@testing-library/react";
import { APIError } from "../api/client";
import { required } from "../../tests/required";
import Resources from "./Resources";
import { VersionRevision } from "./Versions";
import { evidence, versionRevision } from "../../tests/unit/responses";
import {
  administrator,
  change,
  click,
  confirmCommand,
  etagReply,
  readReplies,
  resource,
  screenLifecycle,
  screenWorkspace,
  version,
} from "../../tests/unit/screens";

screenLifecycle("/unit/resources");

it("captures deletion identity, reason and validator before any mutation", async () => {
  const w = screenWorkspace();
  readReplies(w.get, {
    "/api/v1/resources/{resourceId}": resource,
    "/api/v1/resources/{resourceId}/versions": { items: [] },
  });
  render(<Resources {...w.props} id={resource.id} access={administrator} />);
  await screen.findByText("No versions yet.");
  fireEvent.click(screen.getByText("Discard an unused resource"));
  change("Reason", "  Discard this unused draft  ");
  click("Delete unused resource");
  const dialog = await screen.findByRole("dialog");
  const facts = within(dialog).getByLabelText("Command summary");
  for (const value of [
    resource.id,
    resource.canonicalIdentifier,
    resource.namespaceId,
    "Discard this unused draft",
    '"revision-1"',
  ])
    expect(facts.textContent).toContain(value);
  expect(dialog.textContent).toContain("Only an unused draft can be deleted");
  fireEvent.click(within(dialog).getByRole("button", { name: "Cancel" }));
  expect(w.post).not.toHaveBeenCalled();
});

it("captures revalidation references without inventing an If-Match prerequisite", async () => {
  const w = screenWorkspace();
  readReplies(w.get, {
    "/api/v1/version-revisions/{revisionId}": {
      namespaceId: resource.namespaceId,
      revision: versionRevision,
      evidence,
    },
  });
  render(
    <VersionRevision
      {...w.props}
      id={versionRevision.id}
      access={administrator}
    />,
  );
  await screen.findByText("Representation metadata");
  click("Revalidate revision");
  const dialog = await screen.findByRole("dialog");
  const facts = within(dialog).getByLabelText("Command summary");
  for (const value of [versionRevision.id, version.id, resource.namespaceId])
    expect(facts.textContent).toContain(value);
  expect(dialog.textContent).toContain(
    "withdraw entire active discovery snapshots",
  );
  expect(facts.textContent).not.toContain('"revision-1"');
  fireEvent.click(within(dialog).getByRole("button", { name: "Cancel" }));
  expect(w.post).not.toHaveBeenCalled();
  expect(w.get).toHaveBeenCalledOnce();
});

it("retains the captured deletion reason, target and validator through an uncertain response", async () => {
  const w = screenWorkspace();
  readReplies(w.get, {
    "/api/v1/resources/{resourceId}": resource,
    "/api/v1/resources/{resourceId}/versions": { items: [] },
  });
  w.post
    .mockRejectedValueOnce(new APIError(503, "DEPENDENCY_UNAVAILABLE"))
    .mockResolvedValue({ response: new Response(null, { status: 204 }) });
  render(<Resources {...w.props} id={resource.id} access={administrator} />);
  await screen.findByText("No versions yet.");
  fireEvent.click(screen.getByText("Discard an unused resource"));
  change("Reason", "  Captured deletion reason 🙂  ");
  click("Delete unused resource");
  const dialog = screen.getByRole("dialog");
  const facts = within(dialog).getByLabelText("Command summary");
  // Bypass the disabled background field to prove the prepared closure owns
  // its input even if the parent rerenders before submission or recovery.
  change("Reason", "Later unconfirmed reason");
  expect(facts.textContent).toContain("Captured deletion reason 🙂");
  expect(facts.textContent).not.toContain("Later unconfirmed reason");
  expect(w.post).not.toHaveBeenCalled();
  fireEvent.click(within(dialog).getByRole("button", { name: "Confirm" }));
  await within(dialog).findByText(/The outcome is unknown/);
  expect(w.post).toHaveBeenCalledOnce();
  expect(
    within(dialog).getByRole("button", { name: "Back to form" }),
  ).toHaveProperty("disabled", true);
  expect(screen.queryByText(/Resource deleted/)).toBeNull();
  expect(w.post.mock.calls[0]).toEqual([
    "/api/v1/resources/{resourceId}:delete",
    {
      params: {
        path: { resourceId: resource.id },
        header: {
          "Idempotency-Key": expect.any(String) as unknown,
          "If-Match": '"revision-1"',
        },
      },
      body: { reason: "Captured deletion reason 🙂" },
    },
  ]);
  const original = structuredClone(required(w.post.mock.calls[0]));
  change("Reason", "Another unconfirmed reason");
  click("Retry same command");
  await screen.findByText("Resource deleted. Return to authoring.");
  expect(w.post).toHaveBeenCalledTimes(2);
  expect(w.post.mock.calls[1]).toEqual(original);
  expect(w.get).toHaveBeenCalledTimes(2);
  expect(w.props.onFailure).toHaveBeenCalledExactlyOnceWith(
    expect.objectContaining({ status: 503 }),
  );
});

it.each(["unavailable", "malformed"])(
  "preserves revision evidence and exact revalidation recovery after a %s reply",
  async (failure) => {
    const w = screenWorkspace();
    const bundle = {
      namespaceId: resource.namespaceId,
      revision: versionRevision,
      evidence,
    };
    readReplies(w.get, {
      "/api/v1/version-revisions/{revisionId}": bundle,
    });
    if (failure === "unavailable")
      w.post.mockRejectedValueOnce(new APIError(503, "DEPENDENCY_UNAVAILABLE"));
    else
      w.post.mockResolvedValueOnce(
        etagReply({
          ...bundle,
          revision: {},
          evidence: { ...evidence, state: "blocked" },
        }),
      );
    w.post.mockResolvedValue(
      etagReply({ ...bundle, evidence: { ...evidence, state: "blocked" } }),
    );
    render(
      <VersionRevision
        {...w.props}
        id={versionRevision.id}
        access={administrator}
      />,
    );
    await screen.findByRole("heading", { name: "Latest validation: Complete" });
    await confirmCommand("Revalidate revision");
    const dialog = screen.getByRole("dialog");
    await within(dialog).findByText(/The outcome is unknown/);
    expect(
      screen.getByRole("heading", { name: "Latest validation: Complete" }),
    ).toBeTruthy();
    expect(
      screen.queryByRole("heading", { name: "Latest validation: Blocked" }),
    ).toBeNull();
    expect(screen.queryByText("Validation: blocked.")).toBeNull();
    expect(w.post).toHaveBeenCalledOnce();
    expect(
      within(dialog).getByRole("button", { name: "Back to form" }),
    ).toHaveProperty("disabled", true);
    const facts = within(dialog).getByLabelText("Command summary");
    for (const value of [
      versionRevision.id,
      version.id,
      resource.namespaceId,
      "No If-Match",
    ])
      expect(facts.textContent).toContain(value);
    expect(w.post.mock.calls[0]).toEqual([
      "/api/v1/version-revisions/{revisionId}:validate",
      {
        params: {
          path: { revisionId: versionRevision.id },
          header: { "Idempotency-Key": expect.any(String) as unknown },
        },
        body: {},
      },
    ]);
    const original = structuredClone(required(w.post.mock.calls[0]));
    click("Retry same command");
    await screen.findByText("Validation: blocked.");
    expect(
      screen.getByRole("heading", { name: "Latest validation: Blocked" }),
    ).toBeTruthy();
    expect(
      screen.queryByRole("heading", { name: "Latest validation: Complete" }),
    ).toBeNull();
    expect(w.post).toHaveBeenCalledTimes(2);
    expect(w.post.mock.calls[1]).toEqual(original);
    expect(w.get).toHaveBeenCalledOnce();
    expect(w.props.onFailure).toHaveBeenCalledOnce();
  },
);
