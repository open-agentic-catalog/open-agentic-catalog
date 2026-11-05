// Inventory locked documentation dependencies and retain installed license texts.
import fs from 'node:fs';
import path from 'node:path';
import { createHash } from 'node:crypto';
import { project } from './site.mjs';

// Tool-only copyleft binaries are acquired by npm for local builds, not copied
// into the site or Catalog image. Keep their exact metadata visible separately
// from browser-distributed notices; never call the complete npm tree permissive.
const reviewedLicenses = new Set([
  'Apache-2.0','MIT','ISC','BSD-2-Clause','BSD-3-Clause','0BSD','CC0-1.0',
  'BlueOak-1.0.0','Python-2.0','MIT,Apache2','MPL-2.0','LGPL-3.0-or-later',
  'Apache-2.0 AND LGPL-3.0-or-later','Apache-2.0 AND LGPL-3.0-or-later AND MIT',
]);
// These packages supply browser JS/CSS, prebuilt search/WASM, or rendered
// components. The full lock inventory below remains separate build evidence.
// This is a conservative supplier inventory, not an exact tree-shaken closure.
export const browserSuppliers = [
  '@astrojs/starlight', 'astro', 'starlight-openapi', 'pagefind',
  '@pagefind/default-ui', '@expressive-code/core', '@expressive-code/plugin-frames',
  '@expressive-code/plugin-shiki', '@expressive-code/plugin-text-markers',
];
const noticeName = /^(licen[cs]e|copying|notice)([.-]|$)/i;
// The default UI npm archive omits its repository license. Retain the exact
// text from Pagefind v1.5.2, commit bf17396721be637cc67c8ed7ead1dc7b8ac43d96.
const upstreamNotices = {
  '@pagefind/default-ui': {version:'1.5.2', file:'scripts/licenses/pagefind-default-ui-1.5.2.txt'},
};
const requiredNoticePaths = {
  pagefind:['LICENSE/LICENSE','LICENSE/LICENSE-vscode-ripgrep'],
};

function licenseTexts(directory, prefix = '') {
  const files = [];
  for (const entry of fs.readdirSync(directory, {withFileTypes:true}).sort((a,b)=>a.name.localeCompare(b.name,'en'))) {
    if (!prefix && !noticeName.test(entry.name)) continue;
    const relative = prefix + entry.name;
    if (entry.isDirectory()) files.push(...licenseTexts(path.join(directory,entry.name), relative + '/'));
    else if (entry.isFile()) {
      const text = fs.readFileSync(path.join(directory,entry.name),'utf8');
      if (!text.trim()) throw new Error(`Empty license text: ${relative}`);
      files.push({path:relative, text});
    } else throw new Error(`Unsafe license file: ${relative}`);
  }
  return files;
}
// notices writes build-owned inventories after checking the reviewed license set.
// Optional packages absent on this platform still appear in the lock inventory;
// their license files are collected only when the package is installed.
export function notices(directory = project) {
  const lockBytes = fs.readFileSync(path.join(directory,'package-lock.json'));
  const lock = JSON.parse(lockBytes);
  const inventory = [];
  const collected = new Map();
  const texts = ['Open Agentic Catalog documentation - third-party build and asset notices',
    'The npm toolchain is not distributed with the static site. This inclusive notice preserves available package license texts, including build-only packages. Project-owned content uses Apache License 2.0.'];
  for (const [location, entry] of Object.entries(lock.packages)) {
    if (!location) continue;
    const license = Array.isArray(entry.license) ? entry.license.join(',') : entry.license;
    if (!reviewedLicenses.has(license)) throw new Error(`Unreviewed dependency license: ${location}: ${license}`);
    const name = location.split('node_modules/').at(-1);
    if (/LGPL/.test(entry.license) && !name.startsWith('@img/sharp-')) throw new Error(`Unexpected copyleft tool: ${name}`);
    if (entry.license === 'MPL-2.0' && !name.startsWith('lightningcss')) throw new Error(`Unexpected copyleft tool: ${name}`);
    inventory.push({name,version:entry.version,license:entry.license,optional:entry.optional??false});
    const installed = path.join(directory,location);
    if (!fs.existsSync(installed)) continue;
    const licenses = licenseTexts(installed);
    const upstream = upstreamNotices[name];
    if (upstream) {
      if (entry.version !== upstream.version || entry.license !== 'MIT') throw new Error(`Review upstream notice for ${name}@${entry.version}`);
      const text = fs.readFileSync(path.join(directory,upstream.file),'utf8');
      if (!text.trim()) throw new Error(`Empty upstream notice: ${name}`);
      licenses.push({path:upstream.file, text});
    }
    for (const required of requiredNoticePaths[name] ?? []) {
      if (!licenses.some(file=>file.path===required)) throw new Error(`Missing browser supplier notice: ${name} / ${required}`);
    }
    collected.set(location, licenses);
    texts.push(`\n${name}@${entry.version} (${entry.license})`);
    for (const license of licenses) texts.push(`Notice: ${name}@${entry.version} / ${license.path}\n${license.text}`);
  }
  const navigation = fs.readFileSync(path.join(directory,'src/assets/navigation/LICENSE'),'utf8');
  if (!navigation.trim()) throw new Error('Missing navigation icon license');
  texts.push('Navigation icons\n' + navigation);
  const packages = browserSuppliers.map(name => {
    const entry = lock.packages['node_modules/' + name];
    if (!entry || !reviewedLicenses.has(entry.license) || /MPL|LGPL/.test(entry.license)) throw new Error(`Invalid browser supplier: ${name}`);
    const licenses = collected.get('node_modules/' + name);
    if (!licenses?.length) throw new Error(`Missing browser supplier notice: ${name}`);
    return {name, version: entry.version, license: entry.license, notices:licenses.map(file=>file.path)};
  });
  fs.writeFileSync(path.join(directory,'public/generated/third-party-notices.txt'),texts.join('\n\n')+'\n');
  fs.writeFileSync(path.join(directory,'public/generated/dependency-inventory.json'),JSON.stringify(inventory,null,2)+'\n');
  fs.writeFileSync(path.join(directory,'public/generated/browser-inventory.json'),JSON.stringify({schemaVersion:1,
    scope:'reviewed-browser-asset-suppliers', lockSha256:createHash('sha256').update(lockBytes).digest('hex'), packages},null,2)+'\n');
}
