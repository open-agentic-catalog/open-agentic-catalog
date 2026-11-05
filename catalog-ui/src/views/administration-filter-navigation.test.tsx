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
import {
  administrator,
  etagReply,
  screenWorkspace,
} from "../../tests/unit/screens";
import { role, group } from "../../tests/unit/policy-fixtures";
import Members from "./Members";
import Authorization from "./Authorization";
import Feedback from "./Feedback";
import Ratings from "./Ratings";

const membershipRecord = {
  id: "member-a",
  realmId: "realm-a",
  subject: "service-reader",
  actorType: "service",
  state: "active",
};

afterEach(() => {
  cleanup();
  history.replaceState(null, "", "/");
});
function historyMove(url: string, ordinal: number) {
  act(() => {
    history.replaceState({ caseNavigation: ordinal }, "", url);
    window.dispatchEvent(
      new CustomEvent("catalog-ui:navigate", {
        detail: history.state as unknown,
        cancelable: true,
      }),
    );
  });
}
it.each(["members", "access-roles", "access-groups"] as const)(
  "%s keeps its applied filter URL when current permissions refresh",
  async (section) => {
    const w = screenWorkspace();
    const membership = section === "members";
    const search = `?state=active${membership ? "&actorType=service" : ""}`;
    history.replaceState(null, "", `/unit/${section}${search}`);
    w.get.mockResolvedValue(
      etagReply({
        items: [],
        authorizationRevision: administrator.authorizationRevision,
      }),
    );
    const view = render(
      membership ? (
        <Members {...w.props} access={administrator} />
      ) : (
        <Authorization {...w.props} access={administrator} />
      ),
    );
    await waitFor(() => expect(w.get).toHaveBeenCalled());
    expect(screen.getByLabelText("State")).toHaveProperty("value", "active");
    const access = {
      ...administrator,
      authorizationRevision: administrator.authorizationRevision + 1,
    };
    view.rerender(
      membership ? (
        <Members {...w.props} access={access} />
      ) : (
        <Authorization {...w.props} access={access} />
      ),
    );
    expect(location.search).toBe(search);
    expect(screen.getByLabelText("State")).toHaveProperty("value", "active");
    if (membership)
      expect(screen.getByLabelText("Identity type filter")).toHaveProperty(
        "value",
        "service",
      );
    expect(w.post).not.toHaveBeenCalled();
  },
);
it.each(["members", "access-roles", "access-groups"] as const)(
  "%s initializes filters and restores applied criteria with cursor reset",
  async (section) => {
    const w = screenWorkspace(),
      membership = section === "members";
    const path = membership
      ? "/api/v1/memberships"
      : section === "access-roles"
        ? "/api/v1/access/roles"
        : "/api/v1/access/groups";
    const initial = `/unit/${section}?state=active${membership ? "&actorType=service" : ""}`;
    history.replaceState(null, "", initial);
    w.get.mockImplementation(async (route) =>
      etagReply({
        items:
          route === path
            ? [
                membership
                  ? membershipRecord
                  : section === "access-roles"
                    ? role
                    : group,
              ]
            : [],
        nextCursor: "directory-next",
        authorizationRevision: administrator.authorizationRevision,
      }),
    );
    render(
      membership ? (
        <Members {...w.props} access={administrator} />
      ) : (
        <Authorization {...w.props} access={administrator} />
      ),
    );
    await waitFor(() =>
      expect(
        w.get.mock.calls.some(
          ([route, options]) =>
            route === path &&
            (options as { params: { query: { state: string } } }).params.query
              .state === "active",
        ),
      ).toBe(true),
    );
    expect(screen.getByLabelText("State")).toHaveProperty("value", "active");
    if (membership)
      expect(screen.getByLabelText("Identity type filter")).toHaveProperty(
        "value",
        "service",
      );
    fireEvent.click(screen.getByRole("button", { name: "Next page" }));
    await waitFor(() =>
      expect(w.get.mock.calls.at(-1)?.[1]).toMatchObject({
        params: { query: { cursor: "directory-next" } },
      }),
    );
    const next = membership
      ? "offboarded"
      : section === "access-roles"
        ? "archived"
        : "suspended";
    fireEvent.change(screen.getByLabelText("State"), {
      target: { value: next },
    });
    fireEvent.change(
      screen.getByLabelText(membership ? "Exact subject" : "Exact name"),
      { target: { value: "private-filter-text" } },
    );
    fireEvent.click(screen.getByRole("button", { name: "Apply filters" }));
    await waitFor(() =>
      expect(w.get.mock.calls.at(-1)?.[1]).toMatchObject({
        params: { query: { state: next } },
      }),
    );
    expect(w.get.mock.calls.at(-1)?.[1]).not.toHaveProperty(
      "params.query.cursor",
    );
    expect(location.search).not.toContain("private-filter-text");
    fireEvent.click(screen.getByRole("button", { name: "Clear filters" }));
    await waitFor(() => expect(location.search).toBe(""));
    historyMove(initial, 0);
    await waitFor(() =>
      expect(w.get.mock.calls.at(-1)?.[1]).toMatchObject({
        params: { query: { state: "active" } },
      }),
    );
    expect(screen.getByLabelText("State")).toHaveProperty("value", "active");
    expect(
      screen.getByLabelText(membership ? "Exact subject" : "Exact name"),
    ).toHaveProperty("value", "");
    historyMove(
      `/unit/${section}?state=active&state=active${membership ? "&actorType=human&actorType=service" : ""}`,
      3,
    );
    await waitFor(() =>
      expect(w.get.mock.calls.at(-1)?.[1]).toMatchObject({
        params: {
          query: {
            state: "unsupported",
            ...(membership ? { actorType: "unsupported" } : {}),
          },
        },
      }),
    );
    expect(
      screen.getByRole("option", { name: "Unsupported state" }),
    ).toBeTruthy();
    if (membership)
      expect(
        screen.getByRole("option", { name: "Unsupported identity type" }),
      ).toBeTruthy();
  },
);
it.each(["feedback", "ratings"] as const)(
  "%s restores URL criteria without reading resources or widening repeated selections",
  async (section) => {
    const w = screenWorkspace(),
      feedback = section === "feedback";
    const access = {
      ...administrator,
      permissions: [
        feedback
          ? ("feedback.moderate" as const)
          : ("ratings.moderate" as const),
      ],
      availablePermissions: [],
      scopeEnforcement: true,
      scopes: ["tenant.admin" as const],
    };
    const initial = `/unit/${section}?state=${feedback ? "open&category=problem" : "removed"}`;
    history.replaceState(null, "", initial);
    w.get.mockResolvedValue(
      etagReply({ items: [], nextCursor: "moderation-next" }),
    );
    render(
      feedback ? (
        <Feedback {...w.props} access={access} />
      ) : (
        <Ratings {...w.props} access={access} />
      ),
    );
    await waitFor(() => expect(w.get).toHaveBeenCalledTimes(1));
    expect(w.get.mock.calls[0]).toMatchObject([
      `/api/v1/${section}/inbox`,
      {
        params: {
          query: {
            state: feedback ? "open" : "removed",
            ...(feedback ? { category: "problem" } : {}),
          },
        },
      },
    ]);
    fireEvent.click(screen.getByRole("button", { name: "Next page" }));
    await waitFor(() =>
      expect(w.get.mock.calls.at(-1)?.[1]).toHaveProperty(
        "params.query.cursor",
        "moderation-next",
      ),
    );
    fireEvent.change(
      screen.getByLabelText(feedback ? "Feedback state" : "Rating state"),
      { target: { value: feedback ? "addressed" : "active" } },
    );
    await waitFor(() =>
      expect(w.get.mock.calls.at(-1)?.[1]).not.toHaveProperty(
        "params.query.cursor",
      ),
    );
    expect(new URLSearchParams(location.search).get("state")).toBe(
      feedback ? "addressed" : "active",
    );
    historyMove(initial, 0);
    await waitFor(() =>
      expect(w.get.mock.calls.at(-1)?.[1]).toMatchObject({
        params: { query: { state: feedback ? "open" : "removed" } },
      }),
    );
    expect(
      screen.getByLabelText(feedback ? "Feedback state" : "Rating state"),
    ).toHaveProperty("value", feedback ? "open" : "removed");
    historyMove(
      `/unit/${section}?state=active&state=active${feedback ? "&category=problem&category=problem" : ""}`,
      2,
    );
    await waitFor(() =>
      expect(w.get.mock.calls.at(-1)?.[1]).toMatchObject({
        params: {
          query: {
            state: "unsupported",
            ...(feedback ? { category: "unsupported" } : {}),
          },
        },
      }),
    );
    expect(
      w.get.mock.calls.every(([path]) => path === `/api/v1/${section}/inbox`),
    ).toBe(true);
  },
);
