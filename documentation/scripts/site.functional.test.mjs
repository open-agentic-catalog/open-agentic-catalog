import test from 'node:test';
import assert from 'node:assert/strict';
import fs from 'node:fs';
import path from 'node:path';
import { execFileSync } from 'node:child_process';
import { entries, project, pagePath, root } from './site.mjs';

test('preparation removes stale projections and caches without changing authored pages',()=>{
  const sources = ['index.mdx','architecture/overview.mdx','architecture/design.mdx','architecture/decisions/0018-optional-catalog-browser-client.md'].map(f=>path.join(project,'src/content/docs',f));
  const before = sources.map(f=>fs.readFileSync(f));
  const markers = ['src/content/docs/generated/stale-test.md','src/content/docs/generated/reference/implementation-status.md','src/content/docs/generated/architecture/design.md','src/content/docs/generated/architecture/design.mdx','public/generated/stale-test.txt','.astro/cache/stale-test.txt'];
  for (const name of markers) {
    fs.mkdirSync(path.dirname(path.join(project,name)),{recursive:true});
    fs.writeFileSync(path.join(project,name),'invocation-owned stale test marker');
  }
  execFileSync(process.execPath,['scripts/prepare.mjs'],{cwd:project});
  for (const name of markers) assert(!fs.existsSync(path.join(project,name)));
  sources.forEach((source,index)=>assert.deepEqual(fs.readFileSync(source),before[index]));
  for (const entry of entries.filter(e=>e.projection)) {
    const text = fs.readFileSync(path.join(root,pagePath(entry)),'utf8');
    assert(!text.includes('Edit that source, not the generated page.'),entry.source);
    assert(!text.includes('Source: `'),entry.source);
    assert(!text.includes('implementation-status.md'),entry.source);
  }
});

test('unlisted authored MDX fails preparation without deleting the source',()=>{
  const source = path.join(project,'src/content/docs/migration-unlisted-test.mdx');
  fs.writeFileSync(source,'---\ntitle: Unlisted fixture\n---\n',{flag:'wx'});
  try {
    assert.throws(()=>execFileSync(process.execPath,['scripts/prepare.mjs'],{cwd:project,stdio:'pipe'}),/Unlisted authored page/);
    assert(fs.existsSync(source));
  } finally { fs.unlinkSync(source); }
});
