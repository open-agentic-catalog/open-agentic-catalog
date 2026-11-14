// @vitest-environment jsdom
import { cleanup, fireEvent, render, screen } from "@testing-library/react";
import { afterEach, expect, it, vi } from "vitest";
import { useState } from "react";
import type { SearchResult } from "../api/client";
import { DeclaredToolMatch } from "./DeclaredToolMatch";
import {
  RepresentationList,
  newRepresentation,
  representationInput,
} from "./representations";

afterEach(cleanup);

it("renders bounded matching text without treating declarations as links or markup", () => {
  const inspect = vi.fn();
  const item = {
    local: {},
    toolMatch: {
      representationId: "r",
      contentDigest: "sha256:d",
      matchedToolCount: 4,
      truncated: true,
      tools: [
        {
          name: "<script>pdf</script>",
          description: "<img src=x onerror=alert(1)>",
          descriptionTruncated: false,
        },
        {
          name: "second_match",
          description: "λ".repeat(256),
          descriptionTruncated: true,
        },
        { name: "third_match", descriptionTruncated: false },
      ],
    },
  } as SearchResult;
  render(<DeclaredToolMatch item={item} onInspect={inspect} />);
  expect(screen.getByText("<script>pdf</script>")).not.toBeNull();
  expect(screen.getByText("<img src=x onerror=alert(1)>")).not.toBeNull();
  expect(screen.getByText("Showing the first 3 matches.")).not.toBeNull();
  expect(document.querySelector("img,script,a")).toBeNull();
  fireEvent.click(
    screen.getByRole("button", { name: "Inspect released declaration" }),
  );
  expect(inspect).toHaveBeenCalledOnce();
});

it("adds exactly one private JSON tool attachment without replacing the primary MCP document", async () => {
  const primary = {
    ...newRepresentation("mcp_server"),
    content: '{"name":"original"}',
  };
  let current = [primary];
  function Editor() {
    const [rows, setRows] = useState(current);
    current = rows;
    return (
      <RepresentationList kind="mcp_server" value={rows} onChange={setRows} />
    );
  }
  render(<Editor />);
  fireEvent.click(screen.getByRole("button", { name: "Add declared tools" }));
  expect(current).toHaveLength(2);
  expect(current[0]).toEqual(primary);
  expect(
    (
      screen.getByRole("button", {
        name: "Add declared tools",
      }) as HTMLButtonElement
    ).disabled,
  ).toBe(true);
  const content = await screen.findByLabelText("Declared tools (JSON)");
  fireEvent.change(content, {
    target: { value: '{"tools":[{"name":"extract_pdf_tables"}]}' },
  });
  expect(representationInput(current[1]!)).toEqual({
    role: "metadata",
    mediaType: "application/vnd.open-agentic-catalog.mcp-tools+json",
    profileId: "open-agentic-catalog-mcp-tools-1",
    data: { tools: [{ name: "extract_pdf_tables" }] },
  });
});
