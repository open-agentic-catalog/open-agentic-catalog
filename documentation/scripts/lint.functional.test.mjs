// Qualify the owning npm lint command using disposable source and installed tools.
import test from 'node:test';
import assert from 'node:assert/strict';
import fs from 'node:fs';
import os from 'node:os';
import path from 'node:path';
import { fileURLToPath } from 'node:url';
import { spawnSync } from 'node:child_process';

const project = fileURLToPath(new URL('../', import.meta.url));

function fixture(t, installed = true) {
  const root = fs.mkdtempSync(path.join(os.tmpdir(), 'catalog-js-lint-'));
  t.after(() => fs.rmSync(root, { recursive: true, force: true }));
  for (const name of ['package.json', 'eslint.config.mjs']) {
    fs.copyFileSync(path.join(project, name), path.join(root, name));
  }
  // No npm install or global executable fallback during qualification.
  if (installed) fs.symlinkSync(path.join(project, 'node_modules'), path.join(root, 'node_modules'), 'dir');
  return root;
}

function lint(root) {
  return spawnSync('npm', ['--silent', 'run', 'lint', '--', '--format', 'json'], {
    cwd: root, encoding: 'utf8', timeout: 30_000, maxBuffer: 2 << 20,
    env: { ...process.env, NODE_OPTIONS: '', npm_config_update_notifier: 'false' },
  });
}

test('lint checks new MJS, ignores generated output and preserves source', t => {
  const root = fixture(t);
  const source = path.join(root, 'new.mjs');
  const valid = 'export const directory = process.cwd();\n';
  fs.writeFileSync(source, valid);
  // node_modules is symlinked to real dependencies: never write inside it.
  for (const directory of ['.astro', 'dist', 'public/generated', 'src/content/docs/generated']) {
    const target = path.join(root, directory);
    fs.mkdirSync(target, { recursive: true });
    fs.writeFileSync(path.join(target, 'invalid.mjs'), 'invalid syntax !');
  }
  let result = lint(root);
  assert.equal(result.status, 0, result.stderr || result.stdout);
  assert.equal(fs.readFileSync(source, 'utf8'), valid);
  for (const [text, rule] of [
    ['export const broken = missingName;\n', 'no-undef'],
    ['const unused = 1;\n', 'no-unused-vars'],
    ['export const commonjs = require("node:fs");\n', 'no-undef'],
    ['/* eslint-disable no-undef */\nexport const broken = missingName;\n', 'no-undef'],
    ['export const = ;\n', null],
  ]) {
    fs.writeFileSync(source, text);
    result = lint(root);
    assert.equal(result.status, 1, result.stderr || result.stdout);
    const report = JSON.parse(result.stdout);
    const messages = report.find(entry => entry.filePath === source).messages;
    assert(messages.some(message => rule ? message.ruleId === rule : message.fatal), result.stdout);
    assert.equal(fs.readFileSync(source, 'utf8'), text);
    assert(!fs.existsSync(path.join(root, '.eslintcache')));
  }
});

test('lint fails when its local executable is missing, without acquiring tools', t => {
  const root = fixture(t, false);
  fs.writeFileSync(path.join(root, 'new.mjs'), 'export const value = 1;\n');
  const result = lint(root);
  assert.equal(result.status, 1, result.stderr || result.stdout);
  assert.match(result.stderr, /MODULE_NOT_FOUND/);
  assert(!fs.existsSync(path.join(root, 'node_modules')));
});
