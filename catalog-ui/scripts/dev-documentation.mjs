// Copy only the selected prepared manual into private source-launcher assets.
import { createHash } from "node:crypto";
import { lstat, mkdir, readFile, writeFile } from "node:fs/promises";
import { join, resolve } from "node:path";

export async function stageDocumentation(source, assets) {
  const root = resolve(source);
  const failure = () =>
    new Error("Invalid prepared manual; build with DOCS_BASE=/docs/");
  if (!(await lstat(root)).isDirectory()) throw failure();
  async function regular(name) {
    const segments = name.split("/");
    for (let i = 0; i < segments.length; i++) {
      const info = await lstat(join(root, ...segments.slice(0, i + 1)));
      if (i < segments.length - 1 ? !info.isDirectory() : !info.isFile())
        throw failure();
    }
    return join(root, name);
  }
  const manifestName = "documentation-bundle.json";
  const manifestFile = await regular(manifestName);
  if ((await lstat(manifestFile)).size > 2 * 1024 * 1024) throw failure();
  const bytes = await readFile(manifestFile);
  if (bytes.length > 2 * 1024 * 1024) throw failure();
  const manifest = JSON.parse(bytes);
  if (
    manifest.base !== "/docs/" ||
    !Array.isArray(manifest.files) ||
    !manifest.files.length ||
    manifest.files.length > 10000
  )
    throw failure();
  const destination = join(assets, "docs");
  await mkdir(destination);
  const seen = new Set();
  let total = 0;
  for (const file of manifest.files) {
    const name = file.path;
    if (
      typeof name !== "string" ||
      !/^[A-Za-z0-9_-][A-Za-z0-9_./-]*$/.test(name) ||
      name.length > 512 ||
      name.split("/").some((p) => !p || p.startsWith(".")) ||
      name === manifestName ||
      seen.has(name) ||
      !Number.isSafeInteger(file.size) ||
      file.size < 0 ||
      file.size > 32 * 1024 * 1024
    )
      throw failure();
    total += file.size;
    if (total > 256 * 1024 * 1024) throw failure();
    const filename = await regular(name);
    if ((await lstat(filename)).size !== file.size) throw failure();
    const content = await readFile(filename);
    if (
      content.length !== file.size ||
      createHash("sha256").update(content).digest("hex") !== file.sha256
    )
      throw failure();
    await mkdir(resolve(destination, name, ".."), { recursive: true });
    await writeFile(join(destination, name), content);
    seen.add(name);
  }
  if (!seen.has("index.html")) throw failure();
  await writeFile(join(destination, manifestName), bytes);
  // The Go host performs complete strict metadata/CSP admission at startup.
}
