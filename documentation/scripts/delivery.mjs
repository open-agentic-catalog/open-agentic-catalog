// Stage a sealed site and its matching PDF without changing the sealed input.
import fs from 'node:fs';
import path from 'node:path';
import {createHash, randomUUID} from 'node:crypto';
import {pathToFileURL} from 'node:url';
import {readBuild, exportManual} from './pdf-export.mjs';
import {edition} from './edition.mjs';
import {project, root, base} from './site.mjs';

const sha = bytes => createHash('sha256').update(bytes).digest('hex');
export function deliveryInventory(directory) {
  const files = [];
  let total = 0;
  function visit(prefix) {
    for (const entry of fs.readdirSync(path.join(directory, prefix), {withFileTypes:true})) {
      const name = prefix + entry.name;
      if (entry.name.startsWith('.') || !/^[A-Za-z0-9_./-]+$/.test(name) || name.length > 512) throw new Error('Unsafe delivery path');
      if (entry.isDirectory()) visit(name + '/');
      else if (entry.isFile()) {
        if (name === 'documentation-delivery.json') continue;
        const size = fs.statSync(path.join(directory, name)).size;
        const limit = name === 'catalog-manual.pdf' ? 128 * 1024 * 1024 : 32 * 1024 * 1024;
        if (size > limit || (total += size) > 384 * 1024 * 1024 || files.length >= 10005) throw new Error('Delivery exceeds bounds');
        files.push({path:name, size, sha256:sha(fs.readFileSync(path.join(directory,name)))});
      } else throw new Error('Non-regular delivery file');
    }
  }
  visit('');
  return files.sort((a,b) => a.path.localeCompare(b.path, 'en'));
}

export function stageDelivery({input, pdf, output, value, lockBytes, exportResult, expectedBase}) {
  if (!value) throw new Error('Public edition is required');
  const build = readBuild(input, lockBytes);
  const homepage = fs.readFileSync(path.join(input, 'index.html'), 'utf8');
  if (build.manifest.base !== expectedBase || !homepage.includes(value.site + expectedBase + 'catalog-manual.pdf') || !homepage.includes(value.commit) || !homepage.includes('Version ' + value.version + '.')) throw new Error('Build does not match the public edition');
  if (exportResult?.status !== 'complete' || exportResult.cleanup !== 'passed' || exportResult.manifestSha256 !== build.manifestSha256 || exportResult.base !== expectedBase || exportResult.applicationVersion !== value.version) throw new Error('PDF export does not match the completed manual');
  const stat = fs.lstatSync(pdf);
  if (!stat.isFile() || stat.size < 100 || stat.size > 128 * 1024 * 1024 || stat.size !== exportResult.pdfBytes) throw new Error('Missing, invalid or changed complete PDF');
  const pdfBytes = fs.readFileSync(pdf);
  if (pdfBytes.subarray(0,5).toString() !== '%PDF-' || sha(pdfBytes) !== exportResult.pdfSha256) throw new Error('Missing, invalid or changed complete PDF');
  if (fs.existsSync(output)) throw new Error('Delivery output already exists; select a new directory');
  const temporary = output + '.stage-' + randomUUID();
  fs.mkdirSync(path.dirname(output), {recursive:true});
  try {
    fs.mkdirSync(temporary);
    for (const file of [...build.manifest.files, {path:'documentation-bundle.json'}]) {
      const destination = path.join(temporary, file.path);
      fs.mkdirSync(path.dirname(destination), {recursive:true});
      fs.copyFileSync(path.join(input, file.path), destination);
    }
    fs.copyFileSync(pdf, path.join(temporary, 'catalog-manual.pdf'));
    const inventory = deliveryInventory(temporary);
    for (const file of build.manifest.files) {
      const copied = inventory.find(entry => entry.path === file.path);
      if (copied?.sha256 !== file.sha256 || copied.size !== file.size) throw new Error('Manual changed during staging');
    }
    const copiedPDF = inventory.find(file => file.path === 'catalog-manual.pdf');
    if (copiedPDF?.sha256 !== exportResult.pdfSha256 || copiedPDF.size !== exportResult.pdfBytes) throw new Error('PDF changed during staging');
    if (inventory.find(file => file.path === 'documentation-bundle.json')?.sha256 !== build.manifestSha256) throw new Error('Manual manifest changed during staging');
    if (readBuild(input, lockBytes).manifestSha256 !== build.manifestSha256) throw new Error('Manual changed during staging');
    const result = {schemaVersion:1, ...value, base:build.manifest.base, bundleSha256:build.manifestSha256, files:inventory};
    fs.writeFileSync(path.join(temporary,'documentation-delivery.json'), JSON.stringify(result) + '\n');
    fs.renameSync(temporary, output);
    return result;
  } finally { fs.rmSync(temporary, {recursive:true,force:true}); }
}

if (process.argv[1] && import.meta.url === pathToFileURL(path.resolve(process.argv[1])).href) {
  const value = edition();
  if (!value || base === '/') throw new Error('Select the public edition and deployment base before preparation');
  const output = path.resolve(root, process.env.DOCS_DELIVERY_OUTPUT ?? 'output/documentation-delivery/current');
  // Output is always ignored, never a source, browser tree or an existing edition.
  const owned = path.join(root, 'output/documentation-delivery') + path.sep;
  if (!output.startsWith(owned) || output === owned.slice(0,-1) || fs.existsSync(output)) throw new Error('Select a new output/documentation-delivery directory');
  const pdf = output + '.pdf';
  try {
    const exportResult = await exportManual({output:pdf, onStage:stage => process.stderr.write('PDF: ' + stage + '\n')});
    const result = stageDelivery({input:path.join(project,'dist'),pdf,output,value,lockBytes:fs.readFileSync(path.join(project,'package-lock.json')),exportResult,expectedBase:base});
    console.log(`Prepared ${result.files.length} delivery files at ${result.base}, source ${result.commit}. No upload performed.`);
  } finally { fs.rmSync(pdf,{force:true}); fs.rmSync(pdf + '.json',{force:true}); }
}
