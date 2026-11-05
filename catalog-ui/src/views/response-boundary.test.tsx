// @vitest-environment jsdom
// Inert fetch replies exercise the real client, decoders, screen and command recovery together.
import { afterEach, expect, it, vi } from "vitest";
import {
  act,
  fireEvent,
  render,
  screen,
  waitFor,
  within,
} from "@testing-library/react";
import { makeAPI } from "../api/client";
import Namespaces from "./Namespaces";
import {
  administrator,
  change,
  click,
  confirmCommand,
  namespace,
  screenLifecycle,
  screenWorkspace,
} from "../../tests/unit/screens";

screenLifecycle("/unit/namespaces");
afterEach(() => vi.unstubAllGlobals());
it.each([
  null,
  { items: null },
  { items: [{ ...namespace, displayName: { private: "sentinel" } }] },
  { items: [], nextCursor: [] },
  "invalid JSON",
])(
  "keeps a malformed page out of live state and supports explicit recovery: %j",
  async (value) => {
    const { props } = screenWorkspace();
    const fetcher = vi
      .fn()
      .mockResolvedValueOnce(
        value === "invalid JSON"
          ? new Response("private parse sentinel")
          : Response.json(value),
      )
      .mockImplementation(async () =>
        Response.json({ items: [namespace], nextCursor: "opaque+=/%" }),
      );
    vi.stubGlobal("fetch", fetcher);
    render(<Namespaces {...props} client={makeAPI(props.runtime)} />);
    const alert = await screen.findByRole("alert");
    expect(alert.textContent).not.toContain("sentinel");
    expect(
      screen.queryByRole("link", {
        name: namespace.displayName,
      }),
    ).toBeNull();
    expect(screen.queryByText("No namespaces on this page.")).toBeNull();
    expect(fetcher).toHaveBeenCalledTimes(1);
    click("Reload");
    await screen.findByRole("link", {
      name: namespace.displayName,
    });
    click("Next page");
    await waitFor(() => expect(fetcher).toHaveBeenCalledTimes(3));
    const request = fetcher.mock.calls[2]![0] as Request;
    expect(new URL(request.url).searchParams.get("cursor")).toBe("opaque+=/%");
    await screen.findByRole("link", {
      name: namespace.displayName,
    });
    expect(screen.queryByRole("alert")).toBeNull();
  },
);
it.each(["shape", "syntax"])(
  "preserves a prepared write after malformed %s success and retries exact inputs",
  async (kind) => {
    const { props } = screenWorkspace();
    const requests: {
      url: string;
      body: string;
      key: string | null;
      etag: string | null;
    }[] = [];
    const transport = vi.fn(async (request: Request) => {
      requests.push({
        url: request.url,
        body: await request.text(),
        key: request.headers.get("Idempotency-Key"),
        etag: request.headers.get("If-Match"),
      });
      if (request.method === "GET")
        return Response.json(namespace, { headers: { ETag: '"original"' } });
      if (requests.length === 2)
        return new Response(
          kind === "shape" ? '{"id":null}' : '{"private":"parse sentinel"',
          { status: 200 },
        );
      return Response.json({
        ...namespace,
        displayName: "Changed tools",
        revision: 2,
      });
    });
    vi.stubGlobal("fetch", transport);
    render(
      <Namespaces
        {...props}
        id={namespace.id}
        access={administrator}
        client={makeAPI(props.runtime)}
      />,
    );
    await screen.findByLabelText("Display name");
    change("Display name", "Changed tools");
    change("Reason", "Approved rename");
    await confirmCommand("Review changes");
    const dialog = await screen.findByRole("dialog");
    expect(within(dialog).getByRole("alert").textContent).toContain(
      "outcome is unknown",
    );
    expect(within(dialog).getByRole("alert").textContent).not.toContain(
      "parse sentinel",
    );
    expect(
      (
        within(dialog).getByRole("button", {
          name: "Back to form",
        }) as HTMLButtonElement
      ).disabled,
    ).toBe(true);
    expect(requests).toHaveLength(2);
    await act(async () => {});
    expect(requests).toHaveLength(2);
    fireEvent.click(
      within(dialog).getByRole("button", { name: "Retry same command" }),
    );
    await screen.findByText(/Namespace updated/);
    expect(requests).toHaveLength(3);
    expect(requests[2]).toEqual(requests[1]);
    expect(requests[1]!.key).toBeTruthy();
    expect(requests[1]).toMatchObject({
      etag: '"original"',
      body: JSON.stringify({
        displayName: "Changed tools",
        description: "",
        reason: "Approved rename",
      }),
    });
  },
);
