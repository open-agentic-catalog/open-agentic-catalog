// @vitest-environment jsdom
import { expect, it } from "vitest";
import { fireEvent, render, screen, within } from "@testing-library/react";
import type { Kind } from "../api/client";
import { NativeAuthoringHelp } from "./NativeAuthoringHelp";
import { screenLifecycle } from "../../tests/unit/screens";

screenLifecycle("/unit/resources");
const cases: [Kind, string, string][] = [
  ["agent", "create-an-agent-from-fields", "A2A Agent Card"],
  ["mcp_server", "create-mcp-metadata-from-fields", "MCP Registry server.json"],
  ["skill", "write-a-skill-from-fields", "SKILL.md"],
  ["plugin", "prepare-plugin-files-from-fields", "plugin.json"],
];
it.each(cases)(
  "gives %s a maintained guide and honest local preparation guidance",
  (kind, anchor, text) => {
    render(<NativeAuthoringHelp kind={kind} documentationUrl="/docs/" />);
    const help = screen.getByRole("region", { name: "Native input guidance" });
    const summary = screen
      .getByText("Prepare native content:")
      .closest("summary")!;
    const link = within(help).getByRole("link");
    expect(summary.closest("details")?.open).toBe(false);
    expect(within(summary).queryByRole("link")).toBeNull();
    expect(link.closest("details")).toBeNull();
    fireEvent.click(screen.getByText("Prepare native content:"));
    expect(help.textContent).toContain(text);
    expect(help.textContent).toContain("must match Version label");
    expect(help.textContent).toContain("validation findings");
    expect(help.textContent).toContain("Listing display metadata");
    expect(help.textContent).toContain(
      "Listing details before the final confirmation",
    );
    expect(link.getAttribute("href")).toBe(
      `/docs/guides/resource-authoring/#${anchor}`,
    );
    expect(link.getAttribute("target")).toBe("_blank");
    expect(link.getAttribute("rel")).toBe("noopener noreferrer");
    expect(link.textContent).toContain("opens in a new tab");
    if (kind === "mcp_server") {
      expect(help.textContent).toContain(
        "Registry metadata alone does not supply ARD",
      );
      expect(help.textContent).toContain(
        "primary experimental MCP Server Card",
      );
      expect(help.textContent).toContain("does not convert");
    }
    if (kind === "skill")
      expect(help.textContent).toContain("Git commit or OCI digest");
    if (kind === "plugin")
      expect(help.textContent).toContain(
        "manifests and archive uploads are not supported",
      );
    else expect(help.textContent).toContain("only after you confirm");
  },
);
it.each(cases)("preserves the exact external override for %s", (kind) => {
  const url = "https://manual.example.test/custom%20path/#";
  render(<NativeAuthoringHelp kind={kind} documentationUrl={url} />);
  const link = screen.getByRole("link");
  expect(link.getAttribute("href")).toBe(url);
  expect(link.getAttribute("target")).toBe("_blank");
  expect(link.getAttribute("rel")).toBe("noopener noreferrer");
  expect(link.closest("details")).toBeNull();
});
it.each(cases)(
  "keeps %s inline guidance when documentation is disabled",
  (kind, _anchor, text) => {
    render(<NativeAuthoringHelp kind={kind} documentationUrl="" />);
    expect(screen.queryByRole("link")).toBeNull();
    expect(
      screen.getByRole("region", { name: "Native input guidance" }).textContent,
    ).toContain(text);
  },
);
it("keeps inline guidance without a link for an inadmissible documentation destination", () => {
  render(
    <NativeAuthoringHelp
      kind="agent"
      documentationUrl="http://manual.example.test/"
    />,
  );
  expect(screen.queryByRole("link")).toBeNull();
  expect(
    screen.getByRole("region", { name: "Native input guidance" }).textContent,
  ).toContain("A2A Agent Card");
});
