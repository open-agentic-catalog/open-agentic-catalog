// @vitest-environment jsdom
import { afterEach, expect, it, vi } from "vitest";
import { cleanup, fireEvent, render, screen } from "@testing-library/react";
import { CodeBlock, JSONBlock } from "./CodeBlock";

afterEach(() => {
  cleanup();
  vi.unstubAllGlobals();
});

it.each([false, true])(
  "copies exact opted-in raw text regardless of formatting: %s",
  async (original) => {
    const text = ' {"same":1, "same":9007199254740993123456789}\r\n';
    const writeText = vi.fn().mockResolvedValue(undefined);
    vi.stubGlobal("navigator", { clipboard: { writeText } });
    const view = render(<CodeBlock text={text} rawText original={original} />);
    fireEvent.click(screen.getByRole("button", { name: "Format JSON" }));
    fireEvent.click(screen.getByRole("button", { name: "Copy document text" }));
    await screen.findByText("Copied.");
    expect(writeText).toHaveBeenCalledExactlyOnceWith(text);
    writeText.mockRejectedValue(new Error("private clipboard failure"));
    fireEvent.click(screen.getByRole("button", { name: "Copy document text" }));
    await screen.findByText(/Copy unavailable/);
    expect(view.container.textContent).not.toContain(
      "private clipboard failure",
    );
    view.rerender(<CodeBlock text={text} original />);
    expect(
      screen.queryByRole("button", { name: "Copy document text" }),
    ).toBeNull();
    view.rerender(<JSONBlock value={{ raw: "decoded" }} />);
    expect(
      screen.queryByRole("button", { name: "Copy document text" }),
    ).toBeNull();
  },
);

it.each([2 * 1024 * 1024, 2 * 1024 * 1024 + 1])(
  "bounds raw clipboard bytes at %s",
  (size) => {
    render(
      <CodeBlock
        text={
          "\u00e9".repeat(1024 * 1024) + (size > 2 * 1024 * 1024 ? "x" : "")
        }
        rawText
      />,
    );
    expect(
      screen.getByRole("button", { name: "Copy document text" }),
    ).toHaveProperty("disabled", size > 2 * 1024 * 1024);
  },
);

it("toggles display formatting and wrapping without changing exact input", () => {
  const source = '{ "same":1, "same":9007199254740993 }';
  const view = render(<CodeBlock text={source} original />);
  const content = screen.getByRole("group", { name: "JSON document" });
  expect(content.tabIndex).toBe(0);
  expect(content.textContent).toBe(source);
  fireEvent.click(screen.getByRole("button", { name: "Format JSON" }));
  expect(content.textContent).toContain("\n");
  expect(content.textContent).toContain("9007199254740993");
  fireEvent.click(screen.getByRole("button", { name: "Format JSON" }));
  expect(content.textContent).toBe(source);
  fireEvent.click(screen.getByRole("button", { name: "Wrap lines" }));
  expect(content.getAttribute("data-wrap")).toBe("false");
  fireEvent.click(screen.getByRole("button", { name: "Wrap lines" }));
  expect(content.getAttribute("data-wrap")).toBe("true");
  view.rerender(<CodeBlock text="<script>unsafe()</script>" />);
  expect(screen.queryByRole("button", { name: "Format JSON" })).toBeNull();
  const plain = screen.getByRole("group", { name: "Text document" });
  expect(plain.tabIndex).toBe(0);
  expect(plain.textContent).toBe("<script>unsafe()</script>");
  expect(view.container.querySelector("script")).toBeNull();
});

it("renders undefined JSON as empty text and replaces content when values change", () => {
  const view = render(<JSONBlock value={undefined} />);
  expect(screen.getByRole("group", { name: "Text document" }).textContent).toBe(
    "",
  );
  view.rerender(<JSONBlock value={{ fresh: true }} />);
  expect(screen.getByRole("group", { name: "JSON document" }).textContent).toBe(
    '{\n  "fresh": true\n}',
  );
});
