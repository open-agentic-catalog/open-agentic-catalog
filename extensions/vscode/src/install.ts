import { privateMode, sameNativePath, windowsDirectory } from "./platform.js";
import { constants } from "node:fs";
import {
  mkdir,
  lstat,
  realpath,
  open,
  readdir,
  rename,
  rm,
  unlink,
} from "node:fs/promises";
import { join, resolve } from "node:path";
import { randomUUID } from "node:crypto";
import { ClientError, record } from "./config.js";
import { decodeJSON } from "./json.js";
import {
  inventory,
  packageDigest,
  packageHash,
  packageLimits,
  packageFail,
  skillName,
  verifyPackage,
  type SkillPlan,
  type PackageFile,
} from "./packages.js";
import { pluginName } from "./plugins.js";
import type { PackageContent } from "./archive.js";

export interface SkillReceipt {
  name: string;
  identity: string;
  binding: string;
  files: PackageFile[];
}
export type SkillSummary = Omit<SkillReceipt, "files"> & {
  fingerprint: string;
};
export interface ReceiptIssue {
  entry: string;
  code: string;
}
export function receiptSummary(r: SkillReceipt): SkillSummary {
  return {
    name: r.name,
    identity: r.identity,
    binding: r.binding,
    fingerprint: packageHash(JSON.stringify(r)),
  };
}
// 1,024 paths of up to 1,024 UTF-8 bytes plus digests and JSON fields.
const receiptBytes = 2 * 1024 * 1024;
interface Journal {
  stage: string;
  backup: string;
  before?: SkillReceipt;
  after?: SkillReceipt;
}
async function exists(path: string): Promise<boolean> {
  try {
    await lstat(path);
    return true;
  } catch (e) {
    if ((e as NodeJS.ErrnoException).code === "ENOENT") return false;
    throw e;
  }
}
async function directory(path: string, create = false): Promise<void> {
  let created = false;
  if (create)
    try {
      await mkdir(path, { mode: 0o700 });
      created = true;
    } catch (e) {
      if ((e as NodeJS.ErrnoException).code !== "EEXIST") throw e;
    }
  const info = await lstat(path);
  if (
    !info.isDirectory() ||
    !privateMode(info, true) ||
    info.isSymbolicLink() ||
    !sameNativePath(await realpath(path), path)
  )
    return packageFail("unsafe_installation_path");
  await windowsDirectory(path, created);
}
async function read(path: string, limit: number): Promise<Buffer> {
  const handle = await open(
    path,
    constants.O_RDONLY | constants.O_NOFOLLOW | constants.O_NONBLOCK,
  );
  try {
    const info = await handle.stat(),
      current = await lstat(path);
    if (
      current.isSymbolicLink() ||
      current.ino !== info.ino ||
      current.dev !== info.dev ||
      !info.isFile() ||
      info.nlink !== 1 ||
      info.size > limit
    )
      return packageFail("unsafe_installation_path");
    const bytes = Buffer.alloc(limit + 1);
    let size = 0;
    while (size < bytes.length) {
      const next = await handle.read(bytes, size, bytes.length - size, size);
      if (!next.bytesRead) break;
      size += next.bytesRead;
    }
    if (size > limit) return packageFail("unsafe_installation_path");
    return bytes.subarray(0, size);
  } finally {
    await handle.close();
  }
}
function receipt(value: unknown, plugin = false): SkillReceipt {
  const r = record(value),
    name = plugin ? pluginName(r.name) : skillName(r.name);
  return {
    name,
    identity: packageDigest(r.identity),
    binding: packageDigest(r.binding),
    files: inventory(r.files, name, plugin ? "plugin.json" : "SKILL.md"),
  };
}
async function save(path: string, value: unknown): Promise<void> {
  const temporary = path + "." + randomUUID();
  const handle = await open(temporary, "wx", 0o600);
  try {
    await handle.writeFile(JSON.stringify(value));
    await handle.sync();
  } finally {
    await handle.close();
  }
  try {
    await rename(temporary, path);
  } finally {
    await rm(temporary, { force: true });
  }
}
/** Linux execution-host filesystem only. This is not a workspace.fs atomicity claim. */
export class SkillStore {
  readonly state: string;
  readonly destination: string;
  constructor(
    readonly workspace: string,
    readonly kind: "skill" | "plugin" = "skill",
  ) {
    this.state = join(
      workspace,
      ".oac-catalog",
      kind === "plugin" ? "plugins" : "skills",
    );
    this.destination =
      kind === "plugin"
        ? join(this.state, "installed")
        : join(workspace, ".agents", "skills");
  }
  private async prepare(create: boolean): Promise<boolean> {
    if (resolve(this.workspace) !== this.workspace)
      return packageFail("unsafe_installation_path");
    await directory(this.workspace);
    for (const path of [
      join(this.workspace, ".oac-catalog"),
      this.state,
      join(this.state, "receipts"),
    ]) {
      if (!create && !(await exists(path))) return false;
      await directory(path, create);
    }
    return true;
  }
  private async destinations(): Promise<void> {
    if (this.kind === "skill")
      await directory(join(this.workspace, ".agents"), true);
    await directory(this.destination, true);
    if ((await lstat(this.state)).dev !== (await lstat(this.destination)).dev)
      return packageFail("installation_filesystem_mismatch");
  }
  async list(): Promise<SkillSummary[]> {
    const result = await this.scan();
    if (result.issues.length) return packageFail(result.issues[0]!.code);
    return result.items;
  }
  /** Report each invalid receipt independently; it never grants tree ownership. */
  async scan(): Promise<{ items: SkillSummary[]; issues: ReceiptIssue[] }> {
    if (!(await this.prepare(false))) return { items: [], issues: [] };
    const entries = await readdir(join(this.state, "receipts"));
    if (entries.length > 100) return packageFail("installation_limit");
    const items: SkillSummary[] = [],
      issues: ReceiptIssue[] = [];
    for (const entry of entries.sort()) {
      try {
        if (!entry.endsWith(".json"))
          return packageFail("unsafe_installation_path");
        const r = await this.get(entry.slice(0, -5));
        if (!r || entry !== r.name + ".json")
          return packageFail("unsafe_installation_path");
        items.push(receiptSummary(r));
      } catch (error) {
        issues.push({
          entry: JSON.stringify(entry),
          code:
            error instanceof ClientError ? error.code : "receipt_unavailable",
        });
      }
    }
    return { items, issues };
  }
  async get(name: string): Promise<SkillReceipt | undefined> {
    if (this.kind === "plugin") pluginName(name);
    else skillName(name);
    if (!(await this.prepare(false))) return undefined;
    const path = join(this.state, "receipts", name + ".json");
    if (!(await exists(path))) return undefined;
    const result = receipt(
      decodeJSON((await read(path, receiptBytes)).toString("utf8")),
      this.kind === "plugin",
    );
    if (result.name !== name) return packageFail("unsafe_installation_path");
    return result;
  }
  private async checkTree(
    path: string,
    r: SkillReceipt,
  ): Promise<PackageContent> {
    await directory(path);
    const expected = new Map(
      r.files.map((f) => [f.path.slice(r.name.length + 1).normalize("NFC"), f]),
    );
    const content: PackageContent = new Map();
    let count = 0;
    const walk = async (parent: string, prefix: string) => {
      for (const name of await readdir(parent)) {
        if (++count > packageLimits.files * (packageLimits.depth + 1))
          return packageFail("local_skill_changed");
        const relative = (prefix + name).normalize("NFC"),
          child = join(parent, name),
          info = await lstat(child);
        if (info.isSymbolicLink()) return packageFail("local_skill_changed");
        if (info.isDirectory()) {
          if (![...expected.keys()].some((p) => p.startsWith(relative + "/")))
            return packageFail("local_skill_changed");
          await walk(child, relative + "/");
        } else {
          const file = expected.get(relative);
          if (
            !file ||
            !info.isFile() ||
            info.nlink !== 1 ||
            info.size !== file.size ||
            !privateMode(info, false, file.executable)
          )
            return packageFail("local_skill_changed");
          content.set(file.path, {
            bytes: await read(child, file.size),
            executable: file.executable,
          });
        }
      }
    };
    await walk(path, "");
    try {
      verifyPackage(r, content);
    } catch {
      return packageFail("local_skill_changed");
    }
    return content;
  }
  async content(name: string): Promise<PackageContent> {
    const r = await this.get(name);
    if (!r) return packageFail("owned_package_required");
    await this.destinations();
    return this.checkTree(join(this.destination, name), r);
  }
  /** Check before acquisition. Identical installations cause no network or writes. */
  async inspect(
    plan: SkillPlan,
  ): Promise<{ identical: boolean; previous?: SkillReceipt }> {
    const previous = await this.get(plan.name);
    if (await exists(join(this.state, "journal.json")))
      return packageFail("installation_recovery_required");
    const target = join(this.destination, plan.name);
    if (previous) {
      if (this.kind === "skill")
        await directory(join(this.workspace, ".agents"));
      await directory(this.destination);
      if (previous.binding !== plan.binding)
        return packageFail("installation_account_changed");
      if (
        previous.identity === plan.identity &&
        JSON.stringify(previous.files) !== JSON.stringify(plan.files)
      )
        return packageFail("local_skill_changed");
      await this.checkTree(target, previous);
      return { identical: previous.identity === plan.identity, previous };
    }
    // Check ancestors even when there is no receipt: never adopt an existing tree.
    if (await exists(join(this.workspace, ".agents")))
      if (this.kind === "skill")
        await directory(join(this.workspace, ".agents"));
    if (await exists(this.destination)) await directory(this.destination);
    if (await exists(target)) return packageFail("skill_destination_exists");
    return { identical: false };
  }
  async temporary(): Promise<string> {
    await this.prepare(true);
    const path = join(this.state, "acquire-" + randomUUID());
    await directory(path, true);
    return path;
  }
  private async locked<T>(action: () => Promise<T>): Promise<T> {
    await this.prepare(true);
    const path = join(this.state, "lock");
    let handle;
    try {
      handle = await open(path, "wx", 0o600);
    } catch {
      return packageFail("installation_busy_or_interrupted");
    }
    try {
      return await action();
    } finally {
      await handle.close();
      await unlink(path);
    }
  }
  private async confirmed(expected: SkillReceipt): Promise<void> {
    const current = await this.get(expected.name);
    if (!current || JSON.stringify(current) !== JSON.stringify(expected))
      return packageFail("selection_changed");
  }
  /** Fence native configuration against replacement by another installation window. */
  async withReceipt(
    expected: SkillReceipt,
    action: () => Promise<void>,
    guard: () => void,
  ): Promise<void> {
    await this.locked(async () => {
      await this.confirmed(expected);
      guard();
      await action();
      guard();
    });
  }
  /** Read-only, verified, contained target for native Explorer; never adopts files. */
  async ownedPath(expected: SkillReceipt): Promise<string> {
    await this.confirmed(expected);
    if (this.kind === "skill") await directory(join(this.workspace, ".agents"));
    await directory(this.destination);
    const target = join(this.destination, expected.name);
    await this.checkTree(target, expected);
    await this.confirmed(expected);
    return target;
  }
  async validateRecoveryDirectory(): Promise<void> {
    if (resolve(this.workspace) !== this.workspace)
      return packageFail("unsafe_installation_path");
    await directory(this.workspace);
    for (const path of [
      join(this.workspace, ".oac-catalog"),
      this.state,
      this.destination,
      join(this.state, "receipts"),
    ])
      if (await exists(path)) await directory(path);
  }
  /** Native Plugin enablement must exclude another window's replacement/removal. */
  async activatePlugin(
    plan: SkillPlan,
    authorize: () => Promise<void>,
    enable: () => Promise<void>,
    guard: () => void = () => {},
  ): Promise<void> {
    if (this.kind !== "plugin" || plan.kind !== "plugin")
      return packageFail("package_kind_mismatch");
    await this.locked(async () => {
      guard();
      await authorize();
      guard();
      if (!(await this.inspect(plan)).identical)
        return packageFail("install_selected_plugin_first");
      guard();
      await enable();
    });
  }
  async install(
    plan: SkillPlan,
    content: PackageContent,
    authorize: () => Promise<void>,
    guard: () => void = () => {},
  ): Promise<void> {
    if ((plan.kind === "plugin") !== (this.kind === "plugin"))
      return packageFail("package_kind_mismatch");
    verifyPackage(plan, content);
    await this.locked(async () => {
      const status = await this.inspect(plan);
      if (status.identical) return;
      if (!status.previous && (await this.list()).length >= 100)
        return packageFail("installation_limit");
      await this.destinations();
      const stage = "stage-" + randomUUID(),
        backup = "backup-" + randomUUID(),
        path = join(this.state, stage);
      const after: SkillReceipt = {
        name: plan.name,
        identity: plan.identity,
        binding: plan.binding,
        files: plan.files,
      };
      await directory(path, true);
      let journaled = false;
      try {
        for (const file of plan.files) {
          const relative = file.path.slice(plan.name.length + 1),
            parts = relative.split("/");
          let parent = path;
          for (const part of parts.slice(0, -1)) {
            parent = join(parent, part);
            await directory(parent, true);
          }
          const destination = join(path, relative),
            handle = await open(
              destination,
              "wx",
              file.executable ? 0o700 : 0o600,
            );
          try {
            await handle.writeFile(content.get(file.path)!.bytes);
            if (process.platform !== "win32")
              await handle.chmod(file.executable ? 0o700 : 0o600);
            await handle.sync();
          } finally {
            await handle.close();
          }
        }
        await this.checkTree(path, after);
        await authorize();
        if (status.previous)
          await this.checkTree(
            join(this.destination, plan.name),
            status.previous,
          );
        else if (await exists(join(this.destination, plan.name)))
          return packageFail("skill_destination_exists");
        await this.destinations();
        guard();
        const journal: Journal = {
          stage,
          backup,
          ...(status.previous ? { before: status.previous } : {}),
          after,
        };
        await save(join(this.state, "journal.json"), journal);
        journaled = true;
        guard();
        if (status.previous)
          await rename(
            join(this.destination, plan.name),
            join(this.state, backup),
          );
        guard();
        await rename(path, join(this.destination, plan.name));
        await this.finish(journal);
      } finally {
        if (!journaled) await rm(path, { recursive: true, force: true });
      }
    });
  }
  async remove(
    name: string,
    guard: () => void = () => {},
    expected?: SkillReceipt,
    beforeRemove?: () => Promise<void>,
    afterRemove?: () => Promise<void>,
  ): Promise<void> {
    if (this.kind === "plugin") pluginName(name);
    else skillName(name);
    await this.locked(async () => {
      if (await exists(join(this.state, "journal.json")))
        return packageFail("installation_recovery_required");
      const before = await this.get(name);
      if (!before) return packageFail("owned_skill_required");
      if (expected) {
        if (expected.name !== name) return packageFail("selection_changed");
        await this.confirmed(expected);
      }
      await this.destinations();
      await this.checkTree(join(this.destination, name), before);
      guard();
      await beforeRemove?.();
      guard();
      const j: Journal = {
        stage: "stage-" + randomUUID(),
        backup: "backup-" + randomUUID(),
        before,
      };
      await save(join(this.state, "journal.json"), j);
      guard();
      await rename(join(this.destination, name), join(this.state, j.backup));
      await this.finish(j);
      await afterRemove?.();
    });
  }
  private async finish(
    j: Journal,
    guard: () => void = () => {},
  ): Promise<void> {
    const r = j.after ?? j.before!;
    guard();
    if (j.after)
      await save(join(this.state, "receipts", r.name + ".json"), j.after);
    else
      await rm(join(this.state, "receipts", r.name + ".json"), { force: true });
    const backup = join(this.state, j.backup);
    if (await exists(backup)) {
      if (!j.before) return packageFail("installation_recovery_required");
      await this.checkTree(backup, j.before);
      guard();
      await rm(backup, { recursive: true });
    }
    guard();
    await unlink(join(this.state, "journal.json"));
  }
  private async discardTemporary(guard: () => void): Promise<void> {
    const entries = await readdir(this.state);
    if (entries.length > 210)
      return packageFail("installation_recovery_required");
    for (const name of entries)
      if (/^(?:stage|acquire)-[a-f0-9-]{36}$/u.test(name)) {
        const path = join(this.state, name);
        await directory(path);
        guard();
        await rm(path, { recursive: true });
      }
  }
  /** Caller must explicitly confirm that no other window is installing. */
  async recover(guard: () => void = () => {}): Promise<void> {
    if (!(await this.prepare(false))) return;
    const lock = join(this.state, "lock");
    if (await exists(lock)) {
      await read(lock, 0);
      guard();
      await unlink(lock);
    }
    guard();
    await this.locked(async () => {
      guard();
      const journal = join(this.state, "journal.json");
      if (!(await exists(journal))) {
        await this.discardTemporary(guard);
        return;
      }
      const v = record(
        decodeJSON(
          (await read(journal, 2 * receiptBytes + 1024)).toString("utf8"),
        ),
      );
      const id = (value: unknown, prefix: string) => {
        if (
          typeof value !== "string" ||
          !new RegExp("^" + prefix + "-[a-f0-9-]{36}$", "u").test(value)
        )
          return packageFail("unsafe_installation_path");
        return value;
      };
      const j: Journal = {
        stage: id(v.stage, "stage"),
        backup: id(v.backup, "backup"),
        ...(v.before
          ? { before: receipt(v.before, this.kind === "plugin") }
          : {}),
        ...(v.after ? { after: receipt(v.after, this.kind === "plugin") } : {}),
      };
      if (
        (!j.before && !j.after) ||
        (j.before && j.after && j.before.name !== j.after.name)
      )
        return packageFail("unsafe_installation_path");
      guard();
      await this.destinations();
      const target = join(this.destination, (j.after ?? j.before)!.name),
        stage = join(this.state, j.stage),
        backup = join(this.state, j.backup);
      if (await exists(target)) {
        // Determine the side using exact contents. Never delete an unexpected local tree.
        let isNew = false;
        if (j.after)
          try {
            await this.checkTree(target, j.after);
            isNew = true;
          } catch {
            /* The old tree may still be present. */
          }
        // Identical files cannot prove activation. After a rollback removes its
        // stage, only a retained backup or completed receipt proves the switch.
        if (
          isNew &&
          !(await exists(stage)) &&
          (!j.before ||
            (await exists(backup)) ||
            JSON.stringify(await this.get(j.after!.name)) ===
              JSON.stringify(j.after))
        ) {
          await this.finish(j, guard);
          await this.discardTemporary(guard);
          return;
        }
        if (!j.before || (await exists(backup)))
          return packageFail("local_skill_changed");
        await this.checkTree(target, j.before);
      } else if (j.before && (await exists(backup))) {
        if (!j.after) {
          await this.finish(j, guard);
          await this.discardTemporary(guard);
          return;
        }
        await this.checkTree(backup, j.before);
        guard();
        await rename(backup, target);
      } else if (j.before) return packageFail("installation_recovery_required");
      if (await exists(stage)) {
        if (!j.after) return packageFail("installation_recovery_required");
        await this.checkTree(stage, j.after);
        guard();
        await rm(stage, { recursive: true });
      }
      guard();
      if (j.before)
        await save(
          join(this.state, "receipts", j.before.name + ".json"),
          j.before,
        );
      else if (j.after)
        await rm(join(this.state, "receipts", j.after.name + ".json"), {
          force: true,
        });
      guard();
      await unlink(journal);
      await this.discardTemporary(guard);
    });
  }
}
