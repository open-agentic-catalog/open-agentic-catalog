// @vitest-environment jsdom
import { cleanup, fireEvent, render, screen } from "@testing-library/react";
import { afterEach, expect, it } from "vitest";
import { compareTableText, usePageSort } from "./table-sort";

afterEach(cleanup);

const values = Object.freeze(["Tool 10", "tool 2", "TOOL 2", "Agent"]);
function Page({ items }: { items?: readonly string[] }) {
  const sorted = usePageSort(items, { name: compareTableText });
  return (
    <table>
      <thead>
        <tr>{sorted.heading("Name", "name")}</tr>
      </thead>
      <tbody>
        {sorted.rows.map((name) => (
          <tr key={name}>
            <td>{name}</td>
          </tr>
        ))}
      </tbody>
    </table>
  );
}
const rows = () =>
  screen.queryAllByRole("cell").map((cell) => cell.textContent);

it("preserves server order initially, toggles natural text sorting and keeps ties stable without changing the response", () => {
  render(<Page items={values} />);
  expect(rows()).toEqual(["Tool 10", "tool 2", "TOOL 2", "Agent"]);
  expect(screen.getByRole("columnheader").getAttribute("aria-sort")).toBe(
    "none",
  );
  fireEvent.click(
    screen.getByRole("button", { name: "Sort by name, ascending" }),
  );
  expect(rows()).toEqual(["Agent", "tool 2", "TOOL 2", "Tool 10"]);
  const direction = screen.getByRole("columnheader").querySelector("svg");
  expect(direction?.getAttribute("aria-hidden")).toBe("true");
  expect(direction?.parentElement?.style.transform).toBe("rotate(-90deg)");
  expect(screen.getByRole("columnheader").getAttribute("aria-sort")).toBe(
    "ascending",
  );
  fireEvent.click(
    screen.getByRole("button", { name: "Sort by name, descending" }),
  );
  expect(rows()).toEqual(["Tool 10", "tool 2", "TOOL 2", "Agent"]);
  expect(screen.getByRole("columnheader").getAttribute("aria-sort")).toBe(
    "descending",
  );
  expect(
    screen.getByRole("columnheader").querySelector("svg")?.parentElement?.style
      .transform,
  ).toBe("rotate(90deg)");
  expect(values).toEqual(["Tool 10", "tool 2", "TOOL 2", "Agent"]);
});

it("retains the selected sort across loading, empty and replacement pages without retaining old rows", () => {
  const view = render(<Page />);
  expect(rows()).toEqual([]);
  fireEvent.click(
    screen.getByRole("button", { name: "Sort by name, ascending" }),
  );
  view.rerender(<Page items={values} />);
  expect(rows()).toEqual(["Agent", "tool 2", "TOOL 2", "Tool 10"]);
  view.rerender(<Page items={[]} />);
  expect(rows()).toEqual([]);
  view.rerender(<Page items={["Zebra", "Beta"]} />);
  expect(rows()).toEqual(["Beta", "Zebra"]);
  view.rerender(<Page />);
  expect(rows()).toEqual([]);
});
