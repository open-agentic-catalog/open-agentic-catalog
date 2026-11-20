// Disposable browser trust for the Linux packaged fixture. Browser traffic
// still goes through its normal certificate chain and hostname checks.
import { appendFile, mkdir, readFile, writeFile } from "node:fs/promises";
import { join } from "node:path";
import { firefox, webkit, expect } from "@playwright/test";

export async function issueFixtureCertificate(privateDir, directory, command) {
  const ca = join(privateDir, "ca.pem");
  const key = join(privateDir, "ca.key");
  const csr = join(privateDir, "server.csr");
  const extensions = join(privateDir, "leaf.ext");
  const leaf = join(directory, "server.pem");
  await command([
    "openssl",
    "req",
    "-x509",
    "-newkey",
    "rsa:2048",
    "-nodes",
    "-days",
    "2",
    "-subj",
    "/CN=OAC disposable browser CA",
    "-addext",
    "basicConstraints=critical,CA:TRUE",
    "-addext",
    "keyUsage=critical,keyCertSign",
    "-keyout",
    key,
    "-out",
    ca,
  ]);
  await command([
    "openssl",
    "req",
    "-new",
    "-newkey",
    "rsa:2048",
    "-nodes",
    "-subj",
    "/CN=catalog-oidc",
    "-keyout",
    join(directory, "server-key.pem"),
    "-out",
    csr,
  ]);
  await writeFile(
    extensions,
    "basicConstraints=critical,CA:FALSE\nkeyUsage=critical,digitalSignature,keyEncipherment\nextendedKeyUsage=serverAuth\nsubjectAltName=DNS:catalog-oidc,IP:127.0.0.1\n",
  );
  await command([
    "openssl",
    "x509",
    "-req",
    "-in",
    csr,
    "-CA",
    ca,
    "-CAkey",
    key,
    "-CAcreateserial",
    "-days",
    "2",
    "-extfile",
    extensions,
    "-out",
    leaf,
  ]);
  await appendFile(leaf, await readFile(ca));
}

export async function openEngineContext(engine, directory, command) {
  const ca = join(directory, "ca.pem");
  if (engine === "firefox") {
    const profile = join(directory, "firefox");
    await mkdir(profile, { mode: 0o700 });
    await command([
      "certutil",
      "-N",
      "-d",
      "sql:" + profile,
      "--empty-password",
    ]);
    await command([
      "certutil",
      "-A",
      "-n",
      "OAC fixture",
      "-d",
      "sql:" + profile,
      "-t",
      "C,,",
      "-i",
      ca,
    ]);
    return firefox.launchPersistentContext(profile, {
      firefoxUserPrefs: {
        "network.dns.localDomains": "catalog-oidc,wrong-host",
      },
      handleSIGINT: false,
      handleSIGTERM: false,
    });
  }
  const hosts = join(directory, "hosts");
  await writeFile(
    hosts,
    "127.0.0.1 localhost catalog-oidc wrong-host\n::1 localhost\n",
  );
  const shim = join(directory, "webkit-launch");
  await writeFile(
    shim,
    '#!/bin/sh\nexec bwrap --dev-bind / / --ro-bind "$OAC_ENGINE_HOSTS" /etc/hosts --ro-bind "$OAC_ENGINE_CA" /etc/ssl/certs/ca-certificates.crt -- "$OAC_ENGINE_BINARY" "$@"\n',
    { mode: 0o700 },
  );
  const browser = await webkit.launch({
    executablePath: shim,
    env: {
      ...process.env,
      OAC_ENGINE_HOSTS: hosts,
      OAC_ENGINE_CA: ca,
      OAC_ENGINE_BINARY: webkit.executablePath(),
    },
    handleSIGINT: false,
    handleSIGTERM: false,
  });
  try {
    return await browser.newContext();
  } catch (error) {
    await browser.close();
    throw error;
  }
}

export async function verifyIssuerTLS(context, issuer, engine) {
  const url = new URL(issuer + "/.well-known/openid-configuration");
  const page = await context.newPage();
  try {
    const result = await page.goto(url.href);
    expect(result.status()).toBe(200);
    expect((await result.json()).issuer === issuer).toBe(true);
    url.hostname = "wrong-host";
    await expect(page.goto(url.href)).rejects.toThrow(
      engine === "firefox"
        ? /SSL_ERROR_BAD_CERT_DOMAIN/
        : /Unacceptable TLS certificate/,
    );
  } finally {
    await page.close();
  }
}

// Use an independent page for deliberate CSP refusals so the application
// journey can still require an empty console and network ledger.
export async function verifyInlineStylePolicy(context, url) {
  const page = await context.newPage();
  try {
    await page.goto(url);
    await page.evaluate(() => {
      const target = globalThis.document.createElement("div");
      target.id = "fixture-style-control";
      globalThis.document.body.append(target);
      // text-overflow is normally non-inherited. Only the admitted attribute
      // should make the child inherit the parent's computed value.
      const parent = globalThis.document.createElement("div");
      parent.style.textOverflow = "ellipsis";
      const allowed = globalThis.document.createElement("span");
      allowed.setAttribute("style", "text-overflow:inherit");
      parent.append(allowed);
      target.append(parent);
      globalThis.document.addEventListener(
        "securitypolicyviolation",
        (event) => {
          if (
            event.blockedURI === "inline" &&
            event.effectiveDirective === "style-src-attr"
          )
            target.dataset.attributeRefused = "yes";
          if (
            event.blockedURI === "inline" &&
            event.effectiveDirective === "style-src-elem"
          )
            target.dataset.elementRefused = "yes";
        },
      );
      target.setAttribute("style", "visibility:hidden");
      const style = globalThis.document.createElement("style");
      style.textContent = "#fixture-style-control { visibility: hidden; }";
      globalThis.document.head.append(style);
    });
    const control = page.locator("#fixture-style-control");
    await expect(control.locator("span")).toHaveCSS(
      "text-overflow",
      "ellipsis",
    );
    await expect(control).toHaveAttribute("data-attribute-refused", "yes");
    await expect(control).toHaveAttribute("data-element-refused", "yes");
    await expect(control).toHaveCSS("visibility", "visible");
  } finally {
    await page.close();
  }
}
