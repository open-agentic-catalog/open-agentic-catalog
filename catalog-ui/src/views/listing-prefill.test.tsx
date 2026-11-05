import { required } from "../../tests/required";
// @vitest-environment jsdom
import {
  evidence,
  versionRevision,
  representation,
} from "../../tests/unit/responses";
// Prefill tests substitute only transport and preserve explicit reads, selection and session checks.
import { afterEach, expect, it, vi } from "vitest";
import {
  act,
  cleanup,
  fireEvent,
  render,
  screen,
} from "@testing-library/react";
import { ListingPrefill, listingSuggestion } from "./ListingPrefill";
import { makeAPI } from "../api/client";
import { deferred, workspace } from "../../tests/unit/fixtures";

afterEach(() => {
  cleanup();
  vi.restoreAllMocks();
  vi.unstubAllGlobals();
});

const bundle = {
  namespaceId: "namespace-a",
  evidence: { ...evidence, state: "complete" },
  revision: {
    ...versionRevision,
    id: "revision-a",
    representations: [
      {
        ...representation,
        id: "both",
        role: "primary",
        mediaType: "application/json",
        validationState: "valid",
        normalizedFacts: {
          name: "From document",
          description: "Document description",
          publisherId: "never-copy",
          visibility: "public",
        },
      },
      {
        ...representation,
        id: "name",
        role: "metadata",
        mediaType: "application/json",
        validationState: "valid",
        normalizedFacts: { name: "Name only" },
      },
      {
        ...representation,
        id: "description",
        role: "documentation",
        mediaType: "text/markdown",
        validationState: "valid",
        normalizedFacts: { description: "Description only" },
      },
      {
        ...representation,
        id: "empty",
        role: "metadata",
        mediaType: "application/json",
        validationState: "valid",
      },
      {
        ...representation,
        id: "invalid",
        role: "metadata",
        mediaType: "application/json",
        validationState: "invalid",
        normalizedFacts: { name: "Do not offer" },
      },
    ],
  },
};

/** Create one explicit evidence-read response and independently observable apply callback. */
function setup() {
  const { props, get } = workspace();
  get.mockResolvedValue({ data: bundle, response: new Response() });
  return {
    get,
    props: {
      client: props.client,
      onFailure: props.onFailure,
      revisionID: "revision-a",
      name: "",
      description: "",
      onApply: vi.fn(),
    },
  };
}

/** Load server facts and choose one representation without applying or saving it. */
async function choose(id: string) {
  fireEvent.click(
    screen.getByRole("button", {
      name: "Load name and description suggestions",
    }),
  );
  await screen.findByLabelText("Prefill representation");
  fireEvent.change(screen.getByLabelText("Prefill representation"), {
    target: { value: id },
  });
}

it("offers only bounded normalized strings without truncation, guessing or copying other fields", () => {
  expect(listingSuggestion()).toEqual({
    name: undefined,
    description: undefined,
  });
  expect(listingSuggestion({ name: " ", description: 42 })).toEqual({
    name: undefined,
    description: undefined,
  });
  expect(
    listingSuggestion({ name: "n".repeat(161), description: "d".repeat(4001) }),
  ).toEqual({ name: undefined, description: undefined });
  expect(
    listingSuggestion({
      name: "n".repeat(160),
      description: "d".repeat(4000),
      visibility: "public",
    }),
  ).toEqual({ name: "n".repeat(160), description: "d".repeat(4000) });
});

it("requires a revision, an explicit read and selection, then applies only the displayed fields", async () => {
  const { props, get } = setup();
  const view = render(<ListingPrefill {...props} revisionID="" />);
  expect(screen.getByRole("button")).toHaveProperty("disabled", true);
  expect(get).not.toHaveBeenCalled();
  view.rerender(<ListingPrefill {...props} />);
  await choose("both");
  expect(get).toHaveBeenCalledExactlyOnceWith(
    "/api/v1/version-revisions/{revisionId}",
    expect.objectContaining({ params: { path: { revisionId: "revision-a" } } }),
  );
  expect(screen.getByText("revision-a")).toBeTruthy();
  expect(screen.getByText("From document")).toBeTruthy();
  expect(screen.queryByRole("option", { name: /invalid/ })).toBeNull();
  expect(props.onApply).not.toHaveBeenCalled();
  fireEvent.click(
    screen.getByRole("button", { name: "Use suggested name and description" }),
  );
  expect(props.onApply).toHaveBeenCalledExactlyOnceWith({
    name: "From document",
    description: "Document description",
  });
  fireEvent.change(screen.getByLabelText("Prefill representation"), {
    target: { value: "empty" },
  });
  expect(
    screen.getByRole("button", { name: "Use suggested name and description" }),
  ).toHaveProperty("disabled", true);
});

it.each(["name", "description"])(
  "protects existing %s text until replacement is confirmed",
  async (field) => {
    const { props } = setup();
    const confirm = vi
      .spyOn(window, "confirm")
      .mockReturnValueOnce(false)
      .mockReturnValue(true);
    render(
      <ListingPrefill
        {...props}
        name="Existing name"
        description="Existing description"
      />,
    );
    await choose(field);
    const apply = screen.getByRole("button", {
      name: "Use suggested name and description",
    });
    fireEvent.click(apply);
    expect(props.onApply).not.toHaveBeenCalled();
    fireEvent.click(apply);
    expect(props.onApply).toHaveBeenCalledExactlyOnceWith(
      field === "name"
        ? { name: "Name only", description: undefined }
        : { name: undefined, description: "Description only" },
    );
    expect(confirm).toHaveBeenCalledTimes(2);
  },
);

it.each(["blocked", "invalid"])(
  "does not offer %s validation evidence",
  async (state) => {
    const { props, get } = setup();
    get.mockResolvedValue({
      response: new Response(),
      data: {
        ...bundle,
        evidence: {
          ...evidence,
          state: state === "blocked" ? "blocked" : "complete",
        },
        revision: {
          ...versionRevision,
          ...bundle.revision,
          representations:
            state === "blocked"
              ? bundle.revision.representations
              : bundle.revision.representations.filter(
                  (r) => r.validationState === "invalid",
                ),
        },
      },
    });
    render(<ListingPrefill {...props} />);
    fireEvent.click(screen.getByRole("button"));
    await screen.findByText(/No validated representation/);
    expect(screen.queryByLabelText("Prefill representation")).toBeNull();
    expect(props.onApply).not.toHaveBeenCalled();
  },
);

it("clears failed reads, exposes only safe errors and supports explicit retry", async () => {
  const { props, get } = setup();
  get
    .mockResolvedValueOnce({ response: new Response(null, { status: 403 }) })
    .mockRejectedValueOnce(new Error("private backend message"));
  render(<ListingPrefill {...props} />);
  fireEvent.click(screen.getByRole("button"));
  await screen.findByText("Your current access does not permit this action.");
  fireEvent.click(screen.getByRole("button"));
  await screen.findByText("Could not load current suggestions. Try again.");
  expect(screen.queryByText(/private backend/)).toBeNull();
  await choose("name");
  expect(screen.queryByRole("alert")).toBeNull();
  expect(props.onFailure).toHaveBeenCalledTimes(2);
});

it.each(["name", "description"])(
  "protects %s edited during an evidence read and leaves an identical suggestion alone",
  async (field) => {
    const { props, get } = setup();
    const pending = deferred<unknown>();
    const confirm = vi.spyOn(window, "confirm").mockReturnValue(false);
    get.mockReturnValueOnce(pending.promise);
    const view = render(<ListingPrefill {...props} />);
    fireEvent.click(screen.getByRole("button"));
    view.rerender(
      <ListingPrefill {...props} {...{ [field]: "Typed during loading" }} />,
    );
    await act(async () => {
      pending.resolve({ data: bundle, response: new Response() });
    });
    fireEvent.change(screen.getByLabelText("Prefill representation"), {
      target: { value: field },
    });
    fireEvent.click(
      screen.getByRole("button", {
        name: "Use suggested name and description",
      }),
    );
    expect(confirm).toHaveBeenCalledOnce();
    expect(props.onApply).not.toHaveBeenCalled();
    view.rerender(
      <ListingPrefill
        {...props}
        name="Name only"
        description="Description only"
      />,
    );
    fireEvent.click(
      screen.getByRole("button", {
        name: "Use suggested name and description",
      }),
    );
    expect(confirm).toHaveBeenCalledOnce();
    expect(props.onApply).toHaveBeenCalledExactlyOnceWith(
      field === "name"
        ? { name: "Name only", description: undefined }
        : { name: undefined, description: "Description only" },
    );
  },
);

it.each(["resolve", "reject"])(
  "discards a late %s after changing the selected revision or unmounting",
  async (outcome) => {
    const { props, get } = setup();
    const pending = deferred<unknown>();
    get.mockReturnValueOnce(pending.promise);
    const view = render(<ListingPrefill {...props} />);
    fireEvent.click(screen.getByRole("button"));
    expect(screen.getByRole("status").textContent).toBe(
      "Loading suggestions...",
    );
    const signal = (required(get.mock.calls[0])[1] as { signal: AbortSignal })
      .signal;
    view.rerender(<ListingPrefill {...props} revisionID="revision-b" />);
    expect(signal.aborted).toBe(true);
    await act(async () => {
      if (outcome === "resolve")
        pending.resolve({ data: bundle, response: new Response() });
      else pending.reject(new Error("late"));
    });
    expect(screen.queryByLabelText("Prefill representation")).toBeNull();
    expect(screen.queryByRole("alert")).toBeNull();
    const removed = deferred<unknown>();
    get.mockReturnValueOnce(removed.promise);
    fireEvent.click(screen.getByRole("button"));
    view.unmount();
    await act(async () => {
      if (outcome === "resolve")
        removed.resolve({ data: bundle, response: new Response() });
      else removed.reject(new Error("removed"));
    });
    expect(props.onFailure).not.toHaveBeenCalled();
  },
);

it("refuses to apply cached suggestions after the session is invalidated", async () => {
  const { props } = setup();
  vi.stubGlobal(
    "fetch",
    vi.fn().mockResolvedValue(
      new Response(JSON.stringify(bundle), {
        headers: { "Content-Type": "application/json" },
      }),
    ),
  );
  const client = makeAPI({
    basePath: "/unit",
    authMode: "development",
    documentationUrl: "",
  });
  render(<ListingPrefill {...props} client={client} />);
  await choose("both");
  client.invalidate();
  fireEvent.click(
    screen.getByRole("button", { name: "Use suggested name and description" }),
  );
  expect(props.onApply).not.toHaveBeenCalled();
  expect(props.onFailure).toHaveBeenCalledWith(
    expect.objectContaining({ name: "AbortError" }),
  );
  expect(screen.queryByLabelText("Prefill representation")).toBeNull();
});

it("offers full schema-length supplementary suggestions, without truncation or NUL", () => {
  const facts = { name: "🙂".repeat(160), description: "🙂".repeat(4000) };
  expect(listingSuggestion(facts)).toEqual(facts);
  expect(
    listingSuggestion({
      name: facts.name + "a",
      description: facts.description + "a",
    }),
  ).toEqual({ name: undefined, description: undefined });
  expect(listingSuggestion({ name: "a\0b" }).name).toBeUndefined();
});
