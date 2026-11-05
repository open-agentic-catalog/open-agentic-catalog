// @vitest-environment jsdom
import { useState } from "react";
import { expect, it, vi } from "vitest";
import { fireEvent, render, screen, waitFor } from "@testing-library/react";
import { ChoiceField } from "./authoring";
import { ChoicePrerequisite } from "./ChoicePrerequisite";
import { ReleaseReplacement } from "./ReleaseReplacement";
import { PublicationExclusion } from "./PublicationExclusions";
import { release } from "../../tests/unit/responses";
import {
  administrator,
  screenLifecycle,
  screenWorkspace,
  etagReply,
} from "../../tests/unit/screens";
screenLifecycle("/unit");
it.each(["empty", "failed", "later-empty", "selected"])(
  "provides prerequisite recovery for %s choices without assuming a complete inventory",
  async (kind) => {
    const load = vi.fn(async (cursor: string) => {
      if (kind === "failed") throw new Error("Read failed");
      return {
        items:
          kind === "later-empty" && !cursor
            ? [{ id: "first", label: "First" }]
            : [],
        nextCursor: kind === "later-empty" && !cursor ? "next" : undefined,
      };
    });
    render(
      <ChoiceField
        label="Namespace"
        value={kind === "selected" ? "retained" : ""}
        onChange={vi.fn()}
        load={load}
        onFailure={vi.fn()}
        nextSteps={<p>Ask an administrator for a namespace.</p>}
      />,
    );
    await waitFor(() => expect(load).toHaveBeenCalledOnce());
    if (kind === "later-empty")
      fireEvent.click(await screen.findByRole("button", { name: "Next page" }));
    if (kind === "selected") {
      expect(
        screen.queryByText("Ask an administrator for a namespace."),
      ).toBeNull();
      expect(
        screen.getByRole("option", { name: "retained (selected)" }),
      ).toBeTruthy();
    } else
      expect(
        await screen.findByText("Ask an administrator for a namespace."),
      ).toBeTruthy();
  },
);
it.each(["namespace", "publisher", "version", "revision"] as const)(
  "offers only an already-authorized %s link and never fetches prerequisite inventory",
  (kind) => {
    const { props, get } = screenWorkspace();
    const full = { ...props, access: administrator };
    const view = render(
      <ChoicePrerequisite
        props={full}
        kind={kind}
        resourceId="resource-1"
        namespaceId="ns-1"
        versionId="version-1"
      />,
    );
    expect(screen.getByRole("link").getAttribute("href")).toMatch(/^\/unit\//);
    view.rerender(
      <ChoicePrerequisite
        props={{
          ...props,
          access: {
            ...administrator,
            permissions: [],
            availablePermissions: [],
          },
        }}
        kind={kind}
        resourceId="resource-1"
        namespaceId="ns-1"
        versionId="version-1"
      />,
    );
    expect(screen.queryByRole("link")).toBeNull();
    expect(get).not.toHaveBeenCalled();
  },
);
it("loads eligible replacement choices only on request, pages bounded results and retains an exact cross-listing ID", async () => {
  const { props, get } = screenWorkspace();
  get.mockImplementation(async (path, options: unknown) => {
    expect(path).toBe("/api/v1/catalog-listings/{listingId}/releases");
    const query = (
      options as { params: { query: { cursor?: string; limit: number } } }
    ).params.query;
    expect(query.limit).toBe(25);
    return etagReply(
      query.cursor
        ? { items: [{ ...release, id: "replacement-2", state: "deprecated" }] }
        : {
            items: [
              release,
              { ...release, id: "replacement-1" },
              { ...release, id: "retired", state: "retired" },
            ],
            nextCursor: "next",
          },
    );
  });
  function Editor() {
    const [value, setValue] = useState("");
    return (
      <ReleaseReplacement
        props={{ ...props, access: administrator }}
        namespaceId="ns-1"
        listingId={release.listingId}
        releaseId={release.id}
        value={value}
        onChange={setValue}
        required={false}
      />
    );
  }
  render(<Editor />);
  expect(get).not.toHaveBeenCalled();
  fireEvent.click(
    screen.getByRole("button", { name: "Choose same-listing replacement" }),
  );
  await screen.findByRole("option", { name: "replacement-1 (published)" });
  expect(screen.queryByRole("option", { name: /retired/ })).toBeNull();
  expect(screen.queryByRole("option", { name: /^release-1/ })).toBeNull();
  fireEvent.change(screen.getByLabelText("Same-listing replacement"), {
    target: { value: "replacement-1" },
  });
  expect(screen.getByLabelText("Replacement release ID")).toHaveProperty(
    "value",
    "replacement-1",
  );
  fireEvent.click(screen.getByRole("button", { name: "Next page" }));
  await screen.findByRole("option", { name: "replacement-2 (deprecated)" });
  expect(screen.getByLabelText("Replacement release ID")).toHaveProperty(
    "value",
    "replacement-1",
  );
  fireEvent.change(screen.getByLabelText("Replacement release ID"), {
    target: { value: "cross-listing-id" },
  });
  expect(
    screen.getByRole("option", { name: "cross-listing-id (selected)" }),
  ).toBeTruthy();
  fireEvent.click(
    screen.getByRole("button", { name: "Hide same-listing replacement" }),
  );
  expect(screen.getByLabelText("Replacement release ID")).toHaveProperty(
    "value",
    "cross-listing-id",
  );
  expect(get).toHaveBeenCalledTimes(2);
});
it("preserves exact replacement entry without history authority", () => {
  const { props, get } = screenWorkspace();
  render(
    <ReleaseReplacement
      props={{
        ...props,
        access: {
          ...administrator,
          permissions: ["release.transition"],
          availablePermissions: ["release.transition"],
        },
      }}
      namespaceId="ns-1"
      listingId={release.listingId}
      releaseId={release.id}
      value="exact-id"
      onChange={vi.fn()}
      required
    />,
  );
  expect(screen.queryByRole("button")).toBeNull();
  expect(screen.getByLabelText("Replacement release ID")).toHaveProperty(
    "required",
    true,
  );
  expect(get).not.toHaveBeenCalled();
});
it("explains known exclusions, retains unknown/profile evidence and gates resource links with tenant authority", () => {
  const { props, get } = screenWorkspace();
  const item = {
    resourceId: "resource-1",
    identifier: "urn:air:example.com:agents:one",
    profiles: ["ard-pinned"],
    reasons: [
      "LISTING_NOT_PUBLIC",
      "LISTING_NOT_TENANT_VISIBLE",
      "AIR_IDENTIFIER_REQUIRED",
      "PUBLICATION_EVIDENCE_BLOCKED",
      "STANDARD_PUBLIC_REPRESENTATION_REQUIRED",
      "PUBLIC_DELIVERY_FORM_REQUIRED",
      "REPRESENTATIVE_QUERIES_REQUIRED",
      "FUTURE_CODE",
      "constructor",
      "__proto__",
    ],
  };
  const view = render(
    <PublicationExclusion
      item={item}
      props={{ ...props, access: administrator }}
    />,
  );
  expect(screen.getByRole("link").getAttribute("href")).toBe(
    "/unit/resources/resource-1",
  );
  expect(screen.getAllByText(/No local explanation/)).toHaveLength(3);
  expect(
    screen.getByText(/To change approved content, create a new version label/),
  ).toBeTruthy();
  expect(screen.getByText(/then submit the listing for review/)).toBeTruthy();
  expect(screen.getByText(/Profiles: ard-pinned/)).toBeTruthy();
  for (const reason of item.reasons)
    expect(screen.getByText(reason)).toBeTruthy();
  view.rerender(
    <PublicationExclusion
      item={item}
      props={{
        ...props,
        access: {
          ...administrator,
          permissions: [],
          availablePermissions: [],
          namespaceGrants: [
            { namespaceId: "ns-1", permissions: ["resource.read"] },
          ],
        },
      }}
    />,
  );
  expect(screen.queryByRole("link")).toBeNull();
  expect(get).not.toHaveBeenCalled();
});
