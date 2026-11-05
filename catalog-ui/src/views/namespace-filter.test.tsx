// @vitest-environment jsdom
import { expect, it } from "vitest";
import {
  render,
  screen,
  fireEvent,
  waitFor,
  act,
} from "@testing-library/react";
import { NamespaceFilter } from "./NamespaceFilter";
import {
  namespace,
  screenLifecycle,
  screenWorkspace,
} from "../../tests/unit/screens";
import { reply, deferred } from "../../tests/unit/fixtures";
screenLifecycle("/unit/catalog");
it("loads only explicit bounded pages and retains an off-page selection", async () => {
  const { props, get } = screenWorkspace();
  get
    .mockResolvedValueOnce(reply({ items: [namespace], nextCursor: "next" }))
    .mockResolvedValueOnce(reply({ items: [] }))
    .mockResolvedValueOnce(reply({ items: [namespace] }));
  const changes: string[] = [];
  render(
    <NamespaceFilter
      client={props.client}
      value="selected"
      onFailure={props.onFailure}
      onChange={(value) => {
        changes.push(value ?? "");
      }}
    />,
  );
  expect(get).not.toHaveBeenCalled();
  fireEvent.click(screen.getByRole("button", { name: "Load namespaces" }));
  await screen.findByRole("option", { name: "Tools" });
  expect(get.mock.calls[0]?.[1]).toMatchObject({
    params: { query: { limit: 25 } },
  });
  fireEvent.change(screen.getByLabelText("Local namespace"), {
    target: { value: namespace.id },
  });
  expect(changes).toEqual([namespace.id]);
  fireEvent.click(screen.getByRole("button", { name: "Next namespace page" }));
  await waitFor(() =>
    expect(screen.queryByRole("option", { name: "Tools" })).toBeNull(),
  );
  expect(get.mock.calls[1]?.[1]).toMatchObject({
    params: { query: { limit: 25, cursor: "next" } },
  });
  expect(screen.getByRole("option", { name: "selected" })).toBeTruthy();
  fireEvent.change(screen.getByLabelText("Local namespace"), {
    target: { value: "" },
  });
  expect(changes).toEqual([namespace.id, ""]);
  fireEvent.click(screen.getByRole("button", { name: "First namespace page" }));
  await screen.findByRole("option", { name: "Tools" });
  expect(get).toHaveBeenCalledTimes(3);
  expect(get.mock.calls[2]?.[1]).toMatchObject({
    params: { query: { limit: 25 } },
  });
  expect(get.mock.calls[2]?.[1]).not.toHaveProperty("params.query.cursor");
});
it("reports malformed choices and permits an explicit retry", async () => {
  const { props, get } = screenWorkspace();
  get
    .mockResolvedValueOnce(reply({ items: [{ id: "bad" }] }))
    .mockResolvedValueOnce(reply({ items: [] }));
  render(
    <NamespaceFilter
      client={props.client}
      value={undefined}
      onFailure={props.onFailure}
      onChange={() => {}}
    />,
  );
  fireEvent.click(screen.getByRole("button", { name: "Load namespaces" }));
  await screen.findByRole("alert");
  expect(props.onFailure).toHaveBeenCalledOnce();
  fireEvent.click(screen.getByRole("button", { name: "Load namespaces" }));
  await waitFor(() => expect(screen.queryByRole("alert")).toBeNull());
  expect(get).toHaveBeenCalledTimes(2);
});
it("keeps the explicitly loaded selected name on later pages without an extra read", async () => {
  const { props, get } = screenWorkspace();
  get
    .mockResolvedValueOnce(reply({ items: [namespace], nextCursor: "next" }))
    .mockResolvedValueOnce(reply({ items: [] }));
  const choices: unknown[] = [];
  render(
    <NamespaceFilter
      client={props.client}
      value={namespace.id}
      onFailure={props.onFailure}
      onChange={() => {}}
      onChoices={(items) => choices.push(items)}
    />,
  );
  expect(screen.getByRole("option", { name: namespace.id })).toBeTruthy();
  expect(get).not.toHaveBeenCalled();
  fireEvent.click(screen.getByRole("button", { name: "Load namespaces" }));
  await screen.findByRole("option", { name: namespace.displayName });
  fireEvent.click(screen.getByRole("button", { name: "Next namespace page" }));
  await waitFor(() => expect(choices).toEqual([[namespace], []]));
  expect(
    screen.getByRole("option", { name: namespace.displayName }),
  ).toHaveProperty("selected", true);
  expect(screen.getByLabelText("Local namespace")).toHaveProperty(
    "value",
    namespace.id,
  );
  expect(get).toHaveBeenCalledTimes(2);
});
it.each(["success", "failure"])(
  "ignores late %s after aborting an unmounted namespace read",
  async (outcome) => {
    const { props, get } = screenWorkspace();
    const pending = deferred<ReturnType<typeof reply>>();
    get.mockReturnValueOnce(pending.promise);
    const view = render(
      <NamespaceFilter
        client={props.client}
        value={undefined}
        onFailure={props.onFailure}
        onChange={() => {}}
      />,
    );
    fireEvent.click(screen.getByRole("button", { name: "Load namespaces" }));
    const signal = (get.mock.calls[0]?.[1] as { signal: AbortSignal }).signal;
    view.unmount();
    expect(signal.aborted).toBe(true);
    await act(async () => {
      if (outcome === "success") pending.resolve(reply({ items: [namespace] }));
      else pending.reject(new Error("superseded read failed"));
    });
    expect(props.onFailure).not.toHaveBeenCalled();
  },
);
