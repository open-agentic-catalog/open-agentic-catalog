// @vitest-environment jsdom
import { afterEach, expect, it } from "vitest";
import {
  act,
  cleanup,
  fireEvent,
  render,
  screen,
  waitFor,
} from "@testing-library/react";
import type { Access, RecordDetail } from "../api/client";
import {
  deferred,
  reply,
  sessionAccess,
  workspace,
} from "../../tests/unit/fixtures";
import { RecordWorkflows } from "./RecordWorkflows";

afterEach(cleanup);
const namespaceId = "00000000-0000-7000-8000-000000000188";
const record = {
  resource: { id: "resource-one", namespaceId },
  listing: { id: "listing-one", namespaceId },
  versionRevision: { id: "revision-one" },
  release: { id: "release-one", submissionId: "review-one" },
} as unknown as RecordDetail;
const permissions: Access["permissions"] = [
  "resource.read",
  "resource.read_content",
  "resource.read_history",
];
const access = (allowed: Access["permissions"]): Access => ({
  ...sessionAccess,
  permissions: allowed,
  availablePermissions: allowed,
});

it.each([
  {
    allowed: ["resource.read"],
    names: ["Resource metadata", "Listing metadata"],
  },
  {
    allowed: ["resource.read_content"],
    names: ["Inspect retained version revision"],
  },
  {
    allowed: ["resource.read_history"],
    names: ["Approved review", "Release details"],
  },
  {
    allowed: permissions,
    names: [
      "Inspect retained version revision",
      "Resource and versions",
      "Listing and revisions",
      "Release history",
      "Approved review",
      "Release details",
    ],
  },
  { allowed: [], names: [] },
] as { allowed: Access["permissions"]; names: string[] }[])(
  "keeps $allowed independent from other workflow grants",
  async ({ allowed, names }) => {
    const { props, get } = workspace();
    render(
      <RecordWorkflows
        {...props}
        access={access(allowed)}
        record={record}
        basePath="/unit"
        compact={false}
      />,
    );
    await act(async () => {});
    expect(screen.queryAllByRole("link").map((a) => a.textContent)).toEqual(
      names,
    );
    expect(get).not.toHaveBeenCalled();
  },
);

it.each([false, true])(
  "requires exact namespace grants and the OAuth ceiling (scope=%s)",
  async (scope) => {
    const { props, get } = workspace();
    const own = {
      ...access([]),
      availablePermissions: permissions,
      scopeEnforcement: true,
      scopes: scope ? (["catalog.read"] as Access["scopes"]) : [],
      namespaceGrants: [{ namespaceId, permissions }],
    };
    const view = render(
      <RecordWorkflows
        {...props}
        access={own}
        record={record}
        basePath="/unit"
        compact={false}
      />,
    );
    await act(async () => {});
    expect(screen.queryAllByRole("link")).toHaveLength(scope ? 6 : 0);
    view.rerender(
      <RecordWorkflows
        {...props}
        access={{
          ...own,
          namespaceGrants: [
            {
              namespaceId: "00000000-0000-7000-8000-000000000189",
              permissions,
            },
          ],
        }}
        record={record}
        basePath="/unit"
        compact={false}
      />,
    );
    expect(screen.queryAllByRole("link")).toHaveLength(0);
    expect(get).not.toHaveBeenCalled();
  },
);

it("keeps unknown access and compact content distinct from full workflows", async () => {
  const { props, get } = workspace();
  const view = render(
    <RecordWorkflows
      {...props}
      access={undefined}
      record={record}
      basePath="/unit"
      compact={false}
    />,
  );
  expect(
    screen.getByText("Workflow permissions have not been confirmed."),
  ).toBeTruthy();
  expect(screen.queryAllByRole("link")).toHaveLength(0);
  view.rerender(
    <RecordWorkflows
      {...props}
      access={access(permissions)}
      record={record}
      basePath="/unit"
      compact
    />,
  );
  await screen.findByRole("link", {
    name: "Inspect retained version revision",
  });
  expect(
    screen.queryByRole("navigation", { name: "Record workflows" }),
  ).toBeNull();
  expect(screen.queryAllByRole("link")).toHaveLength(1);
  expect(get).not.toHaveBeenCalled();
});

it("resolves only the selected later-page namespace and recovers a failed read deliberately", async () => {
  const { props, get } = workspace();
  const initial = {
    ...access([]),
    availablePermissions: permissions,
    nextCursor: "more-grants",
  };
  get
    .mockRejectedValueOnce(new Error("unavailable"))
    .mockResolvedValue(
      reply({ ...access([]), namespaceGrants: [{ namespaceId, permissions }] }),
    );
  render(
    <RecordWorkflows
      {...props}
      access={initial}
      record={record}
      basePath="/unit"
      compact={false}
    />,
  );
  await screen.findByRole("button", { name: "Retry namespace permissions" });
  expect(screen.queryAllByRole("link")).toHaveLength(0);
  fireEvent.click(
    screen.getByRole("button", { name: "Retry namespace permissions" }),
  );
  await screen.findByRole("link", { name: "Release history" });
  expect(get).toHaveBeenCalledTimes(2);
  for (const call of get.mock.calls)
    expect(call).toMatchObject([
      "/api/v1/me",
      { params: { query: { namespaceId } } },
    ]);
});

it("discards an obsolete later-page permission reply when the selected namespace changes", async () => {
  const { props, get } = workspace();
  const old = deferred<ReturnType<typeof reply>>();
  const next = deferred<ReturnType<typeof reply>>();
  get.mockReturnValueOnce(old.promise).mockReturnValueOnce(next.promise);
  const initial = {
    ...access([]),
    availablePermissions: permissions,
    nextCursor: "more-grants",
  };
  const view = render(
    <RecordWorkflows
      {...props}
      access={initial}
      record={record}
      basePath="/unit"
      compact={false}
    />,
  );
  await waitFor(() => expect(get).toHaveBeenCalledTimes(1));
  const other = {
    ...record,
    resource: {
      ...record.resource,
      namespaceId: "00000000-0000-7000-8000-000000000189",
    },
  };
  view.rerender(
    <RecordWorkflows
      {...props}
      access={initial}
      record={other}
      basePath="/unit"
      compact={false}
    />,
  );
  await waitFor(() => expect(get).toHaveBeenCalledTimes(2));
  await act(async () =>
    old.resolve(
      reply({ ...access([]), namespaceGrants: [{ namespaceId, permissions }] }),
    ),
  );
  expect(screen.queryAllByRole("link")).toHaveLength(0);
  await act(async () =>
    next.resolve(
      reply({
        ...access([]),
        namespaceGrants: [
          {
            namespaceId: other.resource.namespaceId,
            permissions: ["resource.read"],
          },
        ],
      }),
    ),
  );
  await screen.findByRole("link", { name: "Resource metadata" });
  expect(screen.queryByRole("link", { name: "Release details" })).toBeNull();
});
