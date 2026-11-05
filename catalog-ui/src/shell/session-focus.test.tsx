// @vitest-environment jsdom
import { afterEach, expect, it } from "vitest";
import { act, cleanup, render, screen, waitFor } from "@testing-library/react";
import { useSessionFocus } from "./useSessionFocus";

afterEach(cleanup);
function Harness({
  session,
  heading = true,
  tick = 0,
  request,
}: {
  session: boolean;
  heading?: boolean;
  tick?: number;
  request: { current: boolean };
}) {
  useSessionFocus(session, false, false, request);
  return (
    <>
      <button>Header</button>
      <main id="main">
        {heading && <h1>{session ? "Workspace" : "Sign in"}</h1>}
        {tick > 0 && <p>Loading workspace content...</p>}
        {session && <input aria-label="Private draft" />}
      </main>
    </>
  );
}

it("waits for a lazy heading, tolerates interim content and then gives it keyboard focus", async () => {
  const request = { current: false };
  const view = render(<Harness session={false} request={request} />);
  request.current = true;
  view.rerender(<Harness session heading={false} request={request} />);
  expect(document.activeElement).toBe(screen.getByRole("main"));
  await act(async () => {
    view.rerender(
      <Harness session heading={false} tick={1} request={request} />,
    );
  });
  await waitFor(() =>
    expect(document.activeElement).toBe(screen.getByRole("main")),
  );
  view.rerender(<Harness session request={request} />);
  await waitFor(() =>
    expect(document.activeElement).toBe(
      screen.getByRole("heading", { name: "Workspace" }),
    ),
  );
  expect(request.current).toBe(false);
});

it("does not reclaim focus that the user moved while a page was loading", async () => {
  const request = { current: false };
  const view = render(<Harness session={false} request={request} />);
  request.current = true;
  view.rerender(<Harness session heading={false} request={request} />);
  const header = screen.getByRole("button", { name: "Header" });
  header.focus();
  view.rerender(<Harness session request={request} />);
  await waitFor(() => expect(screen.getByRole("heading")).toBeTruthy());
  expect(document.activeElement).toBe(header);
});

it("replaces removed private focus but preserves a surviving header on automatic session loss", () => {
  const request = { current: false };
  const view = render(<Harness session request={request} />);
  screen.getByRole("textbox").focus();
  view.rerender(<Harness session={false} request={request} />);
  expect(document.activeElement).toBe(
    screen.getByRole("heading", { name: "Sign in" }),
  );
  view.rerender(<Harness session request={request} />);
  const header = screen.getByRole("button", { name: "Header" });
  header.focus();
  view.rerender(<Harness session={false} request={request} />);
  expect(document.activeElement).toBe(header);
});
