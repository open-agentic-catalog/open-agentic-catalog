// @vitest-environment jsdom
import { afterEach, expect, it, vi } from "vitest";
import {
  act,
  cleanup,
  fireEvent,
  render,
  screen,
} from "@testing-library/react";
import type { Client } from "../api/client";
import { deferred, reply } from "../../tests/unit/fixtures";
import { CategoryChoices } from "./CategoryChoices";

afterEach(cleanup);
const facet = (values: string[], extra = {}) => ({
  matched: 10,
  partialIndex: true,
  facets: {
    category: {
      buckets: values.map((value) => ({ value, count: 2 })),
      ...extra,
    },
  },
});
function setup(selected: string[] = []) {
  const post = vi.fn<(...args: unknown[]) => Promise<unknown>>();
  const props = {
    client: { api: { POST: post } } as unknown as Client,
    query: { text: "applied", tags: ["exact"] },
    identityKey: "reader-a",
    selected,
    onChange: vi.fn(),
    onFailure: vi.fn(),
  };
  return { post, props };
}

it("requests one bounded local facet, preserves selected labels and reports partial/truncated scope", async () => {
  const { post, props } = setup(["not in buckets", "data, tools"]);
  const categories = facet(["development", "data, tools"], { otherCount: 3 });
  categories.facets.category.buckets[0]!.count = 1;
  post.mockResolvedValue(reply(categories));
  const view = render(<CategoryChoices {...props} />);
  expect(screen.getByRole("status").textContent).toContain("Loading");
  const choice = await screen.findByRole("checkbox", { name: /development/ });
  const signal = (post.mock.calls[0]?.[1] as { signal: AbortSignal }).signal;
  expect(signal).toBeInstanceOf(AbortSignal);
  expect(post).toHaveBeenCalledExactlyOnceWith("/api/v1/catalog/explore", {
    body: {
      query: props.query,
      facets: [{ field: "category", limit: 24, minCount: 1 }],
    },
    signal,
  });
  expect(screen.getByText(/External results are not counted/)).toBeTruthy();
  expect(
    screen.getByText(/Some local records can match only by keywords/),
  ).toBeTruthy();
  expect(screen.getByLabelText("1 local match")).toHaveProperty(
    "textContent",
    "1",
  );
  expect(screen.getByLabelText("2 local matches")).toHaveProperty(
    "textContent",
    "2",
  );
  expect(screen.getByText(/More local categories/)).toBeTruthy();
  expect(
    screen.getByRole("checkbox", { name: "not in buckets" }),
  ).toHaveProperty("checked", true);
  fireEvent.click(choice);
  expect(props.onChange).toHaveBeenCalledExactlyOnceWith([
    "not in buckets",
    "data, tools",
    "development",
  ]);
  fireEvent.click(screen.getByRole("checkbox", { name: /data, tools/ }));
  expect(props.onChange).toHaveBeenLastCalledWith(["not in buckets"]);
  view.rerender(
    <CategoryChoices
      {...props}
      selected={["development"]}
      onFailure={vi.fn()}
    />,
  );
  expect(post).toHaveBeenCalledOnce();
});

it("allows selected removal at the limit and uses Unicode code points for new choices", async () => {
  const { post, props } = setup(
    Array.from({ length: 16 }, (_, n) => `chosen-${n}`),
  );
  const valid = "🙂".repeat(80),
    invalid = "🙂".repeat(81);
  post.mockResolvedValue(reply(facet([valid, invalid, "\0", " "])));
  const view = render(<CategoryChoices {...props} />);
  const choice = await screen.findByRole("checkbox", {
    name: valid,
  });
  expect(choice).toHaveProperty("disabled", true);
  fireEvent.click(screen.getByRole("checkbox", { name: "chosen-0" }));
  expect(props.onChange).toHaveBeenCalledWith(props.selected.slice(1));
  view.rerender(<CategoryChoices {...props} selected={[]} />);
  expect(choice).toHaveProperty("disabled", false);
  expect(
    screen.getByRole("checkbox", {
      name: invalid,
    }),
  ).toHaveProperty("disabled", true);
  expect(post).toHaveBeenCalledOnce();
});

it.each([
  { matched: 0, facets: {} },
  {
    matched: 0,
    facets: { category: { buckets: [{ value: "bad", count: "two" }] } },
  },
  undefined,
])(
  "keeps selected values editable after malformed facet data: %j",
  async (data) => {
    const { post, props } = setup(["saved"]);
    post.mockResolvedValue(reply(data));
    render(<CategoryChoices {...props} />);
    await screen.findByText(/suggestions are unavailable/);
    expect(props.onFailure).toHaveBeenCalledOnce();
    fireEvent.click(screen.getByRole("checkbox", { name: "saved" }));
    expect(props.onChange).toHaveBeenCalledWith([]);
  },
);

it("supports an empty facet and hides dependency error details", async () => {
  const { post, props } = setup();
  post
    .mockResolvedValueOnce(reply({ ...facet([]), partialIndex: false }))
    .mockRejectedValueOnce(new Error("private dependency details"));
  const view = render(<CategoryChoices {...props} />);
  await screen.findByText(/No local category suggestions/);
  expect(
    screen.queryByText(/Some local records can match only by keywords/),
  ).toBeNull();
  expect(post).toHaveBeenCalledOnce();
  view.rerender(<CategoryChoices {...props} query={{ text: "changed" }} />);
  await screen.findByText(/suggestions are unavailable/);
  expect(screen.queryByText(/private dependency details/)).toBeNull();
  expect(props.onFailure).toHaveBeenCalledOnce();
});

it("aborts and ignores old query, identity and client replies, including unmount failures", async () => {
  const { post, props } = setup();
  const old = deferred<ReturnType<typeof reply>>(),
    next = deferred<ReturnType<typeof reply>>();
  post
    .mockReturnValueOnce(old.promise)
    .mockReturnValueOnce(next.promise)
    .mockResolvedValueOnce(reply(facet(["current"])));
  const view = render(<CategoryChoices {...props} />);
  const signal = (post.mock.calls[0]?.[1] as { signal: AbortSignal }).signal;
  view.rerender(<CategoryChoices {...props} query={{ text: "new" }} />);
  expect(signal.aborted).toBe(true);
  await act(async () => old.resolve(reply(facet(["stale"]))));
  expect(screen.queryByRole("checkbox", { name: /stale/ })).toBeNull();
  view.rerender(
    <CategoryChoices
      {...props}
      query={{ text: "new" }}
      identityKey="reader-b"
    />,
  );
  await screen.findByRole("checkbox", { name: /current/ });
  await act(async () => next.reject(new Error("stale failure")));
  expect(props.onFailure).not.toHaveBeenCalled();
  const pending = deferred<ReturnType<typeof reply>>();
  const replacement = vi.fn().mockReturnValue(pending.promise);
  view.rerender(
    <CategoryChoices
      {...props}
      client={{ api: { POST: replacement } } as unknown as Client}
    />,
  );
  expect(screen.queryByRole("checkbox", { name: /current/ })).toBeNull();
  view.unmount();
  expect(
    (replacement.mock.calls[0]?.[1] as { signal: AbortSignal }).signal.aborted,
  ).toBe(true);
  await act(async () => pending.reject(new Error("late failure")));
  expect(props.onFailure).not.toHaveBeenCalled();
});
