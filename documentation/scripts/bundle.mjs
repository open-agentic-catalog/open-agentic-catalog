// Final rendered evidence for the static manual. No source files or npm tree ship.
import fs from 'node:fs';
import path from 'node:path';
import { createHash } from 'node:crypto';
import { pathToFileURL } from 'node:url';
import { parse } from 'parse5';

export const manifestName = 'documentation-bundle.json';
const digest = bytes => createHash('sha256').update(bytes).digest('hex');
const hash = text => 'sha256-' + createHash('sha256').update(text).digest('base64');

// Hash exact raw script/style text, not parsed/re-serialized HTML. Browser CSP
// normalizes CR/CRLF to LF before hashing. Reject policy and handler collisions.
export function inlineHashes(html) {
  const scripts = new Set(), styles = new Set();
  const visit = node => {
    const attrs = Object.fromEntries((node.attrs ?? []).map(a => [a.name, a.value]));
    if (Object.keys(attrs).some(name => /^on/i.test(name))) throw new Error('Inline event handler in manual');
    if (node.tagName === 'meta' && attrs['http-equiv']?.toLowerCase() === 'content-security-policy') throw new Error('Conflicting manual CSP');
    if (node.tagName === 'script' && !attrs.src || node.tagName === 'style') {
      const text = (node.childNodes ?? []).map(n => n.value ?? '').join('');
      (node.tagName === 'script' ? scripts : styles).add(hash(text));
    }
    node.childNodes?.forEach(visit);
    if (node.content) visit(node.content);
  };
  visit(parse(html));
  return {scriptHashes: [...scripts].sort(), styleHashes: [...styles].sort()};
}

export function bundle(directory, base, lockBytes) {
  if (!/^\/(?:[A-Za-z0-9_-]+\/)*$/.test(base)) throw new Error('Invalid manual base');
  const files = [], scripts = new Set(), styles = new Set();
  let total = 0;
  const visit = prefix => {
    for (const entry of fs.readdirSync(path.join(directory, prefix), {withFileTypes: true})) {
      const name = prefix + entry.name;
      if (name === manifestName) {
        if (!entry.isFile()) throw new Error('Non-regular manual manifest');
        continue;
      }
      if (entry.name.startsWith('.') || name.length > 512 || !/^[A-Za-z0-9_./-]+$/.test(name)) throw new Error('Unsafe manual path');
      if (entry.isDirectory()) visit(name + '/');
      else if (entry.isFile()) {
        if (fs.statSync(path.join(directory, name)).size > 32 * 1024 * 1024) throw new Error('Manual file exceeds bounds');
        const bytes = fs.readFileSync(path.join(directory, name));
        total += bytes.length;
        if (bytes.length > 32 * 1024 * 1024 || total > 256 * 1024 * 1024 || files.length >= 10000) throw new Error('Manual inventory exceeds bounds');
        files.push({path: name, size: bytes.length, sha256: digest(bytes)});
        if (name.endsWith('.html')) {
          const hashes = inlineHashes(bytes.toString('utf8'));
          hashes.scriptHashes.forEach(h => scripts.add(h));
          hashes.styleHashes.forEach(h => styles.add(h));
        }
      } else throw new Error('Non-regular manual file');
    }
  };
  visit('');
  if (!files.some(f => f.path === 'index.html') || scripts.size > 64 || styles.size > 64) throw new Error('Incomplete or excessive manual evidence');
  return {schemaVersion: 1, base, lockSha256: digest(lockBytes), files: files.sort((a,b) => a.path.localeCompare(b.path, 'en')), scriptHashes: [...scripts].sort(), styleHashes: [...styles].sort()};
}

if (process.argv[1] && import.meta.url === pathToFileURL(path.resolve(process.argv[1])).href) {
  const {project, base} = await import('./site.mjs');
  const directory = path.join(project, 'dist');
  const value = bundle(directory, base, fs.readFileSync(path.join(project, 'package-lock.json')));
  const bytes = JSON.stringify(value) + '\n';
  if (Buffer.byteLength(bytes) > 2 * 1024 * 1024) throw new Error('Manual manifest exceeds bounds');
  fs.writeFileSync(path.join(directory, manifestName), bytes);
  console.log(`Sealed ${value.files.length} manual files at ${base}.`);
}
