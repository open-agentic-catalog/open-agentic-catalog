import { randomUUID } from "node:crypto";
import { ClientError, record } from "./config.js";
import { decodeJSON } from "./json.js";
import { metadataText, uuid, kinds, type Item } from "./browse.js";
import type { Session } from "./session.js";

export type OpinionFamily = "ratings" | "feedback";
export const feedbackCategories = [
  "problem",
  "documentation",
  "suggestion",
  "positive",
  "other",
] as const;
export function feedbackText(value: unknown): string {
  const result = metadataText(value, 4000);
  if (!result.trim()) throw new ClientError("invalid_feedback");
  return result;
}
export function localRelease(item: Item): string {
  if (
    item.origin !== "local" ||
    !item.releaseId ||
    !kinds.includes(item.kind as (typeof kinds)[number])
  )
    throw new ClientError("local_release_required");
  return uuid(item.releaseId);
}
/** Private opinions never enter globalState, logs, exports or public projections. */
export class Opinions {
  private pending:
    | {
        family: OpinionFamily;
        path: string;
        method: "POST" | "PATCH";
        body: string;
        key: string;
        etag?: string;
        binding: string;
        started: number;
        wallStarted: number;
        uncertain: boolean;
      }
    | undefined;
  private sending = false;
  constructor(
    private session: Session,
    private now: () => number = () => performance.now(),
    private wallNow: () => number = () => Date.now(),
  ) {}
  hasPending(): boolean {
    if (!this.pending) return false;
    if (this.pending?.binding !== this.session.consumerBinding())
      this.pending = undefined;
    return !!this.pending;
  }
  private control() {
    if (this.session.connection().mode !== "control")
      throw new ClientError("control_mode_required");
    return this.session.guard(this.session.connection());
  }
  async page(family: OpinionFamily, cursor?: string) {
    const check = this.control();
    const params = new URLSearchParams({ limit: "25" });
    if (cursor) params.set("cursor", metadataText(cursor, 2048));
    const data = await this.session.json(`/api/v1/${family}?${params}`);
    check();
    if (!Array.isArray(data.items) || data.items.length > 25)
      throw new ClientError("invalid_response");
    return {
      items: data.items.map((value) => this.opinion(family, value)),
      ...(data.nextCursor ? { next: metadataText(data.nextCursor, 2048) } : {}),
    };
  }
  private opinion(family: OpinionFamily, input: unknown) {
    const value = record(input);
    uuid(value.id);
    if (
      !Number.isSafeInteger(value.revision) ||
      (value.revision as number) < 1 ||
      !(
        family === "ratings"
          ? ["active", "withdrawn", "removed"]
          : ["open", "addressed", "withdrawn", "removed"]
      ).includes(value.state as string)
    )
      throw new ClientError("invalid_response");
    if (family === "feedback") {
      if (
        !feedbackCategories.includes(
          value.category as (typeof feedbackCategories)[number],
        )
      )
        throw new ClientError("invalid_response");
      if (value.comment !== undefined) feedbackText(value.comment);
      if (value.response !== undefined)
        metadataText(value.response, 4000, true);
    } else if (
      typeof value.consent !== "boolean" ||
      !Number.isInteger(value.score) ||
      (value.score as number) < 0 ||
      (value.score as number) > 5
    )
      throw new ClientError("invalid_response");
    return value;
  }
  async get(family: OpinionFamily, id: string) {
    const check = this.control();
    const response = await this.session.read(`/api/v1/${family}/${uuid(id)}`);
    check();
    const value = this.opinion(family, decodeJSON(response.text())),
      etag = response.headers.get("etag");
    if (!etag || !/^"rev-[1-9][0-9]*"$/u.test(etag))
      throw new ClientError("invalid_response");
    return { value, etag };
  }
  async rating(item: Item) {
    const releaseId = localRelease(item),
      check = this.control();
    // Fresh exact release evidence; do not infer mutable IDs from the search row.
    const release = await this.session.json(
      `/api/v1/catalog/resources/${releaseId}`,
    );
    check();
    const resource = record(release.resource),
      version = record(release.versionRevision);
    if (
      resource.canonicalIdentifier !== item.identifier ||
      resource.kind !== item.kind ||
      record(release.version).version !== item.version ||
      uuid(record(release.release).id) !== releaseId
    )
      throw new ClientError("selection_changed");
    const target = {
      origin: "local",
      resourceId: uuid(resource.id),
      versionRevisionId: uuid(version.id),
      identifier: item.identifier,
      kind: item.kind,
      versionPresent: true,
      version: item.version,
    };
    const view = await this.session.json("/api/v1/ratings/read", {
      method: "POST",
      headers: { "content-type": "application/json" },
      body: JSON.stringify({ target, releaseId }),
    });
    check();
    const own = view.own
      ? await this.get("ratings", uuid(record(view.own).id))
      : undefined;
    check();
    return { target, releaseId, own };
  }
  async feedbackContext(item: Item) {
    const check = this.control(),
      releaseId = localRelease(item);
    const release = await this.session.json(
      `/api/v1/catalog/resources/${releaseId}`,
    );
    check();
    const resource = record(release.resource),
      version = record(release.versionRevision);
    if (
      resource.canonicalIdentifier !== item.identifier ||
      record(release.version).version !== item.version ||
      uuid(record(release.release).id) !== releaseId ||
      resource.kind !== item.kind
    )
      throw new ClientError("selection_changed");
    return {
      resourceId: uuid(resource.id),
      releaseId,
      versionRevisionId: uuid(version.id),
    };
  }
  async submitRating(
    context: Awaited<ReturnType<Opinions["rating"]>>,
    score: number,
    consent?: boolean,
  ) {
    if (
      !Number.isInteger(score) ||
      score < 1 ||
      score > 5 ||
      (consent !== undefined && typeof consent !== "boolean")
    )
      throw new ClientError("invalid_rating");
    const sharing =
      consent ??
      (context.own?.value.state === "active" &&
        context.own.value.consent === true);
    return this.mutate(
      "ratings",
      "/api/v1/ratings",
      "POST",
      {
        target: context.target,
        releaseId: context.releaseId,
        score,
        consent: sharing,
      },
      context.own?.etag,
    );
  }
  async submitFeedback(
    context: Record<string, unknown>,
    category: string,
    comment: string,
    existing?: { id: string; etag: string },
  ) {
    if (
      !feedbackCategories.includes(
        category as (typeof feedbackCategories)[number],
      )
    )
      throw new ClientError("invalid_feedback");
    const body = {
      category,
      comment: feedbackText(comment),
      ...(existing ? {} : context),
    };
    return this.mutate(
      "feedback",
      `/api/v1/feedback${existing ? "/" + uuid(existing.id) : ""}`,
      existing ? "PATCH" : "POST",
      body,
      existing?.etag,
    );
  }
  async withdraw(
    family: OpinionFamily,
    current: { value: Record<string, unknown>; etag: string },
  ) {
    return this.mutate(
      family,
      `/api/v1/${family}/${uuid(current.value.id)}:withdraw`,
      "POST",
      {},
      current.etag,
    );
  }
  private async mutate(
    family: OpinionFamily,
    path: string,
    method: "POST" | "PATCH",
    body: unknown,
    etag?: string,
  ) {
    this.control();
    if (this.sending) throw new ClientError("opinion_command_in_progress");
    if (this.hasPending()) throw new ClientError("opinion_outcome_uncertain");
    const encoded = JSON.stringify(body);
    if (Buffer.byteLength(encoded) > (family === "feedback" ? 65536 : 8192))
      throw new ClientError("opinion_body_limit");
    this.pending = {
      family,
      path,
      method,
      body: encoded,
      key: randomUUID(),
      binding: this.session.consumerBinding(),
      started: this.now(),
      wallStarted: this.wallNow(),
      uncertain: false,
      ...(etag ? { etag } : {}),
    };
    return this.recover();
  }
  /** Explicit replay of the identical in-memory command only; no blind new write. */
  async recover() {
    const check = this.control();
    if (!this.hasPending()) throw new ClientError("no_pending_opinion");
    const command = this.pending!;
    if (this.sending) throw new ClientError("opinion_command_in_progress");
    // The API retains completed commands for 24 hours. Keep this window's
    // explicit recovery much shorter. Monotonic clocks can pause during sleep;
    // require both budgets and refuse backwards clock steps as well.
    const elapsed = this.now() - command.started,
      wallElapsed = this.wallNow() - command.wallStarted;
    if (
      elapsed < 0 ||
      wallElapsed < 0 ||
      elapsed > 10 * 60 * 1000 ||
      wallElapsed > 10 * 60 * 1000
    )
      throw new ClientError("opinion_recovery_expired");
    this.sending = true;
    try {
      const result = await this.session.read(
        command.path,
        {
          method: command.method,
          headers: {
            "content-type": "application/json",
            "Idempotency-Key": command.key,
            ...(command.etag ? { "If-Match": command.etag } : {}),
          },
          body: command.body,
        },
        [200, 201],
      );
      check();
      const receipt = record(decodeJSON(result.text()));
      uuid(receipt.id);
      if (
        !Number.isSafeInteger(receipt.revision) ||
        (receipt.revision as number) < 1
      )
        throw new ClientError("invalid_response");
      this.pending = undefined;
      return receipt;
    } catch (error) {
      if (
        !command.uncertain &&
        error instanceof ClientError &&
        error.status &&
        error.status >= 400 &&
        error.status < 500 &&
        error.status !== 408 &&
        error.status !== 429
      )
        this.pending = undefined;
      else command.uncertain = true;
      throw error;
    } finally {
      this.sending = false;
    }
  }
  clear() {
    this.pending = undefined;
  }
}
