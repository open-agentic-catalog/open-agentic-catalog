// @vitest-environment jsdom
import { afterEach, expect, it, vi } from "vitest";
import {
  cleanup,
  fireEvent,
  render,
  screen,
  within,
} from "@testing-library/react";
import { SignInChoices } from "./SignInChoices";
import type { LoginOptions } from "../api/session";

afterEach(cleanup);
const users: LoginOptions["users"] = [
  {
    alias: "admin",
    label: "Management workflow",
    description: "Explicit elevated setup",
    profileGroup: "bundle",
    elevated: true,
    separateTenant: true,
  },
  {
    alias: "reader",
    label: "Trusted reader",
    description: "Read private local records",
    profileGroup: "bundle",
  },
  {
    alias: "library",
    label: "Saved items",
    profileGroup: "template",
    elevated: false,
    separateTenant: false,
  },
  {
    alias: "operator-choice",
    label: "<script>configured</script>",
    description: "Operator-supplied choice",
  },
];

it("groups configured intent, preserves literal descriptions and signs in only on explicit choice", () => {
  const select = vi.fn();
  render(<SignInChoices users={users} busy={false} onSelect={select} />);
  expect(screen.getByRole("status").textContent).toBe(
    "Showing 4 of 4 identities.",
  );
  expect(
    within(
      screen.getByRole("region", { name: "Development bundles" }),
    ).getAllByRole("button"),
  ).toHaveLength(2);
  expect(
    screen.getByRole("heading", { name: "Exact templates (1)" }),
  ).toBeTruthy();
  const admin = screen.getByRole("button", { name: "Management workflow" });
  expect(admin.getAttribute("aria-describedby")).toBe(
    "login-description-admin login-elevated-admin login-tenant-admin",
  );
  expect(screen.getByText("Elevated administration")).toBeTruthy();
  expect(screen.getByText("Separate tenant")).toBeTruthy();
  expect(screen.getByText(/not production accounts or passwords/)).toBeTruthy();
  const custom = screen.getByRole("button", {
    name: "<script>configured</script>",
  });
  expect(custom.querySelector("script")).toBeNull();
  expect(
    screen
      .getByRole("button", { name: "Saved items" })
      .getAttribute("aria-describedby"),
  ).toBeNull();
  expect(select).not.toHaveBeenCalled();
  fireEvent.click(admin);
  expect(select).toHaveBeenCalledExactlyOnceWith("admin");
});

it("filters labels, descriptions and exact aliases, shows counts and restores all choices", () => {
  const select = vi.fn();
  render(<SignInChoices users={users} busy={false} onSelect={select} />);
  const search = screen.getByRole("searchbox", {
    name: "Find a development identity",
  });
  fireEvent.change(search, { target: { value: "  TRUSTED  " } });
  expect(screen.getByRole("status").textContent).toBe(
    "Showing 1 of 4 identities.",
  );
  expect(
    screen.getByRole("heading", { name: "Development bundles (1 of 2)" }),
  ).toBeTruthy();
  expect(screen.queryByRole("region", { name: "Exact templates" })).toBeNull();
  fireEvent.change(search, { target: { value: "private local" } });
  expect(screen.getByRole("button", { name: "Trusted reader" })).toBeTruthy();
  fireEvent.change(search, { target: { value: "operator-choice" } });
  fireEvent.click(
    screen.getByRole("button", { name: "<script>configured</script>" }),
  );
  expect(select).toHaveBeenCalledExactlyOnceWith("operator-choice");
  fireEvent.change(search, { target: { value: "missing identity" } });
  expect(screen.getByText(/No matching development identity/)).toBeTruthy();
  expect(screen.getByRole("status").textContent).toBe(
    "Showing 0 of 4 identities.",
  );
  const clear = screen.getByRole("button", { name: "Clear identity search" });
  expect(clear.querySelector("svg")?.getAttribute("aria-hidden")).toBe("true");
  expect(clear.querySelector("svg")?.getAttribute("focusable")).toBe("false");
  fireEvent.click(clear);
  expect(screen.getByRole("status").textContent).toBe(
    "Showing 4 of 4 identities.",
  );
  expect(
    screen.queryByRole("button", { name: "Clear identity search" }),
  ).toBeNull();
});

it("disables selection and search during login without losing the filter", () => {
  const props = { users, busy: false, onSelect: vi.fn() };
  const view = render(<SignInChoices {...props} />);
  fireEvent.change(screen.getByRole("searchbox"), {
    target: { value: "reader" },
  });
  view.rerender(<SignInChoices {...props} busy />);
  expect(screen.getByRole("searchbox").hasAttribute("disabled")).toBe(true);
  for (const button of screen.getAllByRole("button"))
    expect(button.hasAttribute("disabled")).toBe(true);
  expect((screen.getByRole("searchbox") as HTMLInputElement).value).toBe(
    "reader",
  );
});

it("does not invent options for an empty roster", () => {
  const { container } = render(
    <SignInChoices users={[]} busy={false} onSelect={vi.fn()} />,
  );
  expect(container.childElementCount).toBe(0);
});
