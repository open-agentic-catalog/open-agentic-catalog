import { readFile, mkdtemp, rm } from "node:fs/promises";
import { join } from "node:path";
import assert from "node:assert/strict";
const { skillPlan, acquirePackage, packageHash } = await import(
  process.env.CATALOG_VSCODE_LIB
);
export async function qualifyPackages(work) {
  const representation = JSON.parse(
      await readFile(process.env.CATALOG_VSCODE_SKILL_INPUT, "utf8"),
    ),
    plan = skillPlan(representation, packageHash("owned control"));
  const tools = {
    gitPath: "/usr/bin/git",
    orasPath: process.env.CATALOG_VSCODE_ORAS,
    timeoutMs: 20000,
    caFile: process.env.CATALOG_VSCODE_PUBLIC_CA,
  };
  const acquire = async (p, source, settings = tools, signal, credential) => {
    const directory = await mkdtemp(join(work, "package-control-"));
    try {
      const result = await acquirePackage(
        p,
        source,
        directory,
        settings,
        signal,
        credential,
      );
      return result;
    } finally {
      try {
        if (credential && source.type === "git")
          await assert.rejects(
            readFile(join(directory, "private-home/empty-config")),
            { code: "ENOENT" },
          );
      } finally {
        await rm(directory, { recursive: true, force: true });
      }
    }
  };
  for (const source of plan.sources) {
    const content = await acquire(plan, source).catch((error) => {
      throw new Error(
        `Owned ${source.type} acquisition control: ${error.code}`,
      );
    });
    assert.equal(content.size, 3);
    assert.match(
      content.get("owned-skill/SKILL.md").bytes.toString(),
      /Read this owned instruction/,
    );
    assert.equal(
      content.get("owned-skill/scripts/never-run.sh").executable,
      true,
    );
    await assert.rejects(
      acquire(plan, source, { ...tools, caFile: undefined }),
    );
    await assert.rejects(acquire(plan, source, tools, AbortSignal.abort()), {
      code: "package_cancelled",
    });
    await assert.rejects(
      acquire(
        {
          ...plan,
          files: plan.files.map((f, i) =>
            i ? f : { ...f, digest: packageHash("incorrect") },
          ),
        },
        source,
      ),
      { code: "package_integrity_failed" },
    );
  }
  for (const original of plan.sources) {
    const source =
      original.type === "git"
        ? {
            ...original,
            repository: original.repository.replace(
              "/owned.git",
              "/private.git",
            ),
          }
        : {
            ...original,
            reference: original.reference.replace(
              "/skills/owned@",
              "/private/owned@",
            ),
          };
    const credential = {
      type: source.type,
      origin: new URL(
        original.type === "git"
          ? original.repository
          : `https://${original.reference.split("/")[0]}`,
      ).origin,
      scheme: "basic",
      username: "package-user",
      token: "package-secret",
    };
    await assert.rejects(acquire(plan, source));
    const content = await acquire(plan, source, tools, undefined, credential);
    assert.equal(content.size, 3);

    if (source.type === "git") {
      const directory = await mkdtemp(join(work, "private-cancel-")),
        controller = new AbortController();
      const pending = acquirePackage(
        plan,
        {
          ...source,
          repository: source.repository.replace(
            "/private.git",
            "/private-slow.git",
          ),
        },
        directory,
        tools,
        controller.signal,
        credential,
      );
      const rejected = assert.rejects(pending, { code: "package_cancelled" });
      try {
        let created = false;
        for (let attempt = 0; attempt < 50; attempt++) {
          try {
            const value = await readFile(
              join(directory, "private-home/empty-config"),
              "utf8",
            );
            assert.ok(value.includes("Authorization: Basic "));
            created = true;
            break;
          } catch (error) {
            if (error.code !== "ENOENT") throw error;
          }
          await new Promise((done) => setTimeout(done, 20));
        }
        assert.equal(
          created,
          true,
          "Cancellation must occur after the private credential file exists",
        );
        controller.abort();
        await rejected;
        await assert.rejects(
          readFile(join(directory, "private-home/empty-config")),
          { code: "ENOENT" },
        );
      } finally {
        controller.abort();
        await pending.catch(() => {});
        await rm(directory, { recursive: true, force: true });
      }
    }
    if (source.type === "oci") {
      const bearer = {
        ...source,
        reference: source.reference.replace(
          "/private/owned@",
          "/bearer/owned@",
        ),
      };
      assert.equal(
        (await acquire(plan, bearer, tools, undefined, credential)).size,
        3,
      );
      await assert.rejects(
        acquire(plan, bearer, tools, undefined, {
          ...credential,
          token: "wrong-token",
        }),
      );
      await assert.rejects(
        acquire(
          plan,
          {
            ...source,
            reference: source.reference.replace(
              "/private/owned@",
              "/realm-other/owned@",
            ),
          },
          tools,
          undefined,
          credential,
        ),
        { code: "package_credential_origin_mismatch" },
      );
    }
    await assert.rejects(
      acquire(plan, source, tools, undefined, {
        ...credential,
        token: "wrong-token",
      }),
    );
    await assert.rejects(
      acquire(plan, source, tools, AbortSignal.abort(), credential),
      { code: "package_cancelled" },
    );
    await assert.rejects(
      acquire(plan, source, tools, undefined, {
        ...credential,
        origin: "https://different.invalid",
      }),
      { code: "package_credential_origin_mismatch" },
    );
  }
  const oci = plan.sources.find((s) => s.type === "oci");
  await assert.rejects(
    acquire({ ...plan, archiveDigest: packageHash("wrong archive") }, oci),
    { code: "package_integrity_failed" },
  );
  await assert.rejects(
    acquire({ ...plan, mediaType: "application/agent-skills+gzip" }, oci),
    { code: "ambiguous_package_layer" },
  );
  assert.equal(
    await readFile(process.env.CATALOG_VSCODE_SKILL_INPUT, "utf8"),
    JSON.stringify(representation),
  );
  console.log(
    "Real package controls: exact HTTPS Git despite moving branch, OCI manifest/layer identity, TLS refusal, inventory mismatch and cancellation passed",
  );
}
