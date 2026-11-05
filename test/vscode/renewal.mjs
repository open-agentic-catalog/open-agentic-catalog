// Two real editor windows share the production extension's native SecretStorage.
import assert from "node:assert/strict";
import { listenFixture } from "./listen.mjs";
import { createServer } from "node:http";
import { spawn, execFileSync } from "node:child_process";
import { createHash } from "node:crypto";
import { resolveCliArgsFromVSCodeExecutablePath } from "../../extensions/vscode/node_modules/@vscode/test-electron/out/index.js";
import { mkdtemp, mkdir, readFile, writeFile, rm, cp } from "node:fs/promises";
import { basename, join, resolve } from "node:path";
import {
  generateKeyPair,
  exportJWK,
  SignJWT,
  jwtVerify,
} from "../../extensions/vscode/node_modules/jose/dist/webapi/index.js";

const root = resolve(import.meta.dirname, "../..");
const revoked = process.argv.includes("--revoked");
const fixtureDeadline = performance.now() + 45000;
const work = await mkdtemp(join(root, "output/vscode/renewal-"));
const { publicKey, privateKey } = await generateKeyPair("RS256");
const jwk = {
  ...(await exportJWK(publicKey)),
  kid: "owned-renewal",
  alg: "RS256",
};
let origin,
  refreshes = 0,
  heldKeys,
  releaseKeys;
let initialNonce, initialChallenge;
const failures = [];
const issuer = createServer(async (request, response) => {
  try {
    const path = new URL(request.url, origin).pathname;
    const json = (value, status = 200) => {
      response.writeHead(status, { "content-type": "application/json" });
      response.end(JSON.stringify(value));
    };
    if (path === "/.well-known/openid-configuration")
      return json({
        issuer: origin,
        authorization_endpoint: origin + "/authorize",
        token_endpoint: origin + "/token",
        jwks_uri: origin + "/keys",
        response_types_supported: ["code"],
        code_challenge_methods_supported: ["S256"],
      });
    if (path === "/authorize") {
      const q = new URL(request.url, origin).searchParams;
      initialNonce = q.get("nonce");
      initialChallenge = q.get("code_challenge");
      const callback = new URL(q.get("redirect_uri"));
      callback.searchParams.set("state", q.get("state"));
      callback.searchParams.set("code", "owned-code");
      response.writeHead(302, { location: callback.href });
      return response.end();
    }
    if (path === "/keys") {
      if (heldKeys) await heldKeys;
      return json({ keys: [jwk] });
    }
    const sign = (audience, seconds, extra = {}) =>
      new SignJWT(extra)
        .setProtectedHeader({ alg: "RS256", kid: jwk.kid })
        .setIssuer(origin)
        .setAudience(audience)
        .setSubject("owned-renewal-reader")
        .setIssuedAt()
        .setExpirationTime(Math.floor(Date.now() / 1000) + seconds)
        .sign(privateKey);
    if (path === "/token") {
      const chunks = [];
      for await (const chunk of request) chunks.push(chunk);
      const q = new URLSearchParams(Buffer.concat(chunks).toString());
      if (q.get("grant_type") === "refresh_token") {
        assert.equal(q.get("refresh_token"), "owned-initial-refresh");
        refreshes++;
        if (revoked || refreshes > 1)
          return json({ error: "invalid_grant" }, 400);
        heldKeys = new Promise((done) => {
          releaseKeys = done;
        });
        return json({
          token_type: "Bearer",
          expires_in: 3600,
          access_token: await sign("catalog", 3600),
          refresh_token: "owned-rotated-refresh",
        });
      }
      assert.equal(q.get("grant_type"), "authorization_code");
      assert.ok(initialChallenge);
      assert.equal(
        createHash("sha256").update(q.get("code_verifier")).digest("base64url"),
        initialChallenge,
      );
      return json({
        token_type: "Bearer",
        expires_in: 45,
        access_token: await sign("catalog", 45),
        id_token: await sign("extension", 3600, { nonce: initialNonce }),
        refresh_token: "owned-initial-refresh",
      });
    }
    if (path === "/api/v1/me" || path === "/api/v1/capabilities") {
      await jwtVerify(
        request.headers.authorization?.replace(/^Bearer /u, ""),
        publicKey,
        { issuer: origin, audience: "catalog" },
      );
      return json(
        path.endsWith("/me")
          ? {
              tenantId: "00000000-0000-7000-8000-000000000001",
              realmId: "00000000-0000-7000-8000-000000000002",
              membershipId: "00000000-0000-7000-8000-000000000003",
              subject: "owned-renewal-reader",
              permissions: ["resource.read"],
              availablePermissions: ["resource.read"],
            }
          : { resourceKinds: ["agent"] },
      );
    }
    return json({}, 404);
  } catch (error) {
    failures.push(String(error));
    response.writeHead(500);
    response.end();
  }
});
await listenFixture(issuer);
origin = `http://127.0.0.1:${issuer.address().port}`;
const blocked = createServer((_, response) => {
  response.writeHead(403);
  response.end();
});
blocked.on("connect", (_, socket) =>
  socket.end("HTTP/1.1 403 Forbidden\r\n\r\n"),
);
await new Promise((done) => blocked.listen(0, "127.0.0.1", done));
const proxy = `http://127.0.0.1:${blocked.address().port}`;
let editor,
  diagnostics = "",
  stage = "setup";
async function waitFile(name, timeout = 45000) {
  stage = name;
  const deadline = Math.min(fixtureDeadline, performance.now() + timeout);
  while (performance.now() < deadline) {
    try {
      return JSON.parse(await readFile(join(work, name), "utf8"));
    } catch {
      /* The owned readiness file may not exist yet. */
    }
    if (name.endsWith("-ready.json")) {
      try {
        const result = JSON.parse(
          await readFile(
            join(work, name.replace("-ready.json", "-result.json")),
            "utf8",
          ),
        );
        if (result.error)
          throw new Error(
            "Native initial login failed: " + JSON.stringify(result),
          );
      } catch (error) {
        if (error.code !== "ENOENT") throw error;
      }
    }
    await new Promise((done) => setTimeout(done, 25));
  }
  throw new Error(`Native renewal deadline: ${name}\n${diagnostics}`);
}
try {
  await mkdir(join(work, "user/User"), { recursive: true });
  for (const name of ["window-a", "window-b", "driver"])
    await mkdir(join(work, name));
  await writeFile(
    join(work, "user/User/settings.json"),
    JSON.stringify({
      "openAgenticCatalog.connections": [
        {
          id: "renewal",
          label: "Owned renewal",
          origin,
          mode: "control",
          auth: "oidc",
          allowLoopbackHTTP: true,
          oidc: {
            issuer: origin,
            clientId: "extension",
            audience: "catalog",
            scopes: ["openid"],
            refresh: true,
          },
        },
      ],
      "telemetry.telemetryLevel": "off",
      "update.mode": "none",
      "extensions.autoUpdate": false,
      "extensions.autoCheckUpdates": false,
      "workbench.enableExperiments": false,
      "window.dialogStyle": "custom",
      "http.proxy": proxy,
      "http.proxySupport": "override",
      "http.noProxy": ["127.0.0.1"],
    }),
  );
  await writeFile(
    join(work, "driver/package.json"),
    JSON.stringify({
      name: "catalog-renewal-driver",
      publisher: "local",
      version: "0.0.1",
      engines: { vscode: "^1.139.1" },
      main: "index.cjs",
      activationEvents: ["onStartupFinished"],
    }),
  );
  await writeFile(
    join(work, "driver/index.cjs"),
    `
const vscode=require('vscode'),fs=require('node:fs/promises'),path=require('node:path');
exports.activate=(context)=>{void(async()=>{const lane=path.basename(vscode.workspace.workspaceFolders[0].uri.fsPath),work=${JSON.stringify(work)};
const save=(name,value)=>fs.writeFile(path.join(work,name),JSON.stringify(value));
const wait=async(name)=>{const end=performance.now()+40000;while(performance.now()<end){try{return JSON.parse(await fs.readFile(path.join(work,name),'utf8'));}catch{}await new Promise(done=>setTimeout(done,20));}throw new Error('barrier '+name);};
try {const extension=vscode.extensions.getExtension('open-agentic-catalog.open-agentic-catalog'),api=await extension.activate();await api.select('renewal');
if(lane==='window-a'){const url=await api.beginLogin('vscode://open-agentic-catalog.open-agentic-catalog/oidc'),response=await fetch(url,{redirect:'manual'});await api.completeLogin(response.headers.get('location'));await save('login.json',{ready:true,storage:context.globalStorageUri.fsPath});}else{await wait('login.json');await api.select('renewal');}
await save(lane+'-ready.json',{ready:true,storage:context.globalStorageUri.fsPath});await wait('start.json');await save(lane+'-started.json',{ready:true});
let initial;try{await api.test();initial='verified';}catch(error){initial=error.code??'unexpected';}
await save(lane+'-initial.json',{initial});await wait('later.json');
let later,laterAttempts=0;const deadline=performance.now()+5000;do {laterAttempts++;try{await api.select('renewal');later=(await api.test()).identity.subject;}catch(error){later=error.code??'unexpected';}if(!['credential_in_use','credential_update_pending','connection_changed'].includes(later))break;await new Promise(done=>setTimeout(done,20));}while(performance.now()<deadline);
await save(lane+'-result.json',{initial,later});
}catch(error){await save(lane+'-result.json',{error:String(error.stack??error),cause:String(error.cause?.message??''),causeCode:String(error.cause?.code??''),issuerPort:${JSON.stringify(new URL(origin).port)}});}})();};`,
  );
  await mkdir(join(work, "extensions"));
  await cp(
    join(work, "driver"),
    join(work, "extensions/local.catalog-renewal-driver-0.0.1"),
    { recursive: true },
  );
  const target = join(
    work,
    "extensions/open-agentic-catalog.open-agentic-catalog-0.1.0",
  );
  if (!process.env.CATALOG_VSCODE_VSIX) {
    await mkdir(join(target, "dist"), { recursive: true });
    await cp(
      join(root, "extensions/vscode/package.json"),
      join(target, "package.json"),
    );
    await cp(
      join(root, "extensions/vscode/dist/extension.cjs"),
      join(target, "dist/extension.cjs"),
    );
  }
  const profile = [
    "--shared-data-dir",
    join(work, "shared"),
    "--user-data-dir",
    join(work, "user"),
    "--extensions-dir",
    join(work, "extensions"),
  ];
  const flags = [
    ...profile,
    "--no-sandbox",
    "--disable-gpu",
    "--skip-welcome",
    "--skip-release-notes",
    "--disable-updates",
    "--disable-telemetry",
    "--disable-workbench-experiments",
    "--disable-workspace-trust",
    "--password-store=basic",
    `--proxy-server=${proxy}`,
    "--proxy-bypass-list=127.0.0.1;[::1]",
  ];
  const env = {
    ...process.env,
    ELECTRON_RUN_AS_NODE: undefined,
    DONT_PROMPT_WSL_INSTALL: "1",
  };
  if (process.env.CATALOG_VSCODE_VSIX) {
    const [cli, ...args] = resolveCliArgsFromVSCodeExecutablePath(
      process.env.CATALOG_VSCODE_HOST,
    );
    execFileSync(
      cli,
      [
        ...args,
        ...profile,
        "--install-extension",
        process.env.CATALOG_VSCODE_VSIX,
        "--force",
      ],
      { env, stdio: "pipe", timeout: 60000 },
    );
  }
  editor = spawn(
    process.env.CATALOG_VSCODE_HOST,
    [...flags, join(work, "window-a")],
    { env, detached: true, stdio: ["ignore", "pipe", "pipe"] },
  );
  for (const stream of [editor.stdout, editor.stderr])
    stream.on("data", (chunk) => {
      diagnostics = (diagnostics + chunk.toString()).slice(-12000);
    });
  const aReady = await waitFile("window-a-ready.json");
  const second = spawn(
    process.env.CATALOG_VSCODE_HOST,
    [...flags, "--new-window", join(work, "window-b")],
    { env, stdio: "ignore" },
  );
  await new Promise((done, reject) => {
    second.once("error", reject);
    second.once("exit", (code) =>
      code === 0 ? done() : reject(new Error("Second editor refused")),
    );
  });
  const bReady = await waitFile("window-b-ready.json");
  assert.equal(
    aReady.storage,
    bReady.storage,
    "actual windows use the same native storage location",
  );
  await writeFile(join(work, "start.json"), "{}");
  await Promise.all([
    waitFile("window-a-started.json"),
    waitFile("window-b-started.json"),
  ]);
  if (!revoked) {
    const deadline = Math.min(fixtureDeadline, performance.now() + 10000);
    while (!releaseKeys && performance.now() < deadline)
      await new Promise((done) => setTimeout(done, 20));
    assert.ok(releaseKeys, "real refresh reaches held JWKS verification");
    // Let the competing native window reach the ownership boundary while held.
    await new Promise((done) => setTimeout(done, 500));
    releaseKeys();
  }
  heldKeys = undefined;
  const initial = await Promise.all([
    waitFile("window-a-initial.json"),
    waitFile("window-b-initial.json"),
  ]);
  await writeFile(join(work, "later.json"), "{}");
  const results = await Promise.all([
    waitFile("window-a-result.json"),
    waitFile("window-b-result.json"),
  ]);
  const report = {
    refreshes,
    mode: revoked ? "revoked" : "rotated",
    initial,
    results,
    failures,
  };
  console.log(JSON.stringify(report));
  assert.deepEqual(failures, []);
  assert.equal(
    refreshes,
    1,
    "one native window owns the rotating refresh exchange",
  );
  for (const result of results)
    assert.equal(
      result.later,
      revoked ? "sign_in_required" : "owned-renewal-reader",
      "competing renewal must preserve the current valid login",
    );
} catch (error) {
  // Keep bounded progress evidence after deleting profiles and credentials.
  // Native logs and driver error stacks can contain private inputs; omit them.
  const windows = [];
  for (const lane of ["window-a", "window-b"]) {
    const evidence = { lane, ready: false, completed: false, failed: false };
    for (const suffix of ["ready", "result"]) {
      try {
        const value = JSON.parse(
          await readFile(join(work, `${lane}-${suffix}.json`), "utf8"),
        );
        if (suffix === "ready") evidence.ready = value.ready === true;
        else {
          evidence.failed = typeof value.error === "string";
          evidence.completed =
            typeof value.initial === "string" &&
            typeof value.later === "string";
        }
      } catch {
        // Missing/incomplete evidence is not a successful driver result.
      }
    }
    windows.push(evidence);
  }
  await writeFile(
    join(root, "output/vscode", basename(work) + "-failure.json"),
    JSON.stringify({
      status: "failed",
      mode: revoked ? "revoked" : "rotated",
      stage,
      editorExitCode: editor?.exitCode ?? null,
      refreshes,
      issuerFailures: failures.length,
      windows,
    }) + "\n",
  );
  throw error;
} finally {
  releaseKeys?.();
  if (editor) {
    for (const signal of ["SIGTERM", "SIGKILL"])
      try {
        process.kill(-editor.pid, signal);
        await new Promise((done) => setTimeout(done, 250));
      } catch (error) {
        assert.equal(error.code, "ESRCH", "owned editor cleanup failed");
      }
  }
  issuer.closeAllConnections();
  blocked.closeAllConnections();
  await Promise.all([
    new Promise((done) => issuer.close(done)),
    new Promise((done) => blocked.close(done)),
  ]);
  await rm(work, { recursive: true, force: true });
}
