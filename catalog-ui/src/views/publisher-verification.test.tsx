// @vitest-environment jsdom
import { useState } from "react";
import { act, render, screen, waitFor } from "@testing-library/react";
import { expect, it, vi } from "vitest";
import { APIError } from "../api/client";
import type { components } from "../api/schema";
import type { WorkspaceProps } from "./workspace";
import { PublisherVerification } from "./PublisherVerification";
import Publishers from "./Publishers";
import {
  administrator,
  change,
  click,
  confirmCommand,
  etagReply,
  publisher,
  screenLifecycle,
  screenWorkspace,
} from "../../tests/unit/screens";
import { deferred } from "../../tests/unit/fixtures";

screenLifecycle("/unit/publishers/publisher-1");
type Status = components["schemas"]["PublisherDomainVerification"];
const initial: Status = {
  domain: "example.com",
  mode: "optional",
  state: "not_verified",
  challengeState: "not_issued",
};
const pending = {
  ...initial,
  challengeState: "pending",
  challengeId: "challenge-1",
  challengeIssuedAt: new Date(Date.now() - 60000).toISOString(),
  challengeExpiresAt: new Date(Date.now() + 3600000).toISOString(),
} satisfies Status;
const proof: Status = {
  ...pending,
  state: "verified",
  challengeState: "consumed",
  verifiedAt: pending.challengeIssuedAt,
  expiresAt: pending.challengeExpiresAt,
  lastCheckAt: pending.challengeIssuedAt,
  lastCheckOutcome: "matched",
  lastCheckChallengeId: "challenge-1",
};
const instructions = (status: Status = pending) => ({
  status,
  challengeId: status.challengeId,
  name: `_open-agentic-catalog-challenge.${status.domain}.`,
  value: "open-agentic-catalog-verification=" + "a".repeat(43),
  expiresAt: status.challengeExpiresAt,
});
const statusPath =
  "/api/v1/publishers/{publisherId}/domains/{domain}/verification";
function Harness(
  props: WorkspaceProps & { blocked?: boolean; domains?: string[] },
) {
  const [reason, setReason] = useState("");
  return (
    <PublisherVerification
      {...props}
      publisher={{ ...publisher, domains: props.domains ?? publisher.domains }}
      blocked={props.blocked ?? false}
      reason={reason}
      setReason={setReason}
    />
  );
}
function setup(value: Status = initial) {
  const w = screenWorkspace();
  let status = value;
  let version = 1;
  let challenge: unknown;
  w.get.mockImplementation(async (path, options) => {
    if (path === "/api/v1/capabilities")
      return etagReply({
        governance: { publisherDomainVerification: status.mode },
      });
    if (path === statusPath) {
      const domain = (options as { params: { path: { domain: string } } })
        .params.path.domain;
      return etagReply({ ...status, domain }, `"proof-${version}"`);
    }
    if (path === statusPath + "/challenge")
      return etagReply(challenge ?? instructions(status));
    throw new Error("Unexpected route " + String(path));
  });
  const props = { ...w.props, access: administrator };
  const view = render(<Harness {...props} />);
  return {
    ...w,
    props,
    view,
    setStatus(next: Status) {
      status = next;
      version++;
    },
    setInstructions(value: unknown) {
      challenge = value;
    },
    async open() {
      click("View DNS verification");
      await screen.findByText("DNS proof");
    },
  };
}
it("does not request evidence until opened and keeps disabled historical status read-only", async () => {
  const w = setup({ ...initial, mode: "disabled" });
  expect(w.get).not.toHaveBeenCalled();
  await w.open();
  expect(screen.getByText(/DNS verification is turned off/)).toBeTruthy();
  expect(
    screen.queryByRole("button", { name: "Create DNS challenge" }),
  ).toBeNull();
  click("Refresh DNS status");
  await waitFor(() => expect(w.get).toHaveBeenCalledTimes(3));
  expect(w.post).not.toHaveBeenCalled();
});
it("shows approval and required evidence as separate states and retains a reason across domain selection", async () => {
  const w = setup({ ...initial, mode: "required_for_approval" });
  w.view.rerender(
    <Harness {...w.props} domains={["example.com", "second.test"]} />,
  );
  await w.open();
  expect(screen.getByText(/Fresh DNS evidence is required/)).toBeTruthy();
  expect(screen.getByText("Publication approval")).toBeTruthy();
  change("DNS verification reason", "My unsent reason");
  expect(screen.getByLabelText("DNS verification domain")).toHaveProperty(
    "disabled",
    true,
  );
  change("DNS verification reason", "");
  change("DNS verification domain", "second.test");
  await waitFor(() => expect(w.get).toHaveBeenCalledTimes(3));
  expect(screen.getByText("second.test", { selector: "dd" })).toBeTruthy();
});
it("issues an exact command, reloads status, and fetches TXT instructions only on request", async () => {
  const w = setup();
  await w.open();
  w.post.mockImplementation(async () => {
    w.setStatus(pending);
    return etagReply(instructions());
  });
  change("DNS verification reason", "Prove domain control");
  await confirmCommand("Create DNS challenge");
  await screen.findByRole("button", { name: "Show TXT instructions" });
  expect(w.post).toHaveBeenCalledWith(
    statusPath + ":issue",
    expect.objectContaining({
      body: { reason: "Prove domain control" },
      params: {
        path: { publisherId: publisher.id, domain: "example.com" },
        header: {
          "If-Match": '"proof-1"',
          "Idempotency-Key": expect.any(String) as unknown,
        },
      },
    }),
  );
  expect(
    w.get.mock.calls.some((call) => call[0] === statusPath + "/challenge"),
  ).toBe(false);
  click("Show TXT instructions");
  await screen.findByText(instructions().value);
  expect(
    screen.getByRole("button", { name: "Copy TXT record name" }),
  ).toBeTruthy();
  expect(screen.getByLabelText("DNS verification reason")).toHaveProperty(
    "value",
    "",
  );
  expect(document.activeElement).toBe(
    screen.getByRole("button", { name: "Refresh DNS status" }),
  );
});
it.each(["mismatch", "not_found", "unavailable", "response_limit"] as const)(
  "shows completed %s as a check failure and permits a new deliberate check",
  async (outcome) => {
    const w = setup(pending);
    await w.open();
    w.post.mockImplementation(async () => {
      const next = {
        ...pending,
        lastCheckAt: pending.challengeIssuedAt,
        lastCheckOutcome: outcome,
        lastCheckChallengeId: pending.challengeId,
      };
      w.setStatus(next);
      return etagReply(next);
    });
    change("DNS verification reason", "Check current TXT");
    await confirmCommand("Check DNS");
    await screen.findByRole("alert");
    expect(screen.getByRole("alert").textContent).not.toContain("matched");
    await waitFor(() => expect(screen.queryByRole("dialog")).toBeNull());
    change("DNS verification reason", "Query DNS again");
    expect(screen.getByRole("button", { name: "Check DNS" })).toHaveProperty(
      "disabled",
      false,
    );
    expect(w.post).toHaveBeenCalledOnce();
  },
);
it("keeps historical command proof hidden until current status is read", async () => {
  const w = setup(pending);
  await w.open();
  const read = deferred<ReturnType<typeof etagReply>>();
  w.post.mockImplementation(async () => {
    w.get.mockImplementation(async () => read.promise);
    return etagReply(proof);
  });
  change("DNS verification reason", "Verify control");
  await confirmCommand("Check DNS");
  expect(screen.queryByText("Current", { selector: "dd" })).toBeNull();
  await act(async () =>
    read.resolve(etagReply({ ...initial, challengeState: "not_issued" })),
  );
  expect(screen.getByText("Not verified", { selector: "dd" })).toBeTruthy();
});
it("clears evidence with explicit confirmation and no approval command", async () => {
  const w = setup(proof);
  await w.open();
  w.post.mockImplementation(async () => {
    w.setStatus(initial);
    return etagReply(initial);
  });
  change("DNS verification reason", "Remove old evidence");
  await confirmCommand("Clear DNS verification");
  await screen.findByText(/DNS verification cleared/);
  expect(w.post).toHaveBeenCalledWith(statusPath + ":clear", expect.anything());
  await screen.findByRole("button", { name: "Create DNS challenge" });
});
it("replaces a consumed challenge while preserving the earlier proof", async () => {
  const w = setup(proof);
  await w.open();
  w.post.mockImplementation(async () => {
    const next = {
      ...proof,
      challengeState: "pending" as const,
      challengeId: "challenge-2",
    };
    w.setStatus(next);
    return etagReply(instructions(next));
  });
  change("DNS verification reason", "Renew evidence");
  await confirmCommand("Replace DNS challenge");
  await screen.findByText("Current", { selector: "dd" });
  expect(
    screen.getByText(/last DNS check used an earlier challenge/),
  ).toBeTruthy();
});
it("blocks a locally expired challenge without querying or polling", async () => {
  const w = setup({
    ...pending,
    challengeExpiresAt: "2020-01-01T00:00:00Z",
    state: "expired",
    verifiedAt: "2019-01-01T00:00:00Z",
    expiresAt: "2020-01-01T00:00:00Z",
  });
  await w.open();
  change("DNS verification reason", "Expired");
  expect(screen.getByRole("button", { name: "Check DNS" })).toHaveProperty(
    "disabled",
    true,
  );
  expect(
    screen.queryByRole("button", { name: "Show TXT instructions" }),
  ).toBeNull();
  expect(screen.getByText("Expired or not current")).toBeTruthy();
  expect(w.get).toHaveBeenCalledTimes(2);
});
it("hides private instructions on permission loss and does not resurrect them after restoration", async () => {
  const w = setup(pending);
  await w.open();
  click("Show TXT instructions");
  await screen.findByText(instructions().value);
  change("DNS verification reason", "Preserved draft");
  const access: WorkspaceProps["access"] = {
    ...administrator,
    scopeEnforcement: true,
    scopes: ["publishers.read"],
  };
  w.view.rerender(<Harness {...w.props} access={access} />);
  expect(screen.queryByText(instructions().value)).toBeNull();
  expect(
    screen.getByText(/TXT instructions and DNS commands require/),
  ).toBeTruthy();
  w.view.rerender(<Harness {...w.props} />);
  expect(screen.queryByText(instructions().value)).toBeNull();
  expect(screen.getByLabelText("DNS verification reason")).toHaveProperty(
    "value",
    "Preserved draft",
  );
  expect(w.get).toHaveBeenCalledTimes(3);
});
it("disables new commands for unsaved publisher metadata", async () => {
  const w = setup();
  w.view.rerender(<Harness {...w.props} blocked />);
  await w.open();
  expect(screen.getByText(/Finish or discard publisher metadata/)).toBeTruthy();
  expect(
    screen.getByLabelText("DNS verification reason").closest("fieldset"),
  ).toHaveProperty("disabled", true);
});
it("refuses a stale ETag and requires an explicit status refresh", async () => {
  const w = setup(pending);
  await w.open();
  w.post.mockRejectedValue(
    new APIError(412, "PRECONDITION_FAILED", "request-1"),
  );
  change("DNS verification reason", "Check current TXT");
  await confirmCommand("Check DNS");
  await screen.findByText(/PRECONDITION_FAILED/);
  expect(w.post).toHaveBeenCalledOnce();
  click("Back to form");
  click("Refresh DNS status");
  await waitFor(() => expect(w.get).toHaveBeenCalledTimes(3));
});
it("retains the exact key, ETag and body after an uncertain outcome", async () => {
  const w = setup(pending);
  await w.open();
  w.post
    .mockRejectedValueOnce(new Error("Owned lost response"))
    .mockImplementationOnce(async () => {
      w.setStatus(proof);
      return etagReply(proof);
    });
  change("DNS verification reason", "Check current TXT");
  await confirmCommand("Check DNS");
  await screen.findByRole("button", { name: "Retry same command" });
  const original = w.post.mock.calls[0];
  await act(async () => click("Retry same command"));
  await screen.findByText("Current", { selector: "dd" });
  expect(w.post.mock.calls[1]).toEqual(original);
});
it("fences a late successful receipt after permission changes", async () => {
  const w = setup(pending);
  await w.open();
  const reply = deferred<ReturnType<typeof etagReply>>();
  w.post.mockReturnValue(reply.promise);
  change("DNS verification reason", "Check current TXT");
  await confirmCommand("Check DNS");
  w.view.rerender(
    <Harness
      {...w.props}
      access={{
        ...administrator,
        scopeEnforcement: true,
        scopes: ["publishers.read"],
      }}
    />,
  );
  await act(async () => reply.resolve(etagReply(proof)));
  await screen.findByText(/Command acknowledged. Reload with current access/);
  expect(screen.queryByText(/expected TXT value matched/)).toBeNull();
});
it("rejects mismatched status domains and changed TXT instructions", async () => {
  const w = setup(pending);
  w.get
    .mockResolvedValueOnce(
      etagReply({ governance: { publisherDomainVerification: "optional" } }),
    )
    .mockResolvedValueOnce(etagReply({ ...pending, domain: "other.test" }));
  await w.open().catch(() => undefined);
  expect(screen.queryByText("DNS proof")).toBeNull();
  expect(w.props.onFailure).toHaveBeenCalled();
});
it("warns when a challenge changes before instructions arrive", async () => {
  const w = setup(pending);
  w.setInstructions(instructions({ ...pending, challengeId: "challenge-2" }));
  await w.open();
  click("Show TXT instructions");
  await screen.findByText(/challenge changed or expired/);
  expect(screen.queryByText(instructions().value)).toBeNull();
});
it("reports a definitive command failure to the current workspace", async () => {
  const w = setup(pending);
  await w.open();
  w.post.mockRejectedValue(new APIError(403, "FORBIDDEN", "request-1"));
  change("DNS verification reason", "Check TXT");
  await confirmCommand("Check DNS");
  await waitFor(() => expect(w.props.onFailure).toHaveBeenCalled());
});
it("fences a late failure after management authority is lost", async () => {
  const w = setup(pending);
  await w.open();
  const reply = deferred<ReturnType<typeof etagReply>>();
  w.post.mockReturnValue(reply.promise);
  change("DNS verification reason", "Check TXT");
  await confirmCommand("Check DNS");
  w.view.rerender(
    <Harness
      {...w.props}
      access={{
        ...administrator,
        scopeEnforcement: true,
        scopes: ["publishers.read"],
      }}
    />,
  );
  await act(async () =>
    reply.reject(new APIError(403, "FORBIDDEN", "request-1")),
  );
  expect(w.props.onFailure).not.toHaveBeenCalled();
});

it("explains unsupported declared domains without requesting invalid verification status", async () => {
  const w = setup();
  w.view.rerender(
    <Harness {...w.props} domains={["127.0.0.1", "example.com"]} />,
  );
  click("View DNS verification");
  await screen.findByText(/DNS verification requires an exact DNS name/);
  expect(w.get).toHaveBeenCalledOnce();
  change("DNS verification domain", "example.com");
  await screen.findByText("DNS proof");
  expect(w.get).toHaveBeenCalledTimes(2);
});
it("explains a missing ETag and refuses to prepare a mutation", async () => {
  const w = setup();
  w.get
    .mockResolvedValueOnce(
      etagReply({ governance: { publisherDomainVerification: "optional" } }),
    )
    .mockResolvedValueOnce(etagReply(initial, null));
  await w.open();
  await screen.findByText(/API did not return a verification ETag/);
  change("DNS verification reason", "Prepare command");
  expect(
    screen.getByRole("button", { name: "Create DNS challenge" }),
  ).toHaveProperty("disabled", true);
  expect(w.post).not.toHaveBeenCalled();
});
it("keeps an invalid successful response uncertain instead of claiming current proof", async () => {
  const w = setup(pending);
  await w.open();
  w.post.mockResolvedValueOnce(etagReply({ ...proof, domain: "other.test" }));
  change("DNS verification reason", "Check selected domain");
  await confirmCommand("Check DNS");
  await screen.findByRole("button", { name: "Retry same command" });
  expect(screen.queryByText("Current", { selector: "dd" })).toBeNull();
  expect(w.get).toHaveBeenCalledTimes(2);
});

it("preserves a DNS reason when the user cancels reloading the Publisher", async () => {
  const w = screenWorkspace();
  w.get.mockImplementation(async (path) => {
    if (path === "/api/v1/publishers/{publisherId}")
      return etagReply(publisher);
    if (path === "/api/v1/capabilities")
      return etagReply({
        governance: { publisherDomainVerification: "optional" },
      });
    if (path === statusPath) return etagReply(initial);
    throw new Error("Unexpected route " + String(path));
  });
  render(<Publishers {...w.props} id={publisher.id} access={administrator} />);
  await screen.findByRole("button", { name: "View DNS verification" });
  click("View DNS verification");
  await screen.findByText("DNS proof");
  change("DNS verification reason", "Keep this DNS reason");
  const confirm = vi.spyOn(window, "confirm").mockReturnValue(false);
  click("Reload publisher");
  expect(confirm).toHaveBeenCalledWith(
    "Discard unsaved changes and reload this publisher?",
  );
  expect(w.get).toHaveBeenCalledTimes(3);
  expect(screen.getByLabelText("DNS verification reason")).toHaveProperty(
    "value",
    "Keep this DNS reason",
  );
});

it("does not discard an unsent DNS reason after a separate publication approval succeeds", async () => {
  const w = screenWorkspace();
  w.get.mockImplementation(async (path) => {
    if (path === "/api/v1/publishers/{publisherId}")
      return etagReply(publisher);
    if (path === "/api/v1/capabilities")
      return etagReply({
        governance: { publisherDomainVerification: "optional" },
      });
    if (path === statusPath) return etagReply(initial);
    throw new Error("Unexpected route " + String(path));
  });
  w.post.mockResolvedValue(
    etagReply({
      ...publisher,
      revision: 2,
      approvedPublicationDomains: ["example.com"],
    }),
  );
  render(<Publishers {...w.props} id={publisher.id} access={administrator} />);
  await screen.findByRole("button", { name: "View DNS verification" });
  click("View DNS verification");
  await screen.findByText("DNS proof");
  change("DNS verification reason", "Keep this unsent DNS reason");
  change("Publication approval reason", "Approve publication independently");
  await confirmCommand("Approve domain");
  await screen.findByText(/Domain approved for publication/);
  const confirm = vi.spyOn(window, "confirm").mockReturnValue(false);
  click("Reload publisher");
  expect(confirm).toHaveBeenCalledWith(
    "Discard unsaved changes and reload this publisher?",
  );
  expect(screen.getByLabelText("DNS verification reason")).toHaveProperty(
    "value",
    "Keep this unsent DNS reason",
  );
  expect(w.get).toHaveBeenCalledTimes(3);
});

it("does not move focus away from another control while command readback finishes", async () => {
  const w = setup(pending);
  await w.open();
  const read = deferred<ReturnType<typeof etagReply>>();
  w.post.mockImplementation(async () => {
    w.get.mockImplementation(async () => read.promise);
    return etagReply(proof);
  });
  change("DNS verification reason", "Check domain control");
  await confirmCommand("Check DNS");
  const other = document.createElement("button");
  other.textContent = "Another task";
  document.body.append(other);
  try {
    other.focus();
    await act(async () => read.resolve(etagReply(proof)));
    expect(document.activeElement).toBe(other);
  } finally {
    other.remove();
  }
});

it("retains exact-command recovery when a completed check omits its required outcome", async () => {
  const w = setup(pending);
  await w.open();
  w.post.mockResolvedValueOnce(etagReply(pending));
  change("DNS verification reason", "Check the selected domain");
  await confirmCommand("Check DNS");
  await screen.findByRole("button", { name: "Retry same command" });
  expect(screen.queryByText("DNS check completed.")).toBeNull();
  expect(w.get).toHaveBeenCalledTimes(2);
  const original = w.post.mock.calls[0];
  w.post.mockImplementationOnce(async () => {
    w.setStatus(proof);
    return etagReply(proof);
  });
  await act(async () => click("Retry same command"));
  await screen.findByText("Current", { selector: "dd" });
  expect(w.post.mock.calls[1]).toEqual(original);
});

it.each([
  [
    "issue",
    "Create DNS challenge",
    initial,
    { status: pending },
    pending,
    instructions(),
  ],
  [
    "check",
    "Check DNS",
    pending,
    { ...proof, lastCheckChallengeId: "another-challenge" },
    proof,
    proof,
  ],
  ["clear", "Clear DNS verification", proof, proof, initial, initial],
  [
    "clear with retained proof dates",
    "Clear DNS verification",
    proof,
    { ...initial, verifiedAt: proof.verifiedAt, expiresAt: proof.expiresAt },
    initial,
    initial,
  ],
  [
    "clear with retained challenge",
    "Clear DNS verification",
    proof,
    {
      ...initial,
      challengeId: pending.challengeId,
      challengeIssuedAt: pending.challengeIssuedAt,
      challengeExpiresAt: pending.challengeExpiresAt,
    },
    initial,
    initial,
  ],
] as const)(
  "retains exact-command recovery after an inconsistent %s success reply",
  async (_action, command, before, invalid, after, valid) => {
    const w = setup(before);
    await w.open();
    w.post.mockResolvedValueOnce(etagReply(invalid));
    change("DNS verification reason", "Recover this exact DNS command");
    await confirmCommand(command);
    await screen.findByRole("button", { name: "Retry same command" });
    expect(screen.getByLabelText("DNS verification reason")).toHaveProperty(
      "value",
      "Recover this exact DNS command",
    );
    expect(w.get).toHaveBeenCalledTimes(2);
    const original = w.post.mock.calls[0];
    w.post.mockImplementationOnce(async () => {
      w.setStatus(after);
      const reply = etagReply(valid);
      reply.response.headers.set("Idempotency-Replayed", "true");
      return reply;
    });
    await act(async () => click("Retry same command"));
    await screen.findByText(/Original command result:/);
    expect(w.post.mock.calls[1]).toEqual(original);
    expect(screen.queryByRole("dialog")).toBeNull();
    expect(screen.getByLabelText("DNS verification reason")).toHaveProperty(
      "value",
      "",
    );
    await waitFor(() => expect(w.get).toHaveBeenCalledTimes(3));
  },
);

it.each([
  ["issue", "Create DNS challenge", initial, instructions()],
  ["check", "Check DNS", pending, proof],
  ["clear", "Clear DNS verification", proof, initial],
] as const)(
  "labels replayed %s results as historical and displays current readback",
  async (_action, command, before, original) => {
    const w = setup(before);
    await w.open();
    w.post.mockImplementation(async () => {
      w.setStatus(initial);
      const response = etagReply(original);
      response.response.headers.set("Idempotency-Replayed", "true");
      return response;
    });
    change("DNS verification reason", "Recover the original command result");
    await confirmCommand(command);
    await screen.findByText(/Original command result:/);
    expect(
      screen.getByText(
        /The Catalog did not query DNS or change the evidence again/,
      ),
    ).toBeTruthy();
    await screen.findByText("Not verified", { selector: "dd" });
    expect(screen.queryByText("Current", { selector: "dd" })).toBeNull();
    expect(w.post).toHaveBeenCalledOnce();
    if (_action === "issue") {
      expect(
        screen.queryByText(/Show the current TXT instructions below/),
      ).toBeNull();
      expect(
        screen.queryByRole("button", { name: "Show TXT instructions" }),
      ).toBeNull();
    }
  },
);

it("shows a failed renewal separately from an earlier current proof", async () => {
  const renewed = {
    ...proof,
    challengeState: "pending" as const,
    challengeId: "challenge-2",
  };
  const failed = {
    ...renewed,
    lastCheckAt: new Date().toISOString(),
    lastCheckOutcome: "mismatch" as const,
    lastCheckChallengeId: "challenge-2",
  };
  const w = setup(renewed);
  await w.open();
  w.post.mockImplementation(async () => {
    w.setStatus(failed);
    return etagReply(failed);
  });
  change("DNS verification reason", "Check a renewal");
  await confirmCommand("Check DNS");
  expect(await screen.findByRole("alert")).toHaveProperty(
    "textContent",
    expect.stringContaining("expected TXT value was not present"),
  );
  await screen.findByText("Current", { selector: "dd" });
  expect(
    screen
      .getByText("Proof recorded")
      .nextElementSibling?.querySelector("time"),
  ).toHaveProperty("dateTime", proof.verifiedAt);
  expect(
    screen.getByText("Proof expires").nextElementSibling?.querySelector("time"),
  ).toHaveProperty("dateTime", proof.expiresAt);
  expect(w.post).toHaveBeenCalledOnce();
});
