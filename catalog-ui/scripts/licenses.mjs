// Preserve license/notice files for the locked runtime JavaScript closure.
// Build-only packages and Node itself are not copied into the runtime image.
import { readFile, readdir, mkdir, copyFile } from "node:fs/promises";
import { writeFile } from "node:fs/promises";
import { createHash } from "node:crypto";
import { join, resolve } from "node:path";
import { pathToFileURL } from "node:url";

const sha256 = (bytes) => createHash("sha256").update(bytes).digest("hex");

// Retain conservative production-lock evidence, not a claim that every locked
// module survives tree shaking. Hash the actual build output, not source files.
async function productionLicenses(root, output) {
  const lockBytes = await readFile(join(root, "package-lock.json"));
  const lock = JSON.parse(lockBytes);
  const packages = [];
  const notices = [];
  // Compare package paths directly, without stringifying their metadata.
  for (const [path, entry] of Object.entries(lock.packages).sort(
    ([a], [b]) => Number(a > b) - Number(a < b),
  )) {
    if (!path || entry.dev) continue;
    if (
      !/^node_modules\/(?:[@A-Za-z0-9_.-]+\/)*[A-Za-z0-9_.-]+$/.test(path) ||
      path.split("/").includes("..")
    )
      throw new Error("Invalid locked package path");
    if (!entry.version || typeof entry.license !== "string")
      throw new Error(`Missing package identity/license for ${path}`);
    const files = (
      await readdir(join(root, path), { withFileTypes: true })
    ).filter((file) =>
      /^(license|licence|notice|copying)([.-]|$)/i.test(file.name),
    );
    if (!files.length || files.some((file) => !file.isFile()))
      throw new Error(`Missing or unsafe license for ${path}`);
    // Preserve nested node_modules paths to avoid collisions between versions.
    const destination = output ? join(output, path) : undefined;
    if (output) await mkdir(destination, { recursive: true });
    for (const file of files.sort(
      (a, b) => Number(a.name > b.name) - Number(a.name < b.name),
    )) {
      const bytes = await readFile(join(root, path, file.name));
      notices.push(
        Buffer.from(`\n${path}@${entry.version} / ${file.name}\n`),
        bytes,
        Buffer.from("\n"),
      );
      if (output)
        await copyFile(
          join(root, path, file.name),
          join(destination, file.name),
        );
    }
    packages.push({
      path,
      name: path.split("node_modules/").pop(),
      version: entry.version,
      license: entry.license,
      notices: files
        .map((file) => `${path}/${file.name}`)
        .sort((a, b) => Number(a > b) - Number(a < b)),
    });
  }
  return { lockBytes, packages, notices: Buffer.concat(notices) };
}

/** The same locked runtime closure supplies public browser notices and image evidence. */
export function legalAssets(root) {
  return {
    name: "catalog-legal-assets",
    async generateBundle() {
      const { notices } = await productionLicenses(root);
      this.emitFile({
        type: "asset",
        fileName: "legal/LICENSE.txt",
        source: await readFile(join(root, "../LICENSE")),
      });
      this.emitFile({
        type: "asset",
        fileName: "legal/third-party-notices.txt",
        source: Buffer.concat([
          Buffer.from(
            "Open Agentic Catalog browser dependencies\nOriginal license and notice texts for the locked production JavaScript closure.\n",
          ),
          notices,
        ]),
      });
    },
  };
}

export async function collectLicenses(root, output) {
  const { lockBytes, packages } = await productionLicenses(root, output);
  const assets = [];
  // Hash actual regular build assets; reject links and special files from inventory input.
  async function visit(relative = "") {
    for (const file of await readdir(join(root, "dist", relative), {
      withFileTypes: true,
    })) {
      const path = relative ? `${relative}/${file.name}` : file.name;
      if (file.isDirectory()) await visit(path);
      else if (file.isFile())
        assets.push({
          path,
          sha256: sha256(await readFile(join(root, "dist", path))),
        });
      else throw new Error("Non-regular build asset");
    }
  }
  await visit();
  if (!packages.length || !assets.some((asset) => asset.path === "index.html"))
    throw new Error("Missing production packages or built index");
  assets.sort((a, b) => a.path.localeCompare(b.path, "en"));
  await writeFile(
    join(output, "inventory.json"),
    JSON.stringify(
      {
        schemaVersion: 1,
        scope: "locked-production-dependencies",
        lockSha256: sha256(lockBytes),
        packages,
        assets,
      },
      null,
      2,
    ) + "\n",
  );
}

if (
  process.argv[1] &&
  import.meta.url === pathToFileURL(resolve(process.argv[1])).href
)
  await collectLicenses(process.cwd(), "/out/licenses");
