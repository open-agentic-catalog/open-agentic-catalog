// @vitest-environment jsdom
import { afterEach, expect, it } from "vitest";
import { cleanup, render, screen, within } from "@testing-library/react";
import { workspace, reply, processSample } from "../../tests/unit/fixtures";
import { testAccess } from "../../tests/unit/access-fixtures";
import Help from "./Help";

afterEach(cleanup);

it("links the bundled local password guide without reading private API data", () => {
  const { props, get } = workspace();
  render(
    <Help
      {...props}
      runtime={{ ...props.runtime, documentationUrl: "/docs/" }}
      access={undefined}
      page="getting-started"
    />,
  );
  expect(
    screen
      .getByRole("link", { name: /Set up local users and passwords/ })
      .getAttribute("href"),
  ).toBe(
    "/docs/operations/oidc-keycloak/#start-one-local-catalog-with-passwords",
  );
  expect(get).not.toHaveBeenCalled();
});

it("offers public About and exact legal links without any build/API calls", () => {
  const { props, get, buildInfo } = workspace();
  render(<Help {...props} access={undefined} page="about" />);
  expect(screen.getByText("Apache License 2.0")).toBeTruthy();
  expect(screen.getAllByText("Author: Mihai Criveti")).toHaveLength(1);
  expect(
    screen.getByRole("link", { name: "Project license" }).getAttribute("href"),
  ).toBe("/unit/legal/LICENSE.txt");
  expect(
    screen
      .getByRole("link", { name: "Browser third-party notices" })
      .getAttribute("href"),
  ).toBe("/unit/legal/third-party-notices.txt");
  expect(
    screen.queryByRole("heading", { name: "Connected builds" }),
  ).toBeNull();
  for (const label of ["Agents", "MCP Servers", "Skills", "Plugins"]) {
    const icon = screen
      .getByText(label, { exact: true })
      .closest("li")!
      .querySelector("svg")!;
    expect(icon.getAttribute("aria-hidden")).toBe("true");
    expect(icon.getAttribute("focusable")).toBe("false");
  }
  expect(get).not.toHaveBeenCalled();
  expect(buildInfo).not.toHaveBeenCalled();
});

it("shows actual authenticated builds and retains static About through dependency failure", async () => {
  const { props, get } = workspace();
  get.mockResolvedValue(
    reply({
      ...processSample,
      service: "open-agentic-catalog",
      role: "api",
      version: "9.8.7",
      commit: "abc123",
      sourceState: "modified",
      ardProfile: "test",
      aiCatalogProfile: "test",
    }),
  );
  const view = render(<Help {...props} page="about" />);
  await screen.findByText("9.8.7");
  await screen.findByText("ui-unit");
  expect(
    screen
      .getByRole("link", { name: "Service information" })
      .getAttribute("href"),
  ).toBe("/unit/service");
  get.mockRejectedValue(new Error("private dependency"));
  view.unmount();
  render(<Help {...props} page="about" />);
  await screen.findByRole("alert");
  expect(screen.getByText("Apache License 2.0")).toBeTruthy();
  expect(document.body.textContent).not.toContain("private dependency");
});

it.each(["", "/docs/", "https://manual.example.test/exact/"])(
  "keeps public orientation inert with documentation %s",
  (documentationUrl) => {
    const { props, get } = workspace();
    render(
      <Help
        {...props}
        runtime={{ ...props.runtime, documentationUrl }}
        access={undefined}
        page="getting-started"
      />,
    );
    expect(
      screen.getByRole("heading", { name: "Getting started" }),
    ).toBeTruthy();
    expect(screen.queryByRole("link", { name: "Open Catalog" })).toBeNull();
    if (documentationUrl) {
      expect(
        screen
          .getByRole("link", { name: /Read the UI workflow guide/ })
          .getAttribute("href"),
      ).toBe(
        documentationUrl === "/docs/"
          ? "/docs/guides/catalog-ui/"
          : documentationUrl,
      );
      const manual = screen.getByRole("link", {
        name: /Read the UI workflow guide/,
      });
      expect(manual.getAttribute("target")).toBe("_blank");
      expect(manual.getAttribute("rel")).toBe("noopener noreferrer");
      expect(manual.querySelector("svg")?.getAttribute("aria-hidden")).toBe(
        "true",
      );
      expect(manual.querySelector("svg")?.getAttribute("focusable")).toBe(
        "false",
      );
    } else
      expect(
        screen.getByText(/not configured a documentation destination/),
      ).toBeTruthy();
    expect(get).not.toHaveBeenCalled();
  },
);

it.each([
  "reader",
  "contributor",
  "reviewer",
  "publisher",
  "tenant_admin",
] as const)("gates Getting started entry points for %s", (role) => {
  const { props } = workspace();
  const access = { ...props.access, ...testAccess([role]) };
  render(<Help {...props} access={access} page="getting-started" />);
  for (const link of screen.queryAllByRole("link")) {
    expect(link.querySelector("svg")?.getAttribute("aria-hidden")).toBe("true");
    expect(link.querySelector("svg")?.getAttribute("focusable")).toBe("false");
  }
  const contribution = screen
    .getByRole("heading", { name: "Contribute a resource" })
    .closest("section")!;
  expect(
    within(contribution).queryByRole("link", { name: "Open Authoring" }) !==
      null,
  ).toBe(role === "contributor" || role === "tenant_admin");
  const review = screen
    .getByRole("heading", { name: "Review a submission" })
    .closest("section")!;
  expect(
    within(review).queryByRole("link", { name: "Open Reviews" }) !== null,
  ).toBe(role === "reviewer" || role === "tenant_admin");
  const publication = screen
    .getByRole("heading", { name: "Publish an approved release" })
    .closest("section")!;
  expect(within(publication).queryByRole("link") !== null).toBe(
    role === "publisher" || role === "tenant_admin",
  );
});

it("preserves create-only and scoped author navigation while enforcing OAuth ceilings", () => {
  const { props } = workspace();
  const access = {
    ...props.access,
    permissions: [],
    availablePermissions: [
      "resource.create",
    ] as typeof props.access.availablePermissions,
    scopeEnforcement: true,
    scopes: ["catalog.write"] as typeof props.access.scopes,
  };
  const view = render(
    <Help {...props} access={access} page="getting-started" />,
  );
  expect(screen.getByRole("link", { name: "Open Authoring" })).toBeTruthy();
  view.rerender(
    <Help
      {...props}
      access={{
        ...access,
        availablePermissions: ["resource.read", "resource.edit"],
        scopes: ["catalog.read", "catalog.write"],
      }}
      page="getting-started"
    />,
  );
  expect(screen.getByRole("link", { name: "Open Authoring" })).toBeTruthy();
  view.rerender(
    <Help
      {...props}
      access={{ ...access, scopes: [] }}
      page="getting-started"
    />,
  );
  expect(screen.queryByRole("link", { name: "Open Authoring" })).toBeNull();
});
