// @vitest-environment jsdom
import { webcrypto } from "node:crypto";
import { afterEach, beforeEach, expect, it, vi } from "vitest";
import {
  act,
  fireEvent,
  render,
  screen,
  waitFor,
} from "@testing-library/react";
import { APIError, makeAPI } from "../api/client";
import type { Access } from "../api/client";
import TrustAssessment from "./TrustAssessment";
import {
  canReadTrust,
  canReadTrustJob,
  trustID,
  trustResponse,
  trustResultHref,
  evidenceBytes,
} from "./trust-result";
import { deferred } from "../../tests/unit/fixtures";
import {
  screenLifecycle,
  screenWorkspace,
  administrator,
  change,
  click,
  etagReply,
} from "../../tests/unit/screens";
import {
  trustSample,
  trustSource,
  trustJob,
  evidenceSamples,
} from "../../tests/unit/trust-fixtures";

screenLifecycle(`/unit/trust-assessments/${trustJob}?sourceId=${trustSource}`);
const endpoint =
  "/api/v1/federation/sources/{sourceId}/trust-assessments/{jobId}";
const createURL = vi.fn((_blob: Blob) => "blob:unit-evidence");
const revokeURL = vi.fn();
beforeEach(() => {
  vi.stubGlobal("crypto", webcrypto);
  vi.stubGlobal(
    "URL",
    Object.assign(class extends URL {}, {
      createObjectURL: createURL,
      revokeObjectURL: revokeURL,
    }),
  );
  createURL.mockClear();
  revokeURL.mockClear();
});
afterEach(() => vi.unstubAllGlobals());

it.each<{
  permissions: Access["permissions"];
  scopes: Access["scopes"];
  allowed: boolean;
}>([
  {
    permissions: ["job.read", "source.read_evidence"],
    scopes: ["catalog.read"],
    allowed: true,
  },
  {
    permissions: ["job.read", "source.read_evidence"],
    scopes: ["sources.read"],
    allowed: false,
  },
  { permissions: ["job.read"], scopes: ["catalog.read"], allowed: false },
  {
    permissions: ["source.read_evidence"],
    scopes: ["catalog.read"],
    allowed: false,
  },
])(
  "keeps ANS Job metadata admission separate from private evidence admission: %j",
  ({ permissions, scopes, allowed }) => {
    expect(
      canReadTrustJob({
        ...administrator,
        permissions,
        scopeEnforcement: true,
        scopes,
      }),
    ).toBe(allowed);
  },
);

it("explains missing Source context without loading private evidence", () => {
  history.replaceState(null, "", `/unit/trust-assessments/${trustJob}`);
  const w = setup();
  render(<TrustAssessment {...w.props} />);
  expect(
    screen.getByText(/This Job link has no valid Source context/),
  ).toBeTruthy();
  expect(screen.getByLabelText("Assessment Job ID")).toHaveProperty(
    "value",
    trustJob,
  );
  expect(
    screen.getByRole("button", { name: "Read private assessment" }),
  ).toHaveProperty("disabled", true);
  expect(w.get).not.toHaveBeenCalled();
});

function setup() {
  const w = screenWorkspace();
  w.props.access = {
    ...administrator,
    permissions: ["job.read", "source.read_evidence"],
    availablePermissions: ["job.read", "source.read_evidence"],
    scopeEnforcement: true,
    scopes: ["sources.read"],
  };
  w.props.id = trustJob;
  return w;
}
async function loaded() {
  return screen.findByRole("link", { name: "Save original entry" });
}

it("reads only on demand under the dedicated scope, checks binding, and downloads original bytes", async () => {
  const w = setup();
  w.get.mockResolvedValue(etagReply(trustSample()));
  const view = render(<TrustAssessment {...w.props} />);
  expect(w.get).not.toHaveBeenCalled();
  expect((screen.getByLabelText("Source ID") as HTMLInputElement).value).toBe(
    trustSource,
  );
  click("Read private assessment");
  await loaded();
  expect(w.get).toHaveBeenCalledTimes(1);
  expect(w.get).toHaveBeenCalledWith(endpoint, {
    params: { path: { sourceId: trustSource, jobId: trustJob } },
    signal: expect.any(AbortSignal) as unknown,
  });
  expect(screen.queryByRole("link", { name: "View Job status" })).toBeNull();
  expect(screen.getAllByRole("row")).toHaveLength(8);
  expect(
    screen.getByText(/Software safety, artifact integrity/).textContent,
  ).toContain("Not assessed");
  expect(screen.getByText(/Current registration under/)).not.toBeNull();
  expect(
    screen.getByText(/Refresh checks whether this saved result still applies/)
      .textContent,
  ).toContain(
    "To get new evidence, request another assessment from the source entry.",
  );
  const explanation = screen
    .getByText("What these checks mean")
    .closest("details")!;
  expect(explanation.open).toBe(false);
  fireEvent.click(screen.getByText("What these checks mean"));
  expect(explanation.open).toBe(true);
  expect(explanation.textContent).toContain("operator's accepted public keys");
  expect(explanation.textContent).toContain(
    "signed evidence at assessment time",
  );
  expect(w.get).toHaveBeenCalledTimes(1);
  expect(w.post).not.toHaveBeenCalled();
  expect(createURL).toHaveBeenCalledTimes(5);
  for (const [index, original] of Object.values(evidenceSamples).entries()) {
    const blob = createURL.mock.calls[index]![0];
    // Blob bytes are checked in Playwright too; here inspect the independently decoded bytes.
    expect(blob.type).toBe("application/octet-stream");
    expect(blob.size).toBe(original.length);
  }
  expect(
    screen
      .getByRole("link", { name: "Save original entry" })
      .getAttribute("download"),
  ).toBe(`ans-${trustJob}-entry.bin`);
  click("Refresh private assessment");
  await loaded();
  expect(w.get).toHaveBeenCalledTimes(2);
  expect(w.get.mock.calls.every(([path]) => path === endpoint)).toBe(true);
  expect(w.post).not.toHaveBeenCalled();
  expect(revokeURL).toHaveBeenCalledTimes(5);
  view.unmount();
  expect(revokeURL).toHaveBeenCalledTimes(10);
});

it.each(["job.read", "source.read_evidence", "scope"])(
  "denies missing %s without any read",
  (missing) => {
    const w = setup();
    if (missing === "scope") w.props.access.scopes = ["catalog.read"];
    else
      w.props.access.permissions = w.props.access.permissions.filter(
        (p) => p !== missing,
      );
    render(<TrustAssessment {...w.props} />);
    expect(screen.getByRole("alert").textContent).toContain(
      "Private assessments require",
    );
    expect(w.get).not.toHaveBeenCalled();
    expect(canReadTrust(w.props.access)).toBe(false);
  },
);

it.each(["historical", "expired", "negative_or_insufficient"] as const)(
  "shows %s separately from Job success",
  async (applicability) => {
    const w = setup();
    const data = trustSample();
    data.current = false;
    data.applicability = applicability;
    if (applicability === "negative_or_insufficient") {
      const a = data.result.assessment;
      const skipped = {
        state: "not_assessed" as const,
        code: "prerequisite_failed",
      };
      data.result.assessment = {
        profile: a.profile,
        policyDigest: a.policyDigest,
        entryDigest: a.entryDigest,
        checkedAt: a.checkedAt,
        currentRegistration: false,
        receipt: { state: "unavailable", code: "receipt_missing" },
        producer: skipped,
        log: skipped,
        checkpoint: skipped,
        inclusion: skipped,
        target: skipped,
        status: skipped,
      };
      data.result.evidence.receipt = "";
      data.result.evidence.checkpoint = "";
      data.result.evidence.statusToken = "";
    }
    if (applicability === "historical") {
      data.result.assessment.currentRegistration = false;
      data.result.assessment.producer = {
        state: "invalid",
        code: "signature_failed",
      };
      data.result.assessment.log = {
        state: "unsupported",
        code: "algorithm_unsupported",
      };
      data.result.assessment.inclusion = {
        state: "unavailable",
        code: "proof_unavailable",
      };
    }
    delete data.result.evidence.checkpointHistory;
    w.get.mockResolvedValue(etagReply(data));
    render(<TrustAssessment {...w.props} />);
    click("Read private assessment");
    await loaded();
    expect(
      screen.getByText(
        {
          historical: "Historical",
          expired: "Expired",
          negative_or_insufficient: "Negative or insufficient evidence",
        }[applicability],
      ),
    ).not.toBeNull();
    expect(
      screen
        .getByText(
          {
            historical: "Historical",
            expired: "Expired",
            negative_or_insufficient: "Negative or insufficient evidence",
          }[applicability],
        )
        .getAttribute("data-tone"),
    ).toBe("neutral");
    if (applicability === "historical") {
      expect(screen.getByText("Invalid").getAttribute("data-tone")).toBe(
        "danger",
      );
      expect(screen.getByText("Unsupported").getAttribute("data-tone")).toBe(
        "neutral",
      );
      expect(screen.getByText("Unavailable").getAttribute("data-tone")).toBe(
        "neutral",
      );
    }
    expect(screen.getByText("Checkpoint history: not retained")).not.toBeNull();
    if (applicability === "negative_or_insufficient") {
      expect(screen.getByText("Receipt: not retained")).not.toBeNull();
      expect(screen.queryByRole("link", { name: "Save receipt" })).toBeNull();
    } else {
      expect(screen.getByRole("link", { name: "Save receipt" })).not.toBeNull();
    }
    expect(w.get.mock.calls.every(([path]) => path === endpoint)).toBe(true);
  },
);

it.each([
  [409, "INVALID_TRANSITION", "not completed successfully"],
  [404, "NOT_FOUND", "No retained assessment"],
  [503, "DEPENDENCY_UNAVAILABLE", "required disclosure audit"],
  [403, "FORBIDDEN", "current access"],
  [401, "UNAUTHENTICATED", "Sign in again"],
] as const)(
  "shows %s safely and recovers only by explicit refresh",
  async (status, code, message) => {
    const w = setup();
    w.get
      .mockRejectedValueOnce(new APIError(status, code))
      .mockResolvedValue(etagReply(trustSample()));
    render(<TrustAssessment {...w.props} />);
    click("Read private assessment");
    await screen.findByText(new RegExp(message));
    expect(createURL).not.toHaveBeenCalled();
    expect(w.get).toHaveBeenCalledTimes(1);
    expect(w.props.onFailure).toHaveBeenCalledTimes(1);
    click("Refresh private assessment");
    await loaded();
    expect(w.get).toHaveBeenCalledTimes(2);
  },
);

it("discards old evidence on a failed refresh and revokes its local downloads", async () => {
  const w = setup();
  w.get
    .mockResolvedValueOnce(etagReply(trustSample()))
    .mockRejectedValue(new APIError(503, "DEPENDENCY_UNAVAILABLE"));
  render(<TrustAssessment {...w.props} />);
  click("Read private assessment");
  await loaded();
  click("Refresh private assessment");
  await screen.findByRole("alert");
  expect(
    screen.queryByRole("link", { name: "Save original entry" }),
  ).toBeNull();
  expect(revokeURL).toHaveBeenCalledTimes(5);
});

it.each(["wrong source", "bad checksum", "malformed"])(
  "rejects %s before display or download",
  async (kind) => {
    const w = setup();
    const data = trustSample();
    if (kind === "wrong source") data.result.sourceId = trustJob;
    if (kind === "bad checksum")
      data.result.evidence.entry = btoa("different original bytes");
    w.get.mockResolvedValue(etagReply(kind === "malformed" ? {} : data));
    render(<TrustAssessment {...w.props} />);
    click("Read private assessment");
    await screen.findByRole("alert");
    expect(createURL).not.toHaveBeenCalled();
    expect(screen.queryByText("operator-ans")).toBeNull();
  },
);

it("supports exact lookup without a current source read and clears evidence when inputs change", async () => {
  history.replaceState(null, "", "/unit/trust-assessments");
  const w = setup();
  w.props.id = "";
  w.props.access.scopes.push("catalog.read");
  w.get.mockResolvedValue(etagReply(trustSample()));
  render(<TrustAssessment {...w.props} />);
  expect(
    (
      screen.getByRole("button", {
        name: "Read private assessment",
      }) as HTMLButtonElement
    ).disabled,
  ).toBe(true);
  change("Source ID", "invalid");
  change("Assessment Job ID", trustJob);
  fireEvent.submit(screen.getByLabelText("Source ID").closest("form")!);
  expect(w.get).not.toHaveBeenCalled();
  change("Source ID", trustSource);
  click("Read private assessment");
  await loaded();
  expect(
    screen.getByRole("link", { name: "View Job status" }).getAttribute("href"),
  ).toBe(`/unit/jobs/${trustJob}?sourceId=${trustSource}`);
  change("Assessment Job ID", "invalid");
  expect(screen.queryByText("operator-ans")).toBeNull();
  expect(revokeURL).toHaveBeenCalledTimes(5);
});

it("ignores late evidence after access replacement and revokes existing URLs", async () => {
  const w = setup();
  const pending = deferred<ReturnType<typeof etagReply>>();
  w.get.mockReturnValueOnce(pending.promise);
  const view = render(<TrustAssessment {...w.props} />);
  click("Read private assessment");
  view.rerender(
    <TrustAssessment
      {...w.props}
      access={{ ...w.props.access, subject: "different-human" }}
    />,
  );
  await act(async () => pending.resolve(etagReply(trustSample())));
  expect(screen.queryByText("operator-ans")).toBeNull();
  expect(createURL).not.toHaveBeenCalled();
  w.get.mockResolvedValue(etagReply(trustSample()));
  click("Read private assessment");
  await loaded();
  view.rerender(
    <TrustAssessment
      {...w.props}
      access={{ ...w.props.access, permissions: [] }}
    />,
  );
  expect(screen.queryByText("operator-ans")).toBeNull();
  expect(revokeURL).toHaveBeenCalledTimes(5);
});

it("rechecks session authority after asynchronous entry checksum verification", async () => {
  const data = trustSample();
  const hash = await webcrypto.subtle.digest(
    "SHA-256",
    evidenceBytes(data.result.evidence.entry),
  );
  const pending = deferred<ArrayBuffer>();
  const digest = vi.fn().mockReturnValue(pending.promise);
  vi.stubGlobal("crypto", { subtle: { digest } });
  vi.stubGlobal(
    "fetch",
    vi.fn().mockResolvedValue(
      new Response(JSON.stringify(data), {
        headers: { "Content-Type": "application/json" },
      }),
    ),
  );
  const w = setup();
  const client = makeAPI(w.props.runtime);
  render(<TrustAssessment {...w.props} client={client} />);
  click("Read private assessment");
  await waitFor(() => expect(digest).toHaveBeenCalledTimes(1));
  // Keep the view mounted so only the transport's session fence protects this read.
  client.invalidate();
  await act(async () => pending.resolve(hash));
  expect(screen.queryByText("operator-ans")).toBeNull();
  expect(createURL).not.toHaveBeenCalled();
  expect(w.props.onFailure).toHaveBeenCalledExactlyOnceWith(
    expect.objectContaining({ name: "AbortError", message: "Session changed" }),
  );
});

it.each(["Source ID", "Assessment Job ID", "unmount"])(
  "discards a pending checksum after %s changes without disclosing stale evidence",
  async (changeTarget) => {
    const data = trustSample();
    const hash = await webcrypto.subtle.digest(
      "SHA-256",
      evidenceBytes(data.result.evidence.entry),
    );
    const pending = deferred<ArrayBuffer>();
    const digest = vi
      .fn()
      .mockReturnValueOnce(pending.promise)
      .mockResolvedValue(hash);
    vi.stubGlobal("crypto", { subtle: { digest } });
    const w = setup();
    w.get.mockResolvedValue(etagReply(data));
    const view = render(<TrustAssessment {...w.props} />);
    click("Read private assessment");
    await waitFor(() => expect(digest).toHaveBeenCalledTimes(1));
    const request = w.get.mock.calls[0]?.[1] as { signal: AbortSignal };
    const replacement = "00000000-0000-7000-8000-000000000003";
    if (changeTarget === "unmount") view.unmount();
    else change(changeTarget, replacement);
    expect(request.signal.aborted).toBe(true);
    await act(async () => pending.resolve(hash));
    expect(screen.queryByText("operator-ans")).toBeNull();
    expect(createURL).not.toHaveBeenCalled();
    expect(w.props.onFailure).not.toHaveBeenCalled();
    expect(w.get).toHaveBeenCalledTimes(1);
    if (changeTarget === "unmount") return;

    if (changeTarget === "Source ID") data.result.sourceId = replacement;
    click("Read private assessment");
    await loaded();
    expect(w.get).toHaveBeenCalledTimes(2);
    expect(w.get.mock.calls[1]?.[1]).toHaveProperty("params.path", {
      sourceId: changeTarget === "Source ID" ? replacement : trustSource,
      jobId: changeTarget === "Assessment Job ID" ? replacement : trustJob,
    });
    expect(createURL).toHaveBeenCalledTimes(5);
    expect(w.props.onFailure).not.toHaveBeenCalled();
  },
);

it("preserves base64 byte values and exact links", () => {
  expect([...evidenceBytes("AAD/fg==")]).toEqual([0, 0, 255, 126]);
  expect(trustResultHref("/unit", trustSource, trustJob)).toBe(
    `/unit/trust-assessments/${trustJob}?sourceId=${trustSource}`,
  );
  expect(trustID("../escape")).toBe(false);
  expect(trustID(trustJob)).toBe(true);
});

it("canonicalizes UUID letter case and prevents a second form submission from refetching", async () => {
  const w = setup();
  const data = trustSample();
  data.result.sourceId = "abcdef00-0000-7000-8000-000000000001";
  w.get.mockResolvedValue(etagReply(data));
  render(<TrustAssessment {...w.props} />);
  change("Source ID", data.result.sourceId.toUpperCase());
  click("Read private assessment");
  await loaded();
  expect(w.get.mock.calls[0]?.[1]).toHaveProperty(
    "params.path.sourceId",
    data.result.sourceId,
  );
  fireEvent.submit(screen.getByLabelText("Source ID").closest("form")!);
  expect(w.get).toHaveBeenCalledTimes(1);
});

// Mutate one consumed field at a time from an independently valid response.
it.each([
  ["current", "yes"],
  ["applicability", "unknown"],
  ["result", null],
  ["result.sourceId", "../escape"],
  ["result.sourceRevision", 1.5],
  ["result.policyName", ""],
  ["result.authority", "a".repeat(254)],
  ["result.policyDigest", "sha256:" + "A".repeat(64)],
  ["result.policyDigest", "wrong"],
  ["result.exactEntryDigest", "wrong"],
  ["result.mediaType", null],
  ["result.delivery", "other"],
  ["result.softwareTrust", "trusted"],
  ["result.artifactIntegrity", "verified"],
  ["result.rights", "granted"],
  ["result.assessment", []],
  ["result.assessment.profile", "other"],
  ["result.assessment.policyDigest", "a".repeat(64)],
  ["result.assessment.entryDigest", "b".repeat(64)],
  ["result.assessment.checkedAt", "not-a-date"],
  ["result.assessment.validUntil", "not-a-date"],
  ["result.assessment.validUntil", "2026-10-10T01:00:00Z"],
  ["result.assessment.validUntil", undefined],
  ["result.assessment.currentRegistration", "true"],
  ["result.assessment.receipt", null],
  ["result.assessment.receipt.state", "trusted"],
  ["result.assessment.receipt.code", ""],
  ["result.assessment.receipt.state", "invalid"],
  ["result.assessment.subject", "s".repeat(4097)],
  ["result.assessment.logKeyFingerprint", "A".repeat(64)],
  ["result.evidence", null],
  ["result.evidence.profile", "other"],
  ["result.evidence.entry", ""],
  ["result.evidence.entry", "bad!"],
  ["result.evidence.receipt", "YR=="],
  ["result.evidence.receipt", 7],
  ["result.evidence.checkpoint", "YQ==\n"],
  ["result.evidence.statusToken", "Y".repeat(87385)],
  ["current", false],
  ["applicability", "negative_or_insufficient"],
] as [string, unknown][])("rejects malformed %s (%j)", (path, value) => {
  const sample: unknown = trustSample();
  expect(trustResponse(sample)).toBe(true);
  const fields = path.split(".");
  let target = sample as Record<string, unknown>;
  for (const key of fields.slice(0, -1))
    target = target[key] as Record<string, unknown>;
  target[fields.at(-1)!] = value;
  expect(trustResponse(sample)).toBe(false);
});

it("rejects absent roots, inconsistent current claims and oversized complete evidence bundles", () => {
  expect(trustResponse(null)).toBe(false);
  const data = trustSample();
  data.result.assessment.currentRegistration = false;
  expect(trustResponse(data)).toBe(false);
  data.current = false;
  data.applicability = "negative_or_insufficient";
  expect(trustResponse(data)).toBe(true);
  data.result.evidence.entry = btoa("e".repeat(262144));
  data.result.evidence.receipt = btoa("r".repeat(262144));
  data.result.evidence.checkpointHistory = btoa("h".repeat(262144));
  expect(trustResponse(data)).toBe(false);
});

it.each(["text", "large", "binary"])(
  "opens only bounded verified assessed entry text: %s",
  async (kind) => {
    const w = setup();
    const original =
      ' {"identifier":"urn:air:peer.invalid:agents:helper","n":9007199254740993123456789,"literal":"<script>hostile()</script>"}\r\n';
    const bytes =
      kind === "binary"
        ? new Uint8Array([255, 254])
        : new TextEncoder().encode(
            kind === "large"
              ? '{"large":"' + "x".repeat(128 * 1024) + '"}'
              : original,
          );
    const hash = new Uint8Array(
      await webcrypto.subtle.digest("SHA-256", bytes),
    );
    const digest = Array.from(hash, (byte) =>
      byte.toString(16).padStart(2, "0"),
    ).join("");
    const sample = trustSample();
    sample.result.evidence.entry = Buffer.from(bytes).toString("base64");
    sample.result.exactEntryDigest = `sha256:${digest}`;
    sample.result.assessment.entryDigest = digest;
    sample.result.assessment.currentRegistration = false;
    sample.current = false;
    sample.applicability = "negative_or_insufficient";
    w.get.mockResolvedValue(etagReply(sample));
    const view = render(<TrustAssessment {...w.props} />);
    click("Read private assessment");
    await loaded();
    expect(screen.queryByRole("group", { name: "JSON document" })).toBeNull();
    fireEvent.click(screen.getByText("Inspect assessed original entry"));
    if (kind === "text") {
      const content = await screen.findByRole("group", {
        name: "JSON document",
      });
      expect(content.textContent).toBe(original);
      expect(view.container.querySelector("script")).toBeNull();
      expect(
        screen.getByRole("button", { name: "Copy document text" }),
      ).toBeTruthy();
    } else {
      await screen.findByText(
        kind === "large"
          ? /Entry exceeds the 128 KiB/
          : /Entry is not valid UTF-8/,
      );
      expect(
        screen.queryByRole("button", { name: "Copy document text" }),
      ).toBeNull();
    }
    expect(w.get).toHaveBeenCalledOnce();
    expect(
      screen.getByRole("link", { name: "Save original entry" }),
    ).toBeTruthy();
  },
);

it("shows associated UUID format errors without reading private evidence", () => {
  const w = setup();
  render(<TrustAssessment {...w.props} />);
  for (const [label, error] of [
    ["Source ID", "Enter a valid Source ID in UUID format."],
    ["Assessment Job ID", "Enter a valid assessment Job ID in UUID format."],
  ] as const) {
    const input = screen.getByLabelText(label) as HTMLInputElement;
    change(label, "invalid-id");
    expect(input.getAttribute("aria-invalid")).toBe("true");
    expect(input.validity.patternMismatch).toBe(true);
    const hint = screen.getByText(error);
    expect(input.getAttribute("aria-describedby")?.split(" ")).toContain(
      hint.id,
    );
    expect(
      screen.getByRole("button", { name: "Read private assessment" }),
    ).toHaveProperty("disabled", true);
  }
  expect(w.get).not.toHaveBeenCalled();
});
