import test from 'node:test';
import assert from 'node:assert/strict';
import fs from 'node:fs';
import os from 'node:os';
import path from 'node:path';
import { fileURLToPath } from 'node:url';
import { spawnSync } from 'node:child_process';

const project = fileURLToPath(new URL('../', import.meta.url));

function fixture(t, installed = true) {
  const root = fs.mkdtempSync(path.join(os.tmpdir(), 'catalog-astro-check-'));
  t.after(() => fs.rmSync(root, { recursive: true, force: true }));
  for (const name of ['package.json', 'tsconfig.json', 'scripts/astro-check.mjs']) {
    const target = path.join(root, name);
    fs.mkdirSync(path.dirname(target), { recursive: true });
    fs.copyFileSync(path.join(project, name), target);
  }
  // Minimal Astro integration; the real site's configuration is checked separately.
  fs.writeFileSync(path.join(root, 'astro.config.mjs'), 'export default { cacheDir: "./.astro/cache" };\n');
  fs.mkdirSync(path.join(root, 'src/components'), { recursive: true });
  if (installed) {
    // Astro and Vite may write caches below node_modules. Own that directory,
    // linking only packages so no cache or cleanup reaches the shared install.
    fs.mkdirSync(path.join(root, 'node_modules'));
    for (const name of fs.readdirSync(path.join(project, 'node_modules')).filter(name => !name.startsWith('.'))) {
      fs.symlinkSync(path.join(project, 'node_modules', name), path.join(root, 'node_modules', name), 'dir');
    }
  }
  return root;
}

function check(root) {
  const result = spawnSync('npm', ['--silent', 'run', 'check:astro'], {
    // Let the launcher's 120-second child deadline expire before killing npm.
    cwd: root, encoding: 'utf8', timeout: 150_000, maxBuffer: 2 << 20,
    env: { ...process.env, NODE_OPTIONS: '', NO_COLOR: '1', npm_config_update_notifier: 'false' },
  });
  assert.ifError(result.error);
  return { status: result.status, output: result.stdout + result.stderr };
}

test('Astro diagnostics sync a clean cache, reject source errors and preserve authored files', t => {
  const root = fixture(t);
  const component = path.join(root, 'src/components/Fixture.astro');
  const content = path.join(root, 'src/content.config.ts');
  const valid = '---\nconst label: string = "valid";\n---\n<p>{label}</p>\n';
  const collections = 'export const collections = {};\n';
  fs.writeFileSync(component, valid);
  fs.writeFileSync(content, collections);
  for (const directory of ['dist', 'src/content/docs/generated']) {
    const target = path.join(root, directory);
    fs.mkdirSync(target, { recursive: true });
    fs.writeFileSync(path.join(target, 'Ignored.astro'), '---\nconst broken: string = 123;\n---\n{broken}\n');
    fs.writeFileSync(path.join(target, 'ignored.ts'), 'invalid syntax !');
  }
  assert(!fs.existsSync(path.join(root, '.astro')));
  let result = check(root);
  assert.equal(result.status, 0, result.output);
  assert.match(result.output, /0 errors/);
  assert(fs.existsSync(path.join(root, '.astro/types.d.ts')));
  assert.equal(fs.readFileSync(component, 'utf8'), valid);
  for (const [source, invalid, original] of [
    [component, valid.replace('"valid"', '123'), valid],
    [content, collections + 'export const invalid: string = 123;\n', collections],
  ]) {
    fs.writeFileSync(source, invalid);
    result = check(root);
    assert.equal(result.status, 1, result.output);
    assert.match(result.output, /2322/);
    assert(result.output.includes(path.basename(source)), result.output);
    assert.equal(fs.readFileSync(source, 'utf8'), invalid);
    fs.writeFileSync(source, original);
  }
  result = check(root);
  assert.equal(result.status, 0, result.output);
  assert.equal(fs.readFileSync(content, 'utf8'), collections);
});

test('Astro diagnostics require every local tool without prompting or installing', t => {
  for (const missing of ['astro', '@astrojs/check', 'typescript']) {
    const root = fixture(t, false);
    for (const name of ['astro', '@astrojs/check', 'typescript'].filter(name => name !== missing)) {
      const target = path.join(root, 'node_modules', name);
      fs.mkdirSync(path.dirname(target), { recursive: true });
      fs.symlinkSync(path.join(project, 'node_modules', name), target, 'dir');
    }
    const result = check(root);
    assert.equal(result.status, 1, result.output);
    assert.match(result.output, /run make docs-install/);
    assert(!fs.existsSync(path.join(root, 'node_modules', missing)));
    assert(!fs.existsSync(path.join(root, '.astro')));
    assert(!fs.existsSync(path.join(root, 'package-lock.json')));
    assert.doesNotMatch(result.output, /Continue\?|Astro will run the following command/);
  }
});

test('Astro diagnostics fail on missing configuration instead of widening source discovery', t => {
  const root = fixture(t);
  fs.unlinkSync(path.join(root, 'tsconfig.json'));
  const result = check(root);
  assert.notEqual(result.status, 0, result.output);
  assert.match(result.output, /tsconfig\.json/);
});
