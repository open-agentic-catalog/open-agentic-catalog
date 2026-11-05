// Prepare allowlisted reader projections and downloads; replace only build-owned
// output and the Astro cache, never authored pages or their source assets.
import fs from 'node:fs';
import path from 'node:path';
import { parse } from 'yaml';
import { entries, root, project, contentRoot, pagePath, projectManualLinks, projectDiagrams, downloads, walk, validateEntries, assertLocalRefs, omittedSections } from './site.mjs';
import { notices } from './notices.mjs';

validateEntries(entries);
// Astro's content cache does not include arbitrary source-map/plugin inputs.
// Recreate only its owned cache so link-policy changes cannot reuse stale HTML.
fs.rmSync(path.join(project,'.astro'), {recursive:true,force:true});
// Delete only these two build-owned projections; never the authored tree.
for (const name of ['src/content/docs/generated', 'public/generated']) {
  fs.rmSync(path.join(project,name), {recursive:true,force:true});
  fs.mkdirSync(path.join(project,name), {recursive:true});
}
for (const file of walk(path.join(project,'public'))) {
  if (!file.startsWith(path.join(project,'public/generated/'))) throw new Error(`Unlisted public asset: ${file}`);
}
for (const entry of entries) {
  let text = fs.readFileSync(path.join(root,entry.source),'utf8');
  if (!entry.projection) continue;
  if (entry.omitSections) {
    const sections = text.split(/(?=^## )/m);
    const omitted = new Set(omittedSections(entry, text));
    text = sections.filter(section => !omitted.has(section)).join('');
  }
  const title = text.match(/^# (.+)\n/m)?.[1];
  if (!title) throw new Error(`Missing source title: ${entry.source}`);
  text = text.replace(/^# .+\n/m, '').replace(/^- Related backlog:.*\n/m,'');
  text = projectManualLinks(entry, text);
  text = projectDiagrams(entry, text);
  const destination = path.join(root,pagePath(entry));
  fs.mkdirSync(path.dirname(destination),{recursive:true});
  fs.writeFileSync(destination,`---\ntitle: ${JSON.stringify(title)}\nslug: ${entry.route}\n---\n${text}`);
}
const authored = walk(path.join(root,contentRoot)).filter(f => /\.mdx?$/.test(f) && !f.includes('/generated/')).map(f => path.relative(root,f));
for (const source of authored) if (!entries.some(e => e.source === source)) throw new Error(`Unlisted authored page: ${source}`);
const contract = parse(fs.readFileSync(path.join(root,'api/openapi/control-v1.yaml'),'utf8'));
assertLocalRefs(contract);
for (const source of downloads) {
  const destination = path.join(project,'public/generated',source);
  fs.mkdirSync(path.dirname(destination),{recursive:true});
  fs.copyFileSync(path.join(root,source),destination);
}
fs.writeFileSync(path.join(project,'public/generated/content-inventory.json'),JSON.stringify(entries.map(({source,route}) => ({source,route})),null,2)+'\n');
console.log(`Prepared ${entries.length} documentation pages and ${downloads.length} exact downloads and reader assets.`);
notices();
fs.copyFileSync(path.join(project,'assets/favicon.svg'),path.join(project,'public/generated/favicon.svg'));
