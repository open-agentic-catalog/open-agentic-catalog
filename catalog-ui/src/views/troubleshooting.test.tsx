// @vitest-environment jsdom
import { afterEach, expect, it, vi } from "vitest";
import {
  cleanup,
  act,
  fireEvent,
  render,
  screen,
  waitFor,
} from "@testing-library/react";
import Help from "./Help";
import { APIError } from "../api/client";
import {
  deferred,
  workspace,
  reply,
  processSample,
} from "../../tests/unit/fixtures";

afterEach(() => {
  cleanup();
  vi.unstubAllGlobals();
});
const apiBuild = {
  ...processSample,
  service: "open-agentic-catalog",
  role: "api",
  version: "1.2.3",
  commit: "abc123",
  sourceState: "clean",
  ardProfile: "ard",
  aiCatalogProfile: "ai",
};
const click = (name: string) =>
  fireEvent.click(screen.getByRole("button", { name }));

it.each(["", "/docs/", "https://manual.example.test/exact/"])(
  "keeps public troubleshooting independent with manual %s",
  (documentationUrl) => {
    const w = workspace();
    render(
      <Help
        {...w.props}
        runtime={{ ...w.props.runtime, documentationUrl }}
        page="troubleshooting"
        access={undefined}
      />,
    );
    expect(
      screen.getByRole("heading", { name: "Troubleshooting" }),
    ).toBeTruthy();
    expect(screen.getByText("A command has an uncertain outcome")).toBeTruthy();
    expect(
      screen.getByRole("link", { name: "Sign in" }).getAttribute("href"),
    ).toBe("/unit/catalog");
    expect(
      screen.queryByRole("button", { name: "Preview diagnostic details" }),
    ).toBeNull();
    if (documentationUrl)
      expect(
        screen
          .getByRole("link", { name: /manual|Documentation/ })
          .getAttribute("href"),
      ).toBe(
        documentationUrl === "/docs/"
          ? "/docs/guides/catalog-ui/#troubleshooting"
          : documentationUrl,
      );
    else
      expect(screen.getByText(/Documentation is not configured/)).toBeTruthy();
    expect(w.get).not.toHaveBeenCalled();
    expect(w.buildInfo).not.toHaveBeenCalled();
  },
);

it("previews only safe facts, copies without reads, and retains manual copy after denial", async () => {
  const w = workspace();
  w.get.mockResolvedValue(
    reply({ ...apiBuild, tenantId: "PRIVATE_TENANT", secret: "PRIVATE_TOKEN" }),
  );
  const writeText = vi.fn().mockResolvedValue(undefined);
  vi.stubGlobal("navigator", { clipboard: { writeText } });
  const view = render(
    <Help
      {...w.props}
      page="troubleshooting"
      recent={{
        status: 412,
        code: "PRECONDITION_FAILED",
        requestId: "response-1",
      }}
    />,
  );
  expect(w.get).not.toHaveBeenCalled();
  expect(
    screen.getByRole("link", { name: "My access" }).getAttribute("href"),
  ).toBe("/unit/access");
  for (const name of ["Preview diagnostic details"]) {
    const icon = screen.getByRole("button", { name }).querySelector("svg")!;
    expect(icon.getAttribute("aria-hidden")).toBe("true");
    expect(icon.getAttribute("focusable")).toBe("false");
  }
  click("Preview diagnostic details");
  await screen.findByRole("button", { name: "Copy diagnostic details" });
  expect(
    screen.queryByRole("button", { name: "Copy document text" }),
  ).toBeNull();
  for (const name of ["Close diagnostic preview", "Copy diagnostic details"]) {
    const icon = screen.getByRole("button", { name }).querySelector("svg")!;
    expect(icon.getAttribute("aria-hidden")).toBe("true");
    expect(icon.getAttribute("focusable")).toBe("false");
  }
  const reads = w.get.mock.calls.length;
  const uiReads = w.buildInfo.mock.calls.length;
  await act(async () => {
    click("Copy diagnostic details");
  });
  await screen.findByText("Copied.");
  const copied = String(writeText.mock.calls[0]?.[0]);
  expect(JSON.parse(copied)).toMatchObject({
    formatVersion: 1,
    apiBuild: { version: "1.2.3" },
    recentFailure: { requestId: "response-1" },
  });
  expect(copied).not.toMatch(/PRIVATE|tenantId|instanceId|uptimeSeconds/);
  expect(w.get).toHaveBeenCalledTimes(reads);
  expect(w.buildInfo).toHaveBeenCalledTimes(uiReads);
  writeText.mockRejectedValue(new Error("PRIVATE_CLIPBOARD"));
  await act(async () => {
    click("Copy diagnostic details");
  });
  await screen.findByText(/Copy unavailable/);
  expect(view.container.textContent).not.toContain("PRIVATE_CLIPBOARD");
  click("Close diagnostic preview");
  expect(
    screen.queryByRole("button", { name: "Copy diagnostic details" }),
  ).toBeNull();
  click("Preview diagnostic details");
  await screen.findByRole("button", { name: "Copy diagnostic details" });
  view.rerender(
    <Help {...w.props} access={undefined} page="troubleshooting" />,
  );
  expect(
    screen.queryByRole("button", { name: "Copy diagnostic details" }),
  ).toBeNull();
  expect(screen.queryByText("response-1")).toBeNull();
});

it("keeps an available UI build and safe response ID when the API and manual are unavailable", async () => {
  const w = workspace();
  w.get.mockRejectedValue(
    new APIError(503, "DEPENDENCY_UNAVAILABLE", "upstream-7"),
  );
  render(
    <Help
      {...w.props}
      page="troubleshooting"
      runtime={{ ...w.props.runtime, documentationUrl: "" }}
    />,
  );
  click("Preview diagnostic details");
  await screen.findByRole("button", { name: "Copy diagnostic details" });
  expect(
    screen.getByRole("group", { name: "JSON document" }).textContent,
  ).toContain("upstream-7");
  expect(
    screen.getByRole("group", { name: "JSON document" }).textContent,
  ).toContain("ui-unit");
  expect(w.props.onFailure).toHaveBeenCalledWith(expect.any(APIError));
});

it("fences delayed private diagnostics on identity replacement", async () => {
  const w = workspace();
  const pending = deferred<ReturnType<typeof reply>>();
  w.get.mockReturnValue(pending.promise);
  const view = render(<Help {...w.props} page="troubleshooting" />);
  click("Preview diagnostic details");
  await waitFor(() => expect(w.get).toHaveBeenCalledOnce());
  view.rerender(
    <Help
      {...w.props}
      access={{ ...w.props.access, membershipId: "another-member" }}
      page="troubleshooting"
    />,
  );
  pending.resolve(reply(apiBuild));
  await waitFor(() =>
    expect(
      screen.queryByRole("button", { name: "Copy diagnostic details" }),
    ).toBeNull(),
  );
  expect(screen.queryByRole("group", { name: "JSON document" })).toBeNull();
});
