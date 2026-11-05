// @vitest-environment jsdom
import { act, cleanup, render, screen, waitFor } from "@testing-library/react";
import { afterEach, expect, it } from "vitest";
import {
  CatalogPresentation,
  SelectedNamespace,
  originLabel,
} from "./CatalogPresentation";
import {
  deferred,
  reply,
  sessionAccess,
  workspace,
} from "../../tests/unit/fixtures";
import * as checks from "./responses";

afterEach(cleanup);

it("keeps known and unavailable namespace names honest without directory reads", async () => {
  const { props, get } = workspace();
  const view = render(
    <SelectedNamespace {...props} id="exact-namespace" label="Known name" />,
  );
  expect(screen.getByText("Known name")).toBeTruthy();
  view.rerender(
    <SelectedNamespace {...props} id="exact-namespace" access={undefined} />,
  );
  expect(screen.getByText("exact-namespace")).toBeTruthy();
  view.rerender(
    <SelectedNamespace
      {...props}
      access={{ ...sessionAccess, permissions: [], availablePermissions: [] }}
      id="exact-namespace"
    />,
  );
  await act(async () => {});
  expect(screen.getByText("exact-namespace")).toBeTruthy();
  expect(get).not.toHaveBeenCalled();
});

it.each([false, true])(
  "reads only the selected authorized namespace and refuses an unrelated label (mismatch=%s)",
  async (mismatch) => {
    const { props, get } = workspace();
    get.mockResolvedValue(
      reply({
        id: mismatch ? "other" : "selected",
        slug: "selected",
        displayName: "Readable name",
        revision: 1,
      }),
    );
    render(<SelectedNamespace {...props} id="selected" />);
    await waitFor(() => expect(get).toHaveBeenCalledOnce());
    await act(async () => {});
    expect(
      screen.getByText(mismatch ? "selected" : "Readable name"),
    ).toBeTruthy();
    expect(get).toHaveBeenCalledWith(
      "/api/v1/namespaces/{namespaceId}",
      expect.objectContaining({
        params: { path: { namespaceId: "selected" } },
      }),
    );
    expect(props.onFailure).toHaveBeenCalledTimes(mismatch ? 1 : 0);
  },
);

it("never displays a previous namespace label for a new selected identity", async () => {
  const { props, get } = workspace();
  const pending = deferred<ReturnType<typeof reply>>();
  get
    .mockResolvedValueOnce(
      reply({
        id: "one",
        slug: "one",
        displayName: "First label",
        revision: 1,
      }),
    )
    .mockReturnValueOnce(pending.promise);
  const view = render(<SelectedNamespace {...props} id="one" />);
  await screen.findByText("First label");
  view.rerender(<SelectedNamespace {...props} id="two" />);
  expect(screen.queryByText("First label")).toBeNull();
  await act(async () =>
    pending.resolve(
      reply({
        id: "two",
        slug: "two",
        displayName: "Second label",
        revision: 1,
      }),
    ),
  );
  await screen.findByText("Second label");
});

it("shows current labels and exact declared license as escaped text without directory links", () => {
  const display = {
    publisherName: "<b>Publisher</b>",
    namespaceName: "IDE plugins",
    license: "MIT",
  };
  render(<CatalogPresentation display={display} />);
  expect(screen.getByText(display.publisherName).tagName).toBe("DD");
  expect(screen.getByText("IDE plugins")).toBeTruthy();
  expect(screen.getByText("MIT")).toBeTruthy();
  expect(screen.queryByRole("link")).toBeNull();
});

it("renders omission honestly and allows surfaces to avoid duplicated facts", () => {
  const display = {
    publisherName: "Publisher",
    namespaceName: "Namespace",
    licenseOmitted: true as const,
  };
  const view = render(<CatalogPresentation display={display} />);
  expect(
    screen.getByText("See full record for the complete declaration."),
  ).toBeTruthy();
  view.rerender(
    <CatalogPresentation
      display={display}
      includePublisher={false}
      includeLicense={false}
    />,
  );
  expect(screen.queryByText("Publisher")).toBeNull();
  expect(screen.queryByText("License")).toBeNull();
  expect(screen.getByText("Namespace", { selector: "dd" })).toBeTruthy();
  view.rerender(<CatalogPresentation display={undefined} />);
  expect(view.container.textContent).toBe("");
  view.rerender(<CatalogPresentation display={{}} />);
  expect(view.container.textContent).toBe("");
});

it("keeps explicit provenance distinct from local lifecycle and declared delivery", () => {
  expect([
    originLabel("local"),
    originLabel("indexed"),
    originLabel("live"),
  ]).toEqual(["Local Catalog", "Indexed external", "Live external"]);
});

it("strictly validates bounded labels, Unicode license and omission states", () => {
  for (const value of [
    {},
    { publisherName: "P", namespaceName: "N", license: "MIT" },
    { license: "𐐀".repeat(256) },
    { licenseOmitted: true },
  ])
    expect(checks.CatalogPresentation(value)).toBe(true);
  for (const value of [
    null,
    [],
    { publisherName: null },
    { namespaceName: 8 },
    { license: "" },
    { license: "𐐀".repeat(257) },
    { publisherName: "x".repeat(161) },
    { namespaceName: "" },
    { licenseOmitted: false },
    { licenseOmitted: null },
    { license: "MIT", licenseOmitted: true },
    { secret: "private" },
  ])
    expect(checks.CatalogPresentation(value)).toBe(false);
});
