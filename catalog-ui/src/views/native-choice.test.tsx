// @vitest-environment jsdom
// Controlled metadata choices preserve native values without network or service fixtures.
import { useState } from "react";
import { afterEach, expect, it, vi } from "vitest";
import {
  cleanup,
  fireEvent,
  render,
  screen,
  waitFor,
} from "@testing-library/react";
import { NativeChoice } from "./NativeChoice";
import { Field, RequiredFields } from "./workspace";

afterEach(cleanup);
const choices = [
  ["1.0", "1.0"],
  ["0.3", "0.3"],
] as const;
function Harness({ initial = "1.0", changed = vi.fn() }) {
  const [value, setValue] = useState(initial);
  return (
    <RequiredFields>
      <NativeChoice
        id="protocol"
        label="Protocol version"
        customLabel="Custom protocol version"
        value={value}
        choices={choices}
        validate={(next) => (/^\d+\.\d+$/.test(next) ? "" : "Use major.minor.")}
        onChange={(next) => {
          changed(next);
          setValue(next);
        }}
        hint="Choose the advertised protocol."
      />
      <output>{value}</output>
    </RequiredFields>
  );
}
it("keeps preset values native, marks required fields and focuses custom entry and a restored preset", async () => {
  const changed = vi.fn();
  render(<Harness changed={changed} />);
  const select = screen.getByRole<HTMLSelectElement>("combobox", {
    name: "Protocol version",
  });
  expect(select.getAttribute("aria-describedby")).toBeTruthy();
  expect(
    select.closest("label")!.querySelector('[aria-hidden="true"]')!.textContent,
  ).toContain("*");
  fireEvent.change(select, { target: { value: "0.3" } });
  expect(changed).toHaveBeenLastCalledWith("0.3");
  fireEvent.change(select, { target: { value: "__custom__" } });
  const custom = screen.getByRole<HTMLInputElement>("textbox", {
    name: "Custom protocol version",
  });
  await waitFor(() => expect(document.activeElement).toBe(custom));
  expect(custom.checkValidity()).toBe(false);
  expect(custom.getAttribute("aria-invalid")).toBe("true");
  fireEvent.change(custom, { target: { value: "3.1" } });
  expect(custom.checkValidity()).toBe(true);
  expect(changed).toHaveBeenLastCalledWith("3.1");
  fireEvent.change(custom, { target: { value: "1.0" } });
  await waitFor(() => expect(document.activeElement).toBe(select));
  expect(changed).toHaveBeenLastCalledWith("1.0");
  expect(screen.queryByRole("textbox")).toBeNull();
  expect(select.checkValidity()).toBe(true);
});
it("preserves imported custom values, reports invalid input and leaves unrelated Field hints unchanged", () => {
  render(
    <>
      <Harness initial="vendor-1" />
      <Field label="Outside">
        <input required />
      </Field>
    </>,
  );
  const custom = screen.getByLabelText<HTMLInputElement>(
    "Custom protocol version",
  );
  expect(custom).toHaveProperty("value", "vendor-1");
  expect(screen.queryByText("Use major.minor.")).toBeNull();
  fireEvent.focus(custom);
  expect(screen.getByText("Use major.minor.")).toBeTruthy();
  fireEvent.blur(custom);
  fireEvent.change(custom, { target: { value: "" } });
  fireEvent.invalid(custom);
  expect(custom.checkValidity()).toBe(false);
  const outside = screen.getByLabelText("Outside");
  expect(outside.closest(".workspace-field")!.textContent).toContain(
    "Required",
  );
  expect(
    outside.closest("label")!.querySelector(".required-marker"),
  ).toBeNull();
  fireEvent.blur(screen.getByRole("combobox"));
  fireEvent.invalid(screen.getByRole("combobox"));
});

it("keeps empty validation space out of the accessible name and exposes an error only when supplied", () => {
  const view = render(
    <RequiredFields>
      <Field label="Name" error="">
        <input id="name" required />
      </Field>
    </RequiredFields>,
  );
  const input = screen.getByRole("textbox", { name: "Name" });
  expect(input.getAttribute("aria-describedby")).toBeNull();
  expect(screen.queryByText("Missing name.")).toBeNull();
  view.rerender(
    <RequiredFields>
      <Field label="Name" error="Missing name.">
        <input id="name" required />
      </Field>
    </RequiredFields>,
  );
  expect(screen.getByRole("textbox", { name: "Name" })).toBe(input);
  expect(input.getAttribute("aria-invalid")).toBe("true");
  expect(
    document.getElementById(input.getAttribute("aria-describedby")!)!
      .textContent,
  ).toBe("Missing name.");
});
