// Astro can offer installation or return without checking when these are absent.
import { createRequire } from 'node:module';
import { fileURLToPath, pathToFileURL } from 'node:url';
import path from 'node:path';
import fs from 'node:fs';
import { spawnSync } from 'node:child_process';

const project = fileURLToPath(new URL('../', import.meta.url));
const require = createRequire(import.meta.url);
try {
  const cli = path.join(project, 'node_modules/astro/bin/astro.mjs');
  fs.accessSync(cli);
  for (const name of ['@astrojs/check', 'typescript']) {
    // Resolve only the owning installation, never a parent or global package.
    fs.accessSync(path.join(project, 'node_modules', name, 'package.json'));
    await import(pathToFileURL(require.resolve(name)).href);
  }
  const result = spawnSync(process.execPath, [
    cli, 'check',
    '--tsconfig', 'tsconfig.json', '--minimumFailingSeverity', 'warning',
  ], {
    cwd: project, stdio: 'inherit', timeout: 120_000,
    env: { ...process.env, ASTRO_TELEMETRY_DISABLED: '1', CI: 'true' },
  });
  if (result.error) throw result.error;
  process.exitCode = result.status ?? 1;
} catch (error) {
  console.error('Astro diagnostics failed; run make docs-install for the locked local tools.');
  console.error(error.message);
  process.exitCode = 1;
}
