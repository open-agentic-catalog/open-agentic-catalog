import assert from 'node:assert/strict';
import { test } from 'node:test';
import fs from 'node:fs';
import os from 'node:os';
import path from 'node:path';
import { createHash } from 'node:crypto';
import { inlineHashes, bundle, manifestName } from './bundle.mjs';

test('final raw inline text is hashed, including inline Starlight scripts', () => {
  const source = 'const theme = "dark";\r\napply(theme);';
  const expected = 'sha256-' + createHash('sha256').update(source.replaceAll('\r\n','\n')).digest('base64');
  const result = inlineHashes(`<script>${source}</script><script>${source}</script><script src="/docs/app.js"></script><style>body { color: red }</style>`);
  assert.deepEqual(result.scriptHashes, [expected]);
  assert.equal(result.styleHashes.length, 1);
  assert.throws(() => inlineHashes('<button onclick="secret()">'), /event handler/);
  assert.throws(() => inlineHashes('<meta http-equiv="Content-Security-Policy" content="default-src none">'), /Conflicting/);
});

test('inventory hashes actual bytes and rejects non-static inputs', t => {
  const root = fs.mkdtempSync(path.join(os.tmpdir(), 'catalog-doc-bundle-'));
  t.after(() => fs.rmSync(root, {recursive:true,force:true}));
  fs.writeFileSync(path.join(root,'index.html'),'<script>theme()</script>');
  fs.writeFileSync(path.join(root,manifestName),'previous output');
  const result = bundle(root,'/docs/',Buffer.from('lock'));
  assert.equal(result.base,'/docs/');
  assert.equal(result.files.length,1);
  assert.equal(result.files[0].sha256,createHash('sha256').update('<script>theme()</script>').digest('hex'));
  assert.equal(result.files[0].size,24);
  fs.writeFileSync(path.join(root,'.private'),'secret');
  assert.throws(() => bundle(root,'/docs/',Buffer.from('lock')),/Unsafe/);
  fs.unlinkSync(path.join(root,'.private'));
  fs.symlinkSync('index.html',path.join(root,'alias.html'));
  assert.throws(() => bundle(root,'/docs/',Buffer.from('lock')),/Non-regular/);
  assert.throws(() => bundle(root,'/../',Buffer.from('lock')),/Invalid manual base/);
  fs.unlinkSync(path.join(root,'alias.html'));
  fs.unlinkSync(path.join(root,manifestName));
  fs.symlinkSync('index.html',path.join(root,manifestName));
  assert.throws(() => bundle(root,'/docs/',Buffer.from('lock')),/Non-regular manual manifest/);
  fs.unlinkSync(path.join(root,manifestName));
  fs.writeFileSync(path.join(root,'index.html'),Array.from({length:65},(_,i)=>`<script>const hash${i} = true;</script>`).join(''));
  assert.throws(() => bundle(root,'/docs/',Buffer.from('lock')),/excessive/);
});
