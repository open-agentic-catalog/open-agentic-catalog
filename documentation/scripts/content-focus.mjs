// Make manual tables, code blocks and API paths available to keyboard scrolling.
import fs from 'node:fs';
import path from 'node:path';
import { fileURLToPath } from 'node:url';
import { parse } from 'parse5';
import { walk } from './site.mjs';

// Insert only missing tabindex attributes. Preserve all other output bytes,
// including code, copy-button values, inline scripts and existing focus choices.
export function focusScrollableContent(input) {
  const tree = parse(input, { sourceCodeLocationInfo: true });
  const insertions = [];
  function visit(node, inContent = false) {
    const classes = node.attrs?.find(a => a.name === 'class')?.value.split(/\s+/) ?? [];
    inContent ||= classes.includes('sl-markdown-content');
    const scroller = ['table', 'pre'].includes(node.tagName) || classes.includes('sl-openapi-operation-method-path');
    if (inContent && scroller && !node.attrs?.some(a => a.name === 'tabindex')) {
      const offset = node.sourceCodeLocation?.startTag?.endOffset;
      if (!offset || input[offset - 1] !== '>') throw new Error('Manual scroller has no source tag');
      insertions.push(offset - 1);
    }
    node.childNodes?.forEach(child => visit(child, inContent));
  }
  visit(tree);
  for (const offset of insertions.sort((a, b) => b - a)) {
    input = input.slice(0, offset) + ' tabindex="0"' + input.slice(offset);
  }
  return input;
}

// Run after rendering, so Expressive Code and generated API tables are included.
export function contentFocus() {
  return { name: 'catalog-content-keyboard-scroll', hooks: { 'astro:build:generated': ({ dir }) => {
    for (const file of walk(fileURLToPath(dir)).filter(file => path.extname(file) === '.html')) {
      const original = fs.readFileSync(file, 'utf8');
      const fixed = focusScrollableContent(original);
      if (fixed !== original) fs.writeFileSync(file, fixed);
    }
  } } };
}
