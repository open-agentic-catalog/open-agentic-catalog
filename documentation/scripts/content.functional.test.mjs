// Qualify directives through the installed site's real Markdown and MDX build.
import test from 'node:test';
import assert from 'node:assert/strict';
import fs from 'node:fs';
import os from 'node:os';
import path from 'node:path';
import { spawnSync } from 'node:child_process';
import { createHash } from 'node:crypto';
import { parse } from 'parse5';
import { entries, project, root } from './site.mjs';

function elements(node, tag) {
  return [...(node.tagName === tag ? [node] : []), ...(node.childNodes ?? []).flatMap(child => elements(child, tag))];
}
function text(node) {
  return node.nodeName === '#text' ? node.value : (node.childNodes ?? []).map(text).join('');
}
function attribute(node, name) {
  return node.attrs?.find(a => a.name === name)?.value;
}

test('public edition and all aside variants survive the real bundled build', t => {
  const directory = fs.mkdtempSync(path.join(os.tmpdir(), 'catalog-docs-content-'));
  t.after(() => fs.rmSync(directory, { recursive: true, force: true }));
  // Own all writes and caches; other repository inputs are linked for reads.
  for (const name of fs.readdirSync(root).filter(name => name !== 'documentation')) {
    fs.symlinkSync(path.join(root, name), path.join(directory, name));
  }
  const fixture = path.join(directory, 'documentation');
  const authored = new Set(entries.filter(entry => !entry.projection).map(entry => path.join(root, entry.source)));
  fs.cpSync(project, fixture, {
    recursive: true,
    filter: source => {
      if (['node_modules', 'dist', '.astro', 'src/content/docs/generated', 'public/generated'].some(name =>
        source === path.join(project, name) || source.startsWith(path.join(project, name) + path.sep))) return false;
      // Other native tests can briefly create an unlisted source in parallel.
      return !source.startsWith(path.join(project, 'src/content/docs') + path.sep) || !/\.mdx?$/.test(source) || authored.has(source);
    },
  });
  // Astro CSS compilation requires dependency paths inside the fixture root.
  // Copy installed bytes, with copy-on-write where supported; acquire nothing.
  // Private package/cache paths also prevent writes to the shared installation.
  fs.cpSync(path.join(project, 'node_modules'), path.join(fixture, 'node_modules'), {
    recursive: true, verbatimSymlinks: true, mode: fs.constants.COPYFILE_FICLONE,
    filter: source => !['.vite', '.vite-temp', '.astro'].includes(path.basename(source)),
  });
  const cases = [
    { source: 'guides/concepts.md', route: 'guides/concepts', link: '../reference/api-conventions.md#request-rules' },
    { source: 'operations/backup-restore.mdx', route: 'operations/backup-restore', link: '../reference/api-conventions.md#request-rules' },
  ];
  const command = ["cat <<'CATALOG_FIXTURE'", '# Preserve this file content.', 'CATALOG_FIXTURE'].join('\n');
  for (const { source, link } of cases) {
    const directives = ['note', 'tip', 'caution', 'danger'].map(type =>
      `\n:::${type}[Fixture ${type}]\nA **bounded** \`fixture\` uses [request rules](${link}).\n:::\n`).join('');
    fs.appendFileSync(path.join(fixture, 'src/content/docs', source), directives + '\n```sh\n' + command + '\n```\n');
  }
  const base = '/content-fixture/';
  const result = spawnSync('npm', ['run', 'build'], {
    cwd: fixture, encoding: 'utf8', timeout: 180_000, maxBuffer: 4 << 20,
    env: { ...process.env, DOCS_BASE: base, DOCS_SITE:'https://example.test', DOCS_EDITION_COMMIT:'a'.repeat(40), ASTRO_TELEMETRY_DISABLED: '1', NODE_OPTIONS: '', npm_config_update_notifier: 'false' },
  });
  assert.equal(result.error, undefined, String(result.error));
  assert.equal(result.status, 0, result.stdout + result.stderr);
  const homepage = parse(fs.readFileSync(path.join(fixture, 'dist/index.html'), 'utf8'));
  const edition = elements(homepage, 'aside').find(node => attribute(node, 'aria-label') === 'Manual edition');
  assert(edition, 'Public edition survives bundled rendering');
  assert(text(edition).includes('a'.repeat(40)));
  assert(text(edition).includes(fs.readFileSync(path.join(root,'VERSION'),'utf8').trim()));
  assert.equal(attribute(elements(edition,'a')[0],'href'), 'https://example.test' + base + 'catalog-manual.pdf');
  for (const { route } of cases) {
    const document = parse(fs.readFileSync(path.join(fixture, 'dist', route, 'index.html'), 'utf8'));
    for (const type of ['note', 'tip', 'caution', 'danger']) {
      const asides = elements(document, 'aside').filter(node => attribute(node, 'aria-label') === `Fixture ${type}`);
      assert.equal(asides.length, 1, `${route}: labelled ${type} aside`);
      const aside = asides[0];
      assert(attribute(aside, 'class').split(' ').includes(`starlight-aside--${type}`));
      assert.equal(elements(aside, 'strong').map(text).join(''), 'bounded');
      assert.equal(elements(aside, 'code').map(text).join(''), 'fixture');
      assert.equal(elements(aside, 'a').map(node => attribute(node, 'href')).join(''), `${base}reference/api-conventions/#request-rules`);
      assert.equal(elements(aside, 'a').map(text).join(''), 'request rules');
      assert(text(aside).includes('uses'));
    }
    assert(!text(document).includes(':::note'), `${route}: no unprocessed directive`);
    const copies = elements(document, 'button').map(node => attribute(node, 'data-code')?.replaceAll('\u007f', '\n'));
    assert(copies.includes(command), `${route}: copied heredoc retains its file content`);
  }
  assert(!fs.readFileSync(path.join(project, 'src/content/docs/guides/concepts.md'), 'utf8').includes('Fixture note'));
  assert(!fs.readFileSync(path.join(project, 'src/content/docs/operations/backup-restore.mdx'), 'utf8').includes('Fixture note'));
});

test('hook scanner recipe stops on failures before selecting PATH', async t => {
  const source = fs.readFileSync(path.join(project, 'src/content/docs/contributing/testing.md'), 'utf8');
  const recipe = source.match(/```sh\n(# catalog-hook-scanner\n[\s\S]*?)\n```/);
  assert(recipe, 'The displayed scanner recipe must remain testable');
  const prefix = ['mkdir', 'mktemp', 'archive-download', 'checksum-verify', 'extract', 'staged-version', 'install', 'installed-version'];
  const cases = [
    { name: 'successful acquisition', calls: prefix, success: true },
    ...prefix.map((failure, index) => ({ name: failure, failure, calls: prefix.slice(0, index + 1) })),
    { name: 'failed directory change', failure: 'cd', calls: prefix.slice(0, 2) },
    { name: 'reject changed archive bytes', failure: 'tampered-archive', calls: prefix.slice(0, 4) },
    { name: 'reject another archive identity', failure: 'pin-identity', calls: prefix.slice(0, 4) },
    { name: 'partial extraction', failure: 'partial-extract', calls: prefix.slice(0, 5) },
    { name: 'wrong downloaded version', failure: 'staged-version-wrong', calls: prefix.slice(0, 6) },
    { name: 'reuse valid installation', existing: true, calls: ['installed-version'], success: true },
    { name: 'refuse wrong installed version', existing: true, failure: 'installed-version-wrong', calls: ['installed-version'] },
  ];
  for (const item of cases) await t.test(item.name, sub => {
    const directory = fs.mkdtempSync(path.join(os.tmpdir(), 'catalog-hook-recipe-'));
    sub.after(() => fs.rmSync(directory, { recursive: true, force: true }));
    const bin = path.join(directory, 'bin');
    fs.mkdirSync(bin);
    const pins = path.join(directory, 'test/ci');
    fs.mkdirSync(pins, { recursive: true });
    fs.writeFileSync(path.join(pins, 'pins.json'), JSON.stringify({
      archives: {
        gitleaks: {
          version: '8.30.1',
          filename: item.failure === 'pin-identity' ? 'another-platform.tar.gz' : 'gitleaks_8.30.1_linux_x64.tar.gz',
          sha256: createHash('sha256').update('fixture\n').digest('hex'),
        },
      },
    }));
    const log = path.join(directory, 'calls');
    const fake = path.join(directory, 'fake-scanner');
    fs.writeFileSync(fake, [
      '#!/bin/sh',
      'case "$0" in /*) location=$0 ;; *) location="$PWD/$0" ;; esac',
      'case "$location" in */.install.*/*) step=staged-version ;; *) step=installed-version ;; esac',
      'printf "%s\\n" "$step" >> "$HOOK_TEST_LOG"',
      '[ "$HOOK_FAILURE" != "$step" ] || exit 31',
      'if [ "$HOOK_FAILURE" = "$step-wrong" ]; then printf "%s\\n" 0.0.0; else printf "%s\\n" 8.30.1; fi',
      '',
    ].join('\n'), { mode: 0o755 });
    const stub = [
      '#!/bin/sh',
      'name=$(basename "$0")',
      'step=$name',
      'case "$name" in',
      ' curl) while [ "$1" != --output ]; do shift; done; output=$2; step=archive-download ;;',
      ' python3) step=checksum-verify ;; tar) step=extract ;; mv) step=install ;;',
      'esac',
      'printf "%s\\n" "$step" >> "$HOOK_TEST_LOG"',
      '[ "$HOOK_FAILURE" != "$step" ] || exit 31',
      'case "$name" in',
      ' mkdir|mv) PATH="$HOOK_REAL_PATH" "$name" "$@" ;;',
      ' mktemp) stage=$(PATH="$HOOK_REAL_PATH" mktemp "$@") || exit 31;',
      '         if [ "$HOOK_FAILURE" = cd ]; then rm -rf -- "$stage"; fi; printf "%s\\n" "$stage" ;;',
      ' curl) if [ "$HOOK_FAILURE" = tampered-archive ]; then printf "%s\\n" changed; else printf "%s\\n" fixture; fi > "$output" ;;',
      ' python3) PATH="$HOOK_REAL_PATH" python3 "$@" ;;',
      ' tar) [ "$*" = "-xzf gitleaks_8.30.1_linux_x64.tar.gz gitleaks" ] || exit 31;',
      '      cp "$HOOK_FAKE_BINARY" ./gitleaks && chmod 755 ./gitleaks || exit 31;',
      '      [ "$HOOK_FAILURE" != partial-extract ] ;;',
      'esac',
      '',
    ].join('\n');
    for (const name of ['mkdir', 'mktemp', 'curl', 'python3', 'tar', 'mv']) {
      fs.writeFileSync(path.join(bin, name), stub, { mode: 0o755 });
    }
    const install = path.join(directory, '.tools/gitleaks/8.30.1');
    if (item.existing) {
      fs.mkdirSync(install, { recursive: true });
      fs.copyFileSync(fake, path.join(install, 'gitleaks'));
    }
    const initialPath = bin + path.delimiter + process.env.PATH;
    const script = recipe[1] + '\nrecipe_status=$?\nprintf "%s\\n" "$PATH"\nexit "$recipe_status"\n';
    // A real shell runs the displayed recipe. Downloads/extraction are local doubles; Python checks actual fixture bytes.
    const result = spawnSync('/bin/sh', ['-c', script], {
      cwd: directory, encoding: 'utf8', timeout: 5000, maxBuffer: 1 << 20,
      env: { ...process.env, PATH: initialPath, HOOK_REAL_PATH: process.env.PATH,
        HOOK_FAILURE: item.failure ?? '', HOOK_TEST_LOG: log, HOOK_FAKE_BINARY: fake },
    });
    assert.equal(result.error, undefined, String(result.error));
    const calls = fs.readFileSync(log, 'utf8').trim().split('\n');
    assert.equal(result.status === 0, Boolean(item.success), result.stderr + '\nCommands: ' + calls.join(', '));
    assert.equal(result.stdout.trim(), item.success ? install + path.delimiter + initialPath : initialPath);
    assert.deepEqual(calls, item.calls);
    if (fs.existsSync(install)) {
      assert.deepEqual(fs.readdirSync(install), item.success || item.existing || item.failure === 'installed-version' ? ['gitleaks'] : []);
      if (fs.existsSync(path.join(install, 'gitleaks'))) {
        assert.deepEqual(fs.readFileSync(path.join(install, 'gitleaks')), fs.readFileSync(fake));
      }
    }
  });
});
