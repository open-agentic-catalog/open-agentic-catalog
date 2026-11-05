import { ClientError, record } from "./config.js";
import { exactReference, type Store } from "./browse.js";

export interface McpReference {
  reference: string;
  document: number;
  alternative: number;
  digest: string;
  mode: "control" | "published";
}
function validate(value: unknown): McpReference {
  const v = record(value);
  if (
    Object.keys(v).sort().join() !==
      "alternative,digest,document,mode,reference" ||
    !["control", "published"].includes(v.mode as string) ||
    !Number.isInteger(v.document) ||
    (v.document as number) < 0 ||
    (v.document as number) >= 32 ||
    !Number.isInteger(v.alternative) ||
    (v.alternative as number) < 0 ||
    (v.alternative as number) >= 64 ||
    typeof v.digest !== "string" ||
    !/^[a-f0-9]{64}$/u.test(v.digest) ||
    typeof v.reference !== "string" ||
    Buffer.byteLength(v.reference) > 8192
  )
    throw new ClientError("invalid_mcp_references");
  if (v.mode === "control") exactReference(v.reference);
  else if (v.reference !== "fresh-search-required")
    throw new ClientError("invalid_mcp_references");
  return {
    reference: v.reference,
    document: v.document,
    alternative: v.alternative,
    digest: v.digest,
    mode: v.mode,
  } as McpReference;
}
/** Metadata only: no query, cursor, executable recipe, cached document or inputs. */
export class McpReferences {
  private writes = Promise.resolve();
  constructor(private store: Store) {}
  private all(): Record<string, McpReference[]> {
    const all = record(this.store.get("catalogMcpReferences") ?? {});
    if (Object.keys(all).length > 20)
      throw new ClientError("mcp_reference_account_limit");
    const result: Record<string, McpReference[]> = {};
    for (const [account, values] of Object.entries(all)) {
      if (
        !/^[a-f0-9]{64}$/u.test(account) ||
        !Array.isArray(values) ||
        values.length > 20
      )
        throw new ClientError("invalid_mcp_references");
      if (values.length) result[account] = values.map(validate);
    }
    return result;
  }
  list(account: string): McpReference[] {
    return structuredClone(this.all()[account] ?? []);
  }
  private update(action: (all: Record<string, McpReference[]>) => void) {
    const next = this.writes.then(async () => {
      const all = this.all();
      action(all);
      await this.store.update("catalogMcpReferences", all);
    });
    this.writes = next.catch(() => {});
    return next;
  }
  add(account: string, value: McpReference, check: () => void) {
    if (!/^[a-f0-9]{64}$/u.test(account))
      throw new ClientError("invalid_mcp_references");
    const selected = validate(value);
    return this.update((all) => {
      check();
      const values = all[account] ?? [];
      if (!all[account] && Object.keys(all).length >= 20)
        throw new ClientError("mcp_reference_account_limit");
      if (values.some((v) => JSON.stringify(v) === JSON.stringify(selected)))
        return;
      if (values.length >= 20) throw new ClientError("mcp_reference_limit");
      all[account] = [...values, selected];
    });
  }
  remove(account: string, value: McpReference, check: () => void) {
    return this.update((all) => {
      check();
      const remaining = (all[account] ?? []).filter(
        (v) => JSON.stringify(v) !== JSON.stringify(value),
      );
      if (remaining.length) all[account] = remaining;
      else delete all[account];
    });
  }
  forget() {
    return this.update((all) => {
      for (const key of Object.keys(all)) delete all[key];
    });
  }
}
