import assert from 'node:assert/strict';
import { spawnSync } from 'node:child_process';
import fs from 'node:fs';
import os from 'node:os';
import path from 'node:path';
import { project, root } from './site.mjs';
import { bundle, manifestName } from './bundle.mjs';

// Run the documented Make target against a sealed one-page public edition.
export function deliveryFixture(t) {
  const checkout = fs.mkdtempSync(path.join(os.tmpdir(), 'catalog-docs-delivery-test-'));
  t.after(() => fs.rmSync(checkout, { recursive: true, force: true }));
  const docs = path.join(checkout, 'documentation');
  fs.cpSync(path.join(project, 'scripts'), path.join(docs, 'scripts'), { recursive: true });
  for (const file of ['Makefile', 'go.mod', 'VERSION', 'documentation/package-lock.json']) {
    fs.copyFileSync(path.join(root, file), path.join(checkout, file));
  }
  fs.symlinkSync(path.join(project, 'node_modules'), path.join(docs, 'node_modules'), 'dir');
  fs.writeFileSync(path.join(docs, 'content-map.json'), JSON.stringify([{ route: '', source: 'index.md' }]));
  fs.mkdirSync(path.join(checkout, 'api/openapi'), { recursive: true });
  fs.writeFileSync(path.join(checkout, 'api/openapi/control-v1.yaml'),
    'openapi: 3.1.0\ninfo: {title: Test manual, version: 1.0.0}\npaths: {}\n');

  const input = path.join(docs, 'dist');
  const site = 'https://example.test', base = '/catalog/', commit = 'a'.repeat(40);
  const version = fs.readFileSync(path.join(checkout, 'VERSION'), 'utf8').trim();
  const html = `<nav class="sidebar"><a href="${base}">Home</a></nav><main><h1>Catalog delivery guide</h1>
    <div class="sl-markdown-content"><p>Version ${version}. Source ${commit}</p><p>Verify the PDF checksum.</p>
    <a href="${site}${base}catalog-manual.pdf" download>Download the manual</a></div></main>`;
  fs.mkdirSync(path.join(input, 'generated'), { recursive: true });
  fs.copyFileSync(path.join(root, 'LICENSE'), path.join(input, 'generated/LICENSE'));
  fs.writeFileSync(path.join(input, 'generated/third-party-notices.txt'), 'Fixture notice\n');
  fs.writeFileSync(path.join(input, 'index.html'), html);
  const lock = fs.readFileSync(path.join(docs, 'package-lock.json'));
  fs.writeFileSync(path.join(input, manifestName), JSON.stringify(bundle(input, base, lock)) + '\n');

  const delivery = path.join(checkout, 'output/documentation-delivery');
  const output = path.join(delivery, 'edition');
  fs.mkdirSync(delivery, { recursive: true });
  const neighbors = { 'edition.pdf': '%PDF-previous download', 'edition.pdf.json': '{"previous":true}' };
  for (const [name, bytes] of Object.entries(neighbors)) fs.writeFileSync(path.join(delivery, name), bytes);

  function run() {
    const result = spawnSync('make', ['docs-delivery', 'CATALOG_ENV_FILE=/dev/null', 'DOCS_BASE=' + base,
      'DOCS_SITE=' + site, 'DOCS_EDITION_COMMIT=' + commit, 'DOCS_DELIVERY_OUTPUT=' + output], {
      cwd: checkout, encoding: 'utf8', timeout: 90000,
    });
    assert.ifError(result.error);
    return result;
  }
  function checkNeighbors(editionCreated) {
    const expected = Object.keys(neighbors);
    if (editionCreated) expected.push('edition');
    assert.deepEqual(fs.readdirSync(delivery).sort(), expected.sort());
    for (const [name, bytes] of Object.entries(neighbors)) {
      assert.equal(fs.readFileSync(path.join(delivery, name), 'utf8'), bytes);
    }
  }
  return { checkout, input, output, run, checkNeighbors };
}
