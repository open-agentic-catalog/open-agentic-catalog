// @vitest-environment jsdom
import { afterEach, expect, it, vi } from "vitest";
import { screen } from "@testing-library/dom";

afterEach(() => {
  vi.useRealTimers();
  document.body.replaceChildren();
});

it("waits for a control that appears after the default one-second query window", async () => {
  vi.useFakeTimers();
  const control = document.createElement("input");
  control.setAttribute("aria-label", "Delayed control");
  setTimeout(() => document.body.append(control), 1500);
  const waiting = screen
    .findByRole("textbox", { name: "Delayed control" })
    .then(
      (element) => ({ state: "ready", element }),
      (error: unknown) => ({ state: "failed", error }),
    );
  await vi.advanceTimersByTimeAsync(1500);
  expect(await waiting).toEqual({ state: "ready", element: control });
});

it("still rejects a missing control at the bounded async-query deadline", async () => {
  vi.useFakeTimers();
  let settled = false;
  const waiting = screen.findByRole("button", { name: "Missing control" }).then(
    () => {
      settled = true;
      return { state: "unexpectedly ready" };
    },
    (error: unknown) => {
      settled = true;
      return { state: "failed", error };
    },
  );
  await vi.advanceTimersByTimeAsync(4999);
  expect(settled).toBe(false);
  await vi.advanceTimersByTimeAsync(1);
  expect(settled).toBe(true);
  const outcome = await waiting;
  expect(outcome).toHaveProperty("state", "failed");
  expect(outcome).toHaveProperty("error.name", "TestingLibraryElementError");
});
