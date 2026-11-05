// @vitest-environment jsdom
import { useState } from "react";
import { afterEach, expect, it } from "vitest";
import {
  cleanup,
  fireEvent,
  render,
  screen,
  within,
} from "@testing-library/react";
import {
  RelationshipEditor,
  relationshipDraft,
  type RelationshipDraft,
} from "./listing-metadata";

afterEach(cleanup);

// Model the owning editor's controlled state, without replacing its controls.
function Editor({ initial = [] }: { initial?: RelationshipDraft[] }) {
  const [value, setValue] = useState(initial);
  return <RelationshipEditor value={value} onChange={setValue} />;
}

it("edits only the selected row and preserves the other row when types and versions change", () => {
  const first = relationshipDraft({
    type: "depends_on",
    targetIdentifier: "urn:air:example.com:agents:first",
    targetVersion: "1.0",
  });
  render(<Editor initial={[first]} />);
  fireEvent.click(screen.getByRole("button", { name: "Add relationship" }));
  const second = within(screen.getByRole("group", { name: "Relationship 2" }));
  const type = second.getByLabelText("Relationship type");
  expect(document.activeElement).toBe(type);
  fireEvent.change(type, { target: { value: "supersedes" } });
  const target = second.getByLabelText("Target identifier") as HTMLInputElement;
  const version = second.getByLabelText(
    "Target version (required)",
  ) as HTMLInputElement;
  expect(target.required).toBe(true);
  expect(target.maxLength).toBe(1024);
  expect(version.required).toBe(true);
  expect(version.maxLength).toBe(256);
  expect(version.checkValidity()).toBe(false);
  fireEvent.change(target, {
    target: { value: "urn:air:example.com:agents:second" },
  });
  fireEvent.change(version, { target: { value: "2.0" } });
  expect(version.checkValidity()).toBe(true);
  const original = within(
    screen.getByRole("group", { name: "Relationship 1" }),
  );
  expect(
    (original.getByLabelText("Relationship type") as HTMLSelectElement).value,
  ).toBe("depends_on");
  expect(
    (original.getByLabelText("Target identifier") as HTMLInputElement).value,
  ).toBe(first.targetIdentifier);
  expect(
    (original.getByLabelText("Target version (optional)") as HTMLInputElement)
      .value,
  ).toBe("1.0");
  fireEvent.change(type, { target: { value: "recommended_with" } });
  expect(second.queryByLabelText("Target version (required)")).toBeNull();
  expect(version.required).toBe(false);
  expect(version.value).toBe("2.0");
});

it("removes rows by identity, retaining entered values and a useful focus target", () => {
  render(
    <Editor
      initial={["first", "second", "third"].map((name) =>
        relationshipDraft({
          type: "bundled_with",
          targetIdentifier: `urn:air:example.com:agents:${name}`,
        }),
      )}
    />,
  );
  fireEvent.click(
    screen.getByRole("button", { name: "Remove relationship 2" }),
  );
  const remaining = screen.getAllByLabelText(
    "Target identifier",
  ) as HTMLInputElement[];
  expect(remaining.map((input) => input.value)).toEqual([
    "urn:air:example.com:agents:first",
    "urn:air:example.com:agents:third",
  ]);
  const last = within(screen.getByRole("group", { name: "Relationship 2" }));
  expect(document.activeElement).toBe(last.getByLabelText("Relationship type"));
  fireEvent.click(last.getByRole("button", { name: "Remove relationship 2" }));
  expect(document.activeElement).toBe(
    screen.getByLabelText("Relationship type"),
  );
  fireEvent.click(
    screen.getByRole("button", { name: "Remove relationship 1" }),
  );
  expect(screen.getByText("No relationships added.")).toBeTruthy();
  expect(document.activeElement).toBe(
    screen.getByRole("button", { name: "Add relationship" }),
  );
  fireEvent.click(screen.getByRole("button", { name: "Add relationship" }));
  expect(
    (screen.getByLabelText("Target identifier") as HTMLInputElement).value,
  ).toBe("");
  expect(document.activeElement).toBe(
    screen.getByLabelText("Relationship type"),
  );
});

it("enforces the add limit and allows replacing a removed relationship without truncating others", () => {
  render(
    <Editor
      initial={Array.from({ length: 64 }, (_, index) =>
        relationshipDraft({
          type: "depends_on",
          targetIdentifier: `urn:air:example.com:agents:item-${index}`,
        }),
      )}
    />,
  );
  const add = screen.getByRole("button", {
    name: "Add relationship",
  }) as HTMLButtonElement;
  expect(add.disabled).toBe(true);
  fireEvent.click(add);
  expect(screen.getAllByLabelText("Target identifier")).toHaveLength(64);
  fireEvent.click(
    screen.getByRole("button", { name: "Remove relationship 64" }),
  );
  expect(add.disabled).toBe(false);
  fireEvent.click(add);
  const targets = screen.getAllByLabelText(
    "Target identifier",
  ) as HTMLInputElement[];
  expect(targets.map((input) => input.value)).toEqual([
    ...Array.from(
      { length: 63 },
      (_, index) => `urn:air:example.com:agents:item-${index}`,
    ),
    "",
  ]);
  expect(add.disabled).toBe(true);
});
