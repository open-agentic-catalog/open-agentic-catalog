// Validate the owned decorative icon inputs and native rendered sidebar links.
import assert from 'node:assert/strict';
import { parseFragment } from 'parse5';

const attributes = node => Object.fromEntries((node.attrs ?? []).map(a => [a.name, a.value]));
const text = node => node.value ?? (node.childNodes ?? []).map(text).join('');

export function assertIconAssets(css, assets) {
  const icons = new Set();
  let cursor = 0;
  while (cursor < css.length) {
    const start = css.indexOf('url(', cursor);
    if (start === -1) break;
    const end = css.indexOf(')', start + 4);
    // The CSS parser owns malformed syntax; keep ignoring an unmatched final token.
    if (end === -1) break;
    const value = css.slice(start + 4, end).trim();
    cursor = end + 1;
    const target = value.replace(/^(['"])(.*)\1$/, '$2');
    assert(target === '' || /^\.\.\/assets\/navigation\/[a-z-]+\.svg$/.test(target), `Nonlocal navigation mask: ${target}`);
  }
  const pattern = /a\[data-nav-icon="([a-z-]+)"\]\s*\{\s*--catalog-nav-mask:\s*url\("\.\.\/assets\/navigation\/([a-z-]+)\.svg"\);\s*\}/g;
  for (const [, name, file] of css.matchAll(pattern)) {
    assert.equal(name, file, `Icon asset does not match its key: ${name}`);
    assert(!icons.has(name), `Duplicate icon definition: ${name}`);
    icons.add(name);
    const source = assets.get(`${file}.svg`);
    assert(source, `Missing local navigation icon: ${file}`);
    const svg = parseFragment(source).childNodes.find(node => node.tagName === 'svg');
    assert(svg && attributes(svg).viewBox === '0 0 24 24', `Invalid navigation SVG: ${file}`);
    let shapes = 0;
    const visit = node => {
      if (node.tagName) {
        assert(['svg', 'path'].includes(node.tagName), `Unexpected navigation SVG element: ${file}: ${node.tagName}`);
        const attrs = attributes(node);
        assert(Object.keys(attrs).every(name => ['xmlns', 'viewBox', 'd', 'fill', 'fill-rule', 'clip-rule', 'stroke', 'stroke-width', 'stroke-linejoin'].includes(name)), `Unexpected navigation SVG attribute: ${file}`);
        assert(Object.values(attrs).every(value => !/url\(|javascript:|https?:/i.test(value) || value === 'http://www.w3.org/2000/svg'), `Nonlocal navigation SVG content: ${file}`);
        if (node.tagName === 'path') { assert(attrs.d, `Empty navigation shape: ${file}`); shapes++; }
      }
      node.childNodes?.forEach(visit);
    };
    visit(svg);
    assert(shapes > 0, `Missing navigation shape: ${file}`);
  }
  assert(icons.size > 0, 'Navigation icon definitions missing');
  assert.equal(icons.size, assets.size, 'Unused or unregistered navigation SVG');
  return icons;
}

export function assertNavigationIcons(links, entries, base, icons) {
  const manual = new Set(entries.map(entry => base + (entry.route ? entry.route + '/' : '')));
  const used = new Set();
  for (const link of links) {
    const attrs = attributes(link);
    const icon = attrs['data-nav-icon'];
    if (!manual.has(attrs.href)) {
      assert.equal(icon, undefined, `Generated API link must keep native badges: ${attrs.href}`);
      const operationPrefix = base + 'api/operations/';
      if (attrs.href?.startsWith(operationPrefix) && attrs.href.slice(operationPrefix.length).split('/').filter(Boolean).length === 1) {
        const badge = link.childNodes?.find(node => attributes(node).class?.split(' ').includes('sl-badge'));
        assert(badge && /^(GET|POST|PUT|PATCH|DELETE|HEAD|OPTIONS|TRACE)$/.test(text(badge)), `Missing API method badge: ${attrs.href}`);
      }
      continue;
    }
    assert(icons.has(icon), `Missing or unknown manual navigation icon: ${attrs.href}: ${icon}`);
    used.add(icon);
    const label = link.childNodes?.find(node => node.tagName === 'span');
    assert(label && text(label).trim(), `Missing manual navigation label: ${attrs.href}`);
    // Empty CSS decoration must not replace, hide or rename the native link.
    for (const name of ['aria-label', 'aria-labelledby', 'aria-hidden', 'role', 'tabindex', 'title']) {
      assert.equal(attrs[name], undefined, `Overridden navigation accessibility: ${attrs.href}: ${name}`);
    }
    assert.equal(text(link).trim(), text(label).trim(), `Changed manual navigation name: ${attrs.href}`);
    assert(!link.childNodes.some(node => node.tagName && node.tagName !== 'span'), `Nondecorative navigation content: ${attrs.href}`);
  }
  assert.deepEqual(used, icons, 'Unassigned navigation icon');
}
