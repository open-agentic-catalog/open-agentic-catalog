import { required } from "../../tests/required";
// @vitest-environment jsdom
import { release as releaseSeed } from "../../tests/unit/responses";
import { listingRevision } from "../../tests/unit/responses";
import { expect, it, vi } from "vitest";
import {
  act,
  fireEvent,
  render,
  screen,
  waitFor,
  within,
} from "@testing-library/react";
import { deferred, reader, sessionAccess } from "../../tests/unit/fixtures";
import type { Access } from "../api/client";
import Reviews, { Release } from "./Reviews";
import { APIError } from "../api/client";
import {
  administrator,
  change,
  click,
  confirmCommand,
  etagReply,
  listing,
  readReplies,
  screenLifecycle,
  screenWorkspace,
  stamp,
} from "../../tests/unit/screens";
screenLifecycle("/unit/reviews");

it.each(["true", "false", "other"])(
  "retains the exact unreleased criterion %s through queue reads and Back/Forward",
  async (unreleased) => {
    history.replaceState(
      null,
      "",
      `/unit/reviews?state=approved&unreleased=${unreleased}`,
    );
    const w = screenWorkspace();
    w.get.mockResolvedValue(etagReply({ items: [] }));
    render(<Reviews {...w.props} />);
    await screen.findByText("No matching review submissions.");
    expect(w.get.mock.calls[0]?.[1]).toMatchObject({
      params: { query: { state: "approved", unreleased } },
    });
    const control = screen.getByRole("checkbox", {
      name: "Approved submissions without a release",
    });
    if (unreleased === "true") {
      expect((control as HTMLInputElement).checked).toBe(true);
      change("Review state", "approved");
      fireEvent.click(control);
      click("Apply review filters");
      await waitFor(() =>
        expect(w.get).toHaveBeenLastCalledWith(
          "/api/v1/review-submissions",
          expect.objectContaining({
            params: { query: { state: "approved", limit: 25 } },
          }),
        ),
      );
      fireEvent.click(control);
      click("Apply review filters");
      await waitFor(() =>
        expect(location.search).toBe("?state=approved&unreleased=true"),
      );
      change("Review state", "submitted");
      click("Apply review filters");
      await waitFor(() => expect(location.search).toBe("?state=submitted"));
    }
    history.pushState(null, "", "/unit/reviews?state=approved&unreleased=true");
    fireEvent(window, new PopStateEvent("popstate"));
    await waitFor(() =>
      expect((control as HTMLInputElement).checked).toBe(true),
    );
    expect(w.get.mock.calls.at(-1)?.[1]).toMatchObject({
      params: { query: { unreleased: "true", state: "approved" } },
    });
    expect(w.post).not.toHaveBeenCalled();
  },
);

it.each([{ replacementReleaseId: "../foreign" }, { versionRevisionId: null }])(
  "rejects malformed release navigation fields before loading listing context: %j",
  async (bad) => {
    const { props, get, post } = screenWorkspace();
    get.mockResolvedValue(etagReply({ ...releaseSeed, ...bad }));
    render(<Release {...props} access={reader} id={releaseSeed.id} />);
    await screen.findByRole("alert");
    expect(screen.queryByText("Exact version revision")).toBeNull();
    expect(screen.queryByRole("link", { name: "Catalog record" })).toBeNull();
    expect(get.mock.calls).toHaveLength(1);
    expect(post).not.toHaveBeenCalled();
  },
);

it.each(["published", "superseded", "deprecated"])(
  "shows exact %s release facts without hydrating revisions",
  async (state) => {
    const { props, get, post } = screenWorkspace();
    const extras =
      state === "superseded"
        ? {
            replacementReleaseId: "replacement-1",
            dispositionReason:
              '<img src=x onerror="alert(1)">\nReplace the original',
          }
        : state === "deprecated"
          ? {
              sunsetAt: "2026-10-01T12:00:00Z",
              dispositionReason: "Use the replacement when ready",
            }
          : {};
    readReplies(get, {
      "/api/v1/listing-releases/{releaseId}": {
        ...releaseSeed,
        ...extras,
        state,
        updatedAt: "2026-09-02T12:00:00Z",
      },
      "/api/v1/catalog-listings/{listingId}": {
        ...listing,
        currentReleaseId: releaseSeed.id,
        candidateRevisionId: "different-candidate",
      },
    });
    render(<Release {...props} access={reader} id={releaseSeed.id} />);
    const panel = (
      await screen.findByRole("heading", {
        name: `Release ${state}`,
      })
    ).closest("section")!;
    const facts = within(panel);
    expect(facts.getByText("Release ID").nextElementSibling?.textContent).toBe(
      releaseSeed.id,
    );
    expect(
      facts
        .getByText("Listing")
        .nextElementSibling?.querySelector("a")
        ?.getAttribute("href"),
    ).toBe(`/unit/listings/${releaseSeed.listingId}`);
    expect(
      facts.getByText("Activated").nextElementSibling?.querySelector("time")
        ?.dateTime,
    ).toBe(releaseSeed.activatedAt);
    expect(
      facts
        .getByText("Exact version revision")
        .nextElementSibling?.querySelector("a")
        ?.getAttribute("href"),
    ).toBe(`/unit/version-revisions/${releaseSeed.versionRevisionId}`);
    expect(
      facts
        .getByText("Released listing revision")
        .nextElementSibling?.querySelector("a")
        ?.getAttribute("href"),
    ).toBe(`/unit/listing-revisions/${releaseSeed.listingRevisionId}`);
    expect(
      screen
        .getByRole("link", { name: "Release history" })
        .getAttribute("href"),
    ).toBe(`/unit/listings/${listing.id}#release-history`);
    expect(
      facts
        .getByText("Disposition last updated")
        .nextElementSibling?.querySelector("time")?.dateTime,
    ).toBe("2026-09-02T12:00:00Z");
    if (state === "superseded") {
      expect(
        facts
          .getByText("Replacement release")
          .nextElementSibling?.querySelector("a")
          ?.getAttribute("href"),
      ).toBe("/unit/releases/replacement-1");
      expect(
        facts.getByText(
          (_text, element) =>
            element?.tagName === "DD" &&
            element.textContent === extras.dispositionReason,
        ),
      ).toBeTruthy();
      expect(panel.querySelector("img")).toBeNull();
      expect(facts.queryByRole("link", { name: "Catalog record" })).toBeNull();
    } else {
      expect(facts.getByRole("link", { name: "Catalog record" })).toBeTruthy();
      expect(facts.queryByText("Replacement release")).toBeNull();
    }
    expect(facts.queryByText("Declared sunset") !== null).toBe(
      state === "deprecated",
    );
    if (state === "deprecated")
      expect(
        facts
          .getByText("Declared sunset")
          .nextElementSibling?.querySelector("time")?.dateTime,
      ).toBe(extras.sunsetAt);
    if (state === "published")
      expect(facts.queryByText("Disposition reason")).toBeNull();
    expect(get.mock.calls.map(([path]) => path)).toEqual([
      "/api/v1/listing-releases/{releaseId}",
      "/api/v1/catalog-listings/{listingId}",
    ]);
    expect(post).not.toHaveBeenCalled();
  },
);
const submission = {
  id: "submission-1",
  tenantId: listing.tenantId,
  listingId: listing.id,
  listingRevisionId: "listing-rev-1",
  versionRevisionId: "version-rev-1",
  evidenceSetId: "evidence-1",
  evidenceDigest: "digest",
  policyVersion: "policy-1",
  state: "submitted",
  submittedBy: "independent-author",
  submittedAt: stamp,
};
const revision = {
  ...listingRevision,
  id: submission.listingRevisionId,
  listingId: listing.id,
  versionRevisionId: submission.versionRevisionId,
  displayName: "Submitted helper",
  description: "Review this exact metadata",
  tags: [],
  categories: [],
  representativeQueries: [],
  relationships: [],
};
const evidence = {
  id: submission.evidenceSetId,
  digest: submission.evidenceDigest,
  policyVersion: submission.policyVersion,
  validationProfile: "catalog-validation-v7",
  state: "complete",
  trust: { state: "not_assessed" },
  legalRights: { state: "not_assessed" },
  findings: [
    { severity: "info", code: "checked", message: "Structure checked" },
  ],
};
function routes(state = "submitted") {
  return {
    "/api/v1/review-submissions/{submissionId}": { ...submission, state },
    "/api/v1/catalog-listings/{listingId}": listing,
    "/api/v1/review-submissions/{submissionId}/evidence": evidence,
    "/api/v1/listing-revisions/{revisionId}": revision,
  };
}

function publicationReply(
  id: string,
  state: "retired" | "superseded" | null = null,
) {
  return {
    listing: { ...listing, currentReleaseId: id },
    release: { ...releaseSeed, id, tenantId: listing.tenantId },
    priorRelease:
      state === null
        ? null
        : {
            ...releaseSeed,
            tenantId: listing.tenantId,
            state,
            ...(state === "superseded" ? { replacementReleaseId: id } : {}),
          },
  };
}

it.each([true, false])(
  "filters and pages the review queue (listing history=%s)",
  async (historyOnly) => {
    const filterId = "00000000-0000-7000-8000-000000000001";
    history.replaceState(
      null,
      "",
      historyOnly ? `/unit/reviews?listing=${filterId}` : "/unit/reviews",
    );
    const { props, get } = screenWorkspace();
    get.mockResolvedValue(
      etagReply({ items: [submission], nextCursor: "next" }),
    );
    render(<Reviews {...props} />);
    await screen.findByRole("link", { name: submission.id });
    const backlink = screen.queryByRole("link", { name: "Back to listing" });
    if (historyOnly)
      expect(backlink?.getAttribute("href")).toBe(`/unit/listings/${filterId}`);
    else expect(backlink).toBeNull();
    expect(required(get.mock.calls[0])[1]).toMatchObject({
      params: {
        query: {
          ...(historyOnly ? { listingId: filterId } : { state: "submitted" }),
        },
      },
    });
    expect(required(get.mock.calls[0])[1]).not.toHaveProperty(
      historyOnly ? "params.query.state" : "params.query.listingId",
    );
    click("Next page");
    await waitFor(() =>
      expect(get.mock.calls.at(-1)?.[1]).toMatchObject({
        params: { query: { cursor: "next" } },
      }),
    );
    change("Review state", "approved");
    click("Apply review filters");
    await waitFor(() => {
      expect(get.mock.calls.at(-1)?.[1]).toMatchObject({
        params: { query: { state: "approved" } },
      });
      expect(get.mock.calls.at(-1)?.[1]).not.toHaveProperty(
        "params.query.cursor",
      );
    });
    get.mockResolvedValue(etagReply({ items: [] }));
    click("Refresh");
    await screen.findByText("No matching review submissions.");
  },
);

it.each([
  ["submitted", "Submitted"],
  ["approved", "Approved"],
  ["changes_required", "Changes required"],
  ["rejected", "Rejected"],
  ["future_state", "Unsupported review state"],
])(
  "labels applied review state %s without changing exact query values or applying drafts",
  async (state, label) => {
    history.replaceState(null, "", `/unit/reviews?state=${state}`);
    const w = screenWorkspace();
    w.get.mockResolvedValue(etagReply({ items: [] }));
    render(<Reviews {...w.props} />);
    await screen.findByText("No matching review submissions.");
    expect(
      screen.getByRole("option", { name: label }).getAttribute("value"),
    ).toBe(state);
    const applied = screen.getByText(
      /Editing these fields does not change results until Apply/,
    );
    expect(applied.textContent).toContain(`State: ${label}.`);
    expect(w.get.mock.calls[0]?.[1]).toMatchObject({
      params: { query: { state, limit: 25 } },
    });
    change(
      "Review state",
      state === "submitted" ? "changes_required" : "submitted",
    );
    expect(applied.textContent).toContain(`State: ${label}.`);
    expect(w.get).toHaveBeenCalledOnce();
    expect(w.post).not.toHaveBeenCalled();
  },
);

it.each(["first", "continuation"])(
  "recovers a failed %s review page without retaining an unusable cursor",
  async (phase) => {
    const w = screenWorkspace();
    if (phase === "continuation")
      w.get.mockResolvedValueOnce(
        etagReply({ items: [submission], nextCursor: "next" }),
      );
    w.get
      .mockRejectedValueOnce(new Error("Controlled page failure"))
      .mockResolvedValue(etagReply({ items: [] }));
    render(<Reviews {...w.props} />);
    if (phase === "continuation")
      fireEvent.click(await screen.findByRole("button", { name: "Next page" }));
    await screen.findByRole("alert");
    click("Reload");
    await screen.findByText("No matching review submissions.");
    expect(w.get).toHaveBeenLastCalledWith(
      "/api/v1/review-submissions",
      expect.objectContaining({
        params: { query: { state: "submitted", limit: 25 } },
      }),
    );
    expect(w.get.mock.calls.at(-1)?.[1]).not.toHaveProperty(
      "params.query.cursor",
    );
    expect(w.post).not.toHaveBeenCalled();
  },
);

it("retains an unsaved review decision until evidence closing is explicitly confirmed", async () => {
  const { props, get, post } = screenWorkspace();
  readReplies(get, routes());
  const discard = vi
    .spyOn(window, "confirm")
    .mockReturnValueOnce(false)
    .mockReturnValue(true);
  render(<Reviews {...props} id={submission.id} access={administrator} />);
  await screen.findByRole("button", { name: "Inspect submitted evidence" });
  click("Inspect submitted evidence");
  await screen.findByText("Submitted evidence");
  change("Reason", "Unsubmitted review decision");
  click("Inspect submitted evidence");
  expect((screen.getByLabelText("Reason") as HTMLTextAreaElement).value).toBe(
    "Unsubmitted review decision",
  );
  click("Inspect submitted evidence");
  expect(screen.queryByLabelText("Reason")).toBeNull();
  expect(discard).toHaveBeenCalledTimes(2);
  expect(post).not.toHaveBeenCalled();
});

it.each([
  ["approve", "approved"],
  ["request-changes", "changes_required"],
  ["reject", "rejected"],
] as const)(
  "records %s only after exact submitted evidence is inspected",
  async (action, state) => {
    const { props, get, post } = screenWorkspace();
    readReplies(get, routes());
    post.mockResolvedValue(etagReply({ submission: { ...submission, state } }));
    render(<Reviews {...props} id={submission.id} access={administrator} />);
    await screen.findByRole("button", { name: "Inspect submitted evidence" });
    expect(
      get.mock.calls.some(([path]) => String(path).endsWith("/evidence")),
    ).toBe(false);
    click("Inspect submitted evidence");
    await screen.findByText("Submitted evidence");
    expect(screen.getByText("Submitted helper")).toBeTruthy();
    change("Decision", action);
    change("Reason", " Reviewed exact evidence ");
    click("Record review decision");
    const summary = within(await screen.findByRole("dialog")).getByLabelText(
      "Command summary",
    );
    expect(summary.textContent).toContain(
      action === "approve"
        ? "Approve review"
        : action === "reject"
          ? "Reject review"
          : "Request changes",
    );
    for (const value of [
      "Submitted helper",
      submission.id,
      listing.id,
      listing.namespaceId,
      '"revision-1"',
      "Reviewed exact evidence",
    ])
      expect(summary.textContent).toContain(value);
    expect(post).not.toHaveBeenCalled();
    fireEvent.click(
      within(screen.getByRole("dialog")).getByRole("button", {
        name: "Confirm",
      }),
    );
    await screen.findByText(
      new RegExp(`Review ${state.replaceAll("_", " ")}\\.`),
    );
    expect(screen.getByText(/loaded review predates/)).toBeTruthy();
    readReplies(get, routes(state));
    click("Reload review and next steps");
    await screen.findByRole("heading", {
      name: `Review ${state.replaceAll("_", " ")}`,
    });
    expect(
      get.mock.calls.filter(
        ([path]) => path === "/api/v1/review-submissions/{submissionId}",
      ),
    ).toHaveLength(2);
    expect(post.mock.calls[0]).toMatchObject([
      `/api/v1/review-submissions/{submissionId}:${action}`,
      {
        params: {
          path: { submissionId: submission.id },
          header: { "If-Match": '"revision-1"' },
        },
        body: { reason: "Reviewed exact evidence" },
      },
    ]);
  },
);

it.each([200, 409])(
  "leaves matching subject text to server identity admission (HTTP %s)",
  async (status) => {
    const { props, get, post } = screenWorkspace();
    const data = routes();
    const sameName = { ...submission, submittedBy: administrator.subject };
    data["/api/v1/review-submissions/{submissionId}"] = sameName;
    readReplies(get, data);
    if (status === 200)
      post.mockResolvedValue(
        etagReply({ submission: { ...sameName, state: "approved" } }),
      );
    else post.mockRejectedValue(new APIError(409, "SEPARATION_OF_DUTIES"));
    render(<Reviews {...props} id={submission.id} access={administrator} />);
    await screen.findByRole("button", { name: "Inspect submitted evidence" });
    click("Inspect submitted evidence");
    await screen.findByText("Submitted evidence");
    expect(screen.queryByText(/You submitted this listing/)).toBeNull();
    expect(screen.getByText(/The Catalog verifies independence/)).toBeTruthy();
    change("Reason", "Independent identity check");
    await confirmCommand("Record review decision");
    if (status === 200) await screen.findByText(/Review approved/);
    else {
      await screen.findByRole("alert");
      expect(props.onFailure).toHaveBeenCalledWith(
        expect.objectContaining({ code: "SEPARATION_OF_DUTIES" }),
      );
      expect(screen.queryByText(/Review approved/)).toBeNull();
    }
    expect(post).toHaveBeenCalledExactlyOnceWith(
      "/api/v1/review-submissions/{submissionId}:approve",
      expect.objectContaining({
        body: { reason: "Independent identity check" },
      }),
    );
  },
);

it.each(["evidence", "listing"])(
  "blocks a review decision with %s mismatch",
  async (mismatch) => {
    const { props, get, post } = screenWorkspace();
    const data = routes();
    if (mismatch === "evidence")
      data["/api/v1/review-submissions/{submissionId}/evidence"] = {
        ...evidence,
        digest: "different",
      };
    else
      data["/api/v1/listing-revisions/{revisionId}"] = {
        ...revision,
        versionRevisionId: "different",
      };
    readReplies(get, data);
    render(<Reviews {...props} id={submission.id} access={administrator} />);
    await screen.findByRole("button", { name: "Inspect submitted evidence" });
    click("Inspect submitted evidence");
    await screen.findByRole("alert");
    expect(props.onFailure).toHaveBeenCalledExactlyOnceWith(
      expect.objectContaining({
        message:
          mismatch === "evidence"
            ? "Submission evidence does not match."
            : "Submitted listing revision does not match.",
      }),
    );
    expect(
      (
        screen.getByRole("button", {
          name: "Record review decision",
        }) as HTMLButtonElement
      ).disabled,
    ).toBe(true);
    expect(post).not.toHaveBeenCalled();
  },
);

it.each(["retired", "superseded"] as const)(
  "publishes an approved submission with previous release %s",
  async (disposition) => {
    const { props, get, post } = screenWorkspace();
    readReplies(get, {
      ...routes("approved"),
      "/api/v1/catalog-listings/{listingId}": {
        ...listing,
        currentReleaseId: releaseSeed.id,
      },
    });
    post.mockResolvedValue(
      etagReply(publicationReply("release-2", disposition)),
    );
    render(<Reviews {...props} id={submission.id} access={administrator} />);
    await screen.findByLabelText("Previous release disposition");
    expect(
      screen.getByText(/Both choices preserve the active discovery snapshot/),
    ).toBeTruthy();
    expect(screen.queryByText(/Either can withdraw entire/)).toBeNull();
    change("Previous release disposition", disposition);
    if (disposition === "superseded") change("Reason", "Use new release");
    else {
      const reason = screen.getByLabelText(
        "Reason (optional)",
      ) as HTMLTextAreaElement;
      change("Reason (optional)", "🙂".repeat(2000));
      expect(reason.required).toBe(false);
      expect(reason.validity.valid).toBe(true);
      change("Reason (optional)", "🙂".repeat(2000) + "a");
      expect(reason.validity.customError).toBe(true);
      change("Reason (optional)", "Discarded draft reason");
      change("Reason (optional)", "");
    }
    click("Publish approved listing");
    const summary = within(await screen.findByRole("dialog")).getByLabelText(
      "Command summary",
    );
    for (const value of [
      submission.id,
      listing.id,
      listing.namespaceId,
      disposition,
    ])
      expect(summary.textContent).toContain(value);
    expect(post).not.toHaveBeenCalled();
    fireEvent.click(
      within(screen.getByRole("dialog")).getByRole("button", {
        name: "Confirm",
      }),
    );
    expect(
      (
        await screen.findByRole("link", { name: "Open published release" })
      ).getAttribute("href"),
    ).toBe("/unit/releases/release-2");
    expect(post.mock.calls[0]).toMatchObject([
      "/api/v1/catalog-listings/{listingId}:publish",
      {
        body: {
          submissionId: submission.id,
          priorDisposition: disposition,
          ...(disposition === "superseded"
            ? { priorReason: "Use new release" }
            : {}),
        },
      },
    ]);
  },
);

it.each([
  ["deprecate", "deprecated"],
  ["retire", "retired"],
  ["supersede", "superseded"],
  ["revoke", "revoked"],
] as const)(
  "applies release %s with a reason and exact revision",
  async (action, state) => {
    const { props, get, post } = screenWorkspace();
    readReplies(get, {
      "/api/v1/listing-releases/{releaseId}": {
        ...releaseSeed,
        id: "release-1",
        listingId: listing.id,
        submissionId: submission.id,
        state: "published",
      },
      "/api/v1/catalog-listings/{listingId}/releases": {
        items: [{ ...releaseSeed, id: "replacement-1" }],
      },
      "/api/v1/catalog-listings/{listingId}": {
        ...listing,
        currentReleaseId: "release-1",
      },
    });
    post.mockResolvedValue(etagReply({ ...releaseSeed, state }));
    render(<Release {...props} id="release-1" access={administrator} />);
    await screen.findByLabelText("Disposition");
    expect(
      screen.getByRole("link", { name: "Catalog record" }).getAttribute("href"),
    ).toBe("/unit/catalog/release-1");
    change("Disposition", action);
    change("Reason", " Lifecycle decision ");
    if (action === "supersede") {
      click("Choose same-listing replacement");
      await screen.findByRole("option", { name: "replacement-1 (published)" });
      change("Same-listing replacement", "replacement-1");
    } else if (action === "deprecate")
      change("Replacement release ID", "replacement-1");
    if (action === "deprecate")
      change("Sunset time (optional, local time)", "2026-10-01T12:00");
    const consequence =
      action === "deprecate"
        ? /do not change the listing's current release or schedule its withdrawal/
        : action === "supersede"
          ? /Naming a replacement does not make it the listing's current release/
          : /Other entries in those snapshots also become unavailable/;
    expect(screen.getByText(consequence)).toBeTruthy();
    click("Change release disposition");
    const dialog = await screen.findByRole("dialog");
    expect(within(dialog).getByText(consequence)).toBeTruthy();
    if (action !== "deprecate")
      expect(
        within(dialog).getByText(/each entire active discovery snapshot/),
      ).toBeTruthy();
    const summary = within(dialog).getByLabelText("Command summary");
    const label = {
      deprecate: "Deprecate release",
      retire: "Retire release",
      supersede: "Supersede release",
      revoke: "Revoke release",
    }[action];
    for (const value of [
      label,
      "release-1",
      listing.namespaceId,
      "Lifecycle decision",
      '"revision-1"',
    ])
      expect(summary.textContent).toContain(value);
    expect(post).not.toHaveBeenCalled();
    fireEvent.click(
      within(screen.getByRole("dialog")).getByRole("button", {
        name: "Confirm",
      }),
    );
    await screen.findByText(new RegExp(`Release ${state}\\.`));
    expect(post.mock.calls[0]).toMatchObject([
      `/api/v1/listing-releases/{releaseId}:${action}`,
      {
        params: {
          path: { releaseId: "release-1" },
          header: { "If-Match": '"revision-1"' },
        },
        body: {
          reason: "Lifecycle decision",
          ...(["deprecate", "supersede"].includes(action)
            ? { replacementId: "replacement-1" }
            : {}),
          ...(action === "deprecate"
            ? { sunsetAt: new Date("2026-10-01T12:00").toISOString() }
            : {}),
        },
      },
    ]);
  },
);

it.each([
  ["retired", true],
  ["superseded", true],
  ["revoked", false],
] as const)(
  "explains available actions for a %s release",
  async (state, revocable) => {
    const { props, get, post } = screenWorkspace();
    readReplies(get, {
      "/api/v1/listing-releases/{releaseId}": { ...releaseSeed, state },
      "/api/v1/catalog-listings/{listingId}": listing,
    });
    render(<Release {...props} id={releaseSeed.id} access={administrator} />);
    await screen.findByRole("heading", { name: `Release ${state}` });
    if (!revocable) {
      expect(screen.queryByLabelText("Disposition")).toBeNull();
      expect(screen.queryByLabelText("Reason")).toBeNull();
      expect(
        screen.queryByRole("button", { name: "Change release disposition" }),
      ).toBeNull();
      expect(
        screen.getByText(/Revoked releases cannot be changed/),
      ).toBeTruthy();
      expect(post).not.toHaveBeenCalled();
      return;
    }
    await screen.findByLabelText("Disposition");
    change("Reason", "Withdraw this release");
    const button = screen.getByRole("button", {
      name: "Change release disposition",
    });
    for (const action of ["deprecate", "retire", "supersede", "revoke"]) {
      change("Disposition", action);
      const available = action === "revoke" && revocable;
      expect(button).toHaveProperty("disabled", !available);
      const hint = screen.queryByText(
        /Retired and superseded releases can only be revoked\./,
      );
      if (available) expect(hint).toBeNull();
      else {
        expect(hint).not.toBeNull();
        expect(hint?.textContent).toContain(
          "Revoked releases cannot be changed.",
        );
      }
    }
    expect(post).not.toHaveBeenCalled();
    expect(get.mock.calls.map(([path]) => path)).toEqual([
      "/api/v1/listing-releases/{releaseId}",
      "/api/v1/catalog-listings/{listingId}",
    ]);
  },
);

it.each([undefined, 7])(
  "opens exact review evidence without resource.read and preserves listing concurrency (%s)",
  async (listingConcurrencyRevision) => {
    const { props, get, post } = screenWorkspace();
    readReplies(get, {
      ...routes("approved"),
      "/api/v1/review-submissions/{submissionId}": {
        ...submission,
        state: "approved",
        namespaceId: listing.namespaceId,
        ...(listingConcurrencyRevision ? { listingConcurrencyRevision } : {}),
      },
    });
    const permissions = [
      "resource.read_history",
      "resource.read_content",
      "review.decide",
      "release.publish",
    ] as const;
    render(
      <Reviews
        {...props}
        id={submission.id}
        access={{
          ...props.access,
          permissions: [],
          availablePermissions: [...permissions],
          namespaceGrants: [
            { namespaceId: listing.namespaceId, permissions: [...permissions] },
          ],
        }}
      />,
    );
    await screen.findByRole("button", { name: "Inspect submitted evidence" });
    expect(get.mock.calls.map(([path]) => path)).toEqual([
      "/api/v1/review-submissions/{submissionId}",
    ]);
    expect(screen.queryByRole("link", { name: "Listing" })).toBeNull();
    expect(
      screen.getByRole("link", { name: "Submitted listing revision" }),
    ).toBeTruthy();
    expect(
      screen.getByRole("link", { name: "Submitted version revision" }),
    ).toBeTruthy();
    fireEvent.click(
      screen.getByRole("button", { name: "Inspect submitted evidence" }),
    );
    await screen.findByText("Submitted helper");
    expect(
      screen.getByRole("button", { name: "Publish approved listing" }),
    ).toHaveProperty("disabled", !listingConcurrencyRevision);
    expect(post).not.toHaveBeenCalled();
    if (listingConcurrencyRevision) {
      post.mockResolvedValue(etagReply(publicationReply("review-release")));
      await confirmCommand("Publish approved listing");
      await screen.findByRole("link", { name: "Open published release" });
      expect(post.mock.calls[0]).toMatchObject([
        "/api/v1/catalog-listings/{listingId}:publish",
        {
          params: {
            path: { listingId: listing.id },
            header: { "If-Match": '"rev-7"' },
          },
        },
      ]);
    }
  },
);

it.each([undefined, "ns-1"])(
  "reads release history without metadata permission (%s)",
  async (namespaceId) => {
    const { props, get, post } = screenWorkspace();
    readReplies(get, {
      "/api/v1/listing-releases/{releaseId}": releaseSeed,
      "/api/v1/review-submissions/{submissionId}": {
        ...submission,
        ...(namespaceId ? { namespaceId } : {}),
      },
    });
    render(
      <Release
        {...props}
        id={releaseSeed.id}
        access={{
          ...props.access,
          permissions: [],
          availablePermissions: ["resource.read_history"],
          namespaceGrants: [
            { namespaceId: "ns-1", permissions: ["resource.read_history"] },
          ],
        }}
      />,
    );
    if (namespaceId) {
      await screen.findByRole("heading", { name: "Release published" });
      expect(
        screen.queryByRole("link", { name: "Back to listing" }),
      ).toBeNull();
      expect(
        screen.queryByRole("link", { name: "Release history" }),
      ).toBeNull();
      expect(
        screen.getByRole("link", { name: "Approved submission" }),
      ).toBeTruthy();
      for (const label of [
        "Listing",
        "Exact version revision",
        "Released listing revision",
      ]) {
        const reference = screen.getByText(label).nextElementSibling!;
        expect(reference.querySelector("a")).toBeNull();
        expect(reference.querySelector("code")?.textContent).toBeTruthy();
      }
      expect(
        screen.queryByRole("button", { name: "Change release disposition" }),
      ).toBeNull();
      expect(screen.queryByRole("link", { name: "Catalog record" })).toBeNull();
    } else {
      await screen.findByRole("alert");
      expect(
        screen.queryByRole("heading", { name: "Release published" }),
      ).toBeNull();
    }
    expect(get.mock.calls.map(([path]) => path)).toEqual([
      "/api/v1/listing-releases/{releaseId}",
      "/api/v1/review-submissions/{submissionId}",
    ]);
    expect(post).not.toHaveBeenCalled();
  },
);

it("loads release metadata only after resolving its authorized namespace", async () => {
  const { props, get } = screenWorkspace();
  readReplies(get, {
    "/api/v1/listing-releases/{releaseId}": releaseSeed,
    "/api/v1/review-submissions/{submissionId}": {
      ...submission,
      namespaceId: "ns-1",
    },
    "/api/v1/catalog-listings/{listingId}": {
      ...listing,
      namespaceId: "ns-1",
      currentReleaseId: releaseSeed.id,
    },
  });
  render(
    <Release
      {...props}
      id={releaseSeed.id}
      access={{
        ...props.access,
        permissions: [],
        availablePermissions: ["resource.read_history", "resource.read"],
        namespaceGrants: [
          {
            namespaceId: "ns-1",
            permissions: ["resource.read_history", "resource.read"],
          },
        ],
      }}
    />,
  );
  await screen.findByRole("link", { name: "Catalog record" });
  expect(get.mock.calls.map(([path]) => path)).toEqual([
    "/api/v1/listing-releases/{releaseId}",
    "/api/v1/review-submissions/{submissionId}",
    "/api/v1/catalog-listings/{listingId}",
  ]);
});

it.each([
  "history-only",
  "metadata",
  "content",
  "foreign-content",
  "scope-denied",
])(
  "offers only independently readable review destinations: %s",
  async (mode) => {
    const w = screenWorkspace();
    readReplies(w.get, {
      ...routes(),
      "/api/v1/review-submissions/{submissionId}": {
        ...submission,
        namespaceId: listing.namespaceId,
      },
    });
    const permissions: Access["permissions"] = [
      "resource.read_history",
      ...(mode === "metadata" || mode === "scope-denied"
        ? ["resource.read" as const]
        : []),
      ...(mode === "content" || mode === "scope-denied"
        ? ["resource.read_content" as const]
        : []),
    ];
    const foreign: Access["namespaceGrants"] =
      mode === "foreign-content"
        ? [
            {
              namespaceId: "other-namespace",
              permissions: ["resource.read_content"],
            },
          ]
        : [];
    render(
      <Reviews
        {...w.props}
        id={submission.id}
        access={{
          ...reader,
          permissions: [],
          availablePermissions: [
            ...permissions,
            ...(mode === "foreign-content"
              ? ["resource.read_content" as const]
              : []),
          ],
          namespaceGrants: [
            { namespaceId: listing.namespaceId, permissions },
            ...foreign,
          ],
          scopeEnforcement: true,
          scopes:
            mode === "scope-denied" ? ["catalog.review"] : ["catalog.read"],
        }}
      />,
    );
    await screen.findByRole("heading", { name: "Awaiting independent review" });
    expect(screen.queryByRole("link", { name: "Listing" }) !== null).toBe(
      mode === "metadata",
    );
    for (const label of [
      "Submitted listing revision",
      "Submitted version revision",
    ])
      expect(screen.queryByRole("link", { name: label }) !== null).toBe(
        mode === "content",
      );
    const inspect = screen.queryByRole("button", {
      name: "Inspect submitted evidence",
    });
    expect(inspect !== null).toBe(mode === "content");
    if (inspect) {
      fireEvent.click(inspect);
      await screen.findByText("Submitted helper");
    } else {
      expect(
        screen.getAllByText(submission.listingRevisionId).length,
      ).toBeGreaterThan(0);
      expect(screen.getByText(submission.versionRevisionId)).toBeTruthy();
      expect(
        screen.getByText(
          /Submitted documents and evidence require content access/,
        ),
      ).toBeTruthy();
    }
    expect(w.get.mock.calls.map(([path]) => path)).toEqual([
      "/api/v1/review-submissions/{submissionId}",
      ...(mode === "metadata" ? ["/api/v1/catalog-listings/{listingId}"] : []),
      ...(mode === "content"
        ? [
            "/api/v1/review-submissions/{submissionId}/evidence",
            "/api/v1/listing-revisions/{revisionId}",
          ]
        : []),
    ]);
    expect(w.post).not.toHaveBeenCalled();
  },
);

it("clears opened review evidence immediately when content permission is removed", async () => {
  const w = screenWorkspace();
  readReplies(w.get, {
    ...routes(),
    "/api/v1/review-submissions/{submissionId}": {
      ...submission,
      namespaceId: listing.namespaceId,
    },
  });
  const access: Access = {
    ...reader,
    permissions: ["resource.read_history", "resource.read_content"],
    availablePermissions: ["resource.read_history", "resource.read_content"],
  };
  const view = render(
    <Reviews {...w.props} access={access} id={submission.id} />,
  );
  fireEvent.click(
    await screen.findByRole("button", { name: "Inspect submitted evidence" }),
  );
  await screen.findByText("Submitted helper");
  view.rerender(
    <Reviews
      {...w.props}
      id={submission.id}
      access={{
        ...access,
        permissions: ["resource.read_history"],
        availablePermissions: ["resource.read_history"],
      }}
    />,
  );
  expect(screen.queryByText("Submitted helper")).toBeNull();
  expect(
    screen.queryByRole("button", { name: "Inspect submitted evidence" }),
  ).toBeNull();
  expect(
    screen.queryByRole("link", { name: "Submitted listing revision" }),
  ).toBeNull();
  expect(w.get).toHaveBeenCalledTimes(3);
  expect(w.post).not.toHaveBeenCalled();
});

it.each(["resolved", "denied", "failed", "obsolete"])(
  "resolves exact review navigation without eager parent reads: %s",
  async (mode) => {
    const w = screenWorkspace();
    const namespaceId = "00000000-0000-7000-8000-000000000081";
    const pending = deferred<ReturnType<typeof etagReply>>();
    const access: Access = {
      ...sessionAccess,
      permissions: [],
      availablePermissions: [
        "resource.read",
        "resource.read_history",
        "resource.read_content",
      ],
      namespaceGrants: [],
      nextCursor: "more",
    };
    const resolved: Access = {
      ...access,
      namespaceGrants: [
        {
          namespaceId,
          permissions:
            mode === "denied"
              ? ["resource.read_history"]
              : [...access.availablePermissions],
        },
      ],
    };
    const original = {
      ...routes("approved"),
      "/api/v1/review-submissions/{submissionId}": {
        ...submission,
        tenantId: access.tenantId,
        namespaceId,
        state: "approved",
      },
    };
    w.get.mockImplementation(async (path) => {
      if (path === "/api/v1/me") return pending.promise;
      expect(Object.hasOwn(original, String(path))).toBe(true);
      return etagReply(original[String(path) as keyof typeof original]);
    });
    const view = render(
      <Reviews {...w.props} access={access} id={submission.id} />,
    );
    await waitFor(() =>
      expect(w.get.mock.calls.map(([path]) => path)).toEqual([
        "/api/v1/review-submissions/{submissionId}",
        "/api/v1/me",
      ]),
    );
    expect(screen.queryByRole("link", { name: "Listing" })).toBeNull();
    expect(
      screen.queryByRole("button", { name: "Inspect submitted evidence" }),
    ).toBeNull();
    if (mode === "obsolete") {
      const revoked = { ...access, availablePermissions: [] };
      delete revoked.nextCursor;
      view.rerender(
        <Reviews {...w.props} access={revoked} id={submission.id} />,
      );
    }
    await act(async () => {
      if (mode === "failed")
        pending.reject(new Error("Controlled namespace failure"));
      else pending.resolve(etagReply(resolved));
    });
    if (mode === "failed") await screen.findByRole("alert");
    expect(screen.queryByRole("link", { name: "Listing" }) !== null).toBe(
      mode === "resolved",
    );
    const inspect = screen.queryByRole("button", {
      name: "Inspect submitted evidence",
    });
    expect(inspect !== null).toBe(mode === "resolved");
    if (inspect) {
      fireEvent.click(inspect);
      await screen.findByText("Submitted helper");
    }
    expect(
      w.get.mock.calls.filter(([path]) => path === "/api/v1/me"),
    ).toHaveLength(1);
    expect(
      w.get.mock.calls.some(
        ([path]) => path === "/api/v1/catalog-listings/{listingId}",
      ),
    ).toBe(false);
    expect(
      w.get.mock.calls.some(
        ([path]) =>
          path === "/api/v1/review-submissions/{submissionId}/evidence",
      ),
    ).toBe(mode === "resolved");
    expect(w.post).not.toHaveBeenCalled();
  },
);

it.each(["metadata-history", "content-history", "scope-denied"])(
  "keeps release metadata, history and content navigation independent: %s",
  async (mode) => {
    const w = screenWorkspace();
    readReplies(w.get, {
      "/api/v1/listing-releases/{releaseId}": releaseSeed,
      "/api/v1/review-submissions/{submissionId}": {
        ...submission,
        namespaceId: listing.namespaceId,
      },
      "/api/v1/catalog-listings/{listingId}": {
        ...listing,
        currentReleaseId: releaseSeed.id,
      },
    });
    const permissions: Access["permissions"] = [
      "resource.read_history",
      ...(mode !== "content-history" ? ["resource.read" as const] : []),
      ...(mode !== "metadata-history"
        ? ["resource.read_content" as const]
        : []),
    ];
    render(
      <Release
        {...w.props}
        id={releaseSeed.id}
        access={{
          ...reader,
          permissions: [],
          availablePermissions: permissions,
          namespaceGrants: [{ namespaceId: listing.namespaceId, permissions }],
          scopeEnforcement: true,
          scopes: mode === "scope-denied" ? [] : ["catalog.read"],
        }}
      />,
    );
    await screen.findByRole("heading", { name: "Release published" });
    expect(
      screen.queryByRole("link", { name: "Back to listing" }) !== null,
    ).toBe(mode === "metadata-history");
    expect(
      screen.queryByRole("link", { name: "Release history" }) !== null,
    ).toBe(mode === "metadata-history");
    expect(
      screen.queryByRole("link", { name: "Approved submission" }) !== null,
    ).toBe(mode !== "scope-denied");
    for (const label of ["Exact version revision", "Released listing revision"])
      expect(
        !!screen.getByText(label).nextElementSibling?.querySelector("a"),
      ).toBe(mode === "content-history");
    expect(w.get.mock.calls.map(([path]) => path)).toEqual([
      "/api/v1/listing-releases/{releaseId}",
      "/api/v1/review-submissions/{submissionId}",
      ...(mode === "metadata-history"
        ? ["/api/v1/catalog-listings/{listingId}"]
        : []),
    ]);
    expect(w.post).not.toHaveBeenCalled();
  },
);

it.each(["none", "available", "tenant", "scope-denied"])(
  "requires confirmed metadata authority for a filtered review queue: %s",
  async (mode) => {
    history.replaceState(null, "", "/unit/reviews?listing=listing-1");
    const w = screenWorkspace();
    w.get.mockResolvedValue(etagReply({ items: [] }));
    render(
      <Reviews
        {...w.props}
        access={{
          ...reader,
          permissions:
            mode === "tenant"
              ? ["resource.read_history", "resource.read"]
              : ["resource.read_history"],
          availablePermissions:
            mode === "none"
              ? ["resource.read_history"]
              : ["resource.read_history", "resource.read"],
          scopeEnforcement: true,
          scopes: mode === "scope-denied" ? [] : ["catalog.read"],
        }}
      />,
    );
    await screen.findByText("No matching review submissions.");
    expect(
      screen.queryByRole("link", { name: "Back to listing" }) !== null,
    ).toBe(mode === "tenant");
    if (mode !== "tenant") expect(screen.getByText("listing-1")).toBeTruthy();
    expect(w.get.mock.calls.map(([path]) => path)).toEqual([
      "/api/v1/review-submissions",
    ]);
    expect(w.post).not.toHaveBeenCalled();
  },
);

it.each(["resolved", "failed"])(
  "keeps release links closed until the exact namespace hint is %s",
  async (mode) => {
    const w = screenWorkspace();
    const namespaceId = "00000000-0000-7000-8000-000000000081";
    const pending = deferred<ReturnType<typeof etagReply>>();
    const permissions: Access["permissions"] = [
      "resource.read",
      "resource.read_history",
      "resource.read_content",
    ];
    const access: Access = {
      ...sessionAccess,
      permissions: [],
      availablePermissions: permissions,
      namespaceGrants: [],
      nextCursor: "more",
    };
    w.get.mockImplementation(async (path) => {
      if (path === "/api/v1/me") return pending.promise;
      expect([
        "/api/v1/listing-releases/{releaseId}",
        "/api/v1/review-submissions/{submissionId}",
      ]).toContain(path);
      return etagReply(
        path === "/api/v1/listing-releases/{releaseId}"
          ? releaseSeed
          : { ...submission, namespaceId },
      );
    });
    render(<Release {...w.props} access={access} id={releaseSeed.id} />);
    await waitFor(() => expect(w.get).toHaveBeenCalledTimes(3));
    expect(screen.queryByRole("link", { name: "Back to listing" })).toBeNull();
    expect(screen.queryByRole("link", { name: "Release history" })).toBeNull();
    expect(
      screen
        .getByText("Exact version revision")
        .nextElementSibling?.querySelector("a"),
    ).toBeNull();
    await act(async () => {
      if (mode === "failed")
        pending.reject(new Error("Controlled namespace failure"));
      else
        pending.resolve(
          etagReply({
            ...access,
            namespaceGrants: [{ namespaceId, permissions }],
          }),
        );
    });
    if (mode === "failed") await screen.findByRole("alert");
    expect(
      screen.queryByRole("link", { name: "Back to listing" }) !== null,
    ).toBe(mode === "resolved");
    expect(
      screen.queryByRole("link", { name: "Release history" }) !== null,
    ).toBe(mode === "resolved");
    expect(
      screen
        .getByText("Exact version revision")
        .nextElementSibling?.querySelector("a") !== null,
    ).toBe(mode === "resolved");
    expect(w.get.mock.calls.map(([path]) => path)).toEqual([
      "/api/v1/listing-releases/{releaseId}",
      "/api/v1/review-submissions/{submissionId}",
      "/api/v1/me",
    ]);
    expect(w.get).toHaveBeenLastCalledWith(
      "/api/v1/me",
      expect.objectContaining({ params: { query: { namespaceId } } }),
    );
    expect(w.post).not.toHaveBeenCalled();
  },
);

it.each([
  {
    canonicalIdentifier: "urn:air:example.com:Finance:MyAgent",
    submittedDisplayName: "Submitted helper",
  },
  {
    canonicalIdentifier: "🙂".repeat(512),
    submittedDisplayName: "🙂".repeat(160),
  },
  { canonicalIdentifier: undefined, submittedDisplayName: "Submitted helper" },
  {
    canonicalIdentifier: "urn:air:example.com:Finance:MyAgent",
    submittedDisplayName: undefined,
  },
  { canonicalIdentifier: undefined, submittedDisplayName: undefined },
])(
  "shows independently returned submitted context without private hydration: %j",
  async (context) => {
    const { props, get, post } = screenWorkspace();
    get.mockResolvedValue(
      etagReply({ items: [{ ...submission, ...context }] }),
    );
    render(<Reviews {...props} />);
    await screen.findByRole("link", { name: submission.id });
    expect(
      screen.getByRole("cell", {
        name: context.submittedDisplayName ?? "Name unavailable",
      }),
    ).toBeTruthy();
    expect(
      screen.getByRole("cell", {
        name: context.canonicalIdentifier ?? "Identifier unavailable",
      }),
    ).toBeTruthy();
    expect(get.mock.calls).toHaveLength(1);
    expect(get.mock.calls[0]?.[0]).toBe("/api/v1/review-submissions");
    expect(post).not.toHaveBeenCalled();
  },
);

it.each([
  { canonicalIdentifier: null },
  { canonicalIdentifier: "" },
  { canonicalIdentifier: "🙂".repeat(513) },
  { submittedDisplayName: [] },
  { submittedDisplayName: "" },
  { submittedDisplayName: "🙂".repeat(161) },
  { submittedDisplayName: "name\0bad" },
])(
  "rejects malformed review context before rendering rows: %j",
  async (context) => {
    const { props, get } = screenWorkspace();
    get.mockResolvedValue(
      etagReply({ items: [{ ...submission, ...context }] }),
    );
    render(<Reviews {...props} />);
    await screen.findByRole("alert");
    expect(screen.queryByRole("link", { name: submission.id })).toBeNull();
    expect(get.mock.calls).toHaveLength(1);
  },
);
