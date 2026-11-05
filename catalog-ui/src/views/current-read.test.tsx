// @vitest-environment jsdom
import { afterEach, expect, it, vi } from "vitest";
import {
  act,
  cleanup,
  fireEvent,
  render,
  renderHook,
  screen,
  waitFor,
} from "@testing-library/react";
import { CurrentRead, useCurrentRead } from "./current-read";
import { APIError } from "../api/client";

afterEach(cleanup);

it("retains known success and last-loaded metadata across failed explicit readback", async () => {
  const load = vi.fn().mockResolvedValue({ state: "active" });
  const failure = vi.fn();
  const { result } = renderHook(() => useCurrentRead("source", load, failure));
  await waitFor(() => expect(result.current.data).toEqual({ state: "active" }));
  act(() => result.current.acknowledge("Paused"));
  expect(result.current.stale).toBe(true);
  load.mockRejectedValueOnce(new APIError(503, "DEPENDENCY_UNAVAILABLE"));
  act(() => result.current.refresh());
  await waitFor(() => expect(failure).toHaveBeenCalledOnce());
  expect(result.current.data).toEqual({ state: "active" });
  expect(result.current.message).toBe("Paused");
  expect(result.current.stale).toBe(true);
  load.mockResolvedValueOnce({ state: "paused" });
  act(() => result.current.refresh());
  await waitFor(() => expect(result.current.stale).toBe(false));
  expect(result.current.data).toEqual({ state: "paused" });
  expect(result.current.message).toBe("Paused");
  expect(load).toHaveBeenCalledTimes(3);
});

it("does not mistake an in-flight read for readback after a command acknowledgement", async () => {
  let finish!: (value: string) => void;
  const load = vi
    .fn()
    .mockResolvedValueOnce("before")
    .mockImplementationOnce(
      () =>
        new Promise<string>((resolve) => {
          finish = resolve;
        }),
    )
    .mockResolvedValue("current");
  const { result } = renderHook(() => useCurrentRead("job", load, vi.fn()));
  await waitFor(() => expect(result.current.data).toBe("before"));
  act(() => result.current.refresh());
  await waitFor(() => expect(load).toHaveBeenCalledTimes(2));
  act(() => result.current.acknowledge("Cancellation requested"));
  await act(async () => finish("older observation"));
  expect(result.current.stale).toBe(true);
  act(() => result.current.refresh());
  await waitFor(() => expect(result.current.data).toBe("current"));
  expect(result.current.stale).toBe(false);
});

it("fences obsolete keys, hides their receipts and preserves native explicit reload", async () => {
  let finish!: (value: string) => void;
  const load = vi
    .fn()
    .mockImplementationOnce(
      () =>
        new Promise<string>((resolve) => {
          finish = resolve;
        }),
    )
    .mockResolvedValue("second");
  const { result, rerender } = renderHook(
    ({ id }) => useCurrentRead(id, load, vi.fn()),
    { initialProps: { id: "first" } },
  );
  act(() => result.current.acknowledge("Old target"));
  expect(result.current.stale).toBe(true);
  rerender({ id: "second" });
  await waitFor(() => expect(result.current.data).toBe("second"));
  await act(async () => finish("obsolete"));
  expect(result.current.data).toBe("second");
  expect(result.current.message).toBeUndefined();
  expect(result.current.stale).toBe(false);
  const refresh = vi.fn();
  render(
    <CurrentRead
      read={result.current}
      label="target"
      reload="Read current target"
      refresh={refresh}
    />,
  );
  expect(screen.getByText("Last loaded target")).toBeTruthy();
  fireEvent.click(screen.getByRole("button", { name: "Read current target" }));
  expect(refresh).toHaveBeenCalledOnce();
});

it("renders an acknowledgement without claiming a historical replay is current state", async () => {
  const load = vi.fn().mockResolvedValue("retained");
  function View() {
    const read = useCurrentRead("source", load, vi.fn());
    return (
      <>
        <CurrentRead read={read} label="source" reload="Reload source">
          <span>Exact onward reference</span>
        </CurrentRead>
        <button onClick={() => read.acknowledge("Command committed")}>
          Acknowledge
        </button>
      </>
    );
  }
  const view = render(<View />);
  await screen.findByText("Last loaded source");
  fireEvent.click(screen.getByRole("button", { name: "Acknowledge" }));
  expect(screen.getByText(/historical replay/)).toBeTruthy();
  expect(screen.getByText("Exact onward reference")).toBeTruthy();
  expect(screen.getByText(/not been reread/)).toBeTruthy();
  const reload = screen.getByRole("button", { name: "Reload source" });
  reload.focus();
  fireEvent.click(reload);
  await waitFor(() => expect(screen.queryByText(/not been reread/)).toBeNull());
  expect(document.activeElement).toBe(reload);
  view.rerender(<View />);
  await act(async () => {});
  expect(load).toHaveBeenCalledTimes(2);
  expect(document.activeElement).toBe(reload);
  const acknowledge = screen.getByRole("button", { name: "Acknowledge" });
  acknowledge.focus();
  fireEvent.click(acknowledge);
  expect(document.activeElement).toBe(screen.getByRole("status"));
});
