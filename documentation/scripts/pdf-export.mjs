// Optional local PDF export. Reads a sealed site; owns only its print resources.
import fs from 'node:fs';
import path from 'node:path';
import http from 'node:http';
import { createHash, randomUUID } from 'node:crypto';
import { pathToFileURL } from 'node:url';
import { parse as yaml } from 'yaml';
import { bundle, manifestName } from './bundle.mjs';
import { project, root, entries } from './site.mjs';
import { pageInventory, chapter, printChapter, styleInputs, assemble } from './pdf.mjs';

const sha = bytes => createHash('sha256').update(bytes).digest('hex');
export const browserDirectory = path.join(root, '.tools/documentation-pdf');
export const defaults = {timeoutMs: 900000, renderTimeoutMs: 600000};
const browserVersion = '1.64.0';
const mime = {'.css':'text/css', '.svg':'image/svg+xml', '.png':'image/png', '.jpg':'image/jpeg', '.jpeg':'image/jpeg', '.webp':'image/webp', '.woff':'font/woff', '.woff2':'font/woff2', '.ttf':'font/ttf', '.otf':'font/otf'};

export function readBuild(directory, lockBytes) {
  const input = fs.realpathSync(directory);
  const manifestFile = path.join(input, manifestName);
  if (!fs.lstatSync(manifestFile).isFile()) throw new Error('Missing completed manual manifest');
  const bytes = fs.readFileSync(manifestFile);
  if (bytes.length > 2 * 1024 * 1024) throw new Error('Excessive manual manifest');
  const manifest = JSON.parse(bytes);
  const actual = bundle(input, manifest.base, lockBytes);
  if (JSON.stringify(manifest) !== JSON.stringify(actual)) throw new Error('Incomplete or changed manual; run make docs-build');
  return {input, manifest, manifestSha256: sha(bytes), files: new Set(manifest.files.map(file => file.path))};
}

function currentContract() {
  const contract = yaml(fs.readFileSync(path.join(root, 'api/openapi/control-v1.yaml'), 'utf8'));
  const operations = Object.values(contract.paths).flatMap(item => Object.entries(item)
    .filter(([method]) => ['get','post','put','patch','delete','head','options','trace'].includes(method)).map(([,operation]) => operation));
  return {manualRoutes: entries.map(entry => entry.route), operationIds: operations.map(operation => operation.operationId),
    apiTags:[...new Set([...(contract.tags ?? []).map(tag => tag.name), ...operations.flatMap(operation => operation.tags ?? [])])]};
}

function abortError() { return new Error('Export interrupted or deadline exceeded'); }
function guard(promise, signal) {
  if (signal.aborted) { promise.catch(() => {}); return Promise.reject(abortError()); }
  return new Promise((resolve, reject) => {
    const abort = () => reject(abortError());
    signal.addEventListener('abort', abort, {once:true});
    promise.then(resolve, reject).finally(() => signal.removeEventListener('abort', abort));
  });
}
async function bounded(promise, milliseconds) {
  let timer;
  try { return await Promise.race([promise, new Promise((_, reject) => { timer = setTimeout(() => reject(new Error('Cleanup deadline exceeded')), milliseconds); })]); }
  finally { clearTimeout(timer); }
}

// The asset server accepts only the sealed inventory and its own print document.
async function serve(build, html, signal) {
  const printPath = '/_manual-print-' + randomUUID() + '.html';
  const failures = [];
  const server = http.createServer((request, response) => {
    const pathname = new URL(request.url, 'http://manual.invalid').pathname;
    response.setHeader('Content-Security-Policy', "default-src 'none'; script-src 'none'; style-src 'self' 'unsafe-inline'; img-src 'self' data:; font-src 'self'; base-uri 'none'; frame-src 'none'");
    if (!['GET','HEAD'].includes(request.method)) { failures.push('method'); response.writeHead(405).end(); return; }
    if (pathname === printPath) {
      response.writeHead(200, {'Content-Type':'text/html; charset=utf-8'});
      response.end(request.method === 'HEAD' ? undefined : html);
      return;
    }
    const relative = pathname.startsWith(build.manifest.base) ? pathname.slice(build.manifest.base.length) : '';
    if (!build.files.has(relative)) { failures.push('missing-asset'); response.writeHead(404).end(); return; }
    try {
      const bytes = fs.readFileSync(path.join(build.input, relative));
      response.writeHead(200, {'Content-Type': mime[path.extname(relative)] ?? 'application/octet-stream'});
      response.end(request.method === 'HEAD' ? undefined : bytes);
    } catch { failures.push('unreadable-asset'); response.writeHead(404).end(); }
  });
  await new Promise((resolve, reject) => { server.once('error', reject); server.listen(0, '127.0.0.1', resolve); });
  if (signal.aborted) { server.closeAllConnections(); await new Promise(resolve => server.close(resolve)); throw abortError(); }
  const origin = 'http://127.0.0.1:' + server.address().port;
  return {server, origin, printPath, failures};
}

async function cleanup(browser, browserServer, assetServer) {
  let failed = false;
  if (browser) try { await bounded(browser.close(), 5000); } catch { /* The owned server below remains the process owner. */ }
  if (browserServer) {
    try { await bounded(browserServer.close(), 5000); }
    catch { try { await bounded(browserServer.kill(), 5000); } catch { failed = true; } }
  }
  if (assetServer) {
    assetServer.closeAllConnections();
    try { await bounded(new Promise(resolve => assetServer.close(resolve)), 5000); } catch { failed = true; }
  }
  if (failed) throw new Error('Owned print resource cleanup failed');
}

export async function exportManual({input = path.join(project, 'dist'), output = path.join(root, 'output/documentation-pdf/catalog-manual.pdf'), contract = currentContract(), browserPath = browserDirectory, timeoutMs = defaults.timeoutMs, renderTimeoutMs = defaults.renderTimeoutMs, signal, onStage = () => {}} = {}) {
  if (!Number.isSafeInteger(timeoutMs) || timeoutMs <= 0 || timeoutMs > defaults.timeoutMs || !Number.isSafeInteger(renderTimeoutMs) || renderTimeoutMs <= 0 || renderTimeoutMs > defaults.renderTimeoutMs) throw new Error('Invalid PDF deadline');
  const controller = new AbortController();
  const abort = () => controller.abort('interrupted');
  const interrupted = () => { if (controller.signal.aborted) throw abortError(); };
  const overall = setTimeout(() => controller.abort('overall deadline exceeded'), timeoutMs);
  signal?.addEventListener('abort', abort, {once:true});
  if (signal?.aborted) abort();
  const runId = randomUUID();
  let stage = 'inputs', browser, browserServer, asset, candidate, reportPath, result;
  const evidence = {runId, cleanup:'not-run'};
  const phase = value => { stage = value; onStage(value, {...evidence}); interrupted(); };
  try {
    phase('inputs');
    const lockBytes = fs.readFileSync(path.join(project, 'package-lock.json'));
    const versionBytes = fs.readFileSync(path.join(root, 'VERSION'));
    evidence.applicationVersion = versionBytes.toString('utf8').trim();
    const build = readBuild(input, lockBytes);
    output = path.resolve(output);
    // Resolve the prospective parent before creating anything, including through aliases.
    let ancestor = path.dirname(output);
    const missing = [];
    while (!fs.existsSync(ancestor)) {
      missing.unshift(path.basename(ancestor));
      ancestor = path.dirname(ancestor);
    }
    const prospectiveParent = path.join(fs.realpathSync(ancestor), ...missing);
    if (prospectiveParent === build.input || prospectiveParent.startsWith(build.input + path.sep) || path.extname(output) !== '.pdf') throw new Error('PDF output must be outside the built manual');
    fs.mkdirSync(path.dirname(output), {recursive:true, mode:0o700});
    const realParent = fs.realpathSync(path.dirname(output));
    if (realParent !== prospectiveParent) throw new Error('PDF output parent changed during preparation');
    if (fs.existsSync(output)) {
      if (!fs.lstatSync(output).isFile() || !fs.readFileSync(output).subarray(0,5).equals(Buffer.from('%PDF-'))) throw new Error('Refusing to replace a non-PDF output');
    }
    phase('prepare');
    const options = {...contract, base:build.manifest.base, files:build.files};
    const inventory = pageInventory(fs.readFileSync(path.join(build.input, 'index.html'), 'utf8'), options);
    const chapters = [], styles = [], links = [];
    for (const [index, value] of inventory.entries()) {
      const source = fs.readFileSync(path.join(build.input, value.file), 'utf8');
      chapters.push(chapter(source, value, index));
      const sheet = styleInputs(source, value.route, build.manifest.base, build.files);
      styles.push(...sheet.styles); links.push(...sheet.links);
    }
    const rendered = chapters.map(value => printChapter(value, chapters, options));
    const html = assemble(chapters, rendered, {styles, links, css:fs.readFileSync(new URL('./pdf.css', import.meta.url), 'utf8'),
      notices:fs.readFileSync(path.join(build.input, 'generated/third-party-notices.txt'), 'utf8'),
      license:fs.readFileSync(path.join(build.input, 'generated/LICENSE'), 'utf8'), version:evidence.applicationVersion});
    if (Buffer.byteLength(html) > 256 * 1024 * 1024) throw new Error('Print input exceeds the manual bound');
    evidence.pages = chapters.length;
    evidence.manualPages = contract.manualRoutes.length;
    evidence.generatedAPIPages = chapters.filter(ch => ch.route.startsWith('api/')).length;
    evidence.operationPages = contract.operationIds.length;
    evidence.manifestSha256 = build.manifestSha256;
    evidence.lockSha256 = sha(lockBytes);
    evidence.printHTMLSha256 = sha(html);
    evidence.routes = inventory.map(value => value.route);
    phase('browser');
    process.env.PLAYWRIGHT_BROWSERS_PATH = browserPath;
    const installed = JSON.parse(fs.readFileSync(path.join(project, 'node_modules/playwright/package.json'), 'utf8'));
    const core = JSON.parse(fs.readFileSync(path.join(project, 'node_modules/playwright-core/package.json'), 'utf8'));
    if (installed.version !== browserVersion || core.version !== browserVersion) throw new Error('Install the pinned documentation Playwright packages');
    const {chromium} = await import('playwright');
    interrupted();
    // Keep acquisition explicit. LaunchServer provides a public owned-process handle.
    browserServer = await chromium.launchServer({host:'127.0.0.1', headless:true, timeout:30000, args:['--no-proxy-server']});
    evidence.browserPid = browserServer.process().pid;
    interrupted();
    browser = await guard(chromium.connect(browserServer.wsEndpoint(), {timeout:30000}), controller.signal);
    evidence.browserVersion = browser.version();
    evidence.playwrightVersion = installed.version;
    const revision = JSON.parse(fs.readFileSync(path.join(project, 'node_modules/playwright-core/browsers.json'), 'utf8')).browsers.find(value => value.name === 'chromium-headless-shell');
    if (evidence.browserVersion !== revision.browserVersion) throw new Error('The installed browser version does not match its pin');
    evidence.browserRevision = revision.revision;
    evidence.nodeVersion = process.versions.node;
    evidence.platform = process.platform + '/' + process.arch;
    const binary = createHash('sha256');
    for await (const chunk of fs.createReadStream(browserServer.process().spawnfile)) binary.update(chunk);
    evidence.browserExecutableSha256 = binary.digest('hex');
    asset = await serve(build, html, controller.signal);
    evidence.assetPort = asset.server.address().port;
    const context = await guard(browser.newContext({viewport:{width:688,height:1000}, colorScheme:'light', serviceWorkers:'block'}), controller.signal);
    let refused = 0, loadErrors = 0;
    await guard(context.route('**/*', route => {
      const request = route.request();
      if (new URL(request.url()).origin !== asset.origin || !['GET','HEAD'].includes(request.method())) { refused++; return route.abort(); }
      return route.continue();
    }), controller.signal);
    const page = await guard(context.newPage(), controller.signal);
    page.on('pageerror', () => loadErrors++);
    page.on('console', message => { if (message.type() === 'error') loadErrors++; });
    page.setDefaultTimeout(30000);
    await guard(page.emulateMedia({media:'print'}), controller.signal);
    await guard(page.goto(asset.origin + asset.printPath, {waitUntil:'load',timeout:120000}), controller.signal);
    phase('layout');
    // Parse the entire book before its first layout, rather than reflow each HTTP chunk.
    await guard(page.evaluate(() => globalThis.document.body.style.removeProperty('display')), controller.signal);
    await guard(page.evaluate(async () => {
      await globalThis.document.fonts.ready;
      await Promise.all(Array.from(globalThis.document.images).map(image => image.decode()));
    }), controller.signal);
    const layout = await guard(page.evaluate(() => ({
      chapters:globalThis.document.querySelectorAll('.pdf-chapter').length,
      panels:globalThis.document.querySelectorAll('.pdf-panel').length,
      diagrams:globalThis.document.querySelectorAll('svg.architecture-svg').length,
      images:globalThis.document.images.length,
      apiFields:globalThis.document.querySelectorAll('.sl-openapi-key').length,
      unstackedAPIFields:Array.from(globalThis.document.querySelectorAll('.sl-openapi-key')).filter(node => globalThis.getComputedStyle(node).display !== 'block').length,
      clippedBlocks:Array.from(globalThis.document.querySelectorAll('pre, table, .pdf-panel')).filter(node => node.clientWidth > 0 && node.scrollWidth > node.clientWidth + 2).length,
      bodyVisible:globalThis.getComputedStyle(globalThis.document.body).display !== 'none',
      documentFits:globalThis.document.documentElement.scrollWidth <= globalThis.innerWidth + 1,
      hiddenPanels:Array.from(globalThis.document.querySelectorAll('.pdf-panel')).filter(node => globalThis.getComputedStyle(node).display === 'none' || node.getBoundingClientRect().height === 0).length,
    })), controller.signal);
    evidence.layout = layout;
    if (layout.chapters !== chapters.length || !layout.bodyVisible || !layout.documentFits || layout.hiddenPanels || layout.unstackedAPIFields || layout.clippedBlocks || refused || loadErrors || asset.failures.length) throw new Error('Print content or asset validation failed');
    phase('render');
    const rendering = setTimeout(() => controller.abort('print deadline exceeded'), renderTimeoutMs);
    let pdf;
    try { pdf = await guard(page.pdf({format:'A4', preferCSSPageSize:true, printBackground:true, outline:true, tagged:true,
      displayHeaderFooter:true, headerTemplate:'<span></span>', footerTemplate:'<div style="font:8px sans-serif;width:100%;text-align:center"><span class="pageNumber"></span> / <span class="totalPages"></span></div>'}), controller.signal); }
    finally { clearTimeout(rendering); }
    phase('verify');
    if (pdf.length < 1000 || !pdf.subarray(0,5).equals(Buffer.from('%PDF-')) || !pdf.subarray(-1024).includes(Buffer.from('%%EOF'))) throw new Error('Incomplete PDF output');
    if (readBuild(input, lockBytes).manifestSha256 !== build.manifestSha256 || sha(fs.readFileSync(path.join(project, 'package-lock.json'))) !== evidence.lockSha256 || !fs.readFileSync(path.join(root, 'VERSION')).equals(versionBytes)) throw new Error('Manual inputs changed during export');
    evidence.taggedPDF = true;
    evidence.pdfSha256 = sha(pdf);
    evidence.pdfBytes = pdf.length;
    evidence.base = build.manifest.base;
    evidence.downloadReferences = rendered.reduce((count, page) => count + page.downloads, 0);
    evidence.visualQualification = 'not-assessed-by-export';
    phase('cleanup');
    await cleanup(browser, browserServer, asset.server);
    browser = browserServer = asset = undefined;
    evidence.cleanup = 'passed';
    phase('commit');
    const stem = path.basename(output, '.pdf');
    candidate = path.join(realParent, '.' + stem + '-' + runId + '.pdf');
    reportPath = path.join(realParent, stem + '-' + runId + '.json');
    fs.writeFileSync(candidate, pdf, {flag:'wx',mode:0o600});
    // This per-invocation report proves rendering, not a mutable latest-file pointer.
    fs.writeFileSync(reportPath, JSON.stringify({...evidence,status:'rendered-and-verified'}, null, 2) + '\n', {flag:'wx',mode:0o600});
    interrupted();
    fs.renameSync(candidate, output);
    candidate = undefined;
    result = {...evidence,status:'complete',output,report:reportPath};
  } catch (cause) {
    const error = new Error('PDF export failed during ' + stage + (controller.signal.aborted ? ': ' + controller.signal.reason : ''));
    error.cause = cause;
    evidence.stage = stage;
    error.evidence = evidence;
    throw error;
  } finally {
    clearTimeout(overall);
    signal?.removeEventListener('abort', abort);
    if (candidate) fs.rmSync(candidate, {force:true});
    try { await cleanup(browser, browserServer, asset?.server); evidence.cleanup = 'passed'; }
    catch { evidence.cleanup = 'failed'; }
  }
  return result;
}

async function main() {
  const args = process.argv.slice(2);
  if (args.length) throw new Error('Use make docs-pdf; no automatic build or installation is performed');
  const controller = new AbortController();
  const stop = () => controller.abort();
  process.once('SIGINT', stop); process.once('SIGTERM', stop);
  try {
    const result = await exportManual({signal:controller.signal, onStage:stage => process.stderr.write('PDF: ' + stage + '\n')});
    console.log('PDF: ' + result.output + '\nReport: ' + result.report + '\nChapters: ' + result.pages);
  } catch (error) {
    console.error(error.message + '. Check the completed build and run make docs-install and make docs-pdf-browser-install for missing tools.');
    if (error.evidence?.stage === 'browser') console.error('For Linux OS-library prerequisites, run: cd documentation && node node_modules/playwright/cli.js install-deps chromium --dry-run');
    process.exitCode = 1;
  } finally { process.off('SIGINT', stop); process.off('SIGTERM', stop); }
}

if (process.argv[1] && import.meta.url === pathToFileURL(path.resolve(process.argv[1])).href) await main();
