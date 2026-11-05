// Static manual inventory and print transformations. No browser or network imports.
import { parse, parseFragment, serialize, serializeOuter } from 'parse5';
import { slug } from 'github-slugger';

const origin = 'https://manual.invalid';
const attrs = node => Object.fromEntries((node.attrs ?? []).map(a => [a.name, a.value]));
export const text = node => node.value ?? (node.childNodes ?? []).map(text).join('');
export function elements(node, predicate) {
  const found = [];
  const visit = current => {
    if (predicate(current)) found.push(current);
    for (const child of current.childNodes ?? []) visit(child);
  };
  visit(node);
  return found;
}
const hasClass = (node, name) => (attrs(node).class ?? '').split(/\s+/).includes(name);
const escape = value => String(value).replaceAll('&', '&amp;').replaceAll('<', '&lt;').replaceAll('>', '&gt;').replaceAll('"', '&quot;');
function set(node, name, value) {
  node.attrs ??= [];
  node.attrs = node.attrs.filter(a => a.name !== name);
  if (value !== undefined) node.attrs.push({name, value: String(value)});
}
function remove(node) {
  const parent = node.parentNode;
  if (parent) parent.childNodes = parent.childNodes.filter(child => child !== node);
}

// Resolve paths relative to the original page, never the temporary print URL.
export function localTarget(value, owner, base) {
  if (!value || value.length > 4096) throw new Error('Invalid manual reference');
  const url = new URL(value, origin + base + owner);
  if (url.origin !== origin) {
    if (!['https:', 'http:', 'mailto:', 'tel:', 'data:'].includes(url.protocol)) throw new Error('Unsupported manual reference scheme');
    return null;
  }
  if (!url.pathname.startsWith(base) || url.search) throw new Error('Invalid local manual reference');
  const relative = url.pathname.slice(base.length);
  if (!/^[A-Za-z0-9_./-]*$/.test(relative)) throw new Error('Invalid local manual path');
  return {file: !relative || relative.endsWith('/') ? relative + 'index.html' : relative, fragment: decodeURIComponent(url.hash.slice(1))};
}

// Sidebar order includes links inside closed groups and generated API navigation.
export function pageInventory(home, {base, files, manualRoutes, operationIds, apiTags = []}) {
  if (new Set(manualRoutes).size !== manualRoutes.length || new Set(operationIds.map(id => id.toLowerCase())).size !== operationIds.length || new Set(apiTags).size !== apiTags.length) throw new Error('Duplicate required manual route');
  const required = new Set(manualRoutes.map(route => (route ? route + '/' : '') + 'index.html'));
  if (operationIds.length || apiTags.length) required.add('api/index.html');
  for (const id of operationIds) required.add('api/operations/' + id.toLowerCase() + '/index.html');
  for (const tag of apiTags) required.add('api/operations/tags/' + slug(tag) + '/index.html');
  const actual = [...files].filter(file => file.endsWith('.html') && file !== '404.html');
  if (actual.length !== required.size || actual.some(file => !required.has(file))) throw new Error('Unregistered or missing PDF page');
  const document = parse(home);
  const navigation = elements(document, node => node.tagName === 'nav' && hasClass(node, 'sidebar'));
  if (navigation.length !== 1) throw new Error('Expected one rendered manual sidebar');
  const pages = [], byFile = new Map();
  for (const link of elements(navigation[0], node => node.tagName === 'a')) {
    const href = attrs(link).href;
    if (!href) continue;
    const target = localTarget(href, '', base);
    if (!target || !target.file.endsWith('index.html')) throw new Error('Sidebar link is not a manual page');
    if (!files.has(target.file)) throw new Error('Missing sidebar page: ' + target.file);
    let page = byFile.get(target.file);
    if (!page) {
      page = {file: target.file, route: target.file.slice(0, -10), fragments: []};
      byFile.set(target.file, page);
      pages.push(page);
    }
    if (target.fragment && !page.fragments.includes(target.fragment)) page.fragments.push(target.fragment);
  }
  const expected = [...required];
  if (expected.length > 1000 || expected.some(file => !byFile.has(file)) || expected.length !== pages.length) throw new Error('Incomplete or excessive PDF page inventory');
  for (const route of manualRoutes) if (!byFile.has((route ? route + '/' : '') + 'index.html')) throw new Error('Missing registered manual page');
  for (const id of operationIds) if (!byFile.has('api/operations/' + id.toLowerCase() + '/index.html')) throw new Error('Missing generated API operation');
  return pages;
}

export function chapter(documentHTML, page, index) {
  const document = parse(documentHTML);
  const main = elements(document, node => node.tagName === 'main');
  if (main.length !== 1) throw new Error('Expected one manual main element');
  const heading = elements(main[0], node => node.tagName === 'h1');
  const content = elements(main[0], node => hasClass(node, 'sl-markdown-content'));
  if (heading.length !== 1 || content.length !== 1 || !text(heading[0]).trim()) throw new Error('Incomplete manual chapter: ' + page.file);
  const fragment = parseFragment(serializeOuter(heading[0]) + serializeOuter(content[0]));
  const ids = new Set();
  for (const node of elements(fragment, node => Boolean(attrs(node).id))) {
    const id = attrs(node).id;
    if (ids.has(id)) throw new Error('Duplicate chapter anchor: ' + page.file + '#' + id);
    ids.add(id);
  }
  for (const id of page.fragments) if (!ids.has(id)) throw new Error('Missing sidebar fragment');
  return {...page, id: 'pdf-chapter-' + index, title: text(heading[0]).trim(), fragment, ids};
}

function renameLocalReferences(value, prefix) {
  return value.replace(/url\(\s*(['"]?)#([^)'"\s]+)\1\s*\)/g, (_all, _quote, id) => `url(#${prefix}${id})`);
}

// Keep all alternatives as labelled sections. Interactive controls do not print.
function expandContent(fragment) {
  // Keep rendered source lines, without thousands of syntax-token layout nodes.
  for (const code of elements(fragment, node => node.tagName === 'code')) {
    const lines = (code.childNodes ?? []).filter(node => hasClass(node, 'ec-line'));
    if (lines.length) {
      const content = lines.map(line => {
        const source = elements(line, node => hasClass(node, 'code'));
        if (source.length !== 1) throw new Error('Unrecognized rendered code line');
        return text(source[0]);
      }).join('\n');
      code.childNodes = [{nodeName:'#text',value:content,parentNode:code}];
    }
  }
  // Retain code and captions in plain print blocks, outside the screen widget CSS.
  for (const widget of elements(fragment, node => hasClass(node, 'expressive-code'))) {
    const ids = elements(widget, node => Boolean(attrs(node).id)).map(node => attrs(node).id);
    const anchors = ids.map(id => '<span id="' + escape(id) + '"></span>').join('');
    const blocks = elements(widget, node => node.tagName === 'pre').map(pre => {
      let frame = pre.parentNode;
      while (frame && frame !== widget && !hasClass(frame, 'frame')) frame = frame.parentNode;
      const title = frame ? elements(frame, node => hasClass(node, 'title')).map(text).join(' ').trim() : '';
      const label = title || attrs(pre)['data-language'];
      return '<figure class="pdf-code-frame">' + (label ? '<figcaption>' + escape(label) + '</figcaption>' : '') +
        '<pre><code>' + escape(text(pre)) + '</code></pre></figure>';
    });
    if (!blocks.length) throw new Error('Missing rendered code block');
    const replacement = parseFragment('<div class="pdf-code">' + anchors + blocks.join('') + '</div>').childNodes[0];
    replacement.parentNode = widget.parentNode;
    widget.parentNode.childNodes = widget.parentNode.childNodes.map(node => node === widget ? replacement : node);
  }
  const nodes = elements(fragment, node => Boolean(node.tagName));
  const labels = new Map(nodes.filter(node => attrs(node).id).map(node => [attrs(node).id, text(node).trim()]));
  for (const node of nodes) {
    const a = attrs(node);
    if (node.tagName === 'details' || node.tagName === 'summary') {
      const disclosure = node.tagName === 'details';
      node.tagName = node.nodeName = 'div';
      set(node, 'class', (a.class ?? '') + (disclosure ? ' pdf-disclosure' : ' pdf-summary'));
      set(node, 'open', undefined);
      set(node, 'name', undefined);
    }
    if (a.role === 'tabpanel') {
      const label = labels.get(a['aria-labelledby']) || a['data-openapi-content-type'] || a['aria-label'];
      if (label) {
        const caption = parseFragment('<p class="pdf-panel-label"><strong>' + escape(label) + '</strong></p>').childNodes[0];
        caption.parentNode = node;
        node.childNodes.unshift(caption);
      }
      set(node, 'role', undefined);
      set(node, 'aria-labelledby', undefined);
      set(node, 'class', (a.class ?? '') + ' pdf-panel');
    }
    set(node, 'hidden', undefined);
    if (a['aria-hidden'] === 'true' && a.role === 'tabpanel') set(node, 'aria-hidden', undefined);
    if (a.role === 'tablist') {
      set(node, 'role', undefined);
      set(node, 'class', 'pdf-tab-anchors');
    }
    if (['script', 'button', 'select', 'input'].includes(node.tagName)) {
      if (a.id) {
        node.tagName = node.nodeName = 'span';
        node.attrs = [{name:'id',value:a.id}];
        node.childNodes = [];
      } else remove(node);
    }
  }
}

export function printChapter(value, chapters, {base, files}) {
  const prefix = value.id + '--';
  expandContent(value.fragment);
  const byFile = new Map(chapters.map(c => [c.file, c]));
  let downloads = 0;
  for (const node of elements(value.fragment, node => Boolean(node.tagName))) {
    const a = attrs(node);
    if (a.id) set(node, 'id', prefix + a.id);
    for (const attribute of node.attrs ?? []) {
      if (/^on/i.test(attribute.name)) throw new Error('Unexpected manual event handler');
      attribute.value = renameLocalReferences(attribute.value, prefix);
      if (['aria-labelledby', 'aria-describedby', 'for', 'headers'].includes(attribute.name)) attribute.value = attribute.value.split(/\s+/).map(id => prefix + id).join(' ');
    }
    if (node.tagName === 'style') for (const child of node.childNodes ?? []) if (child.value) child.value = renameLocalReferences(child.value, prefix);
    for (const name of ['href', 'src', 'poster']) {
      if (!a[name]) continue;
      const target = localTarget(a[name], value.route, base);
      if (!target) {
        if (name !== 'href' && !a[name].startsWith('data:image/')) throw new Error('External print asset refused');
        continue;
      }
      if (name === 'href' && byFile.has(target.file)) {
        const destination = byFile.get(target.file);
        if (target.fragment && !destination.ids.has(target.fragment)) throw new Error('Unresolved chapter link: ' + target.file + '#' + target.fragment);
        set(node, name, '#' + destination.id + (target.fragment ? '--' + target.fragment : ''));
      } else if (name === 'href' && a[name].startsWith('#')) {
        if (!value.ids.has(target.fragment)) throw new Error('Missing local SVG or chapter anchor');
        set(node, name, '#' + prefix + target.fragment);
      } else {
        if (!files.has(target.file)) throw new Error('Missing local print asset: ' + target.file);
        if (name === 'href' && node.tagName === 'a') {
          // A standalone PDF has no companion download directory or public site origin.
          set(node, 'href', undefined);
          set(node, 'class', (a.class ?? '') + ' pdf-download');
          const note = parseFragment(' <small>(HTML download: ' + escape(base + target.file) + ')</small>').childNodes;
          for (const child of note) child.parentNode = node;
          node.childNodes.push(...note);
          downloads++;
        } else set(node, name, base + target.file + (target.fragment ? '#' + prefix + target.fragment : ''));
      }
    }
    if (a.srcset) set(node, 'srcset', a.srcset.split(',').map(candidate => {
      const [url, ...size] = candidate.trim().split(/\s+/);
      const target = localTarget(url, value.route, base);
      if (!target || !files.has(target.file)) throw new Error('Missing local responsive image');
      return base + target.file + (size.length ? ' ' + size.join(' ') : '');
    }).join(', '));
  }
  return {html: `<section class="pdf-chapter" id="${value.id}">${serialize(value.fragment)}</section>`, downloads};
}

export function styleInputs(documentHTML, owner, base, files) {
  const document = parse(documentHTML), styles = [], links = [];
  const head = elements(document, node => node.tagName === 'head')[0];
  for (const node of elements(head, node => node.tagName === 'style' || node.tagName === 'link')) {
    if (node.tagName === 'style') styles.push(text(node));
    else if (attrs(node).rel === 'stylesheet') {
      const target = localTarget(attrs(node).href, owner, base);
      if (!target || !files.has(target.file)) throw new Error('Missing print stylesheet');
      links.push(base + target.file);
    }
  }
  return {styles, links};
}

export function assemble(chapters, rendered, {styles, links, css, notices, license, version}) {
  const contents = chapters.map(ch => `<li><a href="#${ch.id}">${escape(ch.title)}</a></li>`).join('');
  // Bound each preformatted layout block without discarding or rewriting notice text.
  const lines = (license + '\n\n' + notices).split('\n'), noticeBlocks = [];
  for (let i = 0; i < lines.length; i += 100) {
    const content = lines.slice(i, i + 100).join('\n') + (i + 100 < lines.length ? '\n' : '');
    noticeBlocks.push('<pre><code>' + escape(content) + '</code></pre>');
  }
  return '<!doctype html><html lang="en" data-theme="light"><head><meta charset="utf-8">' +
    '<meta name="author" content="Open Agentic Catalog contributors"><meta name="description" content="Open Agentic Catalog manual">' +
    '<title>Open Agentic Catalog manual - ' + escape(version) + '</title>' +
    [...new Set(links)].map(href => '<link rel="stylesheet" href="' + escape(href) + '">').join('') +
    [...new Set(styles)].map(style => '<style>' + style + '</style>').join('') + '<style>' + css + '</style></head>' +
    '<body class="pdf-book" style="display:none!important"><header class="pdf-cover"><h1>Open Agentic Catalog manual</h1><p>Version ' + escape(version) + '</p>' +
    '<p>Apache License 2.0</p><p>Downloads remain available in the HTML manual. External reference links require their destination service.</p></header>' +
    '<nav class="pdf-contents" aria-label="Contents"><h1>Contents</h1><ol>' + contents + '</ol></nav>' + rendered.map(page => page.html).join('') +
    '<section class="pdf-notices"><h1>Licenses and notices</h1>' + noticeBlocks.join('') + '</section></body></html>';
}
