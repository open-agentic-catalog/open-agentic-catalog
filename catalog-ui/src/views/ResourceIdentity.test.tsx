// @vitest-environment jsdom
import { useState } from "react";
import { afterEach, beforeEach, expect, it, vi } from "vitest";
import { cleanup, fireEvent, render, screen } from "@testing-library/react";
import { ResourceIdentity } from "./ResourceIdentity";

beforeEach(() => {
  vi.stubGlobal("fetch", vi.fn());
});
afterEach(() => {
  cleanup();
  vi.restoreAllMocks();
  vi.unstubAllGlobals();
});

function Controlled({
  initial = "",
  itemName,
  disabled,
  changed,
}: {
  initial?: string;
  itemName?: string;
  disabled?: boolean;
  changed?: (value: string) => void;
}) {
  const [value, setValue] = useState(initial);
  return (
    <ResourceIdentity
      value={value}
      onChange={(next) => {
        changed?.(next);
        setValue(next);
      }}
      itemName={itemName}
      disabled={disabled}
    />
  );
}

function openBuilder() {
  fireEvent.click(screen.getByText("Build an AIR identifier", { exact: true }));
}

it("marks canonical errors after interaction and preserves opaque identifiers and optional builder drafts", () => {
  render(<Controlled />);
  const input = screen.getByLabelText(
    "Canonical resource identifier",
  ) as HTMLInputElement;
  expect(input.hasAttribute("aria-invalid")).toBe(false);
  fireEvent.invalid(input);
  expect(input.getAttribute("aria-invalid")).toBe("true");
  expect(input.validationMessage).toBe(
    "Enter a canonical resource identifier.",
  );
  fireEvent.change(input, { target: { value: "urn:air:localhost:agent" } });
  fireEvent.blur(input);
  expect(input.validationMessage).toContain("dotted publisher domain");
  fireEvent.change(input, { target: { value: "Private/Équipe 🙂" } });
  expect(input.hasAttribute("aria-invalid")).toBe(false);
  expect(input.validationMessage).toBe("");
  openBuilder();
  fields("unfinished", "", "");
  expect(input.validationMessage).toBe("");
  expect(input.value).toBe("Private/Équipe 🙂");
});

function fields(publisher: string, namespace: string, name: string) {
  fireEvent.change(screen.getByRole("textbox", { name: "Publisher domain" }), {
    target: { value: publisher },
  });
  fireEvent.change(
    screen.getByRole("textbox", {
      name: "AIR namespace segments (optional)",
    }),
    {
      target: { value: namespace },
    },
  );
  fireEvent.change(screen.getByRole("textbox", { name: "AIR resource name" }), {
    target: { value: name },
  });
}

it("keeps the legacy exact identifier input immediately available without a builder step", () => {
  const changed = vi.fn();
  render(<Controlled changed={changed} />);
  const input = screen.getByRole("textbox", {
    name: "Canonical resource identifier",
  });
  expect(input).toHaveProperty("required", true);
  expect(
    screen
      .getByText("Build an AIR identifier", { exact: true })
      .closest("details"),
  ).toHaveProperty("open", false);
  fireEvent.change(input, { target: { value: "  Private/Équipe 🙂  " } });
  expect(changed).toHaveBeenLastCalledWith("  Private/Équipe 🙂  ");
  expect(input).toHaveProperty("value", "  Private/Équipe 🙂  ");
  expect(fetch).not.toHaveBeenCalled();
});

it("previews normalized AIR identity locally and changes the exact input only on explicit use", () => {
  const changed = vi.fn();
  render(<Controlled changed={changed} />);
  openBuilder();
  fields("Example.COM.", "Team:agents", "Order_1.2");
  expect(screen.getByLabelText("AIR identifier preview").textContent).toBe(
    "urn:air:example.com:Team:agents:Order_1.2",
  );
  expect(changed).not.toHaveBeenCalled();
  expect(fetch).not.toHaveBeenCalled();
  fireEvent.click(screen.getByRole("button", { name: "Use AIR identifier" }));
  expect(changed).toHaveBeenCalledOnce();
  expect(changed).toHaveBeenCalledWith(
    "urn:air:example.com:Team:agents:Order_1.2",
  );
  expect(
    screen.getByRole("textbox", {
      name: "Canonical resource identifier",
    }),
  ).toHaveProperty("value", "urn:air:example.com:Team:agents:Order_1.2");
  expect(fetch).not.toHaveBeenCalled();
});

it("applies a name suggestion deliberately and permits a terminal-only identity", () => {
  const { rerender } = render(<Controlled itemName="Order Review" />);
  openBuilder();
  const name = screen.getByRole("textbox", {
    name: "AIR resource name",
  });
  expect(name).toHaveProperty("value", "");
  fireEvent.click(
    screen.getByRole("button", {
      name: "Use suggested name: order-review",
    }),
  );
  expect(name).toHaveProperty("value", "order-review");
  fireEvent.change(name, { target: { value: "My_Agent" } });
  rerender(<Controlled itemName="Different title" />);
  expect(name).toHaveProperty("value", "My_Agent");
  fireEvent.change(screen.getByRole("textbox", { name: "Publisher domain" }), {
    target: { value: "example.com" },
  });
  expect(screen.getByLabelText("AIR identifier preview").textContent).toBe(
    "urn:air:example.com:My_Agent",
  );
  fireEvent.click(screen.getByRole("button", { name: "Use AIR identifier" }));
  expect(
    screen.getByRole("textbox", {
      name: "Canonical resource identifier",
    }),
  ).toHaveProperty("value", "urn:air:example.com:My_Agent");
  expect(
    screen.getByText(/separate from the administrative Namespace/),
  ).toBeTruthy();
  expect(screen.getByText(/does not verify domain control/)).toBeTruthy();
  expect(fetch).not.toHaveBeenCalled();
});

it("asks before replacing direct identity and preserves it after cancelled replacement", () => {
  const confirm = vi
    .spyOn(window, "confirm")
    .mockReturnValueOnce(false)
    .mockReturnValue(true);
  const changed = vi.fn();
  render(<Controlled initial="Opaque-ID" changed={changed} />);
  openBuilder();
  fields("example.com", "", "agent");
  const apply = screen.getByRole("button", {
    name: "Use AIR identifier",
  });
  fireEvent.click(apply);
  expect(changed).not.toHaveBeenCalled();
  expect(
    screen.getByRole("textbox", {
      name: "Canonical resource identifier",
    }),
  ).toHaveProperty("value", "Opaque-ID");
  fireEvent.click(apply);
  expect(changed).toHaveBeenCalledWith("urn:air:example.com:agent");
  expect(confirm).toHaveBeenCalledTimes(2);
  fireEvent.click(apply);
  expect(confirm).toHaveBeenCalledTimes(2);
});

it("focuses invalid builder feedback without replacing a valid direct identity", () => {
  const changed = vi.fn();
  render(
    <Controlled
      initial="Opaque-ID"
      itemName="Example Agent"
      changed={changed}
    />,
  );
  openBuilder();
  expect(screen.getByLabelText("AIR identifier preview").textContent).toBe(
    "Complete the publisher domain and resource name.",
  );
  fireEvent.click(screen.getByRole("button", { name: "Use AIR identifier" }));
  const alert = screen.getByRole("alert");
  expect(alert.textContent).toContain("publisher domain");
  expect(document.activeElement).toBe(alert);
  expect(changed).not.toHaveBeenCalled();
  fields("example.com", "Team::agents", "agent");
  expect(screen.queryByRole("alert")).toBeNull();
  fireEvent.click(screen.getByRole("button", { name: "Use AIR identifier" }));
  expect(screen.getByRole("alert").textContent).toContain(
    "namespace segments and resource name",
  );
  expect(changed).not.toHaveBeenCalled();
  fireEvent.click(
    screen.getByRole("button", {
      name: "Use suggested name: example-agent",
    }),
  );
  expect(screen.queryByRole("alert")).toBeNull();
  expect(changed).not.toHaveBeenCalled();
});

it("keeps optional builder fields out of surrounding required-field validation", () => {
  render(
    <form aria-label="Parent draft">
      <Controlled initial="Opaque-ID" itemName="日本語" />
    </form>,
  );
  openBuilder();
  expect(
    (
      screen.getByRole("form", {
        name: "Parent draft",
      }) as HTMLFormElement
    ).checkValidity(),
  ).toBe(true);
  expect(
    screen.queryByRole("button", { name: /^Use suggested name/ }),
  ).toBeNull();
  for (const button of screen.getAllByRole("button"))
    expect(button).toHaveProperty("type", "button");
});

it("disables both direct input and local builder actions when the owning form is read-only", () => {
  const changed = vi.fn();
  render(
    <Controlled
      initial="Opaque-ID"
      itemName="Order Review"
      disabled
      changed={changed}
    />,
  );
  expect(
    screen.getByRole("textbox", {
      name: "Canonical resource identifier",
    }),
  ).toHaveProperty("disabled", true);
  openBuilder();
  for (const input of screen.getAllByRole("textbox"))
    expect(input).toHaveProperty("disabled", true);
  for (const button of screen.getAllByRole("button")) {
    expect(button).toHaveProperty("disabled", true);
    fireEvent.click(button);
  }
  expect(changed).not.toHaveBeenCalled();
  expect(fetch).not.toHaveBeenCalled();
});
