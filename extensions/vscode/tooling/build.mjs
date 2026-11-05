import { build, context } from "../node_modules/esbuild/lib/main.js";
import { readFile, writeFile, mkdir, readdir } from "node:fs/promises";
import { execFileSync } from "node:child_process";
import { resolve } from "node:path";
const root = resolve(import.meta.dirname, "../../..");
const extension = resolve(root, "extensions/vscode");
const metadata = JSON.parse(
  await readFile(resolve(extension, "package.json"), "utf8"),
);
if (
  metadata.version !== (await readFile(resolve(root, "VERSION"), "utf8")).trim()
)
  throw new Error("Extension version differs from VERSION");
const mode = process.argv[2] ?? "development";
const destination = resolve(process.argv[3] ?? resolve(extension, "dist"));
const production = mode === "production";
const outfile = resolve(
  destination,
  production ? "dist/extension.cjs" : "extension.cjs",
);
const watchFiles = (await readdir(resolve(extension, "src")))
  .filter((name) => name.endsWith(".ts"))
  .map((name) => resolve(extension, "src", name));
const options = {
  entryPoints: [resolve(extension, "src/extension.ts")],
  bundle: true,
  outfile,
  minify: production,
  sourcemap: !production,
  sourcesContent: !production,
  plugins: [
    {
      name: "typecheck",
      setup(builder) {
        builder.onStart(() => {
          if (mode === "watch") console.log("[watch] build started");
          try {
            execFileSync(
              process.execPath,
              [
                resolve(extension, "node_modules/typescript/bin/tsc"),
                "-p",
                resolve(extension, "tsconfig.json"),
                "--noEmit",
              ],
              { stdio: "inherit", timeout: 60000 },
            );
          } catch {
            return { errors: [{ text: "TypeScript check failed" }] };
          }
        });
        builder.onLoad({ filter: /\.ts$/ }, async ({ path }) => ({
          contents: await readFile(path, "utf8"),
          loader: "ts",
          watchFiles,
        }));
        builder.onEnd((result) => {
          if (mode === "watch")
            console.log(
              result.errors.length
                ? "[watch] build failed"
                : "[watch] build finished",
            );
        });
      },
    },
  ],
  platform: "node",
  format: "cjs",
  target: "node24.20",
  external: ["vscode"],
  metafile: true,
  legalComments: "inline",
};
if (mode === "watch") {
  const watcher = await context(options);
  await watcher.watch();
  await new Promise((done) => {
    for (const signal of ["SIGINT", "SIGTERM"]) process.once(signal, done);
  });
  await watcher.dispose();
  process.exit(0);
}
const bundle = await build(options);
const packages = new Set();
for (const input of Object.keys(bundle.metafile.inputs)) {
  const marker = input.lastIndexOf("node_modules/");
  if (marker < 0) continue;
  const parts = input.slice(marker + 13).split("/");
  packages.add(
    parts[0].startsWith("@") ? parts.slice(0, 2).join("/") : parts[0],
  );
}
const notices = [];
for (const name of [...packages].sort()) {
  const directory = resolve(extension, "node_modules", name);
  const packageJSON = JSON.parse(
    await readFile(resolve(directory, "package.json"), "utf8"),
  );
  if (!["MIT", "Apache-2.0"].includes(packageJSON.license))
    throw new Error("Unreviewed bundled runtime license");
  let license;
  for (const file of ["LICENSE", "LICENSE.md", "LICENSE-MIT"]) {
    try {
      license = await readFile(resolve(directory, file), "utf8");
      break;
    } catch {
      /* Try known upstream names. */
    }
  }
  if (!license) throw new Error("Missing bundled license");
  let notice = "";
  try {
    notice = await readFile(resolve(directory, "NOTICE"), "utf8");
  } catch {
    /* Optional upstream notice. */
  }
  notices.push(
    `${name}@${packageJSON.version} (${packageJSON.license})\n${license}\n${notice}`,
  );
  if (name === "undici") {
    for (const file of ["lib/web/fetch/LICENSE", "lib/milo/LICENSE.md"]) {
      let text;
      try {
        text = await readFile(resolve(directory, file), "utf8");
      } catch {
        throw new Error(`Cannot read required bundled notice: ${name}/${file}`);
      }
      if (!text.trim())
        throw new Error(`Empty required bundled notice: ${name}/${file}`);
      notices.push(`${name}@${packageJSON.version} - ${file}\n${text}`);
    }
  }
}
await writeFile(
  resolve(destination, "THIRD_PARTY_NOTICES.txt"),
  notices.join("\n\n"),
);
await mkdir(resolve(root, "output/vscode"), { recursive: true });
if (mode === "test") {
  execFileSync(
    process.execPath,
    [
      resolve(extension, "node_modules/typescript/bin/tsc"),
      "-p",
      resolve(extension, "tsconfig.json"),
      "--outDir",
      resolve(extension, "dist/test"),
    ],
    { stdio: "inherit", timeout: 60000 },
  );
  await writeFile(
    resolve(extension, "dist/test/testlib.mjs"),
    'export * from "./platform.js"; export * from "./package-credentials.js"; export * from "./mcp-references.js"; export * from "./opinions.js"; export * from "./config.js"; export * from "./session.js"; export * from "./oidc.js"; export * from "./transport.js"; export * from "./host.js"; export * from "./browse.js"; export * from "./panel.js"; export * from "./json.js"; export * from "./mcp.js"; export * from "./mcp-document.js"; export * from "./packages.js"; export * from "./archive.js"; export * from "./acquire.js"; export * from "./install.js"; export * from "./plugins.js"; export * from "./a2a.js"; export * from "./a2a-client.js";',
  );
}
console.log(`Built ${mode} extension with bundled SDK and runtime notices`);
