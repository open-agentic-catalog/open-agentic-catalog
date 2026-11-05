import { required } from "../../tests/required";
import { describe, expect, it, vi } from "vitest";
import { APIError } from "./client";
import { domainsFromInput, prepareCommand, uncertain } from "./commands";

describe("Deliberate command submission", () => {
  it("retains one key for an exact retry after a lost response", async () => {
    const send = vi
      .fn(async (_key: string) => "done")
      .mockRejectedValueOnce(new TypeError("fetch failed"));
    const command = prepareCommand(send);
    await expect(command()).rejects.toThrow("fetch failed");
    await expect(command()).resolves.toBe("done");
    expect(required(send.mock.calls[0])[0]).toBe(
      required(send.mock.calls[1])[0],
    );
    await prepareCommand(send)();
    expect(required(send.mock.calls[2])[0]).not.toBe(
      required(send.mock.calls[0])[0],
    );
  });
  it("rejects concurrent submits without issuing a second request", async () => {
    let finish!: (value: string) => void;
    const send = vi.fn(
      () =>
        new Promise<string>((resolve) => {
          finish = resolve;
        }),
    );
    const command = prepareCommand(send);
    const first = command();
    await expect(command()).rejects.toThrow("already pending");
    expect(send).toHaveBeenCalledTimes(1);
    finish("done");
    await expect(first).resolves.toBe("done");
  });
  it("separates uncertain transport/command outcomes from a definite rejection", () => {
    for (const error of [
      new TypeError(),
      new APIError(200, "missing_response_body"),
      new APIError(500, "internal"),
      new APIError(502, "gateway"),
      new APIError(503, "DEPENDENCY_UNAVAILABLE"),
      new APIError(500, "WEBHOOK_TEST_BUSY"),
      new APIError(409, "IDEMPOTENCY_IN_PROGRESS"),
      new APIError(429, "limited"),
      new APIError(408, "timeout"),
    ])
      expect(uncertain(error)).toBe(true);
    expect(uncertain(new APIError(503, "WEBHOOK_TEST_BUSY"))).toBe(false);
    for (const status of [400, 401, 403, 404, 412, 422])
      expect(uncertain(new APIError(status, "rejected"))).toBe(false);
  });
  it("releases the pending guard after a synchronous sender failure", async () => {
    const send = vi.fn((_key: string): Promise<string> => {
      throw new Error("sender failed before returning a promise");
    });
    const command = prepareCommand(send);
    await expect(command()).rejects.toThrow("sender failed");
    send.mockImplementationOnce(async () => "recovered");
    await expect(command()).resolves.toBe("recovered");
    expect(send).toHaveBeenCalledTimes(2);
    expect(required(send.mock.calls[1])[0]).toBe(
      required(send.mock.calls[0])[0],
    );
  });
  it("bounds domain input and accepts whitespace or comma separation", () => {
    expect(domainsFromInput(" example.com,\nexample.org example.com ")).toEqual(
      ["example.com", "example.org"],
    );
    expect(() => domainsFromInput(" , ")).toThrow("1 and 64");
    const maximum = Array.from({ length: 64 }, (_, i) => `d${i}.example`);
    expect(domainsFromInput(maximum.join(","))).toEqual(maximum);
    expect(() =>
      domainsFromInput(
        Array.from({ length: 65 }, (_, i) => `d${i}.example`).join(","),
      ),
    ).toThrow("1 and 64");
  });
});
