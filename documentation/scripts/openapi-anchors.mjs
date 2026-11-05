// Repair the pinned OpenAPI renderer's repeated response-header anchors in output.
import fs from 'node:fs';
import path from 'node:path';
import { fileURLToPath } from 'node:url';
import { parse, serialize } from 'parse5';
import { walk } from './site.mjs';

// starlight-openapi 0.26.2 emits the same "headers" heading ID for every
// response. Keep the first deep link and disambiguate subsequent headings and
// their own permalink. This narrow output fix runs before Pagefind indexing.
export function repairHeaderAnchors(input) {
  const tree = parse(input);
  let count = 0;
  // visit renames repeated headings in document order, retaining the first deep link.
  function visit(node) {
    const id = node.attrs?.find(a=>a.name === 'id');
    if (id?.value === 'headers' && /^h[1-6]$/.test(node.tagName)) {
      count++;
      if (count > 1) {
        id.value = `headers-${count}`;
        const update = child => {
          for (const attr of child.attrs??[]) if (attr.name === 'href' && attr.value === '#headers') attr.value = '#' + id.value;
          child.childNodes?.forEach(update);
        };
        update(node.parentNode);
      }
    }
    node.childNodes?.forEach(visit);
  }
  visit(tree);
  return count > 1 ? serialize(tree) : input;
}

// openAPIAnchors installs the repair at Astro's generated-output hook so search
// indexes the corrected HTML. Only generated API pages are eligible for rewriting.
export function openAPIAnchors() {
  return {name:'catalog-openapi-response-anchors',hooks:{'astro:build:generated':({dir})=>{
    const api = path.join(fileURLToPath(dir),'api');
    for (const file of walk(api).filter(f=>f.endsWith('.html'))) {
      const original = fs.readFileSync(file,'utf8');
      const fixed = repairHeaderAnchors(original);
      if (fixed !== original) fs.writeFileSync(file,fixed);
    }
  }}};
}
