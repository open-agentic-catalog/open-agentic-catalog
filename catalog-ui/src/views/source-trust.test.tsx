// @vitest-environment jsdom
import {
  act,
  fireEvent,
  render,
  screen,
  waitFor,
  within,
} from "@testing-library/react";
import { expect, it, vi } from "vitest";
import { APIError } from "../api/client";
import type { Access } from "../api/client";
import type { components } from "../api/schema";
import { SourceTrust, LiveTrust } from "./SourceTrust";
import Federation from "./Federation";
import { FederationSearch } from "./FederationSearch";
import {
  eligibleObservation,
  indexedTrustSelection,
  liveTrustSelection,
} from "./trust-selection";
import { trustPolicyName } from "./federation-inputs";
import { job } from "../../tests/unit/responses";
import { deferred } from "../../tests/unit/fixtures";
import {
  administrator,
  change,
  click,
  confirmCommand,
  etagReply,
  screenLifecycle,
  screenWorkspace,
  stamp,
} from "../../tests/unit/screens";

type Schema = components["schemas"];
screenLifecycle("/unit/sources");
const source: Schema["FederationSource"] = {
  id: "00000000-0000-7000-8000-000000000001",
  tenantId: administrator.tenantId,
  revision: 7,
  displayName: "Registration peer",
  endpoint: "https://peer.invalid/ard/",
  status: "active",
  profileId: "ard",
  retrievalMode: "hybrid",
  updateMode: "manual_review",
  type: "hybrid",
  policyId: "default",
  policyRevision: 1,
  policyDigest: "sha256:" + "b".repeat(64),
  createdBy: administrator.subject,
  scope: { kinds: ["agent"] },
  live: { enabled: true },
  referrals: {},
  nesting: {},
  trustVerificationPolicy: "operator-ans",
  lastCompleteSnapshotId: "00000000-0000-7000-8000-000000000002",
  createdAt: stamp,
  updatedAt: stamp,
};
const digest = `sha256:${"a".repeat(64)}`;
const entry = {
  identifier: "urn:air:peer.invalid:agents:helper",
  type: "application/a2a-agent-card+json",
  data: { name: "Helper" },
};
const observation: Schema["Observation"] = {
  id: "00000000-0000-7000-8000-000000000003",
  tenantId: administrator.tenantId,
  sourceId: source.id,
  sourceKey: "helper",
  identifier: entry.identifier,
  versionPresent: false,
  mediaType: entry.type,
  completeness: "complete",
  compatibility: {
    structural: "valid",
    support: "supported",
    profileId: "ard",
  },
  rawEntryAvailable: true,
  exactEntryDigest: digest,
  identityDigest: digest,
  freshness: "current",
  firstSeenAt: stamp,
  lastSeenAt: stamp,
};
const live: Schema["LiveSearchResponse"]["results"][number] = {
  identifier: entry.identifier,
  versionPresent: false,
  entry,
  sourceId: source.id,
  sourceRevision: source.revision,
  exactEntryDigest: digest,
  source: source.endpoint,
  score: 91,
};

it.each(["", "a", "operator-ans", "a".repeat(63)])(
  "accepts operator policy %j without normalizing it",
  (name) => {
    expect(trustPolicyName(name)).toBe(name);
  },
);
it.each([" Bad", "Bad", "bad_name", "-bad", "bad-", "a".repeat(64), "bad\n"])(
  "rejects invalid policy %j",
  (name) => {
    expect(() => trustPolicyName(name)).toThrow("ANS policy");
  },
);

it("uses current source revision and exact digest for indexed evidence, independently of historic occurrences", () => {
  expect(indexedTrustSelection(source, observation, entry)).toEqual({
    sourceRevision: 7,
    observationId: observation.id,
    identifier: entry.identifier,
    versionPresent: false,
    mediaType: entry.type,
    delivery: "data",
    exactEntryDigest: digest,
  });
  expect(
    liveTrustSelection(
      { ...live, versionPresent: true, entry: { ...entry, version: "" } },
      " original query ",
    ),
  ).toEqual({
    sourceRevision: 7,
    identifier: entry.identifier,
    versionPresent: true,
    version: "",
    mediaType: entry.type,
    delivery: "data",
    exactEntryDigest: digest,
    text: " original query ",
  });
  const { data: _data, ...urlEntry } = entry;
  expect(
    liveTrustSelection(
      {
        ...live,
        entry: { ...urlEntry, url: "https://peer.invalid/card.json" },
      },
      "helper",
    )?.delivery,
  ).toBe("url");
});

it.each([
  { identifier: "https://peer.invalid/one" },
  { identifier: "urn:air:peer.invalid:agents:one" },
  { version: "unexpected" },
  { type: null },
  { type: "" },
  { type: "x".repeat(256) },
  { url: "https://peer.invalid/card" },
  { data: null },
])("withholds a selection with inconsistent native fields %j", (override) => {
  expect(
    indexedTrustSelection(source, observation, { ...entry, ...override }),
  ).toBeUndefined();
  expect(
    liveTrustSelection({ ...live, entry: { ...entry, ...override } }, "helper"),
  ).toBeUndefined();
});
it("rejects absent delivery, overlong versions, stale indexed evidence and unsafe live binding", () => {
  expect(
    indexedTrustSelection({ ...source, revision: 0 }, observation, entry),
  ).toBeUndefined();
  expect(
    liveTrustSelection(
      { ...live, entry: { identifier: entry.identifier, type: entry.type } },
      "helper",
    ),
  ).toBeUndefined();
  expect(
    liveTrustSelection(
      {
        ...live,
        versionPresent: true,
        version: "v".repeat(129),
        entry: { ...entry, version: "v".repeat(129) },
      },
      "helper",
    ),
  ).toBeUndefined();
  for (const item of [
    { ...observation, freshness: "stale" as const },
    { ...observation, sourceId: "different" },
    { ...observation, rawEntryAvailable: false },
    { ...observation, exactEntryDigest: "bad" },
  ])
    expect(eligibleObservation(source, item)).toBe(false);
  expect(
    eligibleObservation({ ...source, status: "paused" }, observation),
  ).toBe(false);
  for (const row of [
    { ...live, sourceRevision: 0 },
    { ...live, exactEntryDigest: "bad" },
  ])
    expect(liveTrustSelection(row, "helper")).toBeUndefined();
  expect(liveTrustSelection(live, " ")).toBeUndefined();
  expect(liveTrustSelection(live, "q".repeat(501))).toBeUndefined();
});

it("opens the ANS tab from source detail without loading entries", async () => {
  const w = screenWorkspace();
  w.get.mockResolvedValue(etagReply(source));
  render(<Federation {...w.props} access={administrator} id={source.id} />);
  await screen.findByRole("heading", { name: source.displayName });
  click("ANS registration");
  await screen.findByRole("heading", { name: "ANS registration assessment" });
  expect(w.get).toHaveBeenCalledOnce();
});

it("measures query and version limits in code points while retaining exact values", () => {
  const version = "😀".repeat(128);
  const query = "😀".repeat(500);
  const selected = {
    ...live,
    versionPresent: true,
    version,
    entry: { ...entry, version },
  };
  expect(liveTrustSelection(selected, query)).toMatchObject({
    version,
    text: query,
  });
  expect(liveTrustSelection(selected, query + "😀")).toBeUndefined();
  expect(
    liveTrustSelection(
      {
        ...selected,
        version: version + "😀",
        entry: { ...entry, version: version + "😀" },
      },
      query,
    ),
  ).toBeUndefined();
});

it("does not discard a nonempty version when metadata declares no version", () => {
  expect(
    indexedTrustSelection(source, { ...observation, version: "v1" }, entry),
  ).toBeUndefined();
  expect(
    liveTrustSelection({ ...live, version: "v1" }, "helper"),
  ).toBeUndefined();
});

it.each([0.5, Number.MAX_SAFE_INTEGER + 1, Number.POSITIVE_INFINITY])(
  "refuses unsafe source revision %s for either selection path",
  (revision) => {
    expect(
      indexedTrustSelection({ ...source, revision }, observation, entry),
    ).toBeUndefined();
    expect(
      liveTrustSelection({ ...live, sourceRevision: revision }, "helper"),
    ).toBeUndefined();
  },
);

it.each([digest + "\n", digest.toUpperCase(), "sha256:" + "a".repeat(63)])(
  "refuses noncanonical exact digests %j without normalizing them",
  (exactEntryDigest) => {
    expect(
      indexedTrustSelection(
        source,
        { ...observation, exactEntryDigest },
        entry,
      ),
    ).toBeUndefined();
    expect(
      liveTrustSelection({ ...live, exactEntryDigest }, "helper"),
    ).toBeUndefined();
  },
);

it("shows incomplete indexed rows without reading raw evidence", async () => {
  const w = screenWorkspace();
  const { identifier: _identifier, ...unidentified } = observation;
  w.get.mockResolvedValue(
    etagReply({
      items: [
        {
          ...unidentified,
          rawEntryAvailable: false,
          versionPresent: true,
          version: "",
        },
        {
          ...observation,
          id: "second",
          versionPresent: true,
          version: "v1",
          rawEntryAvailable: false,
        },
      ],
    }),
  );
  render(<SourceTrust {...w.props} access={administrator} source={source} />);
  click("Choose indexed entry");
  await screen.findByRole("heading", { name: observation.sourceKey });
  expect(screen.getByText("Version: (empty)")).toBeTruthy();
  expect(screen.getByText("Version: v1")).toBeTruthy();
  expect(
    screen
      .getAllByRole("button", { name: "Select indexed entry" })
      .every((button) => button.hasAttribute("disabled")),
  ).toBe(true);
  expect(w.get).toHaveBeenCalledOnce();
});

it.each(["", "v1"])(
  "captures a present indexed version %j without requiring a Job navigation scope",
  async (version) => {
    const w = screenWorkspace();
    w.get
      .mockResolvedValueOnce(
        etagReply({
          items: [{ ...observation, versionPresent: true, version }],
        }),
      )
      .mockResolvedValueOnce(etagReply(JSON.stringify({ ...entry, version })));
    w.post.mockResolvedValue(
      etagReply({ ...job, kind: "source.trust_verify" }),
    );
    const access = {
      ...administrator,
      scopeEnforcement: true,
      scopes: ["sources.read", "sources.manage"] as Access["scopes"],
    };
    render(<SourceTrust {...w.props} access={access} source={source} />);
    click("Choose indexed entry");
    await screen.findByRole("button", { name: "Select indexed entry" });
    click("Select indexed entry");
    await screen.findByRole("heading", { name: "Request ANS assessment" });
    change("Reason", "Assess this exact version");
    await confirmCommand("Request ANS assessment");
    await screen.findByText(/Assessment Job: /);
    expect(w.post.mock.calls[0]?.[1]).toHaveProperty("body.version", version);
    expect(w.post.mock.calls[0]?.[1]).toHaveProperty(
      "body.versionPresent",
      true,
    );
    expect(screen.queryByRole("link", { name: "View Job status" })).toBeNull();
    expect(
      screen
        .getByRole("link", { name: "Open private assessment" })
        .getAttribute("href"),
    ).toBe(`/unit/trust-assessments/${job.id}?sourceId=${source.id}`);
  },
);

it.each(["access", "policy", "status"])(
  "keeps live assessment unavailable after current source %s changes",
  async (condition) => {
    const w = screenWorkspace();
    w.get.mockResolvedValue(
      etagReply({
        ...source,
        ...(condition === "policy" ? { trustVerificationPolicy: "" } : {}),
        ...(condition === "status" ? { status: "paused" } : {}),
      }),
    );
    const access =
      condition === "access"
        ? {
            ...administrator,
            permissions: administrator.permissions.filter(
              (p) => p !== "source.read_evidence",
            ),
          }
        : administrator;
    render(
      <LiveTrust
        {...w.props}
        access={access}
        sourceId={source.id}
        selection={liveTrustSelection(live, "helper")!}
        onReselect={vi.fn()}
      />,
    );
    await screen.findByRole("heading", { name: "Request ANS assessment" });
    if (condition === "access") {
      expect(
        screen.queryByRole("button", { name: "Request ANS assessment" }),
      ).toBeNull();
      expect(screen.queryByLabelText("Reason")).toBeNull();
    } else {
      const send = screen.getByRole("button", {
        name: "Request ANS assessment",
      });
      expect(send).toHaveProperty("disabled", true);
      expect(screen.getByLabelText("Reason").matches(":disabled")).toBe(false);
      change("Reason", "Prepare a reason while prerequisites are missing");
      fireEvent.submit(send.closest("form")!);
    }
    expect(
      screen.getAllByText(/unavailable|requires|policy|paused/i).length,
    ).toBeGreaterThan(0);
    expect(w.post).not.toHaveBeenCalled();
  },
);

it("retries an unavailable first indexed page explicitly", async () => {
  const w = screenWorkspace();
  w.get
    .mockRejectedValueOnce(new APIError(503, "DEPENDENCY_UNAVAILABLE"))
    .mockResolvedValueOnce(etagReply({ items: [] }));
  render(<SourceTrust {...w.props} access={administrator} source={source} />);
  click("Choose indexed entry");
  await screen.findByRole("alert");
  expect(w.get).toHaveBeenCalledOnce();
  click("Reload");
  await screen.findByText("No current indexed entries.");
  expect(w.get).toHaveBeenCalledTimes(2);
});

it.each([
  "source.manage",
  "source.read_evidence",
  "federation.discover",
  "sources.manage",
])("does not load evidence or offer assessment without %s", (missing) => {
  const w = screenWorkspace();
  const access: Access = {
    ...administrator,
    scopeEnforcement: true,
    scopes: [
      "sources.read",
      ...(missing === "sources.manage" ? [] : ["sources.manage" as const]),
    ],
    permissions: administrator.permissions.filter((p) => p !== missing),
  };
  render(<SourceTrust {...w.props} access={access} source={source} />);
  expect(screen.getByRole("alert").textContent).toContain(
    "requires source.manage",
  );
  expect(w.get).not.toHaveBeenCalled();
  expect(w.post).not.toHaveBeenCalled();
});

it.each([
  [{ ...source, status: "paused" }, /active source/],
  [{ ...source, status: "removed" }, /active source/],
  [{ ...source, trustVerificationPolicy: "" }, /Choose an ANS policy/],
  [
    { ...source, lastCompleteSnapshotId: undefined },
    /No complete indexed snapshot/,
  ],
] as const)(
  "explains unavailable indexed selection without reads",
  (selected, message) => {
    const w = screenWorkspace();
    render(
      <SourceTrust
        {...w.props}
        access={administrator}
        source={selected as Schema["FederationSource"]}
      />,
    );
    expect(screen.getByText(message)).toBeTruthy();
    expect(w.get).not.toHaveBeenCalled();
  },
);

async function indexedSetup() {
  const w = screenWorkspace();
  const refreshSource = vi.fn();
  w.get.mockImplementation(async (path) => {
    if (path === "/api/v1/observations")
      return etagReply({ items: [observation] });
    if (path === "/api/v1/observations/{observationId}/raw")
      return etagReply(JSON.stringify(entry));
    throw new Error(`Unexpected GET ${String(path)}`);
  });
  render(
    <SourceTrust
      {...w.props}
      access={administrator}
      source={source}
      refreshSource={refreshSource}
    />,
  );
  expect(w.get).not.toHaveBeenCalled();
  click("Choose indexed entry");
  await screen.findByText(entry.identifier);
  expect(w.get).toHaveBeenCalledOnce();
  click("Select indexed entry");
  await screen.findByRole("heading", { name: "Request ANS assessment" });
  expect(w.post).not.toHaveBeenCalled();
  return { ...w, refreshSource };
}

it("reads indexed evidence explicitly and confirms the exact body without an If-Match header", async () => {
  const w = await indexedSetup();
  w.post.mockResolvedValue(etagReply({ ...job, kind: "source.trust_verify" }));
  change("Reason", " Check registration ");
  await confirmCommand("Request ANS assessment");
  await screen.findByText(/Assessment Job: /);
  expect(w.get.mock.calls[0]?.[1]).toMatchObject({
    params: { query: { sourceId: source.id, freshness: "current", limit: 25 } },
  });
  expect(w.get.mock.calls[1]?.[1]).toMatchObject({
    params: { path: { observationId: observation.id } },
    parseAs: "text",
  });
  expect(w.post.mock.calls[0]).toEqual([
    "/api/v1/federation/sources/{sourceId}/trust-assessments",
    {
      params: {
        path: { sourceId: source.id },
        header: { "Idempotency-Key": expect.any(String) as unknown },
      },
      body: {
        sourceRevision: 7,
        observationId: observation.id,
        identifier: entry.identifier,
        versionPresent: false,
        mediaType: entry.type,
        delivery: "data",
        exactEntryDigest: digest,
        reason: "Check registration",
      },
    },
  ]);
  expect(
    screen.getByRole("link", { name: "View Job status" }).getAttribute("href"),
  ).toBe(`/unit/jobs/${job.id}?sourceId=${source.id}`);
});

it("requires reselection after a stale server response", async () => {
  const w = await indexedSetup();
  w.post.mockRejectedValue(new APIError(412, "precondition_failed"));
  change("Reason", "Check registration");
  await confirmCommand("Request ANS assessment");
  await screen.findByText(/precondition_failed/);
  click("Back to form");
  expect(
    screen
      .getByRole("button", { name: "Request ANS assessment" })
      .hasAttribute("disabled"),
  ).toBe(true);
  expect(screen.getByText(/This selection is stale/)).toBeTruthy();
  expect(screen.getByLabelText("Reason").matches(":disabled")).toBe(false);
  fireEvent.submit(
    screen
      .getByRole("button", { name: "Request ANS assessment" })
      .closest("form")!,
  );
  expect(w.post).toHaveBeenCalledOnce();
  const discard = vi.spyOn(window, "confirm").mockReturnValue(false);
  click("Reload source and reselect");
  expect(w.refreshSource).not.toHaveBeenCalled();
  expect(screen.getByLabelText("Reason")).toHaveProperty(
    "value",
    "Check registration",
  );
  expect(w.post).toHaveBeenCalledOnce();
  discard.mockReturnValue(true);
  click("Reload source and reselect");
  expect(w.refreshSource).toHaveBeenCalledOnce();
  click("Choose another entry");
  expect(
    screen.getByRole("button", { name: "Select indexed entry" }),
  ).toBeTruthy();
  expect(w.post).toHaveBeenCalledOnce();
});

it.each(["lost response", "malformed successful response"])(
  "preserves the key and selection after %s",
  async (failure) => {
    const w = await indexedSetup();
    if (failure === "lost response")
      w.post.mockRejectedValueOnce(new TypeError("network"));
    else w.post.mockResolvedValueOnce(etagReply({ ...job, kind: "different" }));
    w.post.mockResolvedValueOnce(
      etagReply({ ...job, kind: "source.trust_verify" }),
    );
    change("Reason", "Frozen reason");
    await confirmCommand("Request ANS assessment");
    await screen.findByRole("button", { name: "Retry same command" });
    expect(
      screen
        .getByRole("button", { name: "Choose another entry" })
        .hasAttribute("disabled"),
    ).toBe(true);
    fireEvent.change(screen.getByLabelText("Reason"), {
      target: { value: "Changed behind confirmation" },
    });
    click("Retry same command");
    await screen.findByText(/Assessment Job: /);
    expect(w.post).toHaveBeenCalledTimes(2);
    expect(w.post.mock.calls[1]).toEqual(w.post.mock.calls[0]);
    expect(w.post.mock.calls[1]?.[1]).toHaveProperty(
      "body.reason",
      "Frozen reason",
    );
  },
);

it.each([409, 503])(
  "keeps server outcome %i visible without automatic resubmission",
  async (status) => {
    const w = await indexedSetup();
    w.post.mockRejectedValue(
      new APIError(
        status,
        status === 409 ? "CONFLICT" : "DEPENDENCY_UNAVAILABLE",
      ),
    );
    change("Reason", "Check registration");
    await confirmCommand("Request ANS assessment");
    await screen.findByText(
      status === 409 ? /CONFLICT/ : /DEPENDENCY_UNAVAILABLE/,
    );
    expect(w.post).toHaveBeenCalledOnce();
    if (status === 503) {
      expect(
        screen.getByText(/Verification or the selected policy is unavailable/),
      ).toBeTruthy();
      w.post.mockResolvedValueOnce(
        etagReply({ ...job, kind: "source.trust_verify" }),
      );
      click("Retry same command");
      await screen.findByText(/Assessment Job: /);
      expect(
        screen.queryByText(
          /Verification or the selected policy is unavailable/,
        ),
      ).toBeNull();
    }
  },
);

it("discards late live source reads after selection is removed", async () => {
  const w = screenWorkspace();
  const pending = deferred<ReturnType<typeof etagReply>>();
  w.get.mockReturnValue(pending.promise);
  const mounted = render(
    <LiveTrust
      {...w.props}
      access={administrator}
      sourceId={source.id}
      selection={liveTrustSelection(live, "helper")!}
      onReselect={vi.fn()}
    />,
  );
  mounted.unmount();
  await act(async () => {
    pending.resolve(etagReply(source));
  });
  expect(w.post).not.toHaveBeenCalled();
  expect(w.props.onFailure).not.toHaveBeenCalled();
});

it("requires a fresh query if the source revision changed since the live result", async () => {
  const w = screenWorkspace();
  w.get.mockResolvedValue(etagReply({ ...source, revision: 8 }));
  render(
    <LiveTrust
      {...w.props}
      access={administrator}
      sourceId={source.id}
      selection={liveTrustSelection(live, "helper")!}
      onReselect={vi.fn()}
    />,
  );
  await screen.findByText(/Run a fresh live diagnostic query/);
  expect(
    screen.queryByRole("button", { name: "Request ANS assessment" }),
  ).toBeNull();
  expect(w.post).not.toHaveBeenCalled();
});

it("keeps empty pages explicit and recovers a failed indexed continuation from the first page", async () => {
  const w = screenWorkspace();
  w.get
    .mockResolvedValueOnce(etagReply({ items: [], nextCursor: "next-page" }))
    .mockRejectedValueOnce(new APIError(503, "DEPENDENCY_UNAVAILABLE"))
    .mockResolvedValueOnce(etagReply({ items: [] }));
  render(<SourceTrust {...w.props} access={administrator} source={source} />);
  click("Choose indexed entry");
  await screen.findByText("No current indexed entries.");
  click("Next page");
  await screen.findByRole("alert");
  click("Reload");
  await screen.findByText("No current indexed entries.");
  expect(w.get.mock.calls[1]?.[1]).toHaveProperty(
    "params.query.cursor",
    "next-page",
  );
  expect(w.get.mock.calls[2]?.[1]).not.toHaveProperty("params.query.cursor");
  expect(w.post).not.toHaveBeenCalled();
});

it.each(["{}", "not json"])(
  "does not offer an assessment when retained raw evidence is unusable: %s",
  async (raw) => {
    const w = screenWorkspace();
    w.get
      .mockResolvedValueOnce(etagReply({ items: [observation] }))
      .mockResolvedValueOnce(etagReply(raw));
    render(<SourceTrust {...w.props} access={administrator} source={source} />);
    click("Choose indexed entry");
    await screen.findByRole("button", { name: "Select indexed entry" });
    click("Select indexed entry");
    await screen.findByRole("button", { name: "Choose another entry" });
    await waitFor(() => expect(screen.queryByText("Loading...")).toBeNull());
    expect(
      screen.queryByRole("button", { name: "Request ANS assessment" }),
    ).toBeNull();
    expect(w.post).not.toHaveBeenCalled();
  },
);

it("rejects a whitespace-only reason before confirmation and requests", async () => {
  const w = await indexedSetup();
  change("Reason", "   ");
  click("Request ANS assessment");
  await screen.findByRole("alert");
  expect(screen.queryByRole("dialog")).toBeNull();
  expect(w.post).not.toHaveBeenCalled();
});

it("uses the submitted direct-source live query, not later search edits or reserialized evidence", async () => {
  const w = screenWorkspace();
  w.get.mockResolvedValue(etagReply(source));
  w.post
    .mockResolvedValueOnce(etagReply({ results: [live], partial: false }))
    .mockResolvedValueOnce(etagReply({ ...job, kind: "source.trust_verify" }));
  render(<FederationSearch {...w.props} access={administrator} />);
  change("Query", " original query ");
  change("Reason", "Query reason");
  await confirmCommand("Query live sources");
  await screen.findByRole("button", { name: "Select for ANS assessment" });
  change("Query", "different query");
  expect(w.get).not.toHaveBeenCalled();
  click("Select for ANS assessment");
  await screen.findByRole("heading", { name: "Request ANS assessment" });
  click("Choose another entry");
  expect(
    screen
      .getByRole("button", { name: "Select for ANS assessment" })
      .hasAttribute("disabled"),
  ).toBe(false);
  click("Select for ANS assessment");
  await screen.findByRole("heading", { name: "Request ANS assessment" });
  const form = screen
    .getByRole("heading", { name: "Request ANS assessment" })
    .closest("section")!;
  fireEvent.change(within(form).getByLabelText("Reason"), {
    target: { value: "Assess original selection" },
  });
  const discard = vi.spyOn(window, "confirm").mockReturnValue(false);
  click("Choose another entry");
  expect(within(form).getByLabelText("Reason")).toHaveProperty(
    "value",
    "Assess original selection",
  );
  expect(w.get).toHaveBeenCalledTimes(2);
  expect(w.post).toHaveBeenCalledOnce();
  click("Start a new live query");
  expect(
    screen.getByRole("heading", { name: "Request ANS assessment" }),
  ).toBeTruthy();
  expect(
    within(form).getByText(/Confirming this request sends the original query/)
      .textContent,
  ).toContain("original query");
  click("Request ANS assessment");
  const confirmation = await screen.findByRole("dialog");
  expect(
    within(confirmation).getByText(
      /Confirming repeats the original query with this source/,
    ),
  ).toBeTruthy();
  expect(
    within(confirmation).getByLabelText("Command summary").textContent,
  ).toContain("original query");
  expect(
    within(confirmation).getByLabelText("Command summary").textContent,
  ).not.toContain("different query");
  expect(w.post).toHaveBeenCalledOnce();
  click("Confirm");
  await waitFor(() => expect(w.post).toHaveBeenCalledTimes(2));
  expect(w.post.mock.calls[1]?.[1]).toMatchObject({
    params: { path: { sourceId: source.id } },
    body: {
      text: "original query",
      identifier: entry.identifier,
      exactEntryDigest: digest,
      sourceRevision: 7,
      versionPresent: false,
      reason: "Assess original selection",
    },
  });
  expect(w.post.mock.calls[1]?.[1]).not.toHaveProperty("body.version");
  expect(w.post.mock.calls[1]?.[1]).not.toHaveProperty("body.observationId");
  await screen.findByText(/Assessment Job: /);
  discard.mockReturnValue(true);
  click("Start a new live query");
  expect(
    screen.queryByRole("heading", { name: "Request ANS assessment" }),
  ).toBeNull();
  expect(
    screen
      .getByRole("button", { name: "Query live sources" })
      .hasAttribute("disabled"),
  ).toBe(false);
  expect(screen.getByLabelText("Query")).toHaveProperty(
    "value",
    "different query",
  );
});
