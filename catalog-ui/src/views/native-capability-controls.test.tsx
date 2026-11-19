// @vitest-environment jsdom
import { afterEach, expect, it, vi } from "vitest";
import { cleanup, fireEvent, render, screen } from "@testing-library/react";
import { NativeCapabilityFilters } from "./NativeCapabilityFilters";
import {
  nativeArrayFields,
  type NativeCapabilities,
} from "./native-capabilities";

afterEach(cleanup);
it("edits each literal independently and removes the last selector instead of retaining an empty object", () => {
  const changed = vi.fn();
  let value: NativeCapabilities | undefined;
  const view = render(
    <NativeCapabilityFilters prefix="saved" value={value} onChange={changed} />,
  );
  for (const [field, , label] of nativeArrayFields) {
    const details = screen
      .getByText(
        field.startsWith("agent")
          ? "Agent declarations"
          : "MCP server declarations",
        { exact: true },
      )
      .closest("details")!;
    details.open = true;
    fireEvent.click(
      screen.getByRole("button", { name: `Add ${label.toLowerCase()}` }),
    );
    expect(changed).toHaveBeenLastCalledWith({ [field]: [""] });
    value = { [field]: [""] };
    view.rerender(
      <NativeCapabilityFilters
        prefix="saved"
        value={value}
        onChange={changed}
      />,
    );
    fireEvent.change(screen.getByLabelText(`${label} 1`, { exact: true }), {
      target: { value: "literal, value " },
    });
    expect(changed).toHaveBeenLastCalledWith({ [field]: ["literal, value "] });
    fireEvent.click(
      screen.getByRole("button", { name: `Remove ${label.toLowerCase()} 1` }),
    );
    expect(changed).toHaveBeenLastCalledWith(undefined);
    value = undefined;
    view.rerender(
      <NativeCapabilityFilters
        prefix="saved"
        value={value}
        onChange={changed}
      />,
    );
  }
  for (const option of ["true", "false", ""]) {
    fireEvent.change(screen.getByLabelText("Agent streaming declaration"), {
      target: { value: option },
    });
    expect(changed).toHaveBeenLastCalledWith(
      option === "" ? undefined : { agentStreaming: option === "true" },
    );
  }
  view.rerender(
    <NativeCapabilityFilters
      value={{ agentStreaming: false }}
      onChange={changed}
    />,
  );
  fireEvent.click(
    screen.getByRole("button", { name: "Clear declared capabilities" }),
  );
  expect(changed).toHaveBeenLastCalledWith(undefined);
});

it("edits saved native criteria in place, including exact false and local scope, without discovery requests", async () => {
  const { PersonalCriteriaEditor } = await import("./PersonalCriteriaEditor");
  const changed = vi.fn<(value: string) => void>();
  const initial = { text: "invoices", tags: ["kept"] };
  const view = render(
    <PersonalCriteriaEditor
      id="saved-native"
      value={JSON.stringify(initial)}
      onChange={changed}
    />,
  );
  fireEvent.click(screen.getByText("More saved filters", { exact: true }));
  fireEvent.click(screen.getByText("Agent declarations", { exact: true }));
  fireEvent.change(screen.getByLabelText("Agent streaming declaration"), {
    target: { value: "false" },
  });
  const next: unknown = JSON.parse(changed.mock.calls.at(-1)![0]);
  expect(next).toEqual({
    ...initial,
    origins: ["local"],
    nativeCapabilities: { agentStreaming: false },
  });
  view.rerender(
    <PersonalCriteriaEditor
      id="saved-native"
      value={JSON.stringify(next)}
      onChange={changed}
    />,
  );
  fireEvent.click(
    screen.getByRole("button", { name: "Clear declared capabilities" }),
  );
  expect(JSON.parse(changed.mock.calls.at(-1)![0])).toEqual({
    ...initial,
    origins: ["local"],
  });
});
