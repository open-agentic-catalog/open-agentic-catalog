import assert from "node:assert/strict";
import { mkdtemp, rm } from "node:fs/promises";
import { tmpdir } from "node:os";
import { join } from "node:path";
import { fileURLToPath } from "node:url";
import test from "node:test";
import { build } from "vite";

test("startup and Catalog load only their shared screen dependencies", async () => {
  const directory = await mkdtemp(join(tmpdir(), "catalog-chunking-"));
  let chunks;
  try {
    await build({
      root: fileURLToPath(new URL("..", import.meta.url)),
      logLevel: "silent",
      build: { outDir: directory },
      plugins: [
        {
          name: "inspect-owned-chunks",
          generateBundle(_options, bundle) {
            chunks = new Map(
              Object.values(bundle)
                .filter((item) => item.type === "chunk")
                .map((item) => [item.fileName, item]),
            );
          },
        },
      ],
    });
    const entry = [...chunks.values()].filter((item) => item.isEntry);
    assert.equal(entry.length, 1);
    function staticClosure(root) {
      const files = new Set();
      function visit(name) {
        if (files.has(name)) return;
        files.add(name);
        assert.ok(chunks.has(name), "static dependency must exist");
        for (const dependency of chunks.get(name).imports) visit(dependency);
      }
      visit(root.fileName);
      return files;
    }
    const initial = staticClosure(entry[0]);
    const contains = (chunk, suffix) =>
      Object.keys(chunk.modules).some((id) =>
        id.replaceAll("\\", "/").endsWith(suffix),
      );
    assert.ok(
      [...initial].some((name) => contains(chunks.get(name), "/shell/App.tsx")),
      "the actual shell must be in the initial closure",
    );
    for (const helper of [
      "NativeStarter.tsx",
      "RevisionComparison.tsx",
      "SearchFilters.tsx",
      "representations.tsx",
    ]) {
      const owners = [...chunks.values()].filter((chunk) =>
        contains(chunk, `/views/${helper}`),
      );
      assert.equal(owners.length, 1, `${helper} must exist exactly once`);
      assert.equal(initial.has(owners[0].fileName), false, helper);
    }
    assert.equal(
      [...chunks.values()].filter((chunk) =>
        contains(chunk, "/src/react-runtime.ts"),
      ).length,
      1,
      "all screens must share one React binding module",
    );
    const discovery = [...chunks.values()].find((chunk) =>
      chunk.facadeModuleId?.endsWith("/views/DiscoveryWorkspace.tsx"),
    );
    assert.ok(discovery, "the Catalog route must exist");
    const catalogFiles = staticClosure(discovery);
    for (const screen of ["AdoptVersion.tsx", "SourceSnapshotComparison.tsx"]) {
      const owners = [...chunks.values()].filter((chunk) =>
        contains(chunk, `/views/${screen}`),
      );
      assert.equal(owners.length, 1, `${screen} must still ship`);
      assert.equal(
        catalogFiles.has(owners[0].fileName),
        false,
        `Opening Catalog should not load ${screen}`,
      );
    }
  } finally {
    await rm(directory, { recursive: true, force: true });
  }
});
