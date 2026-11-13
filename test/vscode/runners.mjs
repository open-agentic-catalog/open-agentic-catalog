import { execFileSync } from "node:child_process";
import { listenFixture } from "./listen.mjs";
import {
  mkdtemp,
  mkdir,
  writeFile,
  readFile,
  rm,
  readdir,
} from "node:fs/promises";
import { join, resolve, dirname } from "node:path";
import { createServer, request as httpRequest } from "node:http";
import { createHash, randomUUID } from "node:crypto";
import assert from "node:assert/strict";
import { developmentHost } from "./editor.mjs";
import { peers } from "./host-peers.mjs";
import { pathToFileURL } from "node:url";
const digest = (bytes) =>
  "sha256:" + createHash("sha256").update(bytes).digest("hex");

/** No public package/image or language-model account participates. */
export async function qualifyRunners(parent, host, vsix, uvx, go) {
  const work = await mkdtemp(join(parent, "runners-")),
    fixtureID = randomUUID(),
    tag = `oac-vscode-owned:${fixtureID}`;
  let eventReader, eventResponse;
  let built = false,
    registry,
    peer,
    reference;
  const reportPath = resolve("output/vscode/runner-qualification.json");
  const packageSHA256 = createHash("sha256")
    .update(await readFile(vsix))
    .digest("hex");
  await writeFile(
    reportPath,
    JSON.stringify({
      status: "running",
      packageSHA256,
      cleanup: "not_confirmed",
    }) + "\n",
  );
  let passed = false;
  try {
    const fixtureHome = join(work, "home"),
      bin = join(work, "bin"),
      wheels = join(work, "wheels"),
      missingBin = join(work, "missing"),
      context = join(work, "image");
    for (const path of [
      fixtureHome,
      bin,
      wheels,
      missingBin,
      context,
      join(fixtureHome, ".docker"),
    ])
      await mkdir(path, { recursive: true, mode: 0o700 });
    await writeFile(join(fixtureHome, ".docker/config.json"), '{"auths":{}}', {
      mode: 0o600,
    });
    const env = {
      ...process.env,
      HOME: fixtureHome,
      DONT_PROMPT_WSL_INSTALL: "1",
      DOCKER_CONFIG: join(fixtureHome, ".docker"),
      DOCKER_HOST: "unix:///var/run/docker.sock",
      DOCKER_CONTEXT: "",
      DOCKER_TLS_VERIFY: "",
      DOCKER_CERT_PATH: "",
      GOPROXY: "off",
      GOSUMDB: "off",
      GOTOOLCHAIN: "local",
      CGO_ENABLED: "0",
      GOWORK: "off",
    };
    const run = (command, args, timeout = 60000) =>
      execFileSync(command, args, { env, cwd: work, timeout, stdio: "pipe" });
    const uvVersion = run(uvx, ["--version"]).toString().trim();
    assert.deepEqual(uvVersion.split(/\s+/).slice(0, 2), ["uvx", "0.13.6"]);
    const dockerVersion = run("docker", [
      "version",
      "--format",
      "{{.Server.Version}}",
    ])
      .toString()
      .trim();
    await writeFile(
      join(context, "runner.go"),
      await readFile(resolve(import.meta.dirname, "mcp-runner.go.txt")),
    );
    run(go, [
      "build",
      "-trimpath",
      "-ldflags",
      `-s -w -X main.fixtureID=${fixtureID}`,
      "-o",
      join(context, "runner"),
      join(context, "runner.go"),
    ]);
    await writeFile(
      join(context, "Dockerfile"),
      'FROM scratch\nCOPY runner /runner\nENTRYPOINT ["/runner"]\nUSER 65532:65532\n',
    );
    run("docker", [
      "build",
      "--network=none",
      "--pull=false",
      "--tag",
      tag,
      context,
    ]);
    built = true;
    run("docker", ["image", "save", "--output", join(work, "image.tar"), tag]);
    run("python3", [
      "-c",
      `import tarfile,json,pathlib,sys
root=pathlib.Path(sys.argv[2]);root.mkdir()
with tarfile.open(sys.argv[1]) as t:
 m=json.load(t.extractfile('manifest.json'))[0]
 paths=[m['Config'],*m['Layers']]
 for i,p in enumerate(paths):
  f=t.getmember(p)
  assert f.isfile() and f.size < 64*1024*1024
  (root/str(i)).write_bytes(t.extractfile(f).read())
`,
      join(work, "image.tar"),
      join(work, "blobs"),
    ]);
    const config = await readFile(join(work, "blobs/0")),
      layers = [];
    const blobs = new Map([[digest(config), config]]);
    for (const name of (await readdir(join(work, "blobs")))
      .filter((name) => name !== "0")
      .sort()) {
      const bytes = await readFile(join(work, "blobs", name));
      blobs.set(digest(bytes), bytes);
      layers.push({
        mediaType:
          bytes[0] === 31 && bytes[1] === 139
            ? "application/vnd.oci.image.layer.v1.tar+gzip"
            : "application/vnd.oci.image.layer.v1.tar",
        digest: digest(bytes),
        size: bytes.length,
      });
    }
    const manifest = Buffer.from(
      JSON.stringify({
        schemaVersion: 2,
        mediaType: "application/vnd.oci.image.manifest.v1+json",
        config: {
          mediaType: "application/vnd.oci.image.config.v1+json",
          digest: digest(config),
          size: config.length,
        },
        layers,
      }),
    );
    let registryRequests = 0;
    registry = createServer((req, res) => {
      registryRequests++;
      assert.ok(!req.headers.authorization && !req.headers.cookie);
      const path = new URL(req.url, "http://127.0.0.1").pathname;
      let bytes, media;
      if (path === "/v2/") {
        bytes = Buffer.from("{}");
        media = "application/json";
      } else if (path === "/v2/owned/mcp/manifests/" + digest(manifest)) {
        bytes = manifest;
        media = "application/vnd.oci.image.manifest.v1+json";
      } else if (path.startsWith("/v2/owned/mcp/blobs/")) {
        bytes = blobs.get(path.split("/").at(-1));
        media = "application/octet-stream";
      }
      if (!bytes || !["GET", "HEAD"].includes(req.method)) {
        res.writeHead(404);
        res.end();
        return;
      }
      res.writeHead(200, {
        "content-type": media,
        "content-length": bytes.length,
        "docker-content-digest": digest(bytes),
        "docker-distribution-api-version": "registry/2.0",
      });
      res.end(req.method === "HEAD" ? undefined : bytes);
    });
    await listenFixture(registry);
    reference = `127.0.0.1:${registry.address().port}/owned/mcp@${digest(manifest)}`;
    // Asynchronous: the owned registry runs in this same event loop.
    const { execFile } = await import("node:child_process");
    await new Promise((done, reject) =>
      execFile(
        "docker",
        ["pull", "--quiet", reference],
        { env, timeout: 30000 },
        (error) =>
          error ? reject(new Error("Owned Docker digest pull failed")) : done(),
      ),
    );
    run("python3", [
      "-c",
      `import zipfile,hashlib,base64,pathlib,csv,io,sys
source=pathlib.Path(sys.argv[1]).read_bytes();root=pathlib.Path(sys.argv[2]);dist='catalog_owned_uv-1.0.0.dist-info'
files={'catalog_owned_uv.py':source,dist+'/METADATA':b'Metadata-Version: 2.1\\nName: catalog-owned-uv\\nVersion: 1.0.0\\nLicense: Apache-2.0\\n',dist+'/WHEEL':b'Wheel-Version: 1.0\\nGenerator: owned-vscode-fixture\\nRoot-Is-Purelib: true\\nTag: py3-none-any\\n',dist+'/entry_points.txt':b'[console_scripts]\\ncatalog-owned-uv = catalog_owned_uv:main\\n'}
record=io.StringIO();writer=csv.writer(record,lineterminator='\\n')
for name,data in files.items():writer.writerow([name,'sha256='+base64.urlsafe_b64encode(hashlib.sha256(data).digest()).decode().rstrip('='),len(data)])
writer.writerow([dist+'/RECORD','','']);files[dist+'/RECORD']=record.getvalue().encode()
with zipfile.ZipFile(root/'catalog_owned_uv-1.0.0-py3-none-any.whl','w') as z:
 for name,data in files.items():z.writestr(name,data)
`,
      resolve(import.meta.dirname, "mcp_runner.py"),
      wheels,
    ]);
    // A fixture-only launcher configures the real uvx with owned offline inputs.
    // It never replaces protocol output or implements the runner itself.
    await writeFile(
      join(bin, "uvx"),
      `#!/usr/bin/python3\nimport os,sys\nos.execv(${JSON.stringify(uvx)},[${JSON.stringify(uvx)},'--offline','--no-index','--find-links',${JSON.stringify(wheels)},'--python','/usr/bin/python3',*sys.argv[1:]])\n`,
      { mode: 0o700 },
    );
    let dockerStarts = 0,
      eventFailure;
    await new Promise((done, reject) => {
      eventReader = httpRequest(
        {
          socketPath: "/var/run/docker.sock",
          method: "GET",
          path:
            "/events?filters=" +
            encodeURIComponent(JSON.stringify({ image: [reference] })),
        },
        (response) => {
          eventResponse = response;
          if (response.statusCode !== 200) {
            reject(new Error("Owned Docker event subscription refused"));
            return;
          }
          let buffered = "";
          response.on("data", (data) => {
            buffered += data.toString();
            if (buffered.length > 32768) {
              eventFailure = true;
              eventReader.destroy();
              return;
            }
            const lines = buffered.split("\n");
            buffered = lines.pop();
            for (const line of lines)
              if (line) {
                const event = JSON.parse(line);
                if (event.Action === "start") dockerStarts++;
              }
          });
          response.on("error", () => {
            if (!response.destroyed) eventFailure = true;
          });
          done();
        },
      );
      eventReader.on("error", reject);
      eventReader.end();
    });
    const uvxSentinel = join(work, "uvx-starts");
    const directInput =
      '{"jsonrpc":"2.0","id":1,"method":"initialize","params":{}}\n';
    const directUv = execFileSync(
      join(bin, "uvx"),
      ["catalog-owned-uv==1.0.0", "--literal=$(never-run);literal"],
      {
        env: {
          ...env,
          UV_CACHE_DIR: join(fixtureHome, ".cache/uv"),
          OWNED_RUNNER_SENTINEL: join(work, "direct-uvx-starts"),
        },
        input: directInput,
        encoding: "utf8",
        timeout: 30000,
        cwd: work,
      },
    );
    assert.equal(
      JSON.parse(directUv.trim()).result.serverInfo.name,
      "owned-uvx",
    );
    const directDocker = execFileSync(
      "docker",
      ["run", "--rm", "-i", reference, "--literal=$(never-run);literal"],
      { env, input: directInput, encoding: "utf8", timeout: 30000, cwd: work },
    );
    assert.equal(
      JSON.parse(directDocker.trim()).result.serverInfo.name,
      "owned-docker",
    );
    //added NOSONAR as dockerStarts is getting modified asynchronously and giving out a blocker warning in sonarqube
    for (let n = 0; dockerStarts < 1 && n < 50; n++) // NOSONAR
      await new Promise((done) => setTimeout(done, 100));
    assert.equal(
      dockerStarts,
      1,
      "Positive direct Docker control proves the live engine event subscription",
    );
    const arguments_ = [
      { type: "named", name: "--literal", value: "$(never-run);literal" },
    ];
    const document = {
      name: "io.example/owned-runners",
      description: "Owned offline runner qualification",
      version: "1.0.0",
      packages: [
        {
          registryType: "pypi",
          identifier: "catalog-owned-uv",
          environmentVariables: [
            { name: "OWNED_RUNNER_SENTINEL", value: uvxSentinel },
          ],
          version: "1.0.0",
          transport: { type: "stdio" },
          packageArguments: arguments_,
        },
        {
          registryType: "oci",
          identifier: reference,
          transport: { type: "stdio" },
          packageArguments: arguments_,
        },
        {
          registryType: "npm",
          identifier: "catalog-refused",
          version: "1.0.0",
          runtimeHint: "unreviewed-shell",
          transport: { type: "stdio" },
        },
        {
          registryType: "npm",
          identifier: "catalog-owned-mcp",
          version: "1.0.0",
          transport: { type: "stdio" },
        },
      ],
    };
    peer = await peers({
      runnerDocument: document,
      runnerStatus: () => ({ dockerStarts }),
    });
    for (const missing of [false, true]) {
      const modeRoot = join(work, missing ? "missing-editor" : "normal-editor");
      const user = join(modeRoot, "user"),
        extensions = join(modeRoot, "extensions"),
        driver = join(modeRoot, "driver"),
        result = join(modeRoot, "result.json"),
        workspace = join(modeRoot, "workspace");
      for (const path of [join(user, "User"), extensions, driver, workspace])
        await mkdir(path, { recursive: true });
      await writeFile(
        join(user, "User/settings.json"),
        JSON.stringify({
          "openAgenticCatalog.connections": [
            {
              id: "runners",
              label: "Owned runners",
              origin: peer.origin,
              mode: "published",
              auth: "anonymous",
              allowLoopbackHTTP: true,
            },
          ],
          "git.enabled": false,
          "telemetry.telemetryLevel": "off",
          "update.mode": "none",
          "extensions.autoUpdate": false,
          "extensions.autoCheckUpdates": false,
          "workbench.enableExperiments": false,
          "chat.mcp.autostart": "never",
          "chat.mcp.gallery.enabled": false,
          "window.dialogStyle": "custom",
        }),
      );
      await writeFile(
        join(driver, "package.json"),
        JSON.stringify({
          name: "runner-qualification",
          publisher: "local",
          version: "0.0.1",
          engines: { vscode: "^1.139.1" },
          main: "./index.cjs",
          activationEvents: ["onStartupFinished"],
        }),
      );
      await writeFile(
        join(driver, "index.cjs"),
        `const vscode=require('vscode'),fs=require('node:fs');exports.activate=()=>{void(async()=>{try{const api=await vscode.extensions.getExtension('open-agentic-catalog.open-agentic-catalog').activate();await require(${JSON.stringify(resolve(import.meta.dirname, "runners-host-tests.cjs"))}).run(api);fs.writeFileSync(${JSON.stringify(result)},JSON.stringify({passed:true}));}catch(error){fs.writeFileSync(${JSON.stringify(result)},JSON.stringify({passed:false,error:String(error.stack??error)}));}finally{await vscode.commands.executeCommand('workbench.action.quit');}})();};`,
      );
      const profile = [
        "--user-data-dir",
        user,
        "--extensions-dir",
        extensions,
        "--shared-data-dir",
        join(modeRoot, "shared"),
      ];
      run(join(dirname(host), "bin/code"), [
        ...profile,
        "--no-sandbox",
        "--install-extension",
        vsix,
        "--force",
      ]);
      const editorEnv = {
        ...env,
        CATALOG_VSCODE_HOST: host,
        CATALOG_VSCODE_DEBUG_DIR: user,
        CATALOG_VSCODE_RUNNERS: JSON.stringify({
          missingBin,
          missing,
          uvxSentinel,
          peer: peer.origin,
          fixtureHome,
          runnerPath: bin + ":" + dirname(process.execPath) + ":/usr/bin:/bin",
        }),
        CATALOG_MCP_SENTINEL_SECRET: "must-never-reach-a-runner",
        PATH: bin + ":" + dirname(process.execPath) + ":/usr/bin:/bin",
        DONT_PROMPT_WSL_INSTALL: "1",
      };
      await developmentHost(
        [
          ...profile,
          workspace,
          `--extensionDevelopmentPath=${driver}`,
          "--no-sandbox",
          "--disable-gpu",
          "--skip-welcome",
          "--skip-release-notes",
          "--disable-workspace-trust",
          "--password-store=basic",
          "--remote-debugging-port=0",
          "--remote-debugging-address=127.0.0.1",
        ],
        editorEnv,
        result,
        120000,
      );
    }
    assert.ok(registryRequests > 0);
    assert.equal(eventFailure, undefined);
    assert.equal(
      dockerStarts,
      2,
      "Only the explicit successful native start executes the owned image",
    );
    assert.equal(
      await readFile(uvxSentinel, "utf8"),
      "started\ncalled\n",
      "Canceled and missing uvx starts do not execute the package",
    );
    await writeFile(
      reportPath,
      JSON.stringify(
        {
          status: "running",
          packageSHA256,
          uvx: uvVersion,
          docker: dockerVersion,
          image: reference,
          offlineWheel:
            "catalog-owned-uv==1.0.0; explicit real uvx with fixture-only offline launcher",
          publicPackagesOrImages: false,
          cleanup: "pending",
        },
        null,
        2,
      ) + "\n",
    );
    passed = true;
  } finally {
    eventResponse?.destroy();
    eventReader?.destroy();
    if (peer) await peer.close();
    if (registry) {
      registry.closeAllConnections();
      await new Promise((done) => registry.close(done));
    }
    if (built) {
      const env = {
        ...process.env,
        HOME: join(work, "home"),
        DOCKER_CONFIG: join(work, "home/.docker"),
        DOCKER_HOST: "unix:///var/run/docker.sock",
        DOCKER_CONTEXT: "",
        DOCKER_TLS_VERIFY: "",
        DOCKER_CERT_PATH: "",
      };
      if (reference) {
        const containers = execFileSync(
          "docker",
          ["ps", "-aq", "--filter", "ancestor=" + reference],
          { env, encoding: "utf8" },
        )
          .trim()
          .split(/\s+/u)
          .filter(Boolean);
        for (const id of containers)
          execFileSync("docker", ["rm", "--force", id], {
            env,
            stdio: "pipe",
            timeout: 10000,
          });
        execFileSync("docker", ["image", "rm", reference], {
          env,
          stdio: "pipe",
          timeout: 10000,
        });
      }
      execFileSync("docker", ["image", "rm", tag], {
        env,
        stdio: "pipe",
        timeout: 10000,
      });
    }
    await rm(work, { recursive: true, force: true });
    const report = JSON.parse(await readFile(reportPath, "utf8"));
    report.status = passed ? "passed" : "failed";
    report.cleanup = "passed";
    await writeFile(reportPath, JSON.stringify(report, null, 2) + "\n");
  }
}
if (import.meta.url === pathToFileURL(process.argv[1]).href)
  await qualifyRunners(
    resolve("output/vscode"),
    process.env.CATALOG_VSCODE_HOST,
    process.env.CATALOG_VSCODE_VSIX,
    process.env.CATALOG_VSCODE_UVX,
    process.env.CATALOG_VSCODE_GO ?? "/usr/bin/go",
  );
