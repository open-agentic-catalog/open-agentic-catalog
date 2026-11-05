import { createHash, randomUUID } from "node:crypto";
import { constants } from "node:fs";
import {
  link,
  mkdir,
  mkdtemp,
  open,
  rm,
  writeFile,
  lstat,
  unlink,
  rename,
} from "node:fs/promises";
import { join } from "node:path";
import { ClientError } from "./config.js";

export interface CredentialAccess {
  run<T>(
    key: string,
    action: () => Promise<T>,
    uncertain?: boolean,
  ): Promise<T>;
  recover(key: string, clear: () => Promise<void>): Promise<void>;
  current(key: string, value: string | undefined): Promise<boolean>;
  record(key: string, value: string | undefined): Promise<void>;
}
/** One host-local credential writer; a busy/crashed owner is never stolen. */
export function credentialAccess(directory: string): CredentialAccess {
  const path = (key: string) =>
    join(
      directory,
      `.credential-${createHash("sha256").update(key).digest("hex")}.lock`,
    );
  const revision = (value: string | undefined) =>
    createHash("sha256")
      .update(value ?? "")
      .digest("hex");
  const state = (key: string) => path(key).replace(/\.lock$/u, ".state");
  const record = async (key: string, value: string | undefined) => {
    const staging = state(key) + "." + randomUUID();
    try {
      await writeFile(staging, revision(value), { flag: "wx", mode: 0o600 });
      await rename(staging, state(key));
    } finally {
      await rm(staging, { force: true });
    }
  };
  return {
    record,
    async current(key, value) {
      let file;
      try {
        file = await open(
          state(key),
          constants.O_RDONLY |
            (constants.O_NOFOLLOW ?? 0) |
            (constants.O_NONBLOCK ?? 0),
        );
      } catch (error) {
        if ((error as NodeJS.ErrnoException).code === "ENOENT") return true;
        throw new ClientError("host_storage_unavailable");
      }
      try {
        const info = await file.stat();
        if (!info.isFile() || info.size !== 64 || info.nlink !== 1)
          throw new ClientError("host_storage_unavailable");
        const stored = await file.readFile("utf8");
        if (!/^[a-f0-9]{64}$/u.test(stored))
          throw new ClientError("host_storage_unavailable");
        return stored === revision(value);
      } finally {
        await file.close();
      }
    },
    async run(key, action, uncertain = false) {
      let file;
      try {
        file = await open(path(key), "wx", 0o600);
      } catch (error) {
        throw new ClientError(
          (error as NodeJS.ErrnoException).code === "EEXIST"
            ? "credential_in_use"
            : "host_storage_unavailable",
        );
      }
      let released = false;
      try {
        const result = await action();
        released = true;
        return result;
      } catch (error) {
        // Uncertain storage failures retain ownership for human recovery.
        released =
          !uncertain ||
          (error instanceof ClientError &&
            [
              "connection_changed",
              "credential_update_pending",
              "sign_in_required",
            ].includes(error.code));
        throw error;
      } finally {
        await file.close();
        if (released) await unlink(path(key));
      }
    },
    async recover(key, clear) {
      // Human recovery requires all other Catalog windows closed. Clear the
      // uncertain credential before releasing the exact private empty lock.
      const name = path(key);
      let info;
      try {
        info = await lstat(name);
      } catch (error) {
        if ((error as NodeJS.ErrnoException).code !== "ENOENT")
          throw new ClientError("host_storage_unavailable");
      }
      if (info && (!info.isFile() || info.nlink !== 1 || info.size !== 0))
        throw new ClientError("host_storage_unavailable");
      await clear();
      await record(key, undefined);
      if (info) await unlink(name);
    },
  };
}

export async function executionHost(
  directory: string,
  kind: string,
): Promise<string> {
  // remoteName names a transport type. SecretStorage can be shared by remote
  // hosts, whose global storage paths can also be identical. Bind to local state.
  try {
    await mkdir(directory, { recursive: true, mode: 0o700 });
    const path = join(directory, "connection-host-id");
    const staging = await mkdtemp(join(directory, ".connection-host-"));
    try {
      const staged = join(staging, "id");
      await writeFile(staged, randomUUID(), { flag: "wx", mode: 0o600 });
      try {
        // Publish a complete identifier atomically; concurrent windows reuse it.
        await link(staged, path);
      } catch (error) {
        if ((error as NodeJS.ErrnoException).code !== "EEXIST") throw error;
      }
    } finally {
      await rm(staging, { recursive: true, force: true });
    }
    const file = await open(
      path,
      constants.O_RDONLY |
        (constants.O_NOFOLLOW ?? 0) |
        (constants.O_NONBLOCK ?? 0),
    );
    let id: string;
    try {
      const info = await file.stat();
      if (!info.isFile() || info.size !== 36)
        throw new Error("invalid host identifier");
      const buffer = Buffer.alloc(37);
      const { bytesRead } = await file.read(buffer, 0, buffer.length, 0);
      id = buffer.subarray(0, bytesRead).toString("utf8");
      if (
        !/^[0-9a-f]{8}-[0-9a-f]{4}-4[0-9a-f]{3}-[89ab][0-9a-f]{3}-[0-9a-f]{12}$/u.test(
          id,
        )
      )
        throw new Error("invalid host identifier");
    } finally {
      await file.close();
    }
    return createHash("sha256")
      .update(JSON.stringify([kind, directory, id]))
      .digest("hex");
  } catch {
    throw new ClientError("host_storage_unavailable");
  }
}
