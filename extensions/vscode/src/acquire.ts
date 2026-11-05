import {
  nativeAbsolute,
  stopPackageProcess,
  packageProcessControl,
} from "./platform.js";
import { spawn } from "node:child_process";
import {
  mkdir,
  writeFile,
  readdir,
  lstat,
  open,
  unlink,
} from "node:fs/promises";
import { constants } from "node:fs";
import { join } from "node:path";
import { transport } from "./transport.js";
import { ClientError, record } from "./config.js";
import {
  packageDigest,
  packageFail,
  packageHash,
  packageLimits,
  packageNumber,
  packagePath,
  verifyPackage,
  type PackageSource,
  type SkillPlan,
} from "./packages.js";
import { unpackPackage, type PackageContent } from "./archive.js";
import {
  packageAuthorization,
  packageOrigin,
  type PackageCredential,
} from "./package-credentials.js";
import { decodeJSON } from "./json.js";

export interface PackageTools {
  gitPath: string;
  orasPath: string;
  timeoutMs: number;
  caFile?: string;
}
export function packageTools(value: unknown): PackageTools {
  const v = record(value);
  if (
    Object.keys(v).some(
      (k) => !["gitPath", "orasPath", "timeoutMs", "caFile"].includes(k),
    )
  )
    return packageFail("invalid_package_tools");
  const executable = (value: unknown, fallback: string) => {
    if (value === undefined) return fallback;
    if (
      typeof value !== "string" ||
      !nativeAbsolute(value) ||
      [...value].some((c) => c.charCodeAt(0) < 32 || c.charCodeAt(0) === 127)
    )
      return packageFail("invalid_package_tools");
    return value;
  };
  const result: PackageTools = {
    gitPath: executable(v.gitPath, "git"),
    orasPath: executable(v.orasPath, "oras"),
    timeoutMs: packageNumber(v.timeoutMs ?? 60000, 300000, 1000),
  };
  if (v.caFile !== undefined) result.caFile = executable(v.caFile, "");
  return result;
}
async function diskSize(directory: string): Promise<number> {
  let total = 0,
    entries = 0;
  const visit = async (path: string) => {
    for (const entry of await readdir(path, { withFileTypes: true })) {
      if (++entries > 10000 || entry.isSymbolicLink())
        return packageFail("package_disk_limit");
      const child = join(path, entry.name);
      if (entry.isDirectory()) await visit(child);
      else {
        const info = await lstat(child);
        if (!info.isFile()) return packageFail();
        total += info.size;
      }
      if (total > 96 * 1024 ** 2) return packageFail("package_disk_limit");
    }
  };
  await visit(directory);
  return total;
}
export async function packageProcess(
  command: string,
  args: string[],
  work: string,
  env: NodeJS.ProcessEnv,
  signal: AbortSignal,
  outputLimit = 2 * 1024 ** 2,
): Promise<Buffer> {
  if (signal.aborted) return packageFail("package_cancelled");
  packageProcessControl();
  return new Promise((resolve, reject) => {
    const child = spawn(command, args, {
      cwd: work,
      env,
      shell: false,
      detached: process.platform !== "win32",
      stdio: ["ignore", "pipe", "pipe"],
    });
    let fault: Error | undefined,
      size = 0,
      errorSize = 0,
      closed = false,
      checking: Promise<unknown> | undefined,
      stopping: Promise<void> | undefined;
    const chunks: Buffer[] = [];
    const stop = (code: string) => {
      fault ??= new ClientError(code);
      if (!closed && child.pid && !stopping)
        stopping = stopPackageProcess(child.pid).catch(() => {
          if (!closed) {
            fault = new ClientError("package_process_cleanup_failed");
            child.kill("SIGKILL");
          }
        });
    };
    const cancel = () => stop("package_cancelled");
    signal.addEventListener("abort", cancel, { once: true });
    const monitor = setInterval(() => {
      if (checking) return;
      checking = diskSize(work)
        .catch(() => stop("package_disk_limit"))
        .finally(() => {
          checking = undefined;
        });
    }, 100);
    child.stdout.on("data", (chunk: Buffer) => {
      size += chunk.length;
      if (size > outputLimit) stop("package_output_limit");
      else chunks.push(chunk);
    });
    child.stderr.on("data", (chunk: Buffer) => {
      errorSize += chunk.length;
      if (errorSize > 65536) stop("package_output_limit");
    });
    child.on("error", () => {
      fault ??= new ClientError("package_tool_unavailable");
    });
    child.on("close", (code) => {
      closed = true;
      clearInterval(monitor);
      signal.removeEventListener("abort", cancel);
      void Promise.all([checking, stopping]).then(() => {
        if (fault) reject(fault);
        else if (code !== 0)
          reject(new ClientError("package_acquisition_failed"));
        else resolve(Buffer.concat(chunks, size));
      });
    });
    if (signal.aborted) cancel();
  });
}
async function acquire(
  plan: SkillPlan,
  source: PackageSource,
  work: string,
  tools: PackageTools,
  cancellation?: AbortSignal,
  credential?: PackageCredential,
): Promise<PackageContent> {
  const signal = AbortSignal.any([
    AbortSignal.timeout(tools.timeoutMs),
    ...(cancellation ? [cancellation] : []),
  ]);
  if (source.type === "https")
    return acquireArchive(
      plan,
      source.url,
      tools,
      signal,
      transport,
      credential,
    );
  if (source.type === "oci" && credential)
    return acquirePrivateOci(plan, source, tools, signal, credential);
  const home = join(work, "private-home");
  await mkdir(home, { mode: 0o700 });
  const empty = join(home, "empty-config"),
    hooks = join(home, "empty-hooks");
  await writeFile(
    empty,
    credential
      ? `[http "${packageOrigin(source)}/"]\n\textraHeader = Authorization: ${packageAuthorization(credential, source)}\n`
      : "",
    { mode: 0o600, flag: "wx" },
  );
  await mkdir(hooks, { mode: 0o700 });
  const env: NodeJS.ProcessEnv = {
    PATH: process.env.PATH,
    ...(process.platform === "win32"
      ? {
          SystemRoot: process.env.SystemRoot ?? process.env.SYSTEMROOT,
          USERPROFILE: home,
          TEMP: work,
          TMP: work,
        }
      : {}),
    HOME: home,
    XDG_CONFIG_HOME: home,
    LANG: "C.UTF-8",
    LC_ALL: "C.UTF-8",
    TMPDIR: work,
  };
  if (tools.caFile) {
    const info = await lstat(tools.caFile);
    if (!info.isFile() || info.isSymbolicLink() || info.size > 1024 ** 2)
      return packageFail("invalid_package_trust");
  }
  if (source.type === "git") {
    Object.assign(env, {
      GIT_CONFIG_NOSYSTEM: "1",
      GIT_CONFIG_GLOBAL: empty,
      GIT_CONFIG_SYSTEM: empty,
      GIT_ATTR_NOSYSTEM: "1",
      GIT_NO_REPLACE_OBJECTS: "1",
      GIT_TERMINAL_PROMPT: "0",
      GIT_OPTIONAL_LOCKS: "0",
      GIT_TEMPLATE_DIR: hooks,
    });
    const args = [
      "--no-replace-objects",
      "-c",
      `core.hooksPath=${hooks}`,
      "-c",
      `core.attributesFile=${empty}`,
      "-c",
      "credential.helper=",
      "-c",
      "protocol.allow=never",
      "-c",
      "protocol.https.allow=always",
      "-c",
      "http.followRedirects=false",
      "-c",
      "http.sslVerify=true",
      "-c",
      "fetch.recurseSubmodules=false",
      "-c",
      "submodule.recurse=false",
      "-c",
      "core.fsmonitor=false",
      ...(tools.caFile ? ["-c", `http.sslCAInfo=${tools.caFile}`] : []),
    ];
    const git = (...command: string[]) =>
      packageProcess(tools.gitPath, [...args, ...command], work, env, signal);
    await git(
      "init",
      "--bare",
      `--object-format=${source.commit.length === 64 ? "sha256" : "sha1"}`,
      "repository",
    );
    args.push("--git-dir=repository");
    await git(
      "fetch",
      "--no-tags",
      "--depth=1",
      "--filter=blob:none",
      "--no-recurse-submodules",
      "--",
      source.repository,
      source.commit,
    );
    if ((await git("cat-file", "-t", source.commit)).toString() !== "commit\n")
      return packageFail();
    const listing = new TextDecoder("utf-8", { fatal: true }).decode(
      await git(
        "ls-tree",
        "-r",
        "-z",
        "-l",
        `${source.commit}:${source.directory}`,
      ),
    );
    if (!listing.endsWith("\0")) return packageFail();
    const rows = listing.slice(0, -1).split("\0");
    if (rows.length > packageLimits.files)
      return packageFail("package_size_limit");
    const content: PackageContent = new Map();
    let total = 0;
    for (const row of rows) {
      const match =
        /^(100644|100755) blob ([a-f0-9]{40}|[a-f0-9]{64}) +([0-9]+)\t(.+)$/u.exec(
          row,
        );
      if (!match) return packageFail("unsafe_package_path");
      const path = packagePath(`${plan.name}/${match[4]}`),
        size = packageNumber(Number(match[3]), packageLimits.archive);
      total += size;
      if (total > packageLimits.expanded || content.has(path))
        return packageFail("package_size_limit");
      const bytes = await packageProcess(
        tools.gitPath,
        [...args, "cat-file", "blob", match[2]!],
        work,
        env,
        signal,
        Math.max(1, size),
      );
      if (bytes.length !== size) return packageFail("package_integrity_failed");
      content.set(path, { bytes, executable: match[1] === "100755" });
    }
    verifyPackage(plan, content);
    await diskSize(work);
    return content;
  }
  const config = join(home, "registry.json");
  await writeFile(config, '{"auths":{}}', { flag: "wx", mode: 0o600 });
  const flags = [
    "--registry-config",
    config,
    ...(tools.caFile ? ["--ca-file", tools.caFile] : []),
  ];
  const manifest = await packageProcess(
    tools.orasPath,
    ["manifest", "fetch", ...flags, source.reference],
    work,
    env,
    signal,
    1024 ** 2,
  );
  if (packageHash(manifest) !== source.reference.split("@")[1])
    return packageFail("package_integrity_failed");
  const m = record(
    decodeJSON(new TextDecoder("utf-8", { fatal: true }).decode(manifest)),
  );
  if (
    m.schemaVersion !== 2 ||
    m.mediaType !== "application/vnd.oci.image.manifest.v1+json" ||
    !Array.isArray(m.layers) ||
    m.layers.length > 64
  )
    return packageFail("unsupported_oci_manifest");
  const layers = m.layers
    .map(record)
    .filter((layer) => layer.mediaType === plan.mediaType);
  if (layers.length !== 1) return packageFail("ambiguous_package_layer");
  const layer = layers[0]!;
  if (
    packageDigest(layer.digest) !== plan.archiveDigest ||
    layer.size !== plan.archiveSize ||
    layer.urls !== undefined
  )
    return packageFail("package_integrity_failed");
  const archive = join(work, "archive");
  await packageProcess(
    tools.orasPath,
    [
      "blob",
      "fetch",
      ...flags,
      "--output",
      archive,
      source.reference.split("@")[0] + "@" + plan.archiveDigest,
    ],
    work,
    env,
    signal,
  );
  const file = await open(
    archive,
    constants.O_RDONLY | constants.O_NOFOLLOW | constants.O_NONBLOCK,
  );
  try {
    const info = await file.stat();
    if (!info.isFile() || info.size !== plan.archiveSize || info.nlink !== 1)
      return packageFail("package_integrity_failed");
    const bytes = Buffer.alloc(plan.archiveSize + 1);
    let size = 0;
    while (size < bytes.length) {
      const next = await file.read(bytes, size, bytes.length - size, size);
      if (!next.bytesRead) break;
      size += next.bytesRead;
    }
    if (size !== plan.archiveSize)
      return packageFail("package_integrity_failed");
    const archiveBytes = bytes.subarray(0, size);
    if (packageHash(archiveBytes) !== plan.archiveDigest)
      return packageFail("package_integrity_failed");
    await diskSize(work);
    return unpackPackage(plan, archiveBytes);
  } finally {
    await file.close();
  }
}

/** Anonymous exact HTTPS download. Catalog transport settings and credentials are never inputs. */
export async function acquireArchive(
  plan: SkillPlan,
  url: string,
  tools: PackageTools,
  signal: AbortSignal,
  network = transport,
  credential?: PackageCredential,
): Promise<PackageContent> {
  if (signal.aborted) return packageFail("package_cancelled");
  const authorization = credential
    ? packageAuthorization(credential, { type: "https", url })
    : undefined;
  const client = await network({
    id: "package",
    label: "Package",
    origin: new URL(url).origin,
    mode: "published",
    auth: "anonymous",
    allowLoopbackHTTP: false,
    timeoutMs: tools.timeoutMs,
    requestBytes: 1024,
    responseBytes: packageLimits.archive,
    headerBytes: 16384,
    ...(tools.caFile ? { caFile: tools.caFile } : {}),
  });
  let reader: ReadableStreamDefaultReader<Uint8Array> | undefined;
  try {
    const response = await client.fetch(url, {
      method: "GET",
      redirect: "manual",
      signal,
      headers: {
        accept: plan.mediaType,
        ...(authorization ? { authorization } : {}),
      },
    });
    if (
      response.status !== 200 ||
      ![
        plan.mediaType,
        "application/octet-stream",
        plan.mediaType.endsWith("zip") ? "application/zip" : "application/gzip",
      ].includes(
        (response.headers.get("content-type") ?? "")
          .split(";")[0]!
          .trim()
          .toLowerCase(),
      ) ||
      (response.headers.has("content-length") &&
        response.headers.get("content-length") !== String(plan.archiveSize))
    ) {
      await response.body?.cancel();
      return packageFail("package_archive_unavailable");
    }
    reader = response.body?.getReader();
    if (!reader) return packageFail("package_archive_unavailable");
    let size = 0;
    const chunks: Uint8Array[] = [];
    for (;;) {
      const chunk = await reader.read();
      if (chunk.done) break;
      size += chunk.value.length;
      if (size > plan.archiveSize) return packageFail("package_size_limit");
      chunks.push(chunk.value);
    }
    const bytes = Buffer.concat(chunks, size);
    if (size !== plan.archiveSize || packageHash(bytes) !== plan.archiveDigest)
      return packageFail("package_integrity_failed");
    return unpackPackage(plan, bytes);
  } catch (error) {
    if (error instanceof ClientError) throw error;
    return packageFail(
      signal.aborted ? "package_cancelled" : "package_archive_unavailable",
    );
  } finally {
    await reader?.cancel().catch(() => {});
    await client.close();
  }
}

/** Credentials are explicit; the private Git header file never outlives acquisition. */
export async function acquirePackage(
  plan: SkillPlan,
  source: PackageSource,
  work: string,
  tools: PackageTools,
  cancellation?: AbortSignal,
  credential?: PackageCredential,
): Promise<PackageContent> {
  if (credential) packageAuthorization(credential, source);
  try {
    return await acquire(plan, source, work, tools, cancellation, credential);
  } finally {
    if (source.type === "git" && credential)
      await unlink(join(work, "private-home", "empty-config")).catch(
        (error) => {
          if ((error as NodeJS.ErrnoException).code !== "ENOENT")
            throw new ClientError("package_credential_cleanup_failed");
        },
      );
  }
}

/** Basic or bounded same-origin registry token exchange; never follow a token realm off origin. */
async function acquirePrivateOci(
  plan: SkillPlan,
  source: Extract<PackageSource, { type: "oci" }>,
  tools: PackageTools,
  signal: AbortSignal,
  credential: PackageCredential,
): Promise<PackageContent> {
  const origin = packageOrigin(source),
    basic = packageAuthorization(credential, source);
  let authorization = basic,
    exchanged = false;
  const [repository, digest] = source.reference
    .slice(source.reference.indexOf("/") + 1)
    .split("@");
  if (
    !repository ||
    !digest ||
    repository
      .split("/")
      .some((p) => !/^[a-z0-9]+(?:(?:[._]|__|-+)[a-z0-9]+)*$/u.test(p))
  )
    return packageFail("invalid_package_source");
  const client = await transport({
    id: "private-package",
    label: "Private package",
    origin,
    mode: "published",
    auth: "anonymous",
    allowLoopbackHTTP: false,
    timeoutMs: tools.timeoutMs,
    requestBytes: 1024,
    responseBytes: packageLimits.archive,
    headerBytes: 16384,
    ...(tools.caFile ? { caFile: tools.caFile } : {}),
  });
  const bytes = async (response: Response, max: number) => {
    const reader = response.body?.getReader();
    try {
      if (response.status !== 200 || !reader)
        return packageFail("package_archive_unavailable");
      const chunks: Uint8Array[] = [];
      let size = 0;
      for (;;) {
        const part = await reader.read();
        if (part.done) break;
        size += part.value.length;
        if (size > max) return packageFail("package_size_limit");
        chunks.push(part.value);
      }
      return Buffer.concat(chunks, size);
    } finally {
      await reader?.cancel().catch(() => {});
    }
  };
  const get = async (
    path: string,
    max: number,
    accept: string,
  ): Promise<Buffer> => {
    const response = await client.fetch(origin + path, {
      method: "GET",
      redirect: "manual",
      signal,
      headers: { authorization, accept },
    });
    if (response.status === 401 && !exchanged) {
      const challenge = response.headers.get("www-authenticate") ?? "";
      await response.body?.cancel();
      // Closed, deliberately bounded challenge grammar. No quoted escapes or extra scopes.
      const match =
        /^Bearer realm="([^"\\]{1,2048})"(?:,\s*service="([A-Za-z0-9._:-]{1,128})")?(?:,\s*scope="([^"\\]{1,512})")?$/iu.exec(
          challenge,
        );
      if (!match) return packageFail("unsupported_oci_authentication");
      const realm = new URL(match[1]!);
      const scope = `repository:${repository}:pull`;
      if (
        realm.origin !== origin ||
        realm.username ||
        realm.password ||
        realm.hash ||
        realm.search ||
        (match[3] !== undefined && match[3] !== scope)
      )
        return packageFail("package_credential_origin_mismatch");
      realm.searchParams.set("scope", scope);
      if (match[2]) realm.searchParams.set("service", match[2]);
      exchanged = true;
      const tokenResponse = await client.fetch(realm.toString(), {
        method: "GET",
        redirect: "manual",
        signal,
        headers: { authorization: basic, accept: "application/json" },
      });
      const tokenDocument = record(
        decodeJSON(
          new TextDecoder("utf-8", { fatal: true }).decode(
            await bytes(tokenResponse, 16384),
          ),
        ),
      );
      const token = tokenDocument.token ?? tokenDocument.access_token;
      if (
        typeof token !== "string" ||
        !token ||
        token.length > 8192 ||
        !/^[A-Za-z0-9\-._~+/]+=*$/u.test(token)
      )
        return packageFail("invalid_package_credentials");
      authorization = `Bearer ${token}`;
      return get(path, max, accept);
    }
    return bytes(response, max);
  };
  try {
    const manifest = await get(
      `/v2/${repository}/manifests/${digest}`,
      1024 ** 2,
      "application/vnd.oci.image.manifest.v1+json",
    );
    if (packageHash(manifest) !== digest)
      return packageFail("package_integrity_failed");
    const value = record(
      decodeJSON(new TextDecoder("utf-8", { fatal: true }).decode(manifest)),
    );
    if (
      value.schemaVersion !== 2 ||
      value.mediaType !== "application/vnd.oci.image.manifest.v1+json" ||
      !Array.isArray(value.layers) ||
      value.layers.length > 64
    )
      return packageFail("unsupported_oci_manifest");
    const layers = value.layers
      .map(record)
      .filter((v) => v.mediaType === plan.mediaType);
    if (layers.length !== 1) return packageFail("ambiguous_package_layer");
    const layer = layers[0]!;
    if (
      packageDigest(layer.digest) !== plan.archiveDigest ||
      layer.size !== plan.archiveSize ||
      layer.urls !== undefined
    )
      return packageFail("package_integrity_failed");
    const bytes = await get(
      `/v2/${repository}/blobs/${plan.archiveDigest}`,
      plan.archiveSize,
      plan.mediaType,
    );
    if (
      bytes.length !== plan.archiveSize ||
      packageHash(bytes) !== plan.archiveDigest
    )
      return packageFail("package_integrity_failed");
    return unpackPackage(plan, bytes);
  } catch (error) {
    if (error instanceof ClientError) throw error;
    return packageFail(
      signal.aborted ? "package_cancelled" : "package_archive_unavailable",
    );
  } finally {
    await client.close();
  }
}
