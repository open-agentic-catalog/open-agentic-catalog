// @vitest-environment jsdom
import * as React from "react";
import { createRef, isValidElement } from "react";
import { cleanup, fireEvent, render, screen } from "@testing-library/react";
import { afterEach, expect, it, vi } from "vitest";
import * as bridge from "./react-runtime";

const { Fragment, createElement } = bridge;
afterEach(cleanup);

it("retains the public React runtime bindings without wrapping them", () => {
  const names = [
    "Component",
    "Fragment",
    "Suspense",
    "cloneElement",
    "createContext",
    "createElement",
    "isValidElement",
    "lazy",
    "useCallback",
    "useContext",
    "useEffect",
    "useId",
    "useLayoutEffect",
    "useMemo",
    "useRef",
    "useState",
  ] as const;
  expect(Object.keys(bridge).sort()).toEqual([...names].sort());
  for (const name of names) expect(bridge[name]).toBe(React[name]);
});

it("renders compiler-style keyed children with exact text, refs and callbacks", () => {
  const ref = createRef<HTMLButtonElement>();
  const onClick = vi.fn();
  const action = createElement(
    "button",
    { ref, onClick, key: "action" },
    "Choose A & B 🚀",
  );
  expect(isValidElement(action)).toBe(true);
  expect(action.key).toBe("action");
  render(
    createElement(
      Fragment,
      null,
      createElement("h1", { key: "heading" }, "Runtime bridge"),
      action,
    ),
  );
  expect(
    screen.getByRole("heading", { name: "Runtime bridge" }).textContent,
  ).toBe("Runtime bridge");
  const button = screen.getByRole("button", { name: "Choose A & B 🚀" });
  expect(ref.current).toBe(button);
  fireEvent.click(button);
  expect(onClick).toHaveBeenCalledTimes(1);
});
